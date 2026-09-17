// Theme (light / dark / system) switcher. Pairs with themetoggle.templ and the
// blocking InitScript in <head>; this file owns changes after load.
(function () {
  const KEY = "theme";
  const media = window.matchMedia("(prefers-color-scheme: dark)");

  function stored() {
    try {
      return localStorage.getItem(KEY) || "system";
    } catch (e) {
      return "system";
    }
  }

  function apply(theme) {
    const dark = theme === "dark" || (theme === "system" && media.matches);
    const root = document.documentElement;
    root.classList.toggle("dark", dark);
    root.style.colorScheme = dark ? "dark" : "light";
    root.dataset.theme = theme;
    syncGroups(theme);
  }

  // Server-rendered menus default to "system"; mirror the stored choice on
  // every group so the checkmark is right on load and after htmx swaps.
  function syncGroups(theme) {
    document.querySelectorAll("[data-tui-themetoggle]").forEach((group) => {
      group.setAttribute("data-tui-dropdownmenu-radio-value", theme);
      group.querySelectorAll("[data-tui-dropdownmenu-radio-item]").forEach((item) => {
        const checked = item.getAttribute("data-tui-dropdownmenu-radio-value") === theme;
        item.toggleAttribute("data-checked", checked);
        item.toggleAttribute("data-unchecked", !checked);
        item.setAttribute("aria-checked", checked ? "true" : "false");
      });
    });
  }

  function set(theme) {
    try {
      localStorage.setItem(KEY, theme);
    } catch (e) {}
    apply(theme);
  }

  document.addEventListener("dropdownmenu-value-change", (e) => {
    const group = e.target.closest && e.target.closest("[data-tui-themetoggle]");
    if (!group || !e.detail || !e.detail.value) return;
    set(e.detail.value);
  });

  // Follow the OS while in system mode.
  media.addEventListener("change", () => {
    if (stored() === "system") apply("system");
  });

  // Other tabs changing the preference.
  window.addEventListener("storage", (e) => {
    if (e.key === KEY) apply(stored());
  });

  // Sync freshly swapped-in menus right before they open.
  document.addEventListener("dropdownmenu-open-change", (e) => {
    if (e.detail && e.detail.open && e.target.querySelector("[data-tui-themetoggle]")) {
      syncGroups(stored());
    }
  });

  function init() {
    apply(stored());
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
  document.addEventListener("htmx:afterSettle", () => syncGroups(stored()));
})();
