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

  // -------------------------- graph view --------------------------
  // Tiny custom force-directed layout drawn to <canvas>. Avoids
  // shipping d3+pixi (~250KB). For each page, /api/graph.json returns
  // {nodes, links}; we filter to a depth-1 neighborhood around the
  // current slug, run a few hundred force iterations on requestAnimationFrame,
  // then keep idle. Click a node to navigate.
  let graphCache = null;
  let graphRaf = 0;
  function initGraph() {
    cancelAnimationFrame(graphRaf);
    const cv = document.querySelector(".graph-canvas[data-graph-src]");
    if (!cv) return;
    const slug = cv.getAttribute("data-slug");
    const src = cv.getAttribute("data-graph-src");
    if (!slug || !src) return;
    const ctx = cv.getContext("2d");
    if (!ctx) return;
    const dpr = window.devicePixelRatio || 1;
    const W = cv.width, H = cv.height;
    cv.width = W * dpr; cv.height = H * dpr;
    cv.style.width = W + "px"; cv.style.height = H + "px";
    ctx.scale(dpr, dpr);
    const fetchGraph = graphCache
      ? Promise.resolve(graphCache)
      : fetch(src).then(r => r.json()).then(j => (graphCache = j.data || j));
    fetchGraph.then(function (g) {
      if (!g || !g.nodes) return;
      // depth-1 neighborhood around slug
      const adj = new Map();
      g.nodes.forEach(n => adj.set(n.id, new Set()));
      (g.links || []).forEach(function (l) {
        if (!adj.has(l.source) || !adj.has(l.target)) return;
        adj.get(l.source).add(l.target);
        adj.get(l.target).add(l.source);
      });
      const keep = new Set([slug]);
      (adj.get(slug) || new Set()).forEach(id => keep.add(id));
      const nodes = g.nodes.filter(n => keep.has(n.id)).map(function (n, i) {
        return {
          id: n.id,
          title: n.title || n.id,
          url: n.url || ("/" + n.id + "/"),
          x: W / 2 + 40 * Math.cos(i),
          y: H / 2 + 40 * Math.sin(i),
          vx: 0, vy: 0,
          here: n.id === slug,
        };
      });
      const byId = new Map(nodes.map(n => [n.id, n]));
      const links = (g.links || []).filter(l => byId.has(l.source) && byId.has(l.target));
      // hover state for label rendering
      let hover = null;
      cv.style.cursor = "default";
      cv.onmousemove = function (e) {
        const r = cv.getBoundingClientRect();
        const mx = e.clientX - r.left, my = e.clientY - r.top;
        hover = null;
        for (const n of nodes) {
          const dx = n.x - mx, dy = n.y - my;
          if (dx * dx + dy * dy < 64) { hover = n; break; }
        }
        cv.style.cursor = hover ? "pointer" : "default";
      };
      cv.onclick = function () { if (hover) window.location.href = hover.url; };
      // force layout — short fixed iteration budget
      let iter = 0;
      const maxIter = 400;
      const cs = getComputedStyle(document.documentElement);
      const fg = cs.getPropertyValue("--graph-edge").trim() || "#a8b5bd";
      const node = cs.getPropertyValue("--graph-node").trim() || "#284b63";
      const here = cs.getPropertyValue("--graph-node-active").trim() || "#0b4a6f";
      function step() {
        for (let i = 0; i < nodes.length; i++) {
          const a = nodes[i];
          if (a.here) continue;
          for (let j = i + 1; j < nodes.length; j++) {
            const b = nodes[j];
            let dx = a.x - b.x, dy = a.y - b.y;
            const d2 = Math.max(50, dx * dx + dy * dy);
            const force = 1200 / d2;
            const d = Math.sqrt(d2);
            dx /= d; dy /= d;
            a.vx += dx * force; a.vy += dy * force;
            b.vx -= dx * force; b.vy -= dy * force;
          }
        }
        for (const l of links) {
          const a = byId.get(l.source), b = byId.get(l.target);
          const dx = b.x - a.x, dy = b.y - a.y;
          const d = Math.max(1, Math.sqrt(dx * dx + dy * dy));
          const target = 60;
          const k = 0.04 * (d - target);
          a.vx += (dx / d) * k; a.vy += (dy / d) * k;
          b.vx -= (dx / d) * k; b.vy -= (dy / d) * k;
        }
        for (const n of nodes) {
          n.vx += (W / 2 - n.x) * 0.01;
          n.vy += (H / 2 - n.y) * 0.01;
          n.vx *= 0.85; n.vy *= 0.85;
          n.x += n.vx; n.y += n.vy;
          n.x = Math.max(10, Math.min(W - 10, n.x));
          n.y = Math.max(10, Math.min(H - 10, n.y));
        }
        ctx.clearRect(0, 0, W, H);
        ctx.strokeStyle = fg; ctx.lineWidth = 1;
        for (const l of links) {
          const a = byId.get(l.source), b = byId.get(l.target);
          ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.lineTo(b.x, b.y); ctx.stroke();
        }
        for (const n of nodes) {
          ctx.beginPath();
          ctx.arc(n.x, n.y, n.here ? 5 : 4, 0, Math.PI * 2);
          ctx.fillStyle = n.here ? here : node;
          ctx.fill();
        }
        if (hover) {
          ctx.fillStyle = cs.getPropertyValue("--text").trim() || "#000";
          ctx.font = "11px ui-sans-serif, system-ui, sans-serif";
          const t = hover.title;
          const tw = ctx.measureText(t).width;
          const tx = Math.min(W - tw - 4, Math.max(4, hover.x + 8));
          const ty = Math.max(12, hover.y - 8);
          ctx.fillText(t, tx, ty);
        }
        if (++iter < maxIter || hover) {
          graphRaf = requestAnimationFrame(step);
        }
      }
      graphRaf = requestAnimationFrame(step);
    }).catch(function () { /* graph unavailable — leave canvas blank */ });
  }

  // Initial pass on first paint.
  document.addEventListener("DOMContentLoaded", function () {
    initDynamicAssets();
    injectCodeCopy();
    bindTOCScrollSpy();
    initGraph();
  });
})();
