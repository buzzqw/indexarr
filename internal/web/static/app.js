// indexarr UI: progressive enhancement only. Every action also works without
// JavaScript, except the copy shortcuts (which degrade to selecting the field).
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").catch(() => {});
}

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

  document.addEventListener("click", async (event) => {
    const button = event.target.closest("[data-copy]");
    if (!button) {
      return;
    }
    event.preventDefault();
    const text = button.getAttribute("data-copy") || "";
    const original = button.textContent;
    const ok = await copyText(text);
    button.textContent = ok ? "Copied" : "Copy failed";
    window.setTimeout(() => {
      button.textContent = original;
    }, 1500);
  });

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
    if (!event.target.matches("[data-select-all]")) {
      return;
    }
    const checked = event.target.checked;
    document.querySelectorAll('input[name="ids"]').forEach((box) => {
      box.checked = checked;
    });
  });

  const form = document.querySelector("[data-source-form]");
  if (form) {
    const kind = form.querySelector("[data-kind]");
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
