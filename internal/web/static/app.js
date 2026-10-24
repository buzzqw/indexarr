// indexarr UI: progressive enhancement only. Every action also works without
// JavaScript, except the copy shortcuts (which degrade to selecting the field).
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").catch(() => {});
}

// Keep the confirmation next to the buttons that produced it, not only at the
// top of the page. The original flash stays in place for screen readers and
// for browsers without JavaScript; the copy is purely visual.
(() => {
  const placeFlashNearForm = () => {
    const targets = [];
    document.querySelectorAll("main.content form.panel.form").forEach((form) => {
      const actions = form.querySelector(".form-actions, .actions");
      if (actions) {
        targets.push(actions);
      }
    });
    if (!targets.length) {
      document.querySelectorAll("main.content .page-head .actions").forEach((actions) => {
        targets.push(actions);
      });
    }
    document.querySelectorAll("main.content > .flash").forEach((flash) => {
      targets.forEach((actions) => {
        if (actions.parentNode.querySelector("[data-form-flash]")) {
          return;
        }
        const copy = flash.cloneNode(true);
        copy.classList.add("form-flash-inline");
        copy.setAttribute("data-form-flash", "");
        copy.setAttribute("aria-hidden", "true");
        actions.parentNode.insertBefore(copy, actions);
      });
    });
  };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", placeFlashNearForm, { once: true });
  } else {
    placeFlashNearForm();
  }
})();

// Reload pages such as Activity → New releases only while the tab is visible.
// Hiding the tab clears the timer, so a background tab never polls indexarr.
(() => {
  const seconds = Number(document.body.dataset.liveRefresh || "0");
  if (!Number.isFinite(seconds) || seconds <= 0) {
    return;
  }
  let timer = null;
  const start = () => {
    if (timer !== null || document.visibilityState !== "visible") {
      return;
    }
    timer = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        window.location.reload();
      }
    }, seconds * 1000);
  };
  const stop = () => {
    if (timer === null) {
      return;
    }
    window.clearInterval(timer);
    timer = null;
  };
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") {
      start();
    } else {
      stop();
    }
  });
  start();
})();

