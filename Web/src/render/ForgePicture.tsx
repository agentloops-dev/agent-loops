// An image on a Work page (#4011), drawn from bytes the machine that read the page got with
// forge/image: the page never loads the forge's address, and a blob is what the CSP allows. While
// it loads, and when the machine refuses it, it is its alternative text. A click opens it in the
// viewer, in the page, with the other images of the same message to go through (#4954). A small
// image (a logo, a badge) and an image that is a link do not open: a click on a link follows it.
// With a machine that keeps image hosts (#7140, port of #7139), a blocked image names its host, with
// Show image and Always allow: a card on its own line, a chip in words, and beside a link a Blocked
// chip that is not in the link (BlockedChip). The Mac's and the Remote's is MarkdownText's remote
// image (#4010) and BlockedImage.swift.
import { useSignal } from "@preact/signals";
import type { ComponentChildren } from "preact";
import { useEffect, useMemo, useRef } from "preact/hooks";
import { OpenablePicture } from "./OpenablePicture";
import type { ImageBlocked } from "../protocol/generated";
import {
  allowFull, allowWords, blockedWords, failedWords, type ImageControls, type ImagePhase, imageHostsWords,
  learnsWords, shownOnceWords,
} from "../model/imageHosts";
import { Icon } from "../views/Icon";

export type ReadForgePicture = ((url: string) => Promise<{ bytes: Uint8Array; type: string } | null>) & {
  /** The page's controls for a blocked image; none from a machine that keeps no image hosts. */
  controls?: ImageControls | undefined;
};

export function ForgePicture({ url, alt, read, linked = false, inWords = false }: {
  url: string;
  alt: string;
  read: ReadForgePicture;
  linked?: boolean;
  /** The image sits in words: a blocked one is a chip, not a card. */
  inWords?: boolean;
}) {
  if (read.controls) return <ControlledPicture url={url} alt={alt} controls={read.controls} linked={linked} inWords={inWords} />;
  return <PlainPicture url={url} alt={alt} read={read} linked={linked} />;
}

function PlainPicture({ url, alt, read, linked }: { url: string; alt: string; read: ReadForgePicture; linked: boolean }) {
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
  if (!src.value) return <Placeholder url={url} alt={alt} />;
  return <OpenablePicture src={src.value} alt={alt} label={alt || "Image"} linked={linked} />;
}

function Placeholder({ url, alt }: { url: string; alt: string }) {
  return <span class="image-placeholder" title={url}>{alt ? `[image: ${alt}]` : "[image]"}</span>;
}

/** Its alternative text, or its file's name. */
export function pictureName(url: string, alt: string): string {
  if (alt) return alt;
  try {
    return decodeURIComponent(new URL(url).pathname.split("/").pop() || url);
  } catch {
    return url;
  }
}

/** A phase that has controls: blocked, being read after a press, or failed after one. */
const hasCard = (phase: ImagePhase) => phase.kind === "blocked" || phase.kind === "reading" || phase.kind === "failed";

function ControlledPicture({ url, alt, controls, linked, inWords }: {
  url: string; alt: string; controls: ImageControls; linked: boolean; inWords: boolean;
}) {
  const { images, machine } = controls;
  useEffect(() => { images.load(url, machine); }, [images, url, machine]);
  const phase = images.phase(url);
  const src = useBlobURL(phase);
  if (phase.kind === "image" && src) {
    const host = images.shownOnce(url);
    const problem = images.problem(url);
    return (
      <>
        <OpenablePicture src={src} alt={alt} label={alt || "Image"} linked={linked} />
        {host && !linked && (
          <span class="shown-once">
            <span class="quiet">{shownOnceWords(host)} · </span>
            <button type="button" class="link" title={allowFull(host)} aria-label={allowFull(host)}
              onClick={() => void images.allow(host, url, controls.machineName)}>{allowWords(host)}</button>
            {problem && <span class="failure">{problem}</span>}
          </span>
        )}
      </>
    );
  }
  // In a link, the link's words: its Blocked chip is beside the link (BlockedChip).
  if (!hasCard(phase) || linked) return <Placeholder url={url} alt={alt} />;
  if (inWords) return <Chip url={url} words={`${pictureName(url, alt)} · blocked`} controls={controls} phase={phase} />;
  return (
    <div class="blocked-image" role="group" aria-label={pictureName(url, alt)}>
      <Icon name="image-off" class="blocked-image-icon" />
      <div class="blocked-image-body">
        <div class="blocked-image-name">{pictureName(url, alt)}</div>
        <CardParts url={url} controls={controls} phase={phase} />
      </div>
    </div>
  );
}

