// Safari's trackpad pinch (#6624). Chrome and Firefox send it as a wheel with Control held; Safari
// sends gesture events, which have no type in TypeScript's DOM library. An iPhone's or iPad's
// browser sends them beside the two fingers' own pointer events, so a picture that already follows
// the fingers says so with `busy`, and the gesture then only keeps the page from zooming.

interface GestureEvent extends UIEvent { scale: number; clientX: number; clientY: number }

/** Where a Safari pinch on `el` takes a picture: `level()` as it starts, then `zoom` as it goes. */
export function followSafariPinch(el: HTMLElement, { level, busy, zoom }: {
  level: () => number;
  busy: () => boolean;
  zoom: (level: number, x: number, y: number) => void;
}): () => void {
  let from: number | null = null;
  const start = (event: Event) => {
    event.preventDefault();
    from = busy() ? null : level();
  };
  const change = (event: Event) => {
    event.preventDefault();
    const { scale, clientX, clientY } = event as GestureEvent;
    if (from === null || busy() || !(scale > 0)) return;
    zoom(from * scale, clientX, clientY);
  };
  const end = (event: Event) => {
    event.preventDefault();
    from = null;
  };
  el.addEventListener("gesturestart", start, { passive: false });
  el.addEventListener("gesturechange", change, { passive: false });
  el.addEventListener("gestureend", end, { passive: false });
  return () => {
    el.removeEventListener("gesturestart", start);
    el.removeEventListener("gesturechange", change);
    el.removeEventListener("gestureend", end);
  };
}
