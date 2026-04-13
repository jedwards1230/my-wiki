import { QuartzComponent, QuartzComponentConstructor, QuartzComponentProps } from "./types"

const SidebarToggle: QuartzComponent = (_props: QuartzComponentProps) => {
  return (
    <>
      <button
        id="sidebar-toggle"
        class="sidebar-toggle"
        title="Toggle right sidebar"
        aria-label="Toggle right sidebar"
      >
        {"»"}
      </button>
      <script
        dangerouslySetInnerHTML={{
          __html: `
(function() {
  function applySidebarState() {
    var btn = document.getElementById("sidebar-toggle");
    var body = document.getElementById("quartz-body");
    if (!btn || !body) return;

    var collapsed = localStorage.getItem("rightSidebarCollapsed") === "true";
    if (collapsed) {
      body.classList.add("right-sidebar-collapsed");
      btn.textContent = "\\u00AB";
    } else {
      body.classList.remove("right-sidebar-collapsed");
      btn.textContent = "\\u00BB";
    }

    // Replace button to remove old listeners
    var newBtn = btn.cloneNode(true);
    btn.parentNode.replaceChild(newBtn, btn);

    newBtn.addEventListener("click", function() {
      var isCollapsed = document.getElementById("quartz-body").classList.toggle("right-sidebar-collapsed");
      localStorage.setItem("rightSidebarCollapsed", isCollapsed ? "true" : "false");
      newBtn.textContent = isCollapsed ? "\\u00AB" : "\\u00BB";
    });
  }

  // Apply on initial load
  applySidebarState();

  // Re-apply on SPA navigation
  document.addEventListener("nav", function() {
    applySidebarState();
  });
})();
`,
        }}
      />
    </>
  )
}

export default (() => SidebarToggle) satisfies QuartzComponentConstructor
