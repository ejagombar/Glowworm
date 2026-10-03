/* ============================================================
   Glowworm — app glue (server-rendered UI + HTMX)
   Theme, privacy, shortcuts, modals/drawer, composer, widget
   edit-mode, chat streaming client (docs/design/04 §4).
   No mock data; the server owns all state.
   ============================================================ */

/* ---------- Theme (prototype logic, verbatim) ---------- */
function applyTheme(t) {
  const dark = t === "dark" || (t === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
  const sel = document.getElementById("themeSelect");
  if (sel && sel.value !== t) sel.value = t;
}
function initTheme() {
  let t = "system";
  try { t = localStorage.getItem("glowworm-theme") || "system"; } catch (e) {}
  applyTheme(t);
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    let cur = "system";
    try { cur = localStorage.getItem("glowworm-theme") || "system"; } catch (e) {}
    if (cur === "system") applyTheme("system");
  });
}

/* ---------- Toast ---------- */
let toastTimer;
function toast(msg) {
  const el = document.getElementById("toast");
  if (!el) return;
  el.hidden = false;
  el.innerHTML = `<svg class="icon"><use href="#i-check"/></svg> ${esc(msg)}`;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (el.hidden = true), 2600);
}
function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

/* ---------- Modals / drawer ---------- */
function openModal(id) {
  const m = document.getElementById(id);
  if (!m) return;
  m.hidden = false;
  const field = m.querySelector("input:not([type=hidden]), select, textarea");
  if (field) field.focus();
}
function closeModal(id) {
  const m = document.getElementById(id);
  if (m) m.hidden = true;
}
function showConfirm(title, message, okLabel, callback) {
  document.getElementById("confirmTitle").textContent = title;
  document.getElementById("confirmMessage").textContent = message;
  document.getElementById("confirmOk").textContent = okLabel || "Delete";
  confirmCallback = callback;
  document.getElementById("confirmModal").hidden = false;
}
let confirmCallback = null;

/* ---------- Widget drawer ---------- */
function openDrawer(id) {
  const card = document.querySelector(`[data-widget-id="${id}"]`);
  if (!card) return;
  state.editingId = id;
  document.getElementById("wTitle").value = card.dataset.widgetTitle || "";
  document.getElementById("wType").value = card.dataset.widgetType || "bar";
  document.getElementById("wPinned").checked = card.dataset.widgetPinned === "1";
  document.getElementById("widgetDrawer").hidden = false;
  document.getElementById("drawerBackdrop").hidden = false;
  requestAnimationFrame(() => {
    document.getElementById("widgetDrawer").classList.add("show");
    document.getElementById("drawerBackdrop").classList.add("show");
  });
}
function closeDrawer() {
  const d = document.getElementById("widgetDrawer"), b = document.getElementById("drawerBackdrop");
  if (!d) return;
  d.classList.remove("show"); b.classList.remove("show");
  setTimeout(() => { d.hidden = true; b.hidden = true; }, 220);
  state.editingId = null;
}

/* ---------- State ---------- */
const state = {
  editMode: false,
  editingId: null,
  balanceView: "all",
  privacy: false,
  customFrom: null,
  customTo: null,
};

/* ---------- Pinned grid edit mode (drag-swap + resize, prototype logic) ---------- */
const GRID_GAP = 12, GRID_ROW = 72;

function setEditMode(on) {
  state.editMode = on;
  const grid = document.getElementById("pinnedGrid");
  if (grid) grid.classList.toggle("editing", on);
  const btn = document.getElementById("pinnedEditBtn");
  const hint = document.getElementById("pinnedHint");
  if (btn) {
    btn.innerHTML = on
      ? '<svg class="icon"><use href="#i-check"/></svg> Done'
      : '<svg class="icon"><use href="#i-settings"/></svg> Edit';
  }
  if (hint) hint.textContent = on ? "Drag onto another widget to swap · drag the edges to resize" : "Saved from your questions";
}