/** Beside a link whose words are an image: Blocked, with the card in a popover, when the image is blocked. */
export function BlockedChip({ url, read }: { url: string; read: ReadForgePicture }) {
  const controls = read.controls;
  if (!controls) return null;
  const phase = controls.images.phase(url);
  if (!hasCard(phase)) return null;
  return <>{" "}<Chip url={url} words={imageHostsWords.blockedChip} controls={controls} phase={phase} /></>;
}

function Chip({ url, words, controls, phase }: { url: string; words: string; controls: ImageControls; phase: ImagePhase }) {
  const open = useSignal(false);
  const root = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    if (!open.value) return;
    const away = (e: Event) => { if (!root.current?.contains(e.target as Node)) open.value = false; };
    const key = (e: KeyboardEvent) => { if (e.key === "Escape") open.value = false; };
    document.addEventListener("pointerdown", away);
    document.addEventListener("keydown", key);
    return () => { document.removeEventListener("pointerdown", away); document.removeEventListener("keydown", key); };
  }, [open.value]);
  return (
    <span class="blocked-chip-anchor" ref={root}>
      <button type="button" class="blocked-chip" aria-expanded={open.value} aria-haspopup="dialog"
        onClick={(e) => { e.preventDefault(); e.stopPropagation(); open.value = !open.value; }}>
        <Icon name="image-off" /> {words}
      </button>
      {open.value && (
        <span class="popover blocked-popover" role="dialog" aria-label={words}>
          <CardParts url={url} controls={controls} phase={phase} />
        </span>
      )}
    </span>
  );
}

/** What a blocked image says and its buttons, in a card or a chip's popover. */
function CardParts({ url, controls, phase }: { url: string; controls: ImageControls; phase: ImagePhase }) {
  const { images, machineName } = controls;
  switch (phase.kind) {
    case "blocked":
      return (
        <>
          <Words blocked={phase.blocked} machineName={machineName} />
          {phase.problem && <span class="failure blocked-image-line">{phase.problem}</span>}
          <Buttons>
            <button type="button" onClick={() => images.show(url)}>{imageHostsWords.show}</button>
            <button type="button" class="blocked-image-allow" title={allowFull(phase.blocked.host)} aria-label={allowFull(phase.blocked.host)}
              onClick={() => void images.allow(phase.blocked.host, url, machineName)}>{allowWords(phase.blocked.host)}</button>
          </Buttons>
        </>
      );
    case "reading":
      return (
        <>
          <Words blocked={phase.blocked} machineName={machineName} />
          <Buttons>
            <span class="spinner" aria-hidden="true" />
            <button type="button" disabled>{imageHostsWords.reading}</button>
          </Buttons>
        </>
      );
    case "failed":
      return (
        <>
          <span class="quiet blocked-image-line">{failedWords(phase.why)}</span>
          {phase.again && <Buttons><button type="button" onClick={() => images.again(url)}>{imageHostsWords.tryAgain}</button></Buttons>}
        </>
      );
    default:
      return null;
  }
}

function Words({ blocked, machineName }: { blocked: ImageBlocked; machineName: string }) {
  return (
    <>
      <span class="blocked-image-line blocked-image-hosts">{marked(blocked)}</span>
      <span class="quiet blocked-image-line">{learnsWords(blocked.host, machineName)}</span>
    </>
  );
}

function Buttons({ children }: { children: ComponentChildren }) {
  return <span class="blocked-image-actions">{children}</span>;
}

/** The line that names the hosts, with each of them in bold. */
function marked(blocked: ImageBlocked): ComponentChildren {
  const words = blockedWords(blocked);
  const at = words.lastIndexOf(blocked.host);
  const before = words.slice(0, at);
  if (!blocked.from) return <>{before}<b>{blocked.host}</b></>;
  const from = before.indexOf(blocked.from);
  return <>{before.slice(0, from)}<b>{blocked.from}</b>{before.slice(from + blocked.from.length)}<b>{blocked.host}</b></>;
}

/** The blob of an image's bytes while it is shown; given back when it changes or goes. */
function useBlobURL(phase: ImagePhase): string | null {
  const bytes = phase.kind === "image" ? phase : null;
  const src = useMemo(() => bytes ? URL.createObjectURL(new Blob([bytes.bytes as BlobPart], { type: bytes.type })) : null, [bytes?.bytes]);
  useEffect(() => () => { if (src) URL.revokeObjectURL(src); }, [src]);
  return src;
}