(() => {
  // The mobile stylesheet turns tables into stacked cards and uses
  // data-label as the visible field name. Deriving it from <thead> keeps the
  // markup clean and also covers tables swapped in by the live search.
  const labelTables = (root = document) => {
    root.querySelectorAll("table").forEach((table) => {
      const headers = Array.from(table.querySelectorAll("thead th")).map((th) =>
        th.textContent.trim()
      );
      if (!headers.length) {
        return;
      }
      table.classList.toggle("has-check", Boolean(table.querySelector("thead th.check")));
      table.querySelectorAll("tbody tr").forEach((row) => {
        Array.from(row.children).forEach((cell, index) => {
          if (cell.tagName !== "TD" || cell.hasAttribute("colspan")) {
            return;
          }
          const header = headers[index] || "";
          if (!header || header.toLowerCase() === "actions") {
            cell.removeAttribute("data-label");
            return;
          }
          cell.setAttribute("data-label", header);
        });
      });
    });
  };
  labelTables();

  const copyText = async (text) => {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      const area = document.createElement("textarea");
      area.value = text;
      area.setAttribute("readonly", "");
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.appendChild(area);
      area.select();
      let ok = false;
      try {
        ok = document.execCommand("copy");
      } catch {
        ok = false;
      }
      area.remove();
      return ok;
    }
  };

  // Language-dependent labels come from the layout, so the JavaScript never
  // hardcodes a language.
  const copiedLabel = document.body.dataset.copyDone || "Copied";
  const copyFailedLabel = document.body.dataset.copyFailed || "Copy failed";

  // Secrets are never part of the initial HTML. They are fetched only after
  // an explicit Show/Copy action, with a same-origin marker the server checks.
  const fetchSecret = async (field) => {
    const url = field.getAttribute("data-secret-url") || "";
    if (!url) {
      throw new Error("missing secret URL");
    }
    const response = await fetch(url, {
      headers: { "X-Requested-With": "fetch", Accept: "application/json" },
      credentials: "same-origin",
      cache: "no-store",
    });
    if (!response.ok) {
      throw new Error("HTTP " + response.status);
    }
    const payload = await response.json();
    return String(payload.value || "");
  };

  // Empty secret fields mean "unchanged". The Change button only makes the
  // field ready for a replacement; it never reveals the stored value.
  document.addEventListener("click", (event) => {
    const change = event.target.closest("[data-secret-change]");
    if (!change) {
      return;
    }
    event.preventDefault();
    const field = change.closest("[data-secret-change-field]");
    const input = field && field.querySelector("input");
    if (!input) {
      return;
    }
    input.value = "";
    input.focus();
    change.hidden = true;
  });

  // Remember whether the secondary release filters were open, as described in
  // UI-GUIDE §4.5. The panel still works without this enhancement.
  const advancedFilters = document.getElementById("release-advanced-filters");
  if (advancedFilters) {
    const storageKey = "indexarr.releaseFiltersOpen";
    try {
      if (window.localStorage.getItem(storageKey) === "1") {
        advancedFilters.open = true;
      }
    } catch {
      // Storage can be unavailable in private modes; the closed default wins.
    }
    advancedFilters.addEventListener("toggle", () => {
      try {
        window.localStorage.setItem(storageKey, advancedFilters.open ? "1" : "0");
      } catch {
        // Ignore: the panel state is a convenience, not data.
      }
    });
  }

  document.addEventListener("click", async (event) => {
    const show = event.target.closest("[data-secret-show]");
    if (show) {
      event.preventDefault();
      const field = show.closest("[data-secret-field]");
      const input = field && field.querySelector("input");
      if (!field || !input) {
        return;
      }
      if (input.type === "text") {
        input.type = "password";
        input.value = "";
        show.textContent = show.dataset.showLabel || "Show";
        return;
      }
      const original = show.textContent;
      try {
        input.value = await fetchSecret(field);
        input.type = "text";
        show.textContent = show.dataset.hideLabel || "Hide";
      } catch {
        show.textContent = copiedLabel;
        window.setTimeout(() => { show.textContent = original; }, 1500);
      }
      return;
    }
    const secretCopy = event.target.closest("[data-secret-copy]");
    if (secretCopy) {
      event.preventDefault();
      const field = secretCopy.closest("[data-secret-field]");
      const original = secretCopy.textContent;
      try {
        const value = await fetchSecret(field);
        const ok = await copyText(value);
        secretCopy.textContent = ok ? copiedLabel : copyFailedLabel;
      } catch {
        secretCopy.textContent = copyFailedLabel;
      }
      window.setTimeout(() => { secretCopy.textContent = original; }, 1500);
    }
  });

  document.addEventListener("click", async (event) => {
    const button = event.target.closest("[data-copy]");
    if (!button) {
      return;
    }
    event.preventDefault();
    const text = button.getAttribute("data-copy") || "";
    const original = button.textContent;
    const ok = await copyText(text);
    button.textContent = ok ? copiedLabel : copyFailedLabel;
    window.setTimeout(() => {
      button.textContent = original;
    }, 1500);
  });

  // Off-canvas navigation on small screens. The CSS keeps the sidebar in the
  // normal flow when JavaScript is unavailable, so this only enhances it.
  const sidebar = document.querySelector("[data-sidebar]");
  const navToggles = document.querySelectorAll("[data-nav-toggle]");
  const navClosers = document.querySelectorAll("[data-nav-close]");
  const setNavOpen = (open) => {
    document.body.classList.toggle("nav-open", open);
    navToggles.forEach((toggle) => toggle.setAttribute("aria-expanded", String(open)));
    if (!sidebar) {
      return;
    }
    if (open) {
      const target = sidebar.querySelector("[data-nav-close]") || sidebar;
      if (typeof target.focus === "function") {
        target.focus({ preventScroll: true });
      }
    }
  };

  navToggles.forEach((toggle) => {
    toggle.addEventListener("click", () => {
      setNavOpen(!document.body.classList.contains("nav-open"));
    });
  });
  navClosers.forEach((closer) => {
    closer.addEventListener("click", () => setNavOpen(false));
  });
  if (sidebar) {
    sidebar.querySelectorAll("a").forEach((link) => {
      link.addEventListener("click", () => setNavOpen(false));
    });
  }
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && document.body.classList.contains("nav-open")) {
      setNavOpen(false);
    }
  });

  // Reset the drawer state if the viewport grows past the mobile breakpoint.
  const desktop = window.matchMedia("(min-width: 901px)");
  const handleViewportChange = (event) => {
    if (event.matches) {
      setNavOpen(false);
    }
  };
  if (typeof desktop.addEventListener === "function") {
    desktop.addEventListener("change", handleViewportChange);
  } else if (typeof desktop.addListener === "function") {
    desktop.addListener(handleViewportChange);
  }

  document.addEventListener("submit", (event) => {
    const message = event.target.getAttribute("data-confirm");
    if (message && !window.confirm(message)) {
      event.preventDefault();
    }
  });

  document.addEventListener("click", (event) => {
    const button = event.target.closest("button[data-confirm]");
    if (button && !window.confirm(button.getAttribute("data-confirm"))) {
      event.preventDefault();
    }
  });

  document.addEventListener("change", (event) => {
    if (event.target.matches("input[data-confirm]") && event.target.checked) {
      if (!window.confirm(event.target.getAttribute("data-confirm"))) {
        event.target.checked = false;
        return;
      }
    }
    if (!event.target.matches("[data-select-all]")) {
      return;
    }
    const checked = event.target.checked;
    const selector = event.target.getAttribute("data-select-target") || 'input[name="ids"]';
    const group = event.target.closest("form") || document;
    let boxes = group.querySelectorAll(selector);
    if (!boxes.length) {
      boxes = document.querySelectorAll(selector);
    }
    boxes.forEach((box) => {
      box.checked = checked;
    });
  });

  // Bulk actions appear only after a selection (UI-GUIDE §4.2). The list works
  // without this enhancement: every row still has its own "⋯" menu.
  const syncBulkActions = () => {
    const boxes = Array.from(document.querySelectorAll("[data-bulk-check]"));
    const selected = boxes.filter((box) => box.checked).length;
    const bar = document.querySelector("[data-bulk-actions]");
    if (bar) {
      bar.hidden = selected === 0;
    }
    document.querySelectorAll("[data-bulk-count]").forEach((node) => {
      node.textContent = String(selected);
    });
  };
  syncBulkActions();
  document.addEventListener("change", (event) => {
    if (event.target.matches("[data-bulk-check], [data-select-all]")) {
      syncBulkActions();
    }
  });

  // One definition chooser on /indexers/new: changing it reloads the form with
  // the settings generated by that definition. Without JavaScript the selected
  // value still travels with the POST and the server validates the fields.
  const definitionSelect = document.querySelector("[data-definition-select]");
  if (definitionSelect) {
    definitionSelect.addEventListener("change", () => {
      const target = new URL("/indexers/new", window.location.origin);
      target.searchParams.set("definition", definitionSelect.value);
      window.location.assign(target.pathname + target.search);
    });
  }

  const form = document.querySelector("[data-source-form]");
  if (form) {    const kind = form.querySelector("[data-kind]");
    const torznabOnly = form.querySelectorAll("[data-torznab-only]");
    const apply = () => {
      const torznab = !kind || kind.value === "torznab";
      torznabOnly.forEach((element) => {
        element.hidden = !torznab;
      });
    };
    if (kind) {
      kind.addEventListener("change", apply);
    }
    apply();

    const preset = form.querySelector("[data-preset]");
    const urlInput = form.querySelector("#url");
    const manager = form.querySelector("#manager");
    if (preset) {
      preset.addEventListener("change", () => {
        const option = preset.options[preset.selectedIndex];
        if (!option || !option.dataset.kind) {
          return;
        }
        if (kind) {
          kind.value = option.dataset.kind;
          kind.dispatchEvent(new Event("change"));
        }
        if (manager && option.dataset.manager) {
          manager.value = option.dataset.manager;
        }
        if (urlInput && option.dataset.placeholder) {
          urlInput.placeholder = option.dataset.placeholder;
        }
      });
    }
    document.addEventListener("click", (event) => {
      const button = event.target.closest("[data-detect-kind]");
      if (!button || !urlInput) {
        return;
      }
      const value = urlInput.value.toLowerCase();
      const isFeed = /(extto|ext\.to|torrentgalaxy|corsaro|\.xml|\.rss|\.atom|rss|atom|\/feed)/.test(value);
      if (kind) {
        kind.value = isFeed ? "rss" : "torznab";
        kind.dispatchEvent(new Event("change"));
      }
      if (manager) {
        manager.value = "auto";
      }
    });
  }

  // Live release search: debounce typing, abort the previous request and swap
  // only the results fragment. Without JavaScript the same form still submits
  // normally to /releases.
  const liveForm = document.querySelector("[data-live-search]");
  const liveResults = document.getElementById("releases-results");
  if (liveForm && liveResults) {
    let timer = 0;
    let controller = null;

    const runSearch = async () => {
      window.clearTimeout(timer);
      if (controller) {
        controller.abort();
      }
      controller = new AbortController();
      const params = new URLSearchParams(new FormData(liveForm));
      params.delete("page");
      liveResults.setAttribute("aria-busy", "true");
      try {
        const response = await fetch("/releases/results?" + params.toString(), {
          signal: controller.signal,
          headers: { Accept: "text/html" },
          credentials: "same-origin",
        });
        if (!response.ok) {
          throw new Error("HTTP " + response.status);
        }
        liveResults.innerHTML = await response.text();
        labelTables(liveResults);
        liveResults.setAttribute("aria-busy", "false");
        history.replaceState(null, "", "/releases?" + params.toString());
        const savedQuery = document.getElementById("saved-query");
        if (savedQuery) {
          savedQuery.value = params.toString();
        }
      } catch (error) {
        if (error.name !== "AbortError") {
          liveResults.setAttribute("aria-busy", "false");
        }
      }
    };

    const debouncedSearch = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(runSearch, 250);
    };

    liveForm.addEventListener("input", debouncedSearch);
    liveForm.addEventListener("change", runSearch);
    liveForm.addEventListener("submit", (event) => {
      event.preventDefault();
      runSearch();
    });
  }
})();

// Download client form: the protocol follows the client type until the
// operator changes it explicitly. Without JavaScript the select still works.
(() => {
  const kind = document.querySelector("[data-client-kind]");
  const protocol = document.getElementById("client_protocol");
  if (!kind || !protocol) {
    return;
  }
  const usenet = new Set(["sabnzbd", "nzbget"]);
  kind.addEventListener("change", () => {
    protocol.value = usenet.has(kind.value) ? "usenet" : "torrent";
  });
})();
