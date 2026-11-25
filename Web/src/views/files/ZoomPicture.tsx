// A picture that opens fitted to the pane and can be looked into (ImageFile, #258):
// Zoom out, Actual size, Zoom in and Fit, and a pinch. It is never drawn larger than
// itself until the reader asks, and scrolling the pane reaches the edges. The pinch is two fingers
// on a touch screen, or a trackpad's: a wheel with Control held, and Safari's gesture events (#6624).
import { useSignal } from "@preact/signals";
import { useEffect, useLayoutEffect, useRef } from "preact/hooks";
import {
  clampMagnification, fitMagnification, magnificationAfterDoubleClick, magnificationAfterWheel, stepMagnification, zoomMost,
} from "../../model/pageImage";
import { followSafariPinch } from "../../render/safariPinch";

/** Where a pinch is on the picture (0 to 1 each way), kept under (x, y) on the page as it zooms. */
interface Anchor { fx: number; fy: number; x: number; y: number }

export function ZoomPicture({ bytes, type, name, description }: {
  bytes: Uint8Array | string;
  type: string;
  name: string;
  description?: string | undefined;
}) {
  const src = useSignal<string | null>(null);
  const natural = useSignal<{ w: number; h: number } | null>(null);
  /** Null while the picture is fitted, so a resize of the pane refits it. */
  const chosen = useSignal<number | null>(null);
  const room = useSignal({ w: 320, h: 240 });
  const frame = useRef<HTMLDivElement>(null);
  const image = useRef<HTMLImageElement>(null);
  const anchor = useRef<Anchor | null>(null);

  useEffect(() => {
    natural.value = null;
    chosen.value = null;
    const made = URL.createObjectURL(new Blob([bytes as BlobPart], { type }));
    src.value = made;
    return () => URL.revokeObjectURL(made);
  }, [bytes, type]);

  useLayoutEffect(() => {
    const scroll = frame.current?.closest(".scroll");
    if (!scroll) return;
    const measure = () => {
      room.value = { w: Math.max(40, scroll.clientWidth - 24), h: Math.max(80, scroll.clientHeight - 96) };
    };
    measure();
    const watcher = new ResizeObserver(measure);
    watcher.observe(scroll);
    return () => watcher.disconnect();
  }, []);

  useEffect(() => {
    const el = image.current;
    if (!el) return;
    const fitNow = () => {
      const size = natural.peek();
      return size ? fitMagnification(size.w, size.h, room.peek().w, room.peek().h) : 1;
    };
    const shownNow = () => {
      const fit = fitNow();
      const level = chosen.peek();
      return level === null ? fit : clampMagnification(level, fit);
    };
    /** The point a pinch is on is found once, as it starts: a step that the pane cannot scroll
     * for yet does not lose it. A pinch that comes as wheel turns is those close together. */
    let held: { fx: number; fy: number; at: number } | null = null;
    const zoomTo = (level: number, x: number, y: number) => {
      if (!natural.peek()) return;
      const fit = fitNow(), to = clampMagnification(level, fit);
      if (Math.abs(to - shownNow()) < 0.0001) return;
      const box = el.getBoundingClientRect();
      if (!held || Date.now() - held.at > 300) {
        held = { fx: (x - box.left) / box.width, fy: (y - box.top) / box.height, at: 0 };
      }
      held.at = Date.now();
      anchor.current = { fx: held.fx, fy: held.fy, x, y };
      chosen.value = Math.abs(to - fit) < 0.0001 ? null : to;
    };
    const onWheel = (event: WheelEvent) => {
      if (!event.ctrlKey && !event.metaKey) return;
      event.preventDefault();
      const delta = event.deltaMode === 1 ? event.deltaY * 16 : event.deltaY;
      zoomTo(magnificationAfterWheel(shownNow(), fitNow(), delta, event.ctrlKey), event.clientX, event.clientY);
    };
    // The browser takes no touch on the picture, so that a pinch does not zoom the page: two
    // fingers pinch, and one scrolls the pane as it would have.
    const fingers = new Map<number, { x: number; y: number }>();
    let pinch: { distance: number; level: number } | null = null;
    const spread = () => {
      const [a, b] = [...fingers.values()];
      return a && b ? Math.hypot(a.x - b.x, a.y - b.y) : 0;
    };
    const down = (event: PointerEvent) => {
      if (event.pointerType !== "touch") return;
      fingers.set(event.pointerId, { x: event.clientX, y: event.clientY });
      el.setPointerCapture(event.pointerId);
      held = null;
      pinch = fingers.size === 2 ? { distance: spread(), level: shownNow() } : null;
    };
    const move = (event: PointerEvent) => {
      const was = fingers.get(event.pointerId);
      if (!was) return;
      fingers.set(event.pointerId, { x: event.clientX, y: event.clientY });
      const [a, b] = [...fingers.values()];
      if (pinch && a && b) {
        if (pinch.distance > 0) zoomTo(pinch.level * spread() / pinch.distance, (a.x + b.x) / 2, (a.y + b.y) / 2);
      } else if (fingers.size === 1) {
        el.closest(".scroll")?.scrollBy(was.x - event.clientX, was.y - event.clientY);
      }
    };
    const up = (event: PointerEvent) => {
      fingers.delete(event.pointerId);
      pinch = null;
    };
    const unfollow = followSafariPinch(el, { level: shownNow, busy: () => fingers.size > 0, zoom: zoomTo });
    el.addEventListener("wheel", onWheel, { passive: false });
    el.addEventListener("pointerdown", down);
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
    el.addEventListener("pointercancel", up);
    return () => {
      unfollow();
      el.removeEventListener("wheel", onWheel);
      el.removeEventListener("pointerdown", down);
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
      el.removeEventListener("pointercancel", up);
    };
    // The picture is drawn once it has an address.
  }, [src.value !== null]);

  const size = natural.value;
  const fit = size ? fitMagnification(size.w, size.h, room.value.w, room.value.h) : 1;
  const shown = size ? (chosen.value === null ? fit : clampMagnification(chosen.value, fit)) : 1;
  const fitted = chosen.value === null || Math.abs(shown - fit) < 0.0001;

  // After a pinch, the point it was on goes back under the fingers, as far as the pane scrolls.
  useLayoutEffect(() => {
    const el = image.current, at = anchor.current;
    anchor.current = null;
    if (!el || !at) return;
    const box = el.getBoundingClientRect();
    el.closest(".scroll")?.scrollBy(box.left + at.fx * box.width - at.x, box.top + at.fy * box.height - at.y);
  }, [shown]);

  const caption = size && description ? `${description} · ${size.w} × ${size.h}` : description || name;

  return (
    <div class="picture-view" ref={frame}>
      {src.value && (
        <img class="picture" ref={image} draggable={false} src={src.value} alt={name}
          style={size ? { width: `${Math.round(size.w * shown)}px`, height: `${Math.round(size.h * shown)}px` } : undefined}
          onLoad={(event) => {
            const img = event.currentTarget as HTMLImageElement;
            if (img.naturalWidth > 0) natural.value = { w: img.naturalWidth, h: img.naturalHeight };
          }}
          onDblClick={() => { if (size) chosen.value = magnificationAfterDoubleClick(shown, fit); }} />
      )}
      <div class="picture-bar">
        <span class="caption quiet small">{caption}</span>
        <button class="icon" aria-label="Zoom out" title="Zoom out" disabled={!size || shown <= fit + 0.001}
          onClick={() => { chosen.value = stepMagnification(shown, fit, -1); }}>−</button>
        <button class="icon" aria-label="Actual size" title="Actual size" disabled={!size}
          onClick={() => { chosen.value = clampMagnification(1, fit); }}>{size ? `${Math.round(shown * 100)}%` : "100%"}</button>
        <button class="icon" aria-label="Zoom in" title="Zoom in" disabled={!size || shown >= Math.max(zoomMost, fit) - 0.001}
          onClick={() => { chosen.value = stepMagnification(shown, fit, 1); }}>+</button>
        <button class="icon" aria-label="Fit" title="Fit the picture to the pane" disabled={!size || fitted}
          onClick={() => { chosen.value = null; }}>Fit</button>
      </div>
    </div>
  );
}