function persistWidgets(order) {
  fetch("/api/v1/widgets/pinned", {
    method: "PUT",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify({ items: order }),
  }).then((r) => { if (!r.ok) toast("Could not save layout"); }).catch(() => toast("Could not save layout"));
}

function pinnedOrder() {
  const grid = document.getElementById("pinnedGrid");
  if (!grid) return [];
  return Array.from(grid.querySelectorAll("[data-widget-id]")).map((card) => ({
    id: card.dataset.widgetId,
    col: Number(card.dataset.widgetCol || 8),
    rows: Number(card.dataset.widgetRows || 4),
    position: Array.from(grid.children).indexOf(card),
  }));
}

function clamp(n, min, max) { return Math.max(min, Math.min(max, n)); }

/* ---------- Chat streaming client (doc 10 §8) ---------- */
function csrfToken() {
  const c = document.cookie.match(/(?:^|; )glowworm_csrf=([^;]*)/);
  return c ? decodeURIComponent(c[1]) : "";
}

function sendChat(text) {
  text = (text || "").trim();
  if (!text) return;
  const box = document.getElementById("queryMessages");
  const convEl = document.querySelector("[data-conversation-id]");
  const conversationID = convEl ? convEl.dataset.conversationId : "";

  // User bubble + pending assistant with worm loader (prototype choreography).
  const day = document.createElement("div");
  day.className = "msg-day";
  day.textContent = "Today";
  const user = document.createElement("div");
  user.className = "msg user msg-in";
  user.innerHTML = `<div class="msg-content"><div class="msg-bubble">${esc(text)}</div></div>`;
  const pending = document.createElement("div");
  pending.className = "msg assistant msg-in";
  pending.innerHTML = `<div class="msg-content">${wormLoaderHTML()}</div>`;
  box.appendChild(day); box.appendChild(user); box.appendChild(pending);
  box.scrollTop = box.scrollHeight;

  fetch("/api/v1/query/stream", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify({ text, conversation_id: conversationID }),
  }).then(async (resp) => {
    if (!resp.ok || !resp.body) {
      finishAssistant(pending, "Sorry — the query engine is unavailable right now.", [], null);
      return;
    }
    const reader = resp.body.getReader();
    const dec = new TextDecoder();
    let buf = "", textOut = "", steps = [], widget = null;
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let idx;
      while ((idx = buf.indexOf("\n")) >= 0) {
        const line = buf.slice(0, idx).trim();
        buf = buf.slice(idx + 1);
        if (!line) continue;
        let ev;
        try { ev = JSON.parse(line); } catch (e) { continue; }
        if (ev.event === "steps") { steps = steps.concat(ev.items || []); renderSteps(pending, steps); }
        else if (ev.event === "text") { textOut = ev.text || textOut; }
        else if (ev.event === "widget" && ev.widget) { widget = ev.widget; }
        else if (ev.event === "error") { textOut = ev.message || "Query failed."; }
      }
    }
    finishAssistant(pending, textOut, steps, widget);
  }).catch(() => {
    finishAssistant(pending, "Sorry — the query engine is unavailable right now.", [], null);
  });
}

function renderSteps(pending, steps) {
  const reason = pending.querySelector(".msg-reason");
  if (!reason) return;
  reason.querySelector(".reason-steps").innerHTML = steps
    .map((s) => `<div class="reason-step"><span class="tick"><svg class="icon"><use href="#i-check"/></svg></span><span>${esc(s)}</span></div>`)
    .join("");
}

function finishAssistant(pending, text, steps, widget) {
  const loader = pending.querySelector(".glowworm-loader");
  const reveal = () => {
    let content = "";
    if (text) content += `<div class="msg-text">${esc(text)}</div>`;
    if (steps && steps.length) content += msgReasonHTML(steps);
    if (widget) content += widgetCardHTML(widget);
    pending.innerHTML = `<div class="msg-content">${content}</div>`;
    if (widget) {
      const host = pending.querySelector(".chart-host[data-chart-type]");
      if (host && window.drawChartHosts) drawChartHosts(pending);
    }
    const box = document.getElementById("queryMessages");
    if (box) box.scrollTop = box.scrollHeight;
  };
  if (loader && !matchMedia("(prefers-reduced-motion: reduce)").matches) {
    loader.classList.add("worm-out");
    setTimeout(reveal, 320);
  } else {
    reveal();
  }
}

