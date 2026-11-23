// agentloops.dev's pages (#6830): each section is a page, and a page's motion (its words
// coming in, its animation) starts when the page comes into view, and starts again each time
// it comes back. The page dot of the page in view is marked. No cookies, no tracking, no
// requests: this only adds and removes classes. Without it, the page shows everything, still.
(() => {
  const root = document.documentElement;
  if (!("IntersectionObserver" in window)) return;
  root.classList.add("js");
  const dots = new Map(
    [...document.querySelectorAll(".dots a")].map((a) => [a.getAttribute("href").slice(1), a]),
  );
  const seen = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        // In view: 30 % of the page shows, or, for a page taller than the window, 40 % of the window.
        const shows = e.intersectionRatio >= 0.3 || e.intersectionRect.height >= window.innerHeight * 0.4;
        if (e.isIntersecting && shows) {
          e.target.classList.add("in");
        } else if (!e.isIntersecting) {
          e.target.classList.remove("in");
        }
      }
    },
    { threshold: [0, 0.1, 0.2, 0.3, 0.4] },
  );
  const current = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        const dot = dots.get(e.target.id);
        if (!dot) continue;
        if (e.isIntersecting) {
          for (const d of dots.values()) d.removeAttribute("aria-current");
          dot.setAttribute("aria-current", "true");
        }
      }
    },
    { rootMargin: "-50% 0px -50% 0px" },
  );
  for (const page of document.querySelectorAll(".page")) {
    seen.observe(page);
    current.observe(page);
  }
})();
