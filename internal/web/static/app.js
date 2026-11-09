// Progressive enhancement only: every page works without this file.
(() => {
  "use strict";
  document.documentElement.classList.add("js");

  // Notices arrive as ?n=…; drop it from the address bar so a bookmark or a
  // copied link does not replay the message forever.
  if (location.search.includes("n=")) {
    const u = new URL(location.href);
    u.searchParams.delete("n");
    history.replaceState(null, "", u.pathname + u.search + u.hash);
  }

  // Copy buttons next to read-only link fields.
  document.addEventListener("click", async (ev) => {
    const btn = ev.target.closest("[data-copy]");
    if (!btn) return;
    const input = document.getElementById(btn.dataset.copy);
    input.select();
    try {
      await navigator.clipboard.writeText(input.value);
    } catch {
      document.execCommand("copy");
    }
    const label = btn.textContent;
    btn.textContent = "Kopiert ✓";
    setTimeout(() => (btn.textContent = label), 1500);
  });

  // Confirmation for destructive forms and buttons.
  document.addEventListener("submit", (ev) => {
    const btn = ev.submitter;
    const msg = (btn && btn.dataset.confirm) || ev.target.dataset.confirm;
    if (msg && !confirm(msg)) ev.preventDefault();
  });

  // Switches save as soon as they change.
  for (const form of document.querySelectorAll("form[data-autosubmit]")) {
    form.addEventListener("change", () => form.requestSubmit());
  }

  // --- Markdown editor ------------------------------------------------------

  const wrap = (ta, before, after, placeholder) => {
    const { selectionStart: s, selectionEnd: e, value: v } = ta;
    const inner = v.slice(s, e) || placeholder;
    ta.setRangeText(before + inner + after, s, e, "end");
    ta.setSelectionRange(s + before.length, s + before.length + inner.length);
  };

  const list = (ta) => {
    const { selectionStart: s, selectionEnd: e, value: v } = ta;
    const start = v.lastIndexOf("\n", s - 1) + 1;
    let end = v.indexOf("\n", e);
    if (end === -1) end = v.length;
    const lines = v.slice(start, end).split("\n");
    const all = lines.every((l) => l.startsWith("- "));
    const out = lines.map((l) => (all ? l.slice(2) : l.startsWith("- ") ? l : "- " + l)).join("\n");
    ta.setRangeText(out, start, end, "select");
  };

  const link = (ta) => {
    const url = prompt("Adresse des Links (z. B. https://…)", "https://");
    if (!url || url === "https://") return;
    const { selectionStart: s, selectionEnd: e, value: v } = ta;
    const text = v.slice(s, e) || "Linktext";
    ta.setRangeText(`[${text}](${url.trim()})`, s, e, "end");
  };

  // Enter inside a list item starts the next item; Enter on an empty item
  // ends the list with a blank line, so the text after it does not get glued
  // onto the last item. That is what people expect from word processors.
  const continueList = (ev, ta) => {
    const { selectionStart: s, selectionEnd: e, value: v } = ta;
    if (s !== e) return;
    const start = v.lastIndexOf("\n", s - 1) + 1;
    const line = v.slice(start, s);
    const m = /^(\s*)([-*+]|\d+[.)])\s/.exec(line);
    if (!m) return;
    ev.preventDefault();
    if (line.trim() === m[2]) {
      ta.setRangeText("\n", start, s, "end");
      return;
    }
    const marker = /\d/.test(m[2]) ? parseInt(m[2], 10) + 1 + m[2].slice(-1) : m[2];
    ta.setRangeText("\n" + m[1] + marker + " ", s, s, "end");
  };

  const actions = {
    bold: (ta) => wrap(ta, "**", "**", "fetter Text"),
    italic: (ta) => wrap(ta, "*", "*", "kursiver Text"),
    list,
    link,
  };

  for (const ed of document.querySelectorAll(".md-editor")) {
    const bar = ed.querySelector(".md-bar");
    const ta = ed.querySelector("textarea");
    const pv = ed.querySelector(".md-preview");
    bar.hidden = false;

    const setTab = async (tab) => {
      for (const b of bar.querySelectorAll("[data-md-tab]")) {
        const on = b.dataset.mdTab === tab;
        b.classList.toggle("active", on);
        b.setAttribute("aria-pressed", on);
      }
      for (const b of bar.querySelectorAll("[data-md]")) b.disabled = tab === "preview";
      if (tab === "write") {
        pv.hidden = true;
        ta.hidden = false;
        ta.focus();
        return;
      }
      pv.style.minHeight = ta.offsetHeight + "px";
      pv.innerHTML = '<p class="muted">Vorschau wird geladen …</p>';
      ta.hidden = true;
      pv.hidden = false;
      try {
        const res = await fetch(ed.dataset.preview, {
          method: "POST",
          body: new URLSearchParams({ text: ta.value }),
        });
        if (!res.ok) throw new Error(res.status);
        // The server renders with raw HTML stripped, so this is safe.
        pv.innerHTML = await res.text();
      } catch {
        pv.innerHTML = '<p class="muted">Die Vorschau ist gerade nicht verfügbar.</p>';
      }
    };

    bar.addEventListener("click", (ev) => {
      const b = ev.target.closest("button");
      if (!b) return;
      if (b.dataset.mdTab) return setTab(b.dataset.mdTab);
      const fn = actions[b.dataset.md];
      if (fn) {
        ta.focus();
        fn(ta);
        ta.dispatchEvent(new Event("input", { bubbles: true }));
      }
    });

    ta.addEventListener("keydown", (ev) => {
      if (ev.key === "Enter" && !ev.shiftKey && !ev.ctrlKey && !ev.metaKey && !ev.isComposing) {
        continueList(ev, ta);
        return;
      }
      if (!(ev.ctrlKey || ev.metaKey)) return;
      const key = { b: "bold", i: "italic", k: "link" }[ev.key.toLowerCase()];
      if (key) {
        ev.preventDefault();
        actions[key](ta);
      }
    });

    // Submitting from the preview tab must still send the text.
    ed.closest("form")?.addEventListener("submit", () => (ta.hidden = false));
  }
})();
