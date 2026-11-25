// A picture from beside a Markdown page, drawn from bytes the host read (#258). The address
// never becomes the image's src: a blob stays inside the page, which is what the CSP allows.
// A click opens it in the viewer, with the page's other pictures to go through (#6624).
import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import { OpenablePicture } from "./OpenablePicture";

const same = (a: Uint8Array, b: Uint8Array) => a.length === b.length && a.every((byte, i) => byte === b[i]);

export function LocalPicture({ path, alt, revision, read, linked = false }: {
  path: string;
  alt: string;
  revision: number;
  read: (path: string) => Promise<{ bytes: Uint8Array; type: string } | null>;
  linked?: boolean;
}) {
  const src = useSignal<string | null>(null);
  const failed = useSignal(false);
  const readRef = useRef(read);
  readRef.current = read;
  /** What is drawn now. A change in the folder that left this picture as it was draws nothing
   * again, and so does not close the viewer over it. */
  const held = useRef<{ path: string; bytes: Uint8Array; url: string } | null>(null);
  const drop = () => {
    if (held.current) URL.revokeObjectURL(held.current.url);
    held.current = null;
  };
  useEffect(() => {
    let current = true;
    failed.value = false;
    if (held.current?.path !== path) { drop(); src.value = null; }
    void readRef.current(path).then(
      (got) => {
        if (!current) return;
        if (!got) { failed.value = true; return; }
        if (held.current && same(held.current.bytes, got.bytes)) return;
        drop();
        const url = URL.createObjectURL(new Blob([got.bytes as BlobPart], { type: got.type }));
        held.current = { path, bytes: got.bytes, url };
        src.value = url;
      },
      () => { if (current) failed.value = true; },
    );
    return () => { current = false; };
  }, [path, revision]);
  useEffect(() => drop, []);
  if (failed.value) return <span class="image-placeholder">{alt ? `[image: ${alt}]` : "[image]"}</span>;
  return src.value
    ? <OpenablePicture src={src.value} alt={alt} label={alt || path.split("/").pop() || "Image"} linked={linked} />
    : null;
}
