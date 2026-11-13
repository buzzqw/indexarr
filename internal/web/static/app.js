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
    // Never clear a value the user already typed: the stored secret is not in
    // the field (it is only a placeholder), so clearing can only destroy the
    // new password.
    input.focus();
    change.hidden = true;
    const save = field.querySelector("[data-secret-save]");
    if (save) {
      save.hidden = false;
      save.addEventListener("click", () => {
        if (!input.form) {
          return;
        }
        if (typeof input.form.requestSubmit === "function") {
          input.form.requestSubmit();
        } else {
          input.form.submit();
        }
      });
    }
    field.classList.add("is-changing");
    // Say what to do next: the Change button does not save anything.
    if (!field.querySelector("[data-secret-ready]")) {
      const hint = document.createElement("p");
      hint.className = "help secret-ready";
      hint.setAttribute("data-secret-ready", "");
      hint.textContent = document.body.dataset.secretReady || "Now type the new value and press Save.";
      field.parentNode.insertBefore(hint, field.nextSibling);
    }
  });

  // Remember whether the explanatory paragraph at the bottom of a page was
  // open, per browser, as described in UI-GUIDE §2. The paragraph is always
  // present for screen readers; only the open state is a convenience.
  document.querySelectorAll("[data-page-help]").forEach((help) => {
    const storageKey = help.dataset.pageHelpStorage;
    if (!storageKey) {
      return;
    }
    try {
      if (window.localStorage.getItem(storageKey) === "1") {
        help.open = true;
      }
    } catch {
      // Private modes can deny storage: the closed default wins.
    }
    help.addEventListener("toggle", () => {
      try {
        window.localStorage.setItem(storageKey, help.open ? "1" : "0");
      } catch {
        // Ignore: remembering the state is not required for the page to work.
      }
    });
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

  // Catalog previews can contain hundreds of rows. The filter is a progressive
  // enhancement: without JavaScript the full table stays visible.
  document.querySelectorAll("[data-filter-table]").forEach((input) => {
    const table = document.querySelector(input.getAttribute("data-filter-table"));
    const body = table && table.tBodies ? table.tBodies[0] : null;
    if (!body) {
      return;
    }
    const rows = Array.from(body.rows);
    input.addEventListener("input", () => {
      const query = input.value.trim().toLowerCase();
      rows.forEach((row) => {
        row.hidden = query !== "" && !row.textContent.toLowerCase().includes(query);
      });
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

  // Copy every magnet of the selected releases in one action. The per-row
  // button already exists; this only saves opening each row. The label comes
  // from the page so the JavaScript never hardcodes a language.
  const bulkCopyMagnets = document.querySelector("[data-bulk-copy-magnets]");
  if (bulkCopyMagnets) {
    bulkCopyMagnets.addEventListener("click", async () => {
      const magnets = Array.from(document.querySelectorAll("input[name='ids'][data-magnet]"))
        .filter((box) => box.checked && box.dataset.magnet)
        .map((box) => box.dataset.magnet);
      const original = bulkCopyMagnets.textContent;
      const restore = () => {
        window.setTimeout(() => {
          bulkCopyMagnets.textContent = original;
        }, 1800);
      };
      if (!magnets.length) {
        bulkCopyMagnets.textContent = bulkCopyMagnets.dataset.emptyLabel || original;
        restore();
        return;
      }
      const ok = await copyText(magnets.join("\n"));
      bulkCopyMagnets.textContent = ok
        ? bulkCopyMagnets.dataset.copiedLabel || original
        : bulkCopyMagnets.dataset.failedLabel || original;
      restore();
    });
  }

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

  // Filter the rows already returned by the search. It never asks the server
  // and keeps working after the results fragment is replaced.
  const resultsContainer = document.getElementById("releases-results");
  if (resultsContainer) {
    const applyResultFilter = (input) => {
      const needle = input.value.trim().toLowerCase();
      const rows = Array.from(resultsContainer.querySelectorAll(".releases-table tbody tr"))
        .filter((row) => row.querySelector(".release-title-link"));
      let shown = 0;
      rows.forEach((row) => {
        const match = !needle || row.textContent.toLowerCase().includes(needle);
        row.hidden = !match;
        if (match) {
          shown++;
        }
      });
      const counter = resultsContainer.querySelector("[data-results-filter-count]");
      if (counter) {
        counter.textContent = needle ? shown + " / " + rows.length : "";
      }
      const empty = resultsContainer.querySelector("[data-results-filter-empty]");
      if (empty) {
        empty.hidden = !needle || shown > 0 || rows.length === 0;
      }
    };
    // Filtering a large table on every key press forces one synchronous
    // layout pass per character. A short debounce keeps typing responsive;
    // the first render is still immediate.
    let filterTimer = 0;
    document.addEventListener("input", (event) => {
      const input = event.target.closest("[data-results-filter]");
      if (input) {
        window.clearTimeout(filterTimer);
        filterTimer = window.setTimeout(() => applyResultFilter(input), 120);
      }
    });
    const initial = resultsContainer.querySelector("[data-results-filter]");
    if (initial) {
      applyResultFilter(initial);
    }
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
      const source = liveForm.querySelector('input[name="source"]:checked');
      if (source && source.value !== "catalog") {
        return;
      }
      window.clearTimeout(timer);
      if (controller) {
        controller.abort();
      }
      controller = new AbortController();
      const params = new URLSearchParams(new FormData(liveForm));
      params.delete("page");
      params.delete("csrf");
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
      const source = liveForm.querySelector('input[name="source"]:checked');
      if (source && source.value !== "catalog") {
        // The live search must reach the server: it stores what it finds.
        return;
      }
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
  const protocolFor = { sabnzbd: "usenet", nzbget: "usenet", amule: "ed2k", usenetblackhole: "usenet", nzbvortex: "usenet" };
  const fields = {
    name: document.getElementById("client_name"),
    url: document.getElementById("client_url"),
    username: document.getElementById("client_username"),
    category: document.getElementById("client_category"),
    savePath: document.getElementById("client_save_path"),
  };
  const form = kind.form;
  let autoName = Boolean(form && form.dataset.clientAutoName === "1");
  if (fields.name) {
    fields.name.addEventListener("input", () => {
      autoName = false;
    });
  }
  const applyKind = () => {
    const option = kind.options[kind.selectedIndex];
    if (!option) {
      return;
    }
    if (fields.name && autoName) {
      fields.name.value = option.dataset.clientName || "";
    }
    if (fields.name) {
      fields.name.placeholder = option.dataset.clientName || "";
    }
    if (fields.url) {
      fields.url.placeholder = option.dataset.clientAddress || "";
    }
    if (fields.username) {
      fields.username.placeholder = option.dataset.clientUsername || "";
    }
    if (fields.category) {
      fields.category.placeholder = option.dataset.clientCategory || "";
    }
    if (fields.savePath) {
      fields.savePath.placeholder = option.dataset.clientSavePath || "";
    }
  };
  kind.addEventListener("change", () => {
    protocol.value = protocolFor[kind.value] || "torrent";
    applyKind();
  });
  applyKind();
})();

// Download client form: test the values without leaving the form, so a typed
// password is not lost. Without JavaScript the same button submits the form
// and the server renders the same result on the page.
(() => {
  const button = document.querySelector("[data-client-test]");
  const status = document.querySelector("[data-client-test-status]");
  if (!button || !status || !button.form) {
    return;
  }
  button.addEventListener("click", async (event) => {
    const form = button.form;
    if (!form.reportValidity()) {
      return;
    }
    event.preventDefault();
    status.hidden = false;
    status.classList.remove("ok", "err");
    status.textContent = button.dataset.pending || "";
    try {
      const response = await fetch(button.getAttribute("formaction") || "/download-clients/test", {
        method: "POST",
        // URL-encoded, not multipart: the CSRF middleware reads the token from
        // the body of url-encoded posts.
        body: new URLSearchParams(new FormData(form)),
        headers: {
          "Content-Type": "application/x-www-form-urlencoded",
          "X-Requested-With": "fetch",
          Accept: "application/json",
        },
        credentials: "same-origin",
      });
      const payload = await response.json().catch(() => null);
      if (!payload) {
        throw new Error("HTTP " + response.status);
      }
      status.textContent = payload.message || "";
      status.classList.add(payload.ok ? "ok" : "err");
    } catch (error) {
      status.textContent = button.dataset.failed || "";
      status.classList.add("err");
    }
  });
})();

// Per-source refresh interval: the list of common choices only fills the
// number field that is actually submitted. Without JavaScript the field still
// accepts any value, so the form never depends on this enhancement.
(() => {
  document.querySelectorAll("[data-interval-editor]").forEach((editor) => {
    const picker = editor.querySelector("[data-interval-picker]");
    const input = editor.querySelector('input[name="interval_seconds"]');
    const preview = editor.querySelector("[data-interval-preview]");
    if (!picker || !input) {
      return;
    }
    const syncFromInput = () => {
      const value = String(Number(input.value || "0"));
      const option = Array.from(picker.options).find((item) => item.value === value && item.value !== "custom");
      picker.value = option ? value : "custom";
      if (preview) {
        preview.textContent = option ? option.textContent : "";
      }
    };
    picker.addEventListener("change", () => {
      if (picker.value === "custom") {
        input.focus();
        if (typeof input.select === "function") {
          input.select();
        }
        return;
      }
      input.value = picker.value;
      syncFromInput();
    });
    input.addEventListener("input", syncFromInput);
    syncFromInput();
  });
})();

// Dropdown filters apply as soon as a value changes; the form still has a
// submit button for browsers without JavaScript.
(() => {
  document.querySelectorAll("select[data-auto-submit]").forEach((select) => {
    select.addEventListener("change", () => {
      if (select.form) {
        select.form.submit();
      }
    });
  });
})();

// Native tooltips for every control. Templates may already carry an explicit
// title; when they do not, the nearest help paragraph of the field explains the
// control, then its label, then the button text. One pass keeps the whole
// interface consistent without repeating the same help in every template.
(() => {
  const controls = document.querySelectorAll(
    'input:not([type="hidden"]), select, textarea, button'
  );
  // Read every label text once. Searching the whole document for each
  // control was quadratic on the settings page and forced one layout read
  // per input.
  const labelTextByFor = new Map();
  document.querySelectorAll("label[for]").forEach((label) => {
    if (!labelTextByFor.has(label.htmlFor)) {
      labelTextByFor.set(label.htmlFor, label.textContent.trim());
    }
  });
  const labelTextFor = (root, controlId) => {
    if (!controlId) {
      return "";
    }
    if (root === document) {
      return labelTextByFor.get(controlId) || "";
    }
    const label = Array.from(root.querySelectorAll("label")).find(
      (candidate) => candidate.htmlFor === controlId
    );
    return label ? label.textContent.trim() : "";
  };
  controls.forEach((control) => {
    if (control.title) {
      return;
    }
    const id = control.id;
    const labelled = labelTextFor(document, id);
    if (control.type === "checkbox" || control.type === "radio") {
      const wrapping = control.closest("label");
      const text = wrapping ? wrapping.textContent.trim() : labelled;
      if (text) {
        control.title = text;
      }
      return;
    }
    let text = "";
    const field = control.closest(".field, .actions-field, .copy-field, .secret-field, form");
    if (field) {
      const help = field.querySelector(".help");
      if (help) {
        text = help.textContent.trim();
      }
      if (!text && id) {
        text = labelTextFor(field, id);
      }
    }
    if (!text) {
      text = labelled;
    }
    if (!text && control.tagName === "BUTTON") {
      text = control.textContent.trim();
    }
    if (text) {
      control.title = text;
    }
  });
  document.querySelectorAll("a.btn, .badge, .state").forEach((node) => {
    if (!node.title && node.textContent.trim()) {
      node.title = node.textContent.trim();
    }
  });
})();

// The indexer form's section index links to an Advanced block that is closed
// by default: opening it when the hash points there makes the link useful.
(() => {
  const advanced = document.getElementById("form-advanced");
  if (!advanced) {
    return;
  }
  const openAdvanced = () => {
    if (window.location.hash === "#form-advanced") {
      advanced.open = true;
    }
  };
  window.addEventListener("hashchange", openAdvanced);
  openAdvanced();
})();

// Every form answers its button at once. The button keeps its name and value
// (they are part of the POST) but shows "Working…" until the next page.
(() => {
  const workingLabel = document.body.dataset.workingLabel || "Working…";
  document.addEventListener("submit", (event) => {
    if (event.defaultPrevented) {
      return;
    }
    const form = event.target;
    if (!(form instanceof HTMLFormElement)) {
      return;
    }
    if (form.dataset.submitting === "1") {
      event.preventDefault();
      return;
    }
    form.dataset.submitting = "1";
    const submitter = event.submitter;
    if (submitter && submitter.tagName === "BUTTON") {
      submitter.dataset.originalLabel = submitter.textContent;
      submitter.textContent = workingLabel;
      submitter.setAttribute("aria-busy", "true");
    }
  });
  // Back/forward cache: restore the labels if the page comes back.
  window.addEventListener("pageshow", () => {
    document.querySelectorAll("form[data-submitting]").forEach((form) => {
      delete form.dataset.submitting;
      form.querySelectorAll('button[aria-busy="true"]').forEach((button) => {
        if (button.dataset.originalLabel) {
          button.textContent = button.dataset.originalLabel;
        }
        button.removeAttribute("aria-busy");
      });
    });
  });
})();

// The explicit colour theme. The inline script in <head> already applies a
// stored choice before the first paint; this keeps the picker and the meta
// colour in sync and saves the next choice.
(() => {
  const select = document.querySelector("[data-theme-select]");
  if (!select) {
    return;
  }
  const key = "indexarr.theme";
  const valid = new Set(["auto", "light", "dark", "contrast"]);
  let stored = "auto";
  try {
    stored = window.localStorage.getItem(key) || "auto";
  } catch (error) {
    stored = "auto";
  }
  if (!valid.has(stored)) {
    stored = "auto";
  }
  const apply = (value) => {
    if (value === "auto") {
      delete document.documentElement.dataset.theme;
    } else {
      document.documentElement.dataset.theme = value;
    }
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) {
      meta.setAttribute("content", value === "light" ? "#f4f6f9" : value === "contrast" ? "#000000" : "#0e1116");
    }
  };
  apply(stored);
  select.value = stored;
  select.addEventListener("change", () => {
    const value = valid.has(select.value) ? select.value : "auto";
    try {
      if (value === "auto") {
        window.localStorage.removeItem(key);
      } else {
        window.localStorage.setItem(key, value);
      }
    } catch (error) {}
    apply(value);
  });
})();

// Keyboard shortcuts. They are deliberately limited to navigation and to
// actions that already have a visible button, so the page stays usable
// without JavaScript and no shortcut can submit a form by accident.
(() => {
  const dialog = document.querySelector("[data-shortcuts-dialog]");
  if (!dialog) {
    return;
  }
  let lastFocus = null;
  let focusedRow = null;

  const restoreFocus = () => {
    if (lastFocus && typeof lastFocus.focus === "function") {
      lastFocus.focus({ preventScroll: true });
    }
    lastFocus = null;
  };
  const openHelp = () => {
    lastFocus = document.activeElement;
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    const close = dialog.querySelector("button, a, input, select, textarea");
    if (close) {
      close.focus({ preventScroll: true });
    }
  };
  const closeHelp = () => {
    if (typeof dialog.close === "function") {
      if (dialog.open) {
        dialog.close();
      } else {
        restoreFocus();
      }
    } else {
      dialog.removeAttribute("open");
      restoreFocus();
    }
  };
  document.querySelectorAll("[data-shortcuts-open]").forEach((button) => {
    button.addEventListener("click", openHelp);
  });
  dialog.addEventListener("click", (event) => {
    if (event.target === dialog) {
      closeHelp();
    }
  });
  dialog.addEventListener("close", restoreFocus);

  const isTyping = (target) =>
    target instanceof HTMLElement &&
    (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName));

  const tableRows = () =>
    Array.from(document.querySelectorAll("main.content table tbody tr")).filter(
      (row) => !row.hidden && row.querySelector("td")
    );

  const moveRow = (delta) => {
    if (focusedRow && !focusedRow.isConnected) {
      focusedRow = null;
    }
    const rows = tableRows();
    if (!rows.length) {
      return;
    }
    let index = focusedRow ? rows.indexOf(focusedRow) : -1;
    if (index === -1) {
      index = delta > 0 ? -1 : rows.length;
    }
    index = Math.max(0, Math.min(rows.length - 1, index + delta));
    if (focusedRow) {
      focusedRow.classList.remove("row-focus");
    }
    focusedRow = rows[index];
    focusedRow.classList.add("row-focus");
    focusedRow.scrollIntoView({ block: "nearest" });
  };

  const toggleRow = () => {
    if (!focusedRow || focusedRow.hidden || !focusedRow.isConnected) {
      moveRow(1);
      return;
    }
    const box = focusedRow.querySelector(
      'td.check input[type="checkbox"], input[data-bulk-check]'
    );
    if (box) {
      box.click();
    }
  };

  const focusSearch = () => {
    const input = document.getElementById("q") || document.getElementById("global-search");
    if (!input) {
      return;
    }
    input.focus();
    if (typeof input.select === "function") {
      input.select();
    }
  };

  const repeatSearch = () => {
    const form = document.querySelector("[data-live-search]");
    if (!form) {
      return;
    }
    if (typeof form.requestSubmit === "function") {
      form.requestSubmit();
    } else {
      form.submit();
    }
  };

  document.addEventListener("keydown", (event) => {
    if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) {
      return;
    }
    if (dialog.open && event.key === "Escape") {
      closeHelp();
      return;
    }
    if (dialog.open) {
      return;
    }
    if (isTyping(event.target)) {
      return;
    }
    switch (event.key) {
      case "/":
        event.preventDefault();
        focusSearch();
        break;
      case "?":
        event.preventDefault();
        openHelp();
        break;
      case "j":
        event.preventDefault();
        moveRow(1);
        break;
      case "k":
        event.preventDefault();
        moveRow(-1);
        break;
      case "x":
        event.preventDefault();
        toggleRow();
        break;
      case "r":
        event.preventDefault();
        repeatSearch();
        break;
      default:
        break;
    }
  });
})();
