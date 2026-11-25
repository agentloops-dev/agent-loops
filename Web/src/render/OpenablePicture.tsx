// A picture that a click opens in the viewer, with the other pictures of its message or page to go
// through (#4011, #4954): a Work page's (ForgePicture) and one beside a Markdown page (LocalPicture,
// #6624). A small picture (a logo, a badge) and one that is a link do not open: a click on a link
// follows it.
import { useSignal } from "@preact/signals";
import { render } from "preact";
import { useEffect, useRef } from "preact/hooks";
import { ImageViewer, type ViewerImage } from "./ImageViewer";
import { opensLarge } from "../model/pageImage";

interface Opened { images: ViewerImage[]; start: number; host: Element }

/** The pictures `opener` is in a sequence with, in their order, and its place in them. A live page's
 * passages are a `.markdown` each, so the page goes before the message. */
function sequenceOf(opener: HTMLImageElement): Opened {
  const within = opener.closest(".live-page, .file-page") ?? opener.closest(".markdown") ?? opener.parentElement;
  const found = [...(within?.querySelectorAll<HTMLImageElement>("button.page-picture-open > img.page-picture") ?? [])]
    .filter((img) => img.naturalWidth > 0);
  return {
    images: found.map((img) => ({ src: img.src, alt: img.dataset["alt"] ?? "", natural: { w: img.naturalWidth, h: img.naturalHeight } })),
    start: Math.max(0, found.indexOf(opener)),
    host: opener.closest(".console") ?? document.body,
  };
}

/**
 * The viewer, drawn at the end of the page rather than where the picture is: a live page's passage
 * takes every click in it as a click to edit, and its zoom would zoom the viewer too.
 */
function ViewerAway({ opened, close }: { opened: Opened; close: () => void }) {
  const closing = useRef(close);
  closing.current = close;
  useEffect(() => {
    const place = document.createElement("div");
    // No box of its own: the host can be a grid of columns.
    place.style.display = "contents";
    opened.host.append(place);
    render(<ImageViewer images={opened.images} start={opened.start} close={() => closing.current()} />, place);
    return () => {
      render(null, place);
      place.remove();
    };
  }, [opened]);
  return null;
}

export function OpenablePicture({ src, alt, label, linked = false }: {
  src: string;
  alt: string;
  label: string;
  linked?: boolean;
}) {
  const natural = useSignal<{ w: number; h: number } | null>(null);
  const open = useSignal<Opened | null>(null);
  const picture = useRef<HTMLImageElement>(null);
  const img = (
    <img class="page-picture" ref={picture} src={src} alt={label} data-alt={alt}
      onLoad={(event) => {
        const el = event.currentTarget as HTMLImageElement;
        const was = natural.peek();
        if (el.naturalWidth > 0 && (was?.w !== el.naturalWidth || was?.h !== el.naturalHeight)) {
          natural.value = { w: el.naturalWidth, h: el.naturalHeight };
        }
      }} />
  );
  // Until its size is known, and when it is small or a link, it is only drawn.
  if (linked || !natural.value || !opensLarge(natural.value.w, natural.value.h)) return img;
  return (
    <>
      <button type="button" class="page-picture-open" title="Open the image" aria-label={`Open ${label}`}
        onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") event.stopPropagation(); }}
        onClick={(event) => {
          // The passage around it would open for typing.
          event.stopPropagation();
          if (picture.current) open.value = sequenceOf(picture.current);
        }}>
        {img}
      </button>
      {open.value && <ViewerAway opened={open.value} close={() => { open.value = null; }} />}
    </>
  );
}