function wormLoaderHTML() {
  return `<div class="glowworm-loader" role="status" aria-label="Glowworm is thinking">
    <span class="sr-only">Glowworm is thinking…</span>
    <svg class="glowworm" viewBox="48 14 94 44" preserveAspectRatio="xMidYMid meet" aria-hidden="true">
      <g class="glow-worm">
        <g class="glow-body">
          <circle class="seg seg-tail" cx="58" cy="44.2" r="6" fill="#7dffb8"/>
          <circle class="seg seg-5" cx="67" cy="42.6" r="8" fill="#2cc57a"/>
          <circle class="seg seg-4" cx="78.5" cy="41.6" r="10" fill="#27b268"/>
          <circle class="seg seg-3" cx="92" cy="40.9" r="11.5" fill="#22a75e"/>
          <circle class="seg seg-2" cx="106.5" cy="40.3" r="13" fill="#1f9b57"/>
          <g class="glow-head">
            <circle cx="122" cy="40" r="14" fill="#1f9b57"/>
            <path d="M119.5 27.7 q-3 -6.6 4.1 -8.6" stroke="#1f9b57" stroke-width="2.5" fill="none" stroke-linecap="round"/>
            <circle cx="123.7" cy="20.2" r="2.1" fill="#7dffb8"/>
            <path d="M126.9 27.7 q2.6 -5.9 9.2 -6.6" stroke="#1f9b57" stroke-width="2.5" fill="none" stroke-linecap="round"/>
            <circle cx="135.8" cy="22.9" r="2.1" fill="#7dffb8"/>
            <circle cx="118.1" cy="36.1" r="3.45" fill="#ffffff"/>
            <circle cx="127.6" cy="36.1" r="3.45" fill="#ffffff"/>
            <circle cx="118.7" cy="36.7" r="1.9" fill="#0c3a22"/>
            <circle cx="128.3" cy="36.7" r="1.9" fill="#0c3a22"/>
            <path d="M120.4 46.6 q4.9 3.3 9.9 0" stroke="#0c3a22" stroke-width="2.1" fill="none" stroke-linecap="round"/>
          </g>
        </g>
      </g>
    </svg>
  </div>`;
}

function msgReasonHTML(steps) {
  return `<details class="msg-reason"><summary>How I worked this out</summary><div class="reason-steps">${
    steps.map((s) => `<div class="reason-step"><span class="tick"><svg class="icon"><use href="#i-check"/></svg></span><span>${esc(s)}</span></div>`).join("")
  }</div></details>`;
}
function widgetCardHTML(widget) {
  const el = document.createElement("article");
  el.className = "widget span-12";
  el.dataset.widgetId = widget.id || "";
  el.dataset.widgetType = widget.type || "";
  el.dataset.widgetTitle = widget.title || "";
  el.innerHTML = `
    <div class="widget-head">
      <div class="widget-title">${esc(widget.title || "Widget")}</div>
      <div class="widget-actions">
        <button class="widget-btn" data-action="toggle-pin" title="Pin to dashboard"><svg class="icon"><use href="#i-pin"/></svg></button>
        <button class="widget-btn" data-action="edit-widget" title="Widget settings"><svg class="icon"><use href="#i-settings"/></svg></button>
      </div>
    </div>
    <div class="widget-body">${widgetBodyHTML(widget)}</div>`;
  return el.outerHTML;
}
function widgetBodyHTML(widget) {
  const data = widget.data || {};
  if (widget.type === "number") {
    return `<div class="num-widget"><div class="num-value">${esc(data.value)}</div>
      ${data.caption ? `<div class="num-caption">${esc(data.caption)}</div>` : ""}</div>`;
  }
  if (widget.type === "text") return `<div class="text-widget"><p>${esc(data.body)}</p></div>`;
  if (widget.type === "table") {
    return `<div class="table-widget"><table><thead><tr>${(data.columns || []).map((c) => `<th>${esc(c)}</th>`).join("")}</tr></thead>
      <tbody>${(data.rows || []).map((r) => `<tr>${r.map((c, i) => `<td class="${i === r.length - 1 ? "num" : ""}">${esc(c)}</td>`).join("")}</tr>`).join("")}</tbody></table></div>`;
  }
  if (widget.type === "pie" && window.pieChartSVG) {
    const p = pieChartSVG(data);
    return `<div class="pie-wrap"><div class="pie-chart">${p.svg}</div>${p.legend}</div>`;
  }
  if (widget.type === "line" && window.lineChartSVG) {
    return `<div class="chart-host" data-chart-type="line" data-chart-data="${esc(JSON.stringify(data))}"></div>`;
  }
  if (window.barChartSVG) {
    return `<div class="chart-host" data-chart-type="bar" data-chart-data="${esc(JSON.stringify(data))}"></div>`;
  }
  return "";
}

