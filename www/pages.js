// agentloops.dev's motion (#6830): as on apple.com, each part of a section (a heading, a card,
// a drawing) comes into being when it scrolls into view, and an animation starts from the
// beginning each time it comes into view. No cookies, no tracking, no requests: this only adds
// and removes classes. Without it, everything shows, still.
(() => {
  if (!("IntersectionObserver" in window)) return;
  document.documentElement.classList.add("js");
  const still = window.matchMedia("(prefers-reduced-motion: reduce)");

  // The parts that come in: a section's own children, except a grid, which brings its
  // children in one by one instead.
  const groups = ".grid3";
  const parts = [];
  for (const child of document.querySelectorAll(".page > .wrap > *")) {
    if (child.matches(groups)) {
      parts.push(...child.children);
    } else {
      parts.push(child);
    }
  }
  for (const part of parts) part.classList.add("part");

  const comeIn = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        e.target.classList.add("in");
        comeIn.unobserve(e.target);
      }
    },
    { rootMargin: "0px 0px -12% 0px", threshold: 0.15 },
  );
  for (const part of parts) comeIn.observe(part);

  // An animation plays while it is in view, from the beginning each time it comes back.
  const play = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        if (e.intersectionRatio >= 0.4) e.target.classList.add("playing");
        else if (!e.isIntersecting) e.target.classList.remove("playing");
      }
    },
    { threshold: [0, 0.4] },
  );
  for (const anim of document.querySelectorAll(".anim, .loop")) play.observe(anim);

  // A figure spins up to its value when it comes into view. It starts from its own value with
  // the page's text, so without this, or with less motion asked for, it shows the value.
  const format = new Intl.NumberFormat("en-GB");
  const spin = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        if (!e.isIntersecting) continue;
        spin.unobserve(e.target);
        if (still.matches) continue;
        const el = e.target;
        const to = +el.dataset.to;
        const begin = performance.now();
        const ms = 1800;
        const step = (now) => {
          const t = Math.min((now - begin) / ms, 1);
          const eased = 1 - (1 - t) ** 4;
          el.textContent = format.format(Math.round(to * eased));
          if (t < 1) requestAnimationFrame(step);
        };
        el.textContent = "0";
        requestAnimationFrame(step);
      }
    },
    { threshold: 0.6 },
  );
  for (const el of document.querySelectorAll(".figure[data-to]")) spin.observe(el);
})();
