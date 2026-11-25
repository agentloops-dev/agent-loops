// An image on a Work page (#4011), drawn from bytes the machine that read the page got with
// forge/image: the page never loads the forge's address, and a blob is what the CSP allows. While
// it loads, and when the machine refuses it, it is its alternative text. A click opens it in the
// viewer, in the page, with the other images of the same message to go through (#4954). A small
// image (a logo, a badge) and an image that is a link do not open: a click on a link follows it.
// The Mac's and the Remote's is MarkdownText's remote image (#4010).
import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import { OpenablePicture } from "./OpenablePicture";

export function ForgePicture({ url, alt, read, linked = false }: {
  url: string;
  alt: string;
  read: (url: string) => Promise<{ bytes: Uint8Array; type: string } | null>;
  linked?: boolean;
}) {
  const src = useSignal<string | null>(null);
  const readRef = useRef(read);
  readRef.current = read;
  useEffect(() => {
    let current = true;
    let made: string | null = null;
    src.value = null;
    void readRef.current(url).then(
      (got) => {
        if (!current || !got) return;
        made = URL.createObjectURL(new Blob([got.bytes as BlobPart], { type: got.type }));
        src.value = made;
      },
      () => {},
    );
    return () => {
      current = false;
      if (made) URL.revokeObjectURL(made);
    };
  }, [url]);
  if (!src.value) return <span class="image-placeholder" title={url}>{alt ? `[image: ${alt}]` : "[image]"}</span>;
  return <OpenablePicture src={src.value} alt={alt} label={alt || "Image"} linked={linked} />;
}