/* ---------- Boot ---------- */
document.addEventListener("DOMContentLoaded", () => {
  initTheme();

  // Privacy toggle.
  const privacyBtn = document.getElementById("privacyToggle");
  if (privacyBtn) {
    privacyBtn.addEventListener("click", () => {
      state.privacy = !state.privacy;
      document.body.classList.toggle("privacy", state.privacy);
      privacyBtn.classList.toggle("active", state.privacy);
      privacyBtn.title = state.privacy ? "Show balances" : "Hide balances";
      privacyBtn.innerHTML = state.privacy
        ? '<svg class="icon"><use href="#i-eye-off"/></svg>'
        : '<svg class="icon"><use href="#i-eye"/></svg>';
    });
  }

  // Theme select.
  const themeSel = document.getElementById("themeSelect");
  if (themeSel) {
    themeSel.addEventListener("change", (e) => {
      try { localStorage.setItem("glowworm-theme", e.target.value); } catch (err) {}
      applyTheme(e.target.value);
      toast("Theme updated");
    });
  }

  // Charts on load + resize.
  if (window.drawChartHosts) drawChartHosts(document);

  // Brand wiggle.
  const brand = document.getElementById("brandMark");
  if (brand) {
    let timer;
    brand.addEventListener("click", (e) => {
      e.preventDefault();
      brand.classList.remove("wiggle");
      void brand.offsetWidth;
      brand.classList.add("wiggle");
      clearTimeout(timer);
      timer = setTimeout(() => brand.classList.remove("wiggle"), 1000);
    });
  }

  // Confirm modal.
  const confirmOk = document.getElementById("confirmOk");
  const confirmCancel = document.getElementById("confirmCancel");
  if (confirmOk) confirmOk.addEventListener("click", () => { const cb = confirmCallback; confirmCallback = null; document.getElementById("confirmModal").hidden = true; if (cb) cb(); });
  if (confirmCancel) confirmCancel.addEventListener("click", () => { confirmCallback = null; document.getElementById("confirmModal").hidden = true; });
  document.querySelectorAll("[data-action='close-confirm']").forEach((b) => b.addEventListener("click", () => { confirmCallback = null; document.getElementById("confirmModal").hidden = true; }));

  // Drawer.
  document.getElementById("drawerBackdrop")?.addEventListener("click", closeDrawer);
  document.querySelectorAll("[data-action='close-drawer']").forEach((b) => b.addEventListener("click", closeDrawer));
  const wApply = document.getElementById("wApply");
  if (wApply) {
    wApply.addEventListener("click", () => {
      const id = state.editingId;
      const card = document.querySelector(`[data-widget-id="${id}"]`);
      if (!card) { closeDrawer(); return; }
      const newType = document.getElementById("wType").value;
      const newTitle = document.getElementById("wTitle").value.trim() || card.dataset.widgetTitle;
      const shouldPin = document.getElementById("wPinned").checked;
      const dataset = card.dataset.widgetDataset ? JSON.parse(card.dataset.widgetDataset) : null;
      let data = card.dataset.widgetData ? JSON.parse(card.dataset.widgetData) : null;
      if (dataset && window.convertWidgetData) {
        data = convertWidgetData(card.dataset.widgetType, newType, data);
      }
      fetch(`/api/v1/widgets/pinned/${id}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ title: newTitle, type: newType, pinned: shouldPin, data, dataset }),
      }).then((r) => {
        if (!r.ok) { toast("Could not update widget"); return; }
        closeDrawer();
        toast("Widget updated");
        if (window.htmx) htmx.trigger(document.body, "widgets-changed");
        else location.reload();
      }).catch(() => toast("Could not update widget"));
    });
  }

  // Widget actions (delegated).
  document.addEventListener("click", (e) => {
    const edit = e.target.closest("[data-action='edit-widget']");
    if (edit) {
      const card = edit.closest("[data-widget-id]");
      if (card) openDrawer(card.dataset.widgetId);
      return;
    }
    const pin = e.target.closest("[data-action='toggle-pin']");
    if (pin) {
      const card = pin.closest("[data-widget-id]");
      if (card && card.dataset.widgetDataset) {
        fetch("/api/v1/widgets/pinned", {
          method: "POST",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
          body: JSON.stringify({
            title: card.dataset.widgetTitle, type: card.dataset.widgetType,
            dataset: JSON.parse(card.dataset.widgetDataset),
            plan: card.dataset.widgetPlan ? JSON.parse(card.dataset.widgetPlan) : null,
          }),
        }).then((r) => {
          if (!r.ok) { toast("Could not pin widget"); return; }
          pin.classList.add("pinned-on");
          toast("Pinned to dashboard");
        }).catch(() => toast("Could not pin widget"));
      }
    }
  });

  // Pinned grid edit mode (drag-swap + resize) — prototype pointer logic.
  const pinnedEditBtn = document.getElementById("pinnedEditBtn");
  if (pinnedEditBtn) {
    pinnedEditBtn.addEventListener("click", () => setEditMode(!state.editMode));
    let move = null, resize = null;

    document.addEventListener("pointerdown", (e) => {
      if (!state.editMode) return;
      const grid = document.getElementById("pinnedGrid");
      if (!grid) return;
      if (e.target.closest(".resize-handle")) {
        const handle = e.target.closest(".resize-handle");
        const card = handle.closest("[data-widget-id]");
        resize = {
          id: card.dataset.widgetId, dir: handle.dataset.resizeDir,
          startX: e.clientX, startY: e.clientY,
          col: Number(card.dataset.widgetCol || 8), rows: Number(card.dataset.widgetRows || 4),
          colWidth: (grid.clientWidth - 11 * GRID_GAP) / 12, rowStep: GRID_ROW + GRID_GAP, card,
        };
        try { handle.setPointerCapture(e.pointerId); } catch (err) {}
        document.body.classList.add("resizing");
        return;
      }
      if (e.target.closest(".widget-btn")) return;
      const card = e.target.closest("[data-widget-id]");
      if (!card || !grid.contains(card)) return;
      e.preventDefault();
      const rect = card.getBoundingClientRect();
      const ghost = card.cloneNode(true);
      ghost.classList.add("drag-ghost");
      ghost.style.width = rect.width + "px";
      ghost.style.height = rect.height + "px";
      ghost.style.left = rect.left + "px";
      ghost.style.top = rect.top + "px";
      document.body.appendChild(ghost);
      move = { id: card.dataset.widgetId, startX: e.clientX, startY: e.clientY, card, ghost, target: null, moved: false };
      card.classList.add("dragging");
      document.body.classList.add("dragging-widget");
    });

    window.addEventListener("pointermove", (e) => {
      if (resize) {
        e.preventDefault();
        const dx = e.clientX - resize.startX, dy = e.clientY - resize.startY;
        const c = clamp(resize.col + Math.round(dx / resize.colWidth), 2, 12);
        const rw = clamp(resize.rows + Math.round(dy / resize.rowStep), 1, 8);
        resize.card.style.gridColumn = "span " + c;
        resize.card.style.gridRow = "span " + rw;
        resize.card.dataset.widgetCol = c;
        resize.card.dataset.widgetRows = rw;
        if (window.drawChartHosts) drawChartHosts(resize.card);
        return;
      }
      if (!move) return;
      if (!move.moved && Math.hypot(e.clientX - move.startX, e.clientY - move.startY) < 5) return;
      move.moved = true;
      e.preventDefault();
      move.ghost.style.transform = `translate(${e.clientX - move.startX}px, ${e.clientY - move.startY}px)`;
      move.ghost.style.pointerEvents = "none";
      const under = document.elementFromPoint(e.clientX, e.clientY);
      move.ghost.style.pointerEvents = "";
      const target = under ? under.closest("[data-widget-id]") : null;
      if (move.target && move.target !== target) move.target.classList.remove("drop-target");
      const grid = document.getElementById("pinnedGrid");
      if (target && target.dataset.widgetId !== move.id && grid.contains(target)) {
        target.classList.add("drop-target");
        move.target = target;
      }
    });

    window.addEventListener("pointerup", () => {
      if (resize) {
        resize = null;
        document.body.classList.remove("resizing");
        persistWidgets(pinnedOrder());
        return;
      }
      if (!move) return;
      if (move.moved && move.target) {
        // Swap positions in the DOM then persist.
        const grid = document.getElementById("pinnedGrid");
        const kids = Array.from(grid.children);
        const ia = kids.indexOf(move.card), ib = kids.indexOf(move.target);
        if (ia >= 0 && ib >= 0) {
          grid.insertBefore(move.card, kids[Math.min(ia, ib)]);
        }
        persistWidgets(pinnedOrder());
      }
      if (move.card) move.card.classList.remove("dragging");
      if (move.target) move.target.classList.remove("drop-target");
      if (move.ghost) move.ghost.remove();
      document.body.classList.remove("dragging-widget");
      move = null;
    });
  }

  // Queries composer.
  const queryInput = document.getElementById("queryInput");
  const queryBtn = document.getElementById("queryBtn");
  if (queryInput) {
    const submit = () => {
      const text = queryInput.value.trim();
      if (!text) return;
      sendChat(text);
      queryInput.value = "";
      queryInput.style.height = "auto";
    };
    queryBtn?.addEventListener("click", submit);
    queryInput.addEventListener("input", () => {
      queryInput.style.height = "auto";
      queryInput.style.height = Math.min(queryInput.scrollHeight, 140) + "px";
    });
    queryInput.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(); }
    });
    document.querySelectorAll("[data-chat-suggest]").forEach((chip) =>
      chip.addEventListener("click", () => sendChat(chip.dataset.chatSuggest)));

    // Conversation sidebar collapse.
    const sidebar = document.getElementById("queriesSidebar");
    document.getElementById("queriesSidebarToggle")?.addEventListener("click", () => {
      sidebar.classList.add("collapsed");
      document.getElementById("queriesSidebarExpand").hidden = false;
    });
    document.getElementById("queriesSidebarExpand")?.addEventListener("click", () => {
      sidebar.classList.remove("collapsed");
      document.getElementById("queriesSidebarExpand").hidden = true;
    });
  }

  // Recurring modal open (delegated).
  document.addEventListener("click", (e) => {
    const rec = e.target.closest("[data-recurring-key]");
    if (rec) openRecurring(rec.dataset.recurringKey);
    const closers = e.target.closest("[data-action='close-recurring']");
    if (closers) closeModal("recurringModal");
    if (e.target.id === "recurringModal") closeModal("recurringModal");
    if (e.target.id === "accountModal") closeModal("accountModal");
  });

  // Custom range popover.
  const periodSelect = document.getElementById("periodSelect");
  if (periodSelect) {
    periodSelect.addEventListener("change", (e) => {
      if (e.target.value === "custom") {
        document.getElementById("customRange").hidden = false;
        const from = document.getElementById("customFrom");
        const to = document.getElementById("customTo");
        if (!from.value) {
          const d = new Date(); d.setMonth(d.getMonth() - 3);
          from.value = d.toISOString().slice(0, 10);
        }
        if (!to.value) to.value = new Date().toISOString().slice(0, 10);
        from.focus();
        return;
      }
      document.getElementById("customRange").hidden = true;
      const url = new URL(window.location);
      url.searchParams.set("period", e.target.value);
      window.location = url.toString();
    });
    document.getElementById("customApply")?.addEventListener("click", () => {
      const from = document.getElementById("customFrom").value;
      const to = document.getElementById("customTo").value;
      if (!from || !to) { toast("Choose a start and end date"); return; }
      const url = new URL(window.location);
      url.searchParams.set("period", "custom");
      url.searchParams.set("from", from);
      url.searchParams.set("to", to);
      window.location = url.toString();
    });
  }

  // Keyboard shortcuts (doc 04 §9).
  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      window.location = "/queries";
      return;
    }
    if (!e.ctrlKey && !e.metaKey && !e.altKey && /^[0-6]$/.test(e.key)) {
      const editable = e.target && e.target.closest ? e.target.closest("input, textarea, select, [contenteditable]") : null;
      if (!editable) {
        const views = { "1": "/", "2": "/queries", "3": "/transactions", "4": "/categories", "5": "/import", "6": "/history", "0": "/settings" };
        window.location = views[e.key];
      }
    }
    if (e.key === "Escape") {
      const cm = document.getElementById("confirmModal");
      if (cm && !cm.hidden) { cm.hidden = true; confirmCallback = null; return; }
      closeModal("setupWizard"); closeDrawer(); closeModal("recurringModal"); closeModal("accountModal");
    }
  });

  // Setup wizard footer.
  const wizardNext = document.getElementById("wizardNext");
  if (wizardNext) {
    const WIZARD_STEPS = 6;
    let step = Number(document.querySelector(".wizard[data-current-step]")?.dataset.currentStep || 0);
    const render = () => {
      document.querySelectorAll(".wizard-step").forEach((s) => { s.hidden = s.dataset.step !== String(step); });
      const prog = document.getElementById("wizardProgress");
      if (prog) {
        prog.innerHTML = "";
        for (let i = 0; i < WIZARD_STEPS; i++) {
          const span = document.createElement("span");
          if (i <= step) span.classList.add("done");
          prog.appendChild(span);
        }
      }
      const back = document.getElementById("wizardBack");
      if (back) back.hidden = step === 0;
      const skip = document.getElementById("wizardSkip");
      if (skip) skip.hidden = step === WIZARD_STEPS - 1;
      wizardNext.innerHTML = step === WIZARD_STEPS - 1
        ? "Finish <svg class='icon'><use href='#i-check'/></svg>"
        : "Continue <svg class='icon'><use href='#i-arrow-right'/></svg>";
    };
    render();
    wizardNext.addEventListener("click", async () => {
      if (step === 0) { step++; render(); return; }
      if (step === 1) {
        // Create the owner account (setup claim, doc 12 §3).
        const u = document.getElementById("wizUsername").value.trim();
        const p = document.getElementById("wizPassword").value;
        const pc = document.getElementById("wizPasswordConfirm").value;
        const err = document.getElementById("wizAccountError");
        err.hidden = true;
        try {
          const resp = await fetch("/api/v1/setup/account", {
            method: "POST",
            headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
            body: JSON.stringify({ username: u, password: p, password_confirm: pc }),
          });
          if (!resp.ok) {
            const body = await resp.json().catch(() => ({}));
            err.textContent = (body.error && body.error.message) || "Could not create the account.";
            err.hidden = false;
            return;
          }
        } catch (e2) {
          err.textContent = "Could not create the account.";
          err.hidden = false;
          return;
        }
        step++; render(); return;
      }
      if (step < WIZARD_STEPS - 1) {
        if (step === WIZARD_STEPS - 2) {
          // Final step marker: complete setup then show success.
          try { await fetch("/api/v1/setup/complete", { method: "POST", headers: { "X-CSRF-Token": csrfToken() } }); } catch (e2) {}
          step++; render(); return;
        }
        step++; render(); return;
      }
      window.location = "/";
    });
    document.getElementById("wizardBack")?.addEventListener("click", () => { if (step > 0) { step--; render(); } });
    document.getElementById("wizardSkip")?.addEventListener("click", () => { window.location = "/"; });
  }
});

/* ---------- Recurring modal ---------- */
function openRecurring(key) {
  fetch(`/api/v1/recurring/${encodeURIComponent(key)}/transactions`)
    .then((r) => r.json())
    .then((body) => {
      const txs = body.transactions || [];
      const list = document.getElementById("recurringTxList");
      list.innerHTML = txs.map((t) => recurringRowHTML(t)).join("");
      document.getElementById("recurringTitle").textContent = key;
      document.getElementById("recurringSub").textContent =
        txs.length + (txs.length === 1 ? " transaction" : " transactions") + " · auto-detected series";
      let total = 0;
      txs.forEach((t) => { total += t.amount_minor || 0; });
      document.getElementById("recurringTotal").textContent =
        (total < 0 ? "\\u2212" : total > 0 ? "+" : "") + fmtMoneyMinor(total, body.currency || "GBP");
      document.getElementById("recurringTotalLabel").textContent = total < 0 ? "Total spent" : total > 0 ? "Total received" : "Total";
      openModal("recurringModal");
    })
    .catch(() => toast("Could not load the series"));
}
function recurringRowHTML(t) {
  const cat = t.category ? t.category.name : "Uncategorised";
  const color = t.category ? t.category.color : "#999";
  const amt = fmtMoneySigned(t.amount_minor, t.currency || "GBP");
  const cls = t.type === "transfer" ? "transfer" : t.amount_minor >= 0 ? "pos" : "neg";
  return `<div class="tx-row recurring-row">
    <div class="tx-icon" style="background:${esc(color)}">${t.type === "transfer" ? "⇄" : esc(initialOf(t.description))}</div>
    <div class="tx-main"><div class="tx-merchant">${esc(t.description)}</div><div class="tx-sub">${esc(cat)} · ${esc(t.account ? t.account.name : "")}</div></div>
    <div class="tx-date">${esc(shortDate(t.date))}</div>
    <div class="tx-amount ${cls}">${esc(amt)}</div>
  </div>`;
}
function fmtMoneyMinor(minor, cc) {
  const sym = ({ GBP: "£", USD: "$", EUR: "€" })[cc] || cc + " ";
  const n = Math.abs(Number(minor)) / 100;
  return sym + n.toLocaleString("en-GB", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
function fmtMoneySigned(minor, cc) {
  const sign = minor < 0 ? "\\u2212" : minor > 0 ? "+" : "";
  return sign + fmtMoneyMinor(minor, cc);
}
function initialOf(s) {
  const words = String(s || "").trim().split(/\\s+/);
  return (words[0] && words[0][0] ? words[0][0] : "?").toUpperCase();
}
function shortDate(iso) {
  if (!iso) return "";
  const d = new Date(iso + "T00:00:00");
  if (isNaN(d)) return iso;
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" });
}

/* ---------- HTMX wiring: CSRF header + event toasts ---------- */
document.addEventListener("htmx:configRequest", (e) => {
  e.detail.headers["X-CSRF-Token"] = csrfToken();
});
document.body.addEventListener("htmx:afterSwap", (e) => {
  if (window.drawChartHosts) drawChartHosts(document);
});
document.addEventListener("glowworm-toast", (e) => {
  if (e.detail && e.detail.value) toast(e.detail.value);
});