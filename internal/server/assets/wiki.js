// wiki.js — ~3 KB of glue around htmx + Alpine + KaTeX + Mermaid.
// Avoids any build step; written as plain modern JS so the embedded
// asset is byte-for-byte the same on every deploy.
//
// Responsibilities:
//   1. htmx config + post-swap focus management + theme bootstrap
//   2. KaTeX auto-render (when present) on initial load and after swaps
//   3. Mermaid init (when present) on initial load and after swaps
//   4. Code-copy button injection on every <pre><code> block
//   5. IntersectionObserver-based TOC scroll-spy

(function () {
  "use strict";

  // -------------------------- theme bootstrap --------------------------
  // Persist key matches Alpine's $persist binding so initial paint sees
  // the user's preferred theme without a flash. The persisted value is
  // "light", "dark", or "auto" — only the first two map to a data-theme
  // attribute; "auto" (and any unknown value) leaves the attribute unset
  // so prefers-color-scheme drives the CSS fallback.
  try {
    let stored = localStorage.getItem("_x_darkmode");
    if (stored) stored = JSON.parse(stored);
    let theme = stored;
    if (theme !== "light" && theme !== "dark") {
      theme = window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light";
    }
    document.documentElement.setAttribute("data-theme", theme);
  } catch (_) {
    /* private mode / quota — fall back to OS preference at CSS time */
  }

  // -------------------------- htmx config --------------------------
  document.addEventListener("htmx:configRequest", function (e) {
    e.detail.headers["X-Requested-With"] = "wiki-htmx";
  });

  // After every #main swap, restore focus + announce title for SR users.
  document.body.addEventListener("htmx:afterSwap", function (e) {
    const main = document.getElementById("main");
    if (e.detail.target === main || (e.detail.target && e.detail.target.id === "main")) {
      if (main && typeof main.focus === "function") {
        main.focus({ preventScroll: false });
      }
      const announcer = document.getElementById("a11y-announcer");
      if (announcer) announcer.textContent = document.title;
      initDynamicAssets();
      injectCodeCopy();
      bindTOCScrollSpy();
    }
  });

  // -------------------------- dynamic asset init --------------------------
  function initDynamicAssets() {
    if (window.renderMathInElement) {
      window.renderMathInElement(document.body, {
        delimiters: [
          { left: "$$", right: "$$", display: true },
          { left: "$", right: "$", display: false },
        ],
        throwOnError: false,
      });
    }
    if (window.mermaid && typeof window.mermaid.run === "function") {
      try {
        window.mermaid.run({ querySelector: "pre.mermaid, .mermaid" });
      } catch (_) {
        /* mermaid throws on stale nodes after swap; safe to ignore */
      }
    }
  }

  // -------------------------- code-copy buttons --------------------------
  function injectCodeCopy() {
    const blocks = document.querySelectorAll("pre:not([data-copy-bound]) > code");
    blocks.forEach(function (code) {
      const pre = code.parentElement;
      pre.setAttribute("data-copy-bound", "1");
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "code-copy";
      btn.textContent = "Copy";
      btn.addEventListener("click", function () {
        const text = code.textContent || "";
        const restore = function () {
          setTimeout(function () { btn.textContent = "Copy"; }, 1500);
        };
        const onOk = function () { btn.textContent = "Copied"; restore(); };
        const onErr = function () { btn.textContent = "Failed"; restore(); };
        // navigator.clipboard requires a secure context; fall back to the
        // legacy execCommand path so the button works on http:// LAN
        // deployments and older browsers without throwing.
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).then(onOk, onErr);
          return;
        }
        try {
          const ta = document.createElement("textarea");
          ta.value = text;
          ta.setAttribute("readonly", "");
          ta.style.position = "absolute";
          ta.style.left = "-9999px";
          document.body.appendChild(ta);
          ta.select();
          const ok = document.execCommand && document.execCommand("copy");
          document.body.removeChild(ta);
          ok ? onOk() : onErr();
        } catch (_) {
          onErr();
        }
      });
      pre.insertBefore(btn, code);
    });
  }

  // -------------------------- TOC scroll spy --------------------------
  // Module-level observer so successive htmx swaps don't pile up
  // IntersectionObservers and double-fire scrollspy callbacks across the
  // session. bindTOCScrollSpy disconnects the prior observer before
  // wiring a new one against the freshly-swapped headings.
  let tocObserver = null;
  function bindTOCScrollSpy() {
    if (tocObserver) {
      tocObserver.disconnect();
      tocObserver = null;
    }
    const tocLinks = document.querySelectorAll(".toc a[href^='#']");
    if (!tocLinks.length || !("IntersectionObserver" in window)) return;
    const byId = new Map();
    tocLinks.forEach(function (a) {
      const id = a.getAttribute("href").slice(1);
      const target = document.getElementById(id);
      if (target) byId.set(target, a);
    });
    if (!byId.size) return;
    tocObserver = new IntersectionObserver(
      function (entries) {
        entries.forEach(function (entry) {
          const link = byId.get(entry.target);
          if (!link) return;
          if (entry.isIntersecting) {
            tocLinks.forEach(function (l) { l.classList.remove("is-active"); });
            link.classList.add("is-active");
          }
        });
      },
      { rootMargin: "0px 0px -70% 0px", threshold: 0 }
    );
    byId.forEach(function (_, heading) { tocObserver.observe(heading); });
  }

  // Initial pass on first paint.
  document.addEventListener("DOMContentLoaded", function () {
    initDynamicAssets();
    injectCodeCopy();
    bindTOCScrollSpy();
  });
})();
