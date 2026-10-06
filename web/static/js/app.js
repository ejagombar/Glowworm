
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

let toastTimer;
function toast(msg) {
  const el = document.getElementById("toast");
  if (!el) return;
  el.hidden = false;
  el.innerHTML = `<svg class="icon"><use href="#i-check"/></svg> ${esc(msg)}`;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (el.hidden = true), 2600);
}
function reloadWithToast(msg) {
  try { sessionStorage.setItem("glowworm-toast", msg); } catch (e) {}
  location.reload();
}
async function apiErrorText(r, fallback) {
  const b = await r.json().catch(() => ({}));
  return (b.error && b.error.message) || fallback;
}
function restoredMessage(res) {
  const n = res.transactions || 0;
  let msg = `Restored “${res.name}” · ${n} transaction${n === 1 ? "" : "s"}`;
  if (res.categories_created && res.categories_created.length) {
    msg += ` · added categor${res.categories_created.length === 1 ? "y" : "ies"} ${res.categories_created.join(", ")}`;
  }
  return msg;
}
function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

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
function closePasswordModal() {
  closeModal("passwordModal");
  ["pwCurrent", "pwNew", "pwConfirm"].forEach((id) => {
    const el = document.getElementById(id);
    if (el) el.value = "";
  });
}
function closeNameModal() {
  closeModal("nameModal");
}
function showConfirm(title, message, okLabel, callback, tone) {
  document.getElementById("confirmTitle").textContent = title;
  document.getElementById("confirmMessage").textContent = message;
  const ok = document.getElementById("confirmOk");
  ok.textContent = okLabel || "Delete";
  ok.classList.toggle("btn-danger", tone !== "primary");
  ok.classList.toggle("btn-primary", tone === "primary");
  confirmCallback = callback;
  document.getElementById("confirmModal").hidden = false;
}
let confirmCallback = null;

const WIDGET_CONVERSIONS = {
  bar: ["bar", "line", "table"],
  line: ["bar", "line", "table"],
  pie: ["pie", "table"],
  table: ["bar", "line", "pie", "table"],
  number: ["number"],
  text: ["text"],
  transactions: ["transactions", "table"],
};

function openDrawer(id) {
  const card = document.querySelector(`[data-widget-id="${id}"]`);
  if (!card) return;
  const drawer = document.getElementById("widgetDrawer");
  if (!drawer || !document.getElementById("drawerBackdrop") || !document.getElementById("wTitle")) return;
  state.editingId = id;
  document.getElementById("wTitle").value = card.dataset.widgetTitle || "";
  const sel = document.getElementById("wType");
  const typ = card.dataset.widgetType || "bar";
  const allowed = WIDGET_CONVERSIONS[typ] || [typ];
  Array.from(sel.options).forEach((o) => {
    o.hidden = !allowed.includes(o.value);
    o.disabled = !allowed.includes(o.value);
  });
  if (!allowed.includes(typ)) {
    const opt = document.createElement("option");
    opt.value = typ;
    opt.textContent = typ.charAt(0).toUpperCase() + typ.slice(1);
    sel.appendChild(opt);
  }
  sel.value = typ;
  document.getElementById("wPinned").checked = !!card.dataset.pinnedId;
  drawer.hidden = false;
  document.getElementById("drawerBackdrop").hidden = false;
  requestAnimationFrame(() => {
    drawer.classList.add("show");
    document.getElementById("drawerBackdrop").classList.add("show");
  });
}
function closeDrawer() {
  const d = document.getElementById("widgetDrawer"), b = document.getElementById("drawerBackdrop");
  if (!d || !b) return;
  d.classList.remove("show"); b.classList.remove("show");
  setTimeout(() => { d.hidden = true; b.hidden = true; }, 220);
  state.editingId = null;
}

const state = {
  editMode: false,
  editingId: null,
  balanceView: "all",
  privacy: false,
  customFrom: null,
  customTo: null,
};

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

function csrfToken() {
  const c = document.cookie.match(/(?:^|; )glowworm_csrf=([^;]*)/);
  return c ? decodeURIComponent(c[1]) : "";
}

function currentViewPeriod() {
  const sel = document.getElementById("periodSelect");
  if (!sel) return {};
  const out = { period: sel.value };
  if (sel.value === "custom") {
    out.from = document.getElementById("customFrom")?.value || "";
    out.to = document.getElementById("customTo")?.value || "";
    if (!out.from && !out.to) delete out.period;
  }
  return out;
}

let sentHistory = [];
let sentHistoryIdx = -1;

function pushSentHistory(text) {
  if (!text) return;
  if (sentHistory[sentHistory.length - 1] === text) return;
  sentHistory.push(text);
  if (sentHistory.length > 100) sentHistory.shift();
  sentHistoryIdx = -1;
}

function seedSentHistoryFromDOM(box) {
  if (!box) return;
  const bubbles = box.querySelectorAll(".msg.user .msg-bubble");
  const texts = [];
  bubbles.forEach((b) => {
    const t = (b.textContent || "").trim();
    if (t) texts.push(t);
  });
  const next = [];
  for (const t of texts) {
    if (next[next.length - 1] !== t) next.push(t);
  }
  sentHistory = next.slice(-100);
  sentHistoryIdx = -1;
}

function recallSent(direction, input) {
  if (!input || !sentHistory.length) return false;
  if (direction === "up") {
    if (sentHistoryIdx === -1 && input.value !== "") return false;
    if (sentHistoryIdx === -1) sentHistoryIdx = sentHistory.length;
    if (sentHistoryIdx > 0) {
      sentHistoryIdx--;
      input.value = sentHistory[sentHistoryIdx];
      input.setSelectionRange(input.value.length, input.value.length);
      return true;
    }
    return false;
  } else {
    if (sentHistoryIdx === -1) return false;
    if (sentHistoryIdx < sentHistory.length - 1) {
      sentHistoryIdx++;
      input.value = sentHistory[sentHistoryIdx];
    } else {
      sentHistoryIdx = -1;
      input.value = "";
    }
    input.setSelectionRange(input.value.length, input.value.length);
    return true;
  }
  return false;
}

function pollDaySummaries() {
  if (!document.querySelector('[data-day-summarising="1"]')) return;
  setTimeout(() => {
    fetch("/api/v1/queries/days").then((r) => (r.ok ? r.json() : null)).then((out) => {
      if (!out || !out.items || !out.items.length) return;
      renderDaySidebar(out.items);
      if (out.items.some((d) => !d.summary)) setTimeout(pollDaySummaries, 5000);
    }).catch(() => {});
  }, 3000);
}

function renderDaySidebar(entries) {
  const wrap = document.getElementById("queriesDays");
  if (!wrap) return;
  wrap.innerHTML = entries.map((d) => {
    const conv = esc(d.conversation_id || "");
    return `<a class="queries-day" href="/queries?c=${conv}&day=${esc(d.date)}">` +
      `<span class="queries-day-top"><span>${esc(d.label)}</span><span class="queries-day-count">${esc(d.count)}</span></span>` +
      (d.summary ? `<span class="queries-day-summary">${esc(d.summary)}</span>` : "") +
      `</a>`;
  }).join("");
}

function jumpToDay(date) {
  const box = queriesScrollBox();
  if (!box) return;
  let tries = 0;
  const seek = () => {
    const hit = Array.from(box.querySelectorAll(".msg[data-created]")).find(
      (m) => localDayKey(m.dataset.created) === date);
    if (hit) {
      // getBoundingClientRect is viewport-relative; add scrollTop to get the
      // offset within the box.
      const offset = (hit.getBoundingClientRect().top - box.getBoundingClientRect().top) + box.scrollTop;
      box.scrollTop = Math.max(0, offset - 40);
      return;
    }
    if (tries++ >= 15 || box.dataset.hasMore !== "1") return;
    const before = box.dataset.oldest;
    if (!before) return;
    fetch(`/api/v1/conversations/${queriesConvID()}/messages?before=${encodeURIComponent(before)}&limit=${QUERIES_PAGE_SIZE}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((out) => {
        if (!out || !out.items || !out.items.length) { box.dataset.hasMore = "0"; return; }
        if (!out.has_more) box.dataset.hasMore = "0";
        box.dataset.oldest = out.items[0].created_at + "|" + out.items[0].id;
        prependMessages(box, out.items);
        seek();
      }).catch(() => {});
  };
  requestAnimationFrame(seek);
}

const QUERIES_PAGE_SIZE = 20;

function queriesScrollBox() { return document.getElementById("queryMessages"); }
function queriesConvID() {
  const main = document.querySelector(".queries-main");
  return main ? main.dataset.conversationId || "" : "";
}

function initQueriesScroll() {
  const box = queriesScrollBox();
  if (!box) return;
  const savedConv = sessionStorage.getItem("glowworm-queries-conv");
  const urlConv = new URLSearchParams(location.search).get("c");
  if (savedConv && !urlConv && savedConv !== queriesConvID()) {
    location.replace("/queries?c=" + encodeURIComponent(savedConv));
    return;
  }
  if (queriesConvID()) {
    try { sessionStorage.setItem("glowworm-queries-conv", queriesConvID()); } catch (e) {}
  }
  const key = "glowworm-qpos-" + queriesConvID();
  const saved = sessionStorage.getItem(key);
  requestAnimationFrame(() => {
    if (saved === "bottom") scrollQueriesToBottom();
    else if (saved !== null) box.scrollTop = Number(saved) || 0;
    else scrollQueriesToBottom();
  });
  let saveTimer;
  box.addEventListener("scroll", () => {
    clearTimeout(saveTimer);
    saveTimer = setTimeout(saveQueriesScroll, 150);
    maybeLoadOlder();
  }, { passive: true });
  // The debounce can miss the last movement before navigating away; pagehide
  // always fires.
  window.addEventListener("pagehide", saveQueriesScroll);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") saveQueriesScroll();
  });
  maybeLoadOlder();
}

function scrollQueriesToBottom() {
  const box = queriesScrollBox();
  if (box) box.scrollTop = box.scrollHeight;
}

function saveQueriesScroll() {
  const box = queriesScrollBox();
  const key = "glowworm-qpos-" + queriesConvID();
  if (!box || !queriesConvID()) return;
  const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
  try { sessionStorage.setItem(key, atBottom ? "bottom" : String(box.scrollTop)); } catch (e) {}
}

let loadingOlder = false;
function maybeLoadOlder() {
  const box = queriesScrollBox();
  if (!box || loadingOlder) return;
  if (box.dataset.hasMore !== "1" || box.scrollTop > 80) return;
  loadOlderMessages();
}

function loadOlderMessages() {
  const box = queriesScrollBox();
  const convID = queriesConvID();
  const cursor = box.dataset.oldest;
  if (!convID || !cursor) { box.dataset.hasMore = "0"; return; }
  loadingOlder = true;
  const loader = document.createElement("div");
  loader.className = "history-loading";
  loader.textContent = "Loading earlier messages…";
  box.prepend(loader);
  fetch(`/api/v1/conversations/${convID}/messages?before=${encodeURIComponent(cursor)}&limit=${QUERIES_PAGE_SIZE}`)
    .then((r) => (r.ok ? r.json() : Promise.reject(new Error("load failed"))))
    .then((out) => {
      loader.remove();
      const items = out.items || [];
      if (!items.length || !out.has_more) box.dataset.hasMore = "0";
      if (items.length) {
        box.dataset.oldest = items[0].created_at + "|" + items[0].id;
        prependMessages(box, items);
        if (window.drawChartHosts) drawChartHosts(box);
        seedSentHistoryFromDOM(box);
      }
      loadingOlder = false;
      maybeLoadOlder();
    })
    .catch(() => { loader.remove(); loadingOlder = false; });
}

function messageHTML(m) {
  const created = esc(m.created_at || "");
  if (m.role === "user") {
    return `<div class="msg user" data-created="${created}"><div class="msg-content"><div class="msg-bubble">${esc(m.text)}</div></div></div>`;
  }
  let content = "";
  if (m.text) content += `<div class="msg-text">${fmtAssistantText(m.text)}</div>`;
  if (m.steps && m.steps.length) content += msgReasonHTML(m.steps);
  for (const w of m.widgets || []) content += widgetCardHTML(w);
  return `<div class="msg assistant" data-created="${created}"><div class="msg-content">${content}</div></div>`;
}

function prependMessages(box, items) {
  const firstMsg = box.querySelector(".msg");
  const prevHeight = box.scrollHeight;
  const prevTop = box.scrollTop;
  let runningDay = null;
  const frag = document.createDocumentFragment();
  for (const m of items) {
    const day = dayKeyOf(m.created_at);
    if (day && day !== runningDay) {
      const badge = document.createElement("div");
      badge.className = "msg-day";
      badge.textContent = dayLabelOf(m.created_at);
      frag.appendChild(badge);
      runningDay = day;
    }
    const tpl = document.createElement("template");
    tpl.innerHTML = messageHTML(m);
    frag.appendChild(tpl.content.firstElementChild);
  }
  box.prepend(frag);
  box.scrollTop = prevTop + (box.scrollHeight - prevHeight);
  if (firstMsg && firstMsg.dataset.created) {
    const badgeEl = firstMsg.previousElementSibling;
    if (badgeEl && badgeEl.classList.contains("msg-day") &&
        dayKeyOf(firstMsg.dataset.created) === dayKeyOf(items[items.length - 1].created_at)) {
      badgeEl.remove();
    }
  }
}

// Local-timezone day keys, matching the server: UTC grouping mislabels
// late-evening messages.
function localDayKey(ts) {
  const d = new Date(ts || "");
  if (isNaN(d)) return "";
  const p = (n) => String(n).padStart(2, "0");
  return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate());
}
function dayKeyOf(ts) { return localDayKey(ts); }
function dayLabelOf(ts) {
  const d = new Date(ts || "");
  if (isNaN(d)) return "";
  const key = (x) => x.getFullYear() + "." + x.getMonth() + "." + x.getDate();
  const now = new Date();
  if (key(d) === key(now)) return "Today";
  const yest = new Date(); yest.setDate(yest.getDate() - 1);
  if (key(d) === key(yest)) return "Yesterday";
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric" });
}

function sendChat(text) {
  text = (text || "").trim();
  if (!text) return;
  pushSentHistory(text);
  const box = document.getElementById("queryMessages");
  const convEl = document.querySelector("[data-conversation-id]");
  const conversationID = convEl ? convEl.dataset.conversationId : "";

  // The day badge is decided from the DOM's last message, never a cached
  // format string (that produced duplicate Today badges).
  const user = document.createElement("div");
  user.className = "msg user msg-in";
  user.innerHTML = `<div class="msg-content"><div class="msg-bubble">${esc(text)}</div></div>`;
  const pending = document.createElement("div");
  pending.className = "msg assistant msg-in";
  pending.innerHTML = `<div class="msg-content">${wormLoaderHTML()}<details class="msg-reason" hidden><summary>How I worked this out</summary><div class="reason-steps"></div></details></div>`;
  const msgs = box.querySelectorAll(".msg[data-created]");
  const lastCreated = msgs.length ? msgs[msgs.length - 1].dataset.created : null;
  const nowTs = new Date().toISOString();
  if (!lastCreated || localDayKey(lastCreated) !== localDayKey(nowTs)) {
    const day = document.createElement("div");
    day.className = "msg-day";
    day.textContent = dayLabelOf(nowTs);
    box.appendChild(day);
  }
  box.appendChild(user); box.appendChild(pending);
  box.scrollTop = box.scrollHeight;

  fetch("/api/v1/query/stream", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify({ text, conversation_id: conversationID, ...currentViewPeriod() }),
  }).then(async (resp) => {
    if (!resp.ok || !resp.body) {
      finishAssistant(pending, { text: "Sorry — the query engine is unavailable right now.", isError: true });
      return;
    }
    const reader = resp.body.getReader();
    const dec = new TextDecoder();
    let buf = "", out = { text: "", steps: [], widgets: [], isError: false, suggestions: [] };
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
        if (ev.event === "steps") {
          out.steps = out.steps.concat(ev.items || []);
          renderSteps(pending, out.steps);
        }
        else if (ev.event === "text") { out.text = ev.text || out.text; }
        else if (ev.event === "widget" && ev.widget) { out.widgets.push(ev.widget); }
        else if (ev.event === "done" && ev.conversation_id) {
          adoptConversation(ev.conversation_id, text);
        }
        else if (ev.event === "error") {
          out.text = ev.message || "Query failed.";
          out.isError = true;
          out.steps = [];
          out.suggestions = ev.suggestions || [];
        }
      }
    }
    finishAssistant(pending, out);
  }).catch(() => {
    finishAssistant(pending, { text: "Sorry — the query engine is unavailable right now.", isError: true });
  });
}

function pinWidget(card, btn) {
  const payload = {
    title: card.dataset.widgetTitle || "",
    type: card.dataset.widgetType || "bar",
    data: card.dataset.widgetData ? JSON.parse(card.dataset.widgetData) : null,
    dataset: card.dataset.widgetDataset ? JSON.parse(card.dataset.widgetDataset) : null,
    plan: card.dataset.widgetPlan ? JSON.parse(card.dataset.widgetPlan) : null,
  };
  fetch("/api/v1/widgets/pinned", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify(payload),
  }).then((r) => r.json()).then((out) => {
    if (!out || !out.id) { toast("Could not pin widget"); return; }
    card.dataset.pinnedId = out.id;
    btn.classList.add("pinned-on");
    btn.title = "Unpin from dashboard";
    toast("Pinned to dashboard");
  }).catch(() => toast("Could not pin widget"));
}

function unpinWidget(card, btn) {
  fetch(`/api/v1/widgets/pinned/${card.dataset.pinnedId}`, {
    method: "DELETE",
    headers: { "X-CSRF-Token": csrfToken() },
  }).then((r) => {
    if (!r.ok) { toast("Could not unpin widget"); return; }
    delete card.dataset.pinnedId;
    btn.classList.remove("pinned-on");
    btn.title = "Pin to dashboard";
    const grid = document.getElementById("pinnedGrid");
    if (grid && grid.contains(card)) card.remove();
    toast("Removed from dashboard");
  }).catch(() => toast("Could not unpin widget"));
}

function adoptConversation(id, firstQuestion) {
  if (!id) return;
  const main = document.querySelector(".queries-main");
  if (main) main.dataset.conversationId = id;
  try { sessionStorage.setItem("glowworm-queries-conv", id); } catch (e) {}
  const sidebar = document.getElementById("queriesDays");
  if (sidebar && !sidebar.querySelector(`a[href="/queries?c=${id}"]`)) {
    const a = document.createElement("a");
    a.className = "queries-day";
    a.href = `/queries?c=${id}`;
    const t = document.createElement("span");
    t.textContent = (firstQuestion || "New conversation").slice(0, 60);
    a.appendChild(t);
    sidebar.prepend(a);
  }
}

function renderSteps(pending, steps) {
  const reason = pending.querySelector(".msg-reason");
  if (!reason) return;
  reason.hidden = false;
  reason.querySelector(".reason-steps").innerHTML = steps
    .map((s) => `<div class="reason-step"><span class="tick"><svg class="icon"><use href="#i-check"/></svg></span><span>${esc(s)}</span></div>`)
    .join("");
}

function fmtAssistantText(text) {
  const escd = esc(text || "");
  return escd.split("**").map((p, i) => (i % 2 ? `<strong>${p}</strong>` : p)).join("");
}

function finishAssistant(pending, out) {
  out = out || {};
  const { text = "", steps = [], widgets = [], isError = false, suggestions = [] } = out;
  const loader = pending.querySelector(".glowworm-loader");
  const reveal = () => {
    let content = "";
    const errCls = isError && !(suggestions && suggestions.length) ? " msg-error" : "";
    if (text) content += `<div class="msg-text${errCls}">${fmtAssistantText(text)}</div>`;
    if (!isError && steps && steps.length) content += msgReasonHTML(steps);
    if (suggestions && suggestions.length) {
      content += `<div class="msg-suggestions">${suggestions
        .map((s) => `<button class="chip" data-chat-suggest="${esc(s)}">${esc(s)}</button>`)
        .join("")}</div>`;
    }
    if (!isError && widgets) {
      for (const w of widgets) content += widgetCardHTML(w);
    }
    pending.innerHTML = `<div class="msg-content">${content}</div>`;
    if (widgets && widgets.length) {
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
  el.dataset.widgetData = JSON.stringify(widget.data || {});
  if (widget.plan) el.dataset.widgetPlan = JSON.stringify(widget.plan);
  if (widget.dataset) el.dataset.widgetDataset = JSON.stringify(widget.dataset);
  el.innerHTML = `
    <div class="widget-head">
      <div class="widget-title">${esc(widget.title || "Widget")}</div>
      <div class="widget-actions">
        <button class="widget-btn${widget.pinned ? " pinned-on" : ""}" data-action="toggle-pin" title="Pin to dashboard"><svg class="icon"><use href="#i-pin"/></svg></button>
        <button class="widget-btn" data-action="edit-widget" title="Widget settings"><svg class="icon"><use href="#i-settings"/></svg></button>
      </div>
    </div>
    <div class="widget-body">${widgetBodyHTML(widget)}</div>`;
  return el.outerHTML;
}
function widgetBodyHTML(widget) {
  const data = widget.data || {};
  if (data.transactions) {
    data.transactions = data.transactions.map((tx) => {
      const v = tx.amount_minor ?? tx.amount ?? 0;
      return { ...tx, amount: Number(v) };
    });
  }
  if (widget.type === "number") {
    return `<div class="num-widget"><div class="num-value">${esc(data.value)}</div>
      ${data.caption ? `<div class="num-caption">${esc(data.caption)}</div>` : ""}</div>`;
  }
  if (widget.type === "text") return `<div class="text-widget"><p>${esc(data.body)}</p></div>`;
  if (widget.type === "table") {
    return `<div class="table-widget"><table><thead><tr>${(data.columns || []).map((c) => `<th>${esc(c)}</th>`).join("")}</tr></thead>
      <tbody>${(data.rows || []).map((r) => `<tr>${r.map((c, i) => `<td class="${i === r.length - 1 ? "num" : ""}">${esc(c)}</td>`).join("")}</tr>`).join("")}</tbody></table></div>`;
  }
  if (widget.type === "transactions") {
    const monthNames = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
    const rows = (data.transactions || []).map((tx) => {
      const d = String(tx.date || "");
      const day = d.slice(8, 10);
      const mon = monthNames[Number(d.slice(5, 7)) - 1] || "";
      const cur = tx.currency || "GBP";
      const sym = ({ GBP: "£", USD: "$", EUR: "€" })[cur] || cur + " ";
      const amt = Number(tx.amount || 0) / 100;
      const amtStr = (tx.type === "transfer" ? "⇄ " : tx.amount >= 0 ? "+" : "−") +
        sym + Math.abs(amt).toLocaleString("en-GB", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
      const cls = tx.type === "transfer" ? "transfer" : tx.amount >= 0 ? "pos" : "neg";
      return `<tr><td class="cell-sub">${esc(day)} ${esc(mon)}</td>` +
        `<td><div class="cell-merchant">${esc(tx.description || tx.merchant || "")}</div></td>` +
        `<td>${esc(tx.category || "")}</td>` +
        `<td class="num tx-amount ${cls}">${esc(amtStr)}</td></tr>`;
    }).join("");
    return `<div class="table-widget tx-widget"><table class="tx-table">
      <thead><tr><th>Date</th><th>Description</th><th>Category</th><th class="num">Amount</th></tr></thead>
      <tbody>${rows}</tbody>
    </table></div>`;
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

document.addEventListener("DOMContentLoaded", () => {
  initTheme();
  try {
    const pending = sessionStorage.getItem("glowworm-toast");
    if (pending) { sessionStorage.removeItem("glowworm-toast"); toast(pending); }
  } catch (e) {}

  const privacyBtn = document.getElementById("privacyToggle");
  const applyPrivacy = (on) => {
    state.privacy = on;
    document.body.classList.toggle("privacy", on);
    if (!privacyBtn) return;
    privacyBtn.classList.toggle("active", on);
    privacyBtn.setAttribute("aria-pressed", on ? "true" : "false");
    privacyBtn.title = on ? "Show balances" : "Hide balances";
    privacyBtn.innerHTML = on
      ? '<svg class="icon"><use href="#i-eye-off"/></svg>'
      : '<svg class="icon"><use href="#i-eye"/></svg>';
  };
  const privacyCookie = () => {
    const m = document.cookie.match(/(?:^|;\s*)glowworm_privacy=([^;]*)/);
    return m ? m[1] === "1" : null;
  };
  state.privacy = document.body.classList.contains("privacy");
  if (privacyBtn) {
    privacyBtn.addEventListener("click", () => {
      applyPrivacy(!state.privacy);
      document.cookie = `glowworm_privacy=${state.privacy ? "1" : "0"}; Path=/; Max-Age=31536000; SameSite=Lax`;
    });
  }
  // bfcache restores the old state; resync with a choice made on another page.
  window.addEventListener("pageshow", (e) => {
    const saved = privacyCookie();
    if (e.persisted && saved !== null && saved !== state.privacy) applyPrivacy(saved);
  });

  const themeSel = document.getElementById("themeSelect");
  if (themeSel) {
    themeSel.addEventListener("change", (e) => {
      try { localStorage.setItem("glowworm-theme", e.target.value); } catch (err) {}
      applyTheme(e.target.value);
      toast("Theme updated");
    });
  }

  if (window.drawChartHosts) drawChartHosts(document);

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

  const confirmOk = document.getElementById("confirmOk");
  const confirmCancel = document.getElementById("confirmCancel");
  if (confirmOk) confirmOk.addEventListener("click", () => { const cb = confirmCallback; confirmCallback = null; document.getElementById("confirmModal").hidden = true; if (cb) cb(); });
  if (confirmCancel) confirmCancel.addEventListener("click", () => { confirmCallback = null; document.getElementById("confirmModal").hidden = true; });
  document.querySelectorAll("[data-action='close-confirm']").forEach((b) => b.addEventListener("click", () => { confirmCallback = null; document.getElementById("confirmModal").hidden = true; }));

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
      const wasPinned = !!card.dataset.pinnedId;
      let data = card.dataset.widgetData ? JSON.parse(card.dataset.widgetData) : null;
      if (data && window.convertWidgetData) {
        data = convertWidgetData(card.dataset.widgetType, newType, data);
      }

      const updateCardDOM = () => {
        card.dataset.widgetType = newType;
        card.dataset.widgetTitle = newTitle;
        card.dataset.widgetData = JSON.stringify(data || {});
        card.querySelector(".widget-title").textContent = newTitle;
        const body = card.querySelector(".widget-body");
        if (body) body.innerHTML = widgetBodyHTML({ type: newType, data: data || {} });
        if (window.drawChartHosts) drawChartHosts(card);
      };

      if (wasPinned) {
        fetch(`/api/v1/widgets/pinned/${card.dataset.pinnedId}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
          body: JSON.stringify({ title: newTitle, type: newType, pinned: shouldPin, data }),
        }).then((r) => {
          if (!r.ok) { toast("Could not update widget"); return; }
          if (!shouldPin) {
            card.remove();
            toast("Removed from dashboard");
          } else {
            updateCardDOM();
            toast("Widget updated");
          }
          closeDrawer();
        }).catch(() => toast("Could not update widget"));
        return;
      }

      updateCardDOM();
      if (shouldPin) {
        const btn = card.querySelector("[data-action='toggle-pin']");
        if (btn) pinWidget(card, btn);
      }
      closeDrawer();
      toast("Widget updated");
    });
  }

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
      if (!card) return;
      if (card.dataset.pinnedId) {
        showConfirm(
          "Unpin widget?",
          "Remove this widget from your dashboard?",
          "Unpin",
          () => unpinWidget(card, pin),
        );
      } else {
        pinWidget(card, pin);
      }
    }
  });

  document.addEventListener("click", (e) => {
    const chip = e.target.closest("[data-chat-suggest]");
    if (chip) sendChat(chip.dataset.chatSuggest);
  });

  // Available balance / Savings cards swap the overview chart to that slice;
  // clicking the selected card again goes back to all accounts.
  const heroChart = document.getElementById("heroChart");
  const balanceCards = document.querySelectorAll("[data-balance-view]");
  if (heroChart && balanceCards.length) {
    const HERO_TITLES = { all: "Balance overview", available: "Available balance", savings: "Savings" };
    const HERO_RANGES = { all: "All accounts", available: "Current and other non-savings accounts", savings: "Savings accounts" };
    const setBalanceView = (view) => {
      state.balanceView = view;
      balanceCards.forEach((card) => {
        const on = card.dataset.balanceView === view;
        card.classList.toggle("selected", on);
        card.setAttribute("aria-pressed", on ? "true" : "false");
        const label = card.querySelector("[data-graph-label]");
        if (label) label.textContent = on ? "Showing graph · click to reset" : "Show graph";
      });
      heroChart.dataset.chartData = heroChart.dataset[`series${view[0].toUpperCase()}${view.slice(1)}`] || heroChart.dataset.seriesAll;
      const title = document.getElementById("heroTitle");
      if (title) title.textContent = HERO_TITLES[view];
      const range = document.getElementById("heroRange");
      if (range) range.textContent = `${HERO_RANGES[view]} · all time`;
      if (typeof redrawChartHost === "function") redrawChartHost(heroChart);
    };
    balanceCards.forEach((card) => {
      const pick = () => setBalanceView(state.balanceView === card.dataset.balanceView ? "all" : card.dataset.balanceView);
      card.addEventListener("click", pick);
      card.addEventListener("keydown", (e) => {
        if (e.key === "Enter" || e.key === " ") { e.preventDefault(); pick(); }
      });
    });
  }

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
        if (resize.dir === "e") {
          const c = clamp(resize.col + Math.round(dx / resize.colWidth), 2, 12);
          resize.card.style.gridColumn = "span " + c;
          resize.card.dataset.widgetCol = c;
        } else if (resize.dir === "w") {
          const c = clamp(resize.col - Math.round(dx / resize.colWidth), 2, 12);
          resize.card.style.gridColumn = "span " + c;
          resize.card.dataset.widgetCol = c;
        } else if (resize.dir === "s") {
          const rw = clamp(resize.rows + Math.round(dy / resize.rowStep), 1, 8);
          resize.card.style.gridRow = "span " + rw;
          resize.card.dataset.widgetRows = rw;
        }
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
        const a = move.card, b = move.target;
        const aCol = a.dataset.widgetCol || "8", aRows = a.dataset.widgetRows || "4";
        const bCol = b.dataset.widgetCol || "8", bRows = b.dataset.widgetRows || "4";
        a.dataset.widgetCol = bCol; a.dataset.widgetRows = bRows;
        a.style.gridColumn = "span " + bCol; a.style.gridRow = "span " + bRows;
        b.dataset.widgetCol = aCol; b.dataset.widgetRows = aRows;
        b.style.gridColumn = "span " + aCol; b.style.gridRow = "span " + aRows;
        const placeholder = document.createElement("span");
        a.replaceWith(placeholder);
        b.replaceWith(a);
        placeholder.replaceWith(b);
        if (window.drawChartHosts) {
          drawChartHosts(a);
          drawChartHosts(b);
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
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(); return; }
      if (e.key === "ArrowUp") {
        if (queryInput.value === "" || sentHistoryIdx !== -1) {
          if (recallSent("up", queryInput)) e.preventDefault();
        }
      } else if (e.key === "ArrowDown") {
        if (sentHistoryIdx !== -1 && recallSent("down", queryInput)) e.preventDefault();
      }
    });

    const qbox = document.getElementById("queryMessages");
    if (qbox) {
      initQueriesScroll();
      seedSentHistoryFromDOM(qbox);
      pollDaySummaries();
      const jumpDay = document.querySelector(".queries-main")?.dataset.jumpDay;
      if (jumpDay) jumpToDay(jumpDay);
    }
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

  document.addEventListener("click", (e) => {
    const rec = e.target.closest("[data-recurring-key]");
    if (rec) openRecurring(rec.dataset.recurringKey);
    const closers = e.target.closest("[data-action='close-recurring']");
    if (closers) closeModal("recurringModal");
    if (e.target.id === "recurringModal") closeModal("recurringModal");
    if (e.target.id === "accountModal") closeModal("accountModal");
  });

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
      closeDrawer(); closeModal("recurringModal"); closeModal("accountModal"); closeModal("txModal"); closePasswordModal(); closeNameModal();
    }
  });

  const newTxBtn = document.getElementById("newTxBtn");
  if (newTxBtn) {
    newTxBtn.addEventListener("click", () => openModal("txModal"));
    document.querySelectorAll("[data-action='close-tx-modal']").forEach((b) =>
      b.addEventListener("click", () => closeModal("txModal")));
    document.getElementById("txModal")?.addEventListener("click", (e) => {
      if (e.target.id === "txModal") closeModal("txModal");
    });
    document.getElementById("txAddForm").addEventListener("submit", (e) => {
      e.preventDefault();
      const date = document.getElementById("txDate").value;
      const description = document.getElementById("txDesc").value.trim();
      const amount = parseFloat(document.getElementById("txAmount").value);
      const accountID = document.getElementById("txAccount").value;
      const categoryID = document.getElementById("txCategory").value || null;
      if (!accountID) { toast("Add an account first (Settings → Accounts)"); return; }
      if (!date) { toast("Date is required"); return; }
      if (!description) { toast("Description is required"); return; }
      if (isNaN(amount) || amount === 0) { toast("Amount is required (negative for spending)"); return; }
      fetch("/api/v1/transactions", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ account_id: accountID, date, description, amount_minor: Math.round(amount * 100), category_id: categoryID }),
      }).then(async (r) => {
        if (!r.ok) {
          const b = await r.json().catch(() => ({}));
          toast((b.error && b.error.message) || "Could not add transaction");
          return;
        }
        toast("Transaction added");
        location.reload();
      }).catch(() => toast("Could not add transaction"));
    });
  }
});

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
  const sign = minor < 0 ? "\u2212" : minor > 0 ? "+" : "";
  return sign + fmtMoneyMinor(minor, cc);
}
function initialOf(s) {
  const words = String(s || "").trim().split(/\\s+/);
  return (words[0] && words[0][0] ? words[0][0] : "?").toUpperCase();
}
function monthEndOf(ym) {
  const [y, m] = String(ym || "").split("-").map(Number);
  if (!y || !m) return ym;
  return ym + "-" + String(new Date(y, m, 0).getDate()).padStart(2, "0");
}

function shortDate(iso) {
  if (!iso) return "";
  const d = new Date(iso + "T00:00:00");
  if (isNaN(d)) return iso;
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" });
}

document.addEventListener("glowworm-toast", (e) => {
  if (e.detail && e.detail.value) toast(e.detail.value);
});

function accountPayload() {
  const opening = parseFloat(document.getElementById("accOpening").value);
  return {
    name: document.getElementById("accName").value.trim(),
    type: document.getElementById("accType").value,
    opening_balance_minor: isNaN(opening) ? 0 : Math.round(opening * 100),
    opening_balance_date: document.getElementById("accOpeningDate").value || "",
    account_number: document.getElementById("accNumber").value.trim(),
    sort_code: document.getElementById("accSortCode").value.trim(),
    iban: document.getElementById("accIban").value.trim(),
    bic: document.getElementById("accBic").value.trim(),
  };
}

function openAccountForm(id) {
  const modal = document.getElementById("accountModal");
  const form = document.getElementById("accountForm");
  form.dataset.accountId = id || "";
  document.getElementById("accSaveBtn").textContent = id ? "Save changes" : "Add account";
  document.getElementById("accountModalTitle").textContent = id ? "Account settings" : "Add account";
  document.getElementById("accDeleteBtn").hidden = !id;
  const today = new Date().toISOString().slice(0, 10);
  document.getElementById("accValuationDate").value = today;
  if (id) {
    fetch(`/api/v1/accounts/${id}`).then((r) => r.json()).then((a) => {
      document.getElementById("accName").value = a.name || "";
      document.getElementById("accType").value = a.type || "debit";
      document.getElementById("accOpening").value = a.opening_balance_minor != null ? (a.opening_balance_minor / 100).toFixed(2) : "";
      document.getElementById("accOpeningDate").value = a.opening_balance_date || "";
      document.getElementById("accNumber").value = a.account_number || "";
      document.getElementById("accSortCode").value = a.sort_code || "";
      document.getElementById("accIban").value = a.iban || "";
      document.getElementById("accBic").value = a.bic || "";
      document.getElementById("accValuation").value = a.current_balance_minor != null ? (a.current_balance_minor / 100).toFixed(2) : "";
    }).catch(() => toast("Could not load account"));
  } else {
    ["accName","accOpening","accNumber","accSortCode","accIban","accBic","accValuation"].forEach((x) => (document.getElementById(x).value = ""));
    document.getElementById("accType").value = "debit";
  }
  modal.hidden = false;
  document.getElementById("accName").focus();
}

function saveAccount() {
  const form = document.getElementById("accountForm");
  const id = form.dataset.accountId;
  const body = accountPayload();
  const val = parseFloat(document.getElementById("accValuation").value);
  const valDate = document.getElementById("accValuationDate").value;
  const url = id ? `/api/v1/accounts/${id}` : "/api/v1/accounts";
  fetch(url, {
    method: id ? "PATCH" : "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify(body),
  }).then(async (r) => {
    if (!r.ok) {
      const b = await r.json().catch(() => ({}));
      toast((b.error && b.error.message) || "Could not save the account");
      return null;
    }
    return r.json();
  }).then((acct) => {
    if (!acct) return;
    if (id && !isNaN(val) && document.getElementById("accValuation").value !== "") {
      return fetch(`/api/v1/accounts/${id}/valuations`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ value_minor: Math.round(val * 100), date: valDate, note: "Manual valuation" }),
      });
    }
    if (!id && !isNaN(val) && document.getElementById("accValuation").value !== "") {
      return fetch(`/api/v1/accounts/${acct.id}/valuations`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ value_minor: Math.round(val * 100), date: valDate, note: "Manual valuation" }),
      });
    }
  }).then(() => {
    closeModal("accountModal");
    toast("Account saved");
    location.reload();
  }).catch(() => toast("Could not save the account"));
}

document.addEventListener("DOMContentLoaded", () => {
  document.getElementById("settingsAddAccountBtn")?.addEventListener("click", () => openAccountForm(null));
  document.querySelectorAll("[data-account-form]").forEach((card) =>
    card.addEventListener("click", () => openAccountForm(card.dataset.accountForm)));
  document.getElementById("accSaveBtn")?.addEventListener("click", (e) => { e.preventDefault(); saveAccount(); });
  document.getElementById("accCancelBtn")?.addEventListener("click", () => closeModal("accountModal"));
  document.querySelectorAll("[data-action='close-account']").forEach((b) => b.addEventListener("click", () => closeModal("accountModal")));
  // The new-transaction modal is wired in the boot handler (guarded by
  // #newTxBtn). An unguarded copy here threw on pages without it and killed
  // every binding below.
  document.getElementById("accDeleteBtn")?.addEventListener("click", () => {
    const form = document.getElementById("accountForm");
    const id = form.dataset.accountId;
    if (!id) return;
    const name = document.getElementById("accName").value.trim() || "this account";
    showConfirm(`Delete “${name}”?`,
      "Its transactions, valuations and transfer links are removed everywhere. A copy is kept under Deleted accounts in Settings, so you can restore it later.",
      "Delete account", () => {
        fetch(`/api/v1/accounts/${id}`, { method: "DELETE", headers: { "X-CSRF-Token": csrfToken() } })
          .then(async (r) => {
            if (!r.ok) { toast(await apiErrorText(r, "Could not delete the account")); return; }
            closeModal("accountModal");
            reloadWithToast(`Deleted “${name}” — restore it from Deleted accounts`);
          });
      });
  });

  document.querySelectorAll("[data-close-modal]").forEach((b) =>
    b.addEventListener("click", () => closeModal(b.dataset.closeModal)));
  const instanceBackup = async (password) => {
    const r = await fetch("/api/v1/instance/backup", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ password }),
    });
    if (!r.ok) throw new Error(await apiErrorText(r, "Backup failed"));
    const name = (r.headers.get("Content-Disposition") || "").match(/filename="([^"]+)"/);
    const url = URL.createObjectURL(await r.blob());
    const a = document.createElement("a");
    a.href = url;
    a.download = name ? name[1] : "glowworm-backup.zip";
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  const instanceBackupModal = (afterwards) => {
    const pw = document.getElementById("instanceBackupPassword");
    if (pw) pw.value = "";
    document.getElementById("instanceBackupModal").dataset.after = afterwards || "";
    openModal("instanceBackupModal");
  };
  document.getElementById("instanceBackupBtn")?.addEventListener("click", () => instanceBackupModal());
  document.getElementById("instanceBackupGo")?.addEventListener("click", async (e) => {
    const btn = e.currentTarget;
    const pw = document.getElementById("instanceBackupPassword");
    btn.disabled = true;
    btn.textContent = "Preparing…";
    try {
      await instanceBackup(pw ? pw.value : "");
      closeModal("instanceBackupModal");
      toast("Backup downloaded");
      if (document.getElementById("instanceBackupModal").dataset.after === "delete") openModal("instanceDeleteModal");
    } catch (err) {
      toast(err.message);
    } finally {
      btn.disabled = false;
      btn.textContent = "Download backup";
    }
  });
  const delConfirm = document.getElementById("instanceDeleteConfirm");
  const delGo = document.getElementById("instanceDeleteGo");
  document.getElementById("instanceDeleteBtn")?.addEventListener("click", () => {
    delConfirm.value = "";
    delGo.disabled = true;
    const pw = document.getElementById("instanceDeletePassword");
    if (pw) pw.value = "";
    openModal("instanceDeleteModal");
  });
  document.getElementById("instanceDeleteBackupFirst")?.addEventListener("click", () => {
    closeModal("instanceDeleteModal");
    instanceBackupModal("delete");
  });
  delConfirm?.addEventListener("input", () => { delGo.disabled = delConfirm.value.trim() !== "DELETE"; });
  delGo?.addEventListener("click", () => {
    const pw = document.getElementById("instanceDeletePassword");
    delGo.disabled = true;
    delGo.textContent = "Deleting…";
    fetch("/api/v1/instance/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ password: pw ? pw.value : "", confirm: delConfirm.value.trim() }),
    }).then(async (r) => {
      if (!r.ok) {
        toast(await apiErrorText(r, "Delete failed"));
        delGo.disabled = false;
        delGo.textContent = "Delete everything";
        return;
      }
      location.href = "/setup";
    }).catch(() => {
      toast("Delete failed: network error");
      delGo.disabled = false;
      delGo.textContent = "Delete everything";
    });
  });

  document.getElementById("deletedAccounts")?.addEventListener("click", (e) => {
    const li = e.target.closest("[data-archive-id]");
    if (!li) return;
    const { archiveId, archiveName } = li.dataset;
    const restore = e.target.closest("[data-archive-restore]");
    if (restore) {
      restore.disabled = true;
      fetch(`/api/v1/accounts/archives/${archiveId}/restore`, { method: "POST", headers: { "X-CSRF-Token": csrfToken() } })
        .then(async (r) => {
          if (!r.ok) { restore.disabled = false; toast(await apiErrorText(r, "Restore failed")); return; }
          reloadWithToast(restoredMessage(await r.json()));
        });
    } else if (e.target.closest("[data-archive-purge]")) {
      showConfirm(`Delete “${archiveName}” forever?`,
        "Its transactions can't be restored after this. Use Download first if you might need them.",
        "Delete forever", () => {
          fetch(`/api/v1/accounts/archives/${archiveId}`, { method: "DELETE", headers: { "X-CSRF-Token": csrfToken() } })
            .then(async (r) => {
              if (!r.ok) { toast(await apiErrorText(r, "Could not delete")); return; }
              reloadWithToast(`“${archiveName}” deleted for good`);
            });
        });
    }
  });

  const pwOpen = document.getElementById("changePasswordBtn");
  if (pwOpen) {
    pwOpen.addEventListener("click", () => openModal("passwordModal"));
    document.getElementById("passwordCancel")?.addEventListener("click", () => closePasswordModal());
    document.querySelectorAll('[data-action="close-password"]').forEach((b) => b.addEventListener("click", closePasswordModal));
    document.getElementById("passwordModal")?.addEventListener("click", (e) => {
      if (e.target.id === "passwordModal") closePasswordModal();
    });
    const pwSave = document.getElementById("passwordSave");
    pwSave?.addEventListener("click", async () => {
      const cur = document.getElementById("pwCurrent").value;
      const nw = document.getElementById("pwNew").value;
      const cf = document.getElementById("pwConfirm").value;
      if (!cur || !nw) { toast("Fill in both password fields"); return; }
      if (nw !== cf) { toast("New passwords do not match"); return; }
      const r = await fetch("/api/v1/account/password", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ current_password: cur, new_password: nw }),
      });
      const b = await r.json().catch(() => ({}));
      if (!r.ok) { toast((b.error && b.error.message) || "Could not change password"); return; }
      closePasswordModal();
      toast("Password changed");
    });
  }

  const nameOpen = document.getElementById("changeNameBtn");
  if (nameOpen) {
    nameOpen.addEventListener("click", () => {
      const shown = (document.getElementById("signedInName")?.textContent || "").trim();
      const input = document.getElementById("nameInput");
      if (input && shown && shown !== "you") input.value = shown;
      openModal("nameModal");
    });
    document.getElementById("nameCancel")?.addEventListener("click", closeNameModal);
    document.querySelectorAll('[data-action="close-name"]').forEach((b) => b.addEventListener("click", closeNameModal));
    document.getElementById("nameModal")?.addEventListener("click", (e) => {
      if (e.target.id === "nameModal") closeNameModal();
    });
    document.getElementById("nameSaveModal")?.addEventListener("click", () => {
      const name = document.getElementById("nameInput").value.trim();
      fetch("/api/v1/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ user_name: name }),
      }).then(async (r) => {
        if (!r.ok) {
          const b = await r.json().catch(() => ({}));
          toast((b.error && b.error.message) || "Could not update name");
          return;
        }
        const shown = document.getElementById("signedInName");
        if (shown) shown.textContent = name || "you";
        closeNameModal();
        toast("Name updated");
      }).catch(() => toast("Could not update name"));
    });
  }

  document.getElementById("appearanceSave")?.addEventListener("click", () => {
    const theme = document.getElementById("themeSelect").value;
    const currency = document.getElementById("currencySelect").value;
    fetch("/api/v1/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ theme, currency }),
    }).then((r) => {
      if (!r.ok) { toast("Could not save settings"); return; }
      try { localStorage.setItem("glowworm-theme", theme); } catch (e) {}
      applyTheme(theme);
      toast("Appearance updated");
    }).catch(() => toast("Could not save settings"));
  });

  const KEY_MASK = "*********";
  const aiProviderSel = document.getElementById("aiProvider");
  const aiKeyInput = document.getElementById("aiKey");
  const aiKeyClear = document.getElementById("aiKeyClear");
  const aiBaseUrlRow = document.getElementById("aiBaseUrlRow");
  let aiHasSavedKey = aiKeyInput ? aiKeyInput.dataset.hasKey === "1" : false;
  let aiKeyUnlocked = false;
  const syncAIKeyRow = () => {
    if (!aiKeyInput) return;
    if (aiHasSavedKey && !aiKeyUnlocked) {
      aiKeyInput.value = KEY_MASK;
      aiKeyInput.type = "text";
      aiKeyInput.readOnly = true;
    } else {
      aiKeyInput.type = "password";
      aiKeyInput.readOnly = false;
      if (aiKeyInput.value === KEY_MASK) aiKeyInput.value = "";
    }
    if (aiKeyClear) aiKeyClear.hidden = !aiHasSavedKey;
  };
  if (aiKeyInput) {
    aiKeyInput.addEventListener("focus", () => {
      if (aiHasSavedKey && !aiKeyUnlocked) {
        aiKeyUnlocked = true;
        aiKeyInput.value = "";
        aiKeyInput.placeholder = "Type a new key to replace the saved one";
        aiKeyInput.type = "password";
        aiKeyInput.readOnly = false;
      }
    });
    aiKeyInput.addEventListener("blur", () => {
      if (aiHasSavedKey && aiKeyUnlocked && aiKeyInput.value.trim() === "") {
        aiKeyUnlocked = false;
        syncAIKeyRow();
      }
    });
  }
  if (aiKeyClear && aiKeyInput) {
    aiKeyClear.addEventListener("click", () => {
      fetch("/api/v1/settings/ai", {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ provider: aiProviderSel.value, clear_key: true }),
      }).then(async (r) => {
        const b = await r.json().catch(() => ({}));
        if (!r.ok) { toast((b.error && b.error.message) || "Could not clear key"); return; }
        aiHasSavedKey = false;
        aiKeyUnlocked = false;
        aiKeyInput.value = "";
        aiKeyInput.placeholder = "sk-…";
        syncAIKeyRow();
        toast("API key cleared");
      }).catch(() => toast("Could not clear key"));
    });
  }
  const syncAIBaseUrlRow = () => {
    if (!aiBaseUrlRow || !aiProviderSel) return;
    const p = aiProviderSel.value;
    aiBaseUrlRow.hidden = !(p === "custom" || p === "ollama" || p === "openrouter");
    if (p === "openrouter") {
      const base = document.getElementById("aiBaseUrl");
      if (base && !base.value) base.placeholder = "https://openrouter.ai/api/v1";
    }
  };
  const aiModelSel = document.getElementById("aiModel");
  const aiModelsStatus = document.getElementById("aiModelsStatus");
  const aiLoadBtn = document.getElementById("aiLoadModels");
  const savedModel = aiModelSel && aiModelSel.options.length > 1 ? aiModelSel.options[1].value : "";
  const loadAIModels = async () => {
    if (!aiModelSel || !aiProviderSel || aiProviderSel.value === "") {
      if (aiModelsStatus) aiModelsStatus.textContent = "Pick a provider first.";
      return;
    }
    if (aiLoadBtn) aiLoadBtn.disabled = true;
    if (aiModelsStatus) aiModelsStatus.textContent = "Loading models…";
    try {
      const r = await fetch("/api/v1/settings/ai/models", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({
          provider: aiProviderSel.value,
          base_url: document.getElementById("aiBaseUrl")?.value.trim() || "",
          api_key: (aiKeyInput.value.trim() === KEY_MASK ? "" : aiKeyInput.value.trim()),
        }),
      });
      const b = await r.json().catch(() => ({}));
      if (!r.ok || !b.ok) throw new Error((b.error && b.error.message) || "could not list models");
      const models = b.models || [];
      const current = aiModelSel.value || savedModel;
      aiModelSel.innerHTML = "";
      aiModelSel.appendChild(new Option("Auto (provider default)", ""));
      models.forEach((m) => aiModelSel.appendChild(new Option(m.name || m.id, m.id)));
      if (current && models.some((m) => m.id === current)) aiModelSel.value = current;
      if (aiModelsStatus) {
        if (b.source === "live") {
          aiModelsStatus.textContent = models.length + " models loaded from the provider.";
        } else if (b.source === "needs-key") {
          aiModelsStatus.textContent = "Enter your API key to load this provider's model list.";
        } else if (models.length) {
          aiModelsStatus.textContent = "Couldn't reach the provider — showing a built-in list." + (b.error ? " (" + b.error + ")" : "");
        } else {
          aiModelsStatus.textContent = "Couldn't reach the provider." + (b.error ? " (" + b.error + ")" : "");
        }
      }
    } catch (e) {
      if (aiModelsStatus) aiModelsStatus.textContent = "Could not load models: " + e.message;
    } finally {
      if (aiLoadBtn) aiLoadBtn.disabled = false;
    }
  };
  if (aiLoadBtn) aiLoadBtn.addEventListener("click", loadAIModels);
  aiProviderSel?.addEventListener("change", () => { syncAIBaseUrlRow(); if (aiProviderSel.value) loadAIModels(); });
  syncAIBaseUrlRow();
  syncAIKeyRow();
  if (aiProviderSel && aiProviderSel.value) loadAIModels();
  document.getElementById("aiSave")?.addEventListener("click", () => {
    const provider = aiProviderSel.value;
    const baseUrl = document.getElementById("aiBaseUrl")?.value.trim() || "";
    if ((provider === "custom" || provider === "ollama") && !baseUrl) {
      toast("Enter the endpoint URL for this provider");
      return;
    }
    // The masked placeholder means "keep the saved key": never send the mask.
    const typedKey = aiKeyInput.value.trim();
    const key = typedKey === KEY_MASK ? "" : typedKey;
    const model = aiModelSel ? aiModelSel.value : "";
    const models = { default: model };
    fetch("/api/v1/settings/ai", {
      method: "PUT",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ provider, api_key: key || undefined, base_url: baseUrl || undefined, models }),
    }).then(async (r) => {
      const b = await r.json().catch(() => ({}));
      if (!r.ok) { toast((b.error && b.error.message) || "Could not save"); return; }
      if (key) { aiHasSavedKey = true; aiKeyUnlocked = false; syncAIKeyRow(); }
      toast("AI provider updated");
      if (aiProviderSel.value) loadAIModels();
    }).catch(() => toast("Could not save"));
  });
  document.getElementById("aiTest")?.addEventListener("click", () => {
    fetch("/api/v1/settings/ai/test", { method: "POST", headers: { "X-CSRF-Token": csrfToken() } })
      .then((r) => r.json())
      .then((b) => toast(b.ok ? `Connection OK (${b.latency_ms}ms)` : `Failed: ${b.error || "unknown error"}`))
      .catch(() => toast("Test failed"));
  });

  const catForm = document.getElementById("categoryForm");
  const catGrid = document.getElementById("categoriesGrid");
  const catColors = document.getElementById("catColors");
  let selectedColor = null;
  if (catColors) {
    catColors.addEventListener("click", (e) => {
      const sw = e.target.closest("[data-cat-color]");
      if (!sw) return;
      selectedColor = sw.dataset.catColor;
      document.getElementById("catColorInput").value = selectedColor;
      catColors.querySelectorAll(".cat-color").forEach((el) => el.classList.toggle("selected", el === sw));
    });
  }
  const openCatForm = (editing) => {
    catForm.hidden = false;
    document.getElementById("catFormTitle").textContent = editing ? "Edit category" : "New category";
    document.getElementById("catSaveBtn").textContent = editing ? "Save changes" : "Add category";
    document.getElementById("catDeleteBtn").hidden = !editing;
    document.getElementById("catTypeRow").hidden = !!editing;
    document.getElementById("catName").focus();
  };
  document.getElementById("addCategoryBtn")?.addEventListener("click", () => {
    catForm.dataset.categoryId = "";
    document.getElementById("catName").value = "";
    document.getElementById("catColorInput").value = "";
    selectedColor = null;
    catColors?.querySelectorAll(".cat-color").forEach((el) => el.classList.remove("selected"));
    document.getElementById("catType").value = "spend";
    openCatForm(false);
  });
  if (window.__EDIT_CATEGORY__) {
    catForm.dataset.categoryId = window.__EDIT_CATEGORY__.id;
    document.getElementById("catName").value = window.__EDIT_CATEGORY__.name;
    document.getElementById("catType").value = window.__EDIT_CATEGORY__.type;
    selectedColor = window.__EDIT_CATEGORY__.color;
    document.getElementById("catColorInput").value = selectedColor;
    catForm.hidden = false;
    openCatForm(true);
    catColors?.querySelectorAll(".cat-color").forEach((el) => {
      el.classList.toggle("selected", el.dataset.catColor === selectedColor);
    });
  }
  document.getElementById("catCancelBtn")?.addEventListener("click", () => { catForm.hidden = true; });
  catForm?.addEventListener("submit", (e) => {
    e.preventDefault();
    const id = catForm.dataset.categoryId;
    const body = {
      name: document.getElementById("catName").value.trim(),
      color: document.getElementById("catColorInput").value,
      type: document.getElementById("catType").value || "spend",
    };
    if (!body.name) { toast("Category name is required"); return; }
    fetch(id ? `/api/v1/categories/${id}` : "/api/v1/categories", {
      method: id ? "PATCH" : "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify(body),
    }).then(async (r) => {
      if (!r.ok) {
        const b = await r.json().catch(() => ({}));
        toast((b.error && b.error.message) || "Could not save the category");
        return;
      }
      toast(id ? "Category updated" : "Category added");
      location.href = "/categories?period=" + new URLSearchParams(location.search).get("period") || "/categories";
    }).catch(() => toast("Could not save the category"));
  });
  document.getElementById("catDeleteBtn")?.addEventListener("click", () => {
    const id = catForm.dataset.categoryId;
    if (!id) return;
    showConfirm("Delete category?", "Its transactions will be marked as Uncategorised.", "Delete category", () => {
      fetch(`/api/v1/categories/${id}`, { method: "DELETE", headers: { "X-CSRF-Token": csrfToken() } })
        .then((r) => { if (!r.ok) { toast("Could not delete"); return; } toast("Category deleted"); location.href = "/categories"; });
    });
  });

  function wireAuditActions(root) {
    (root || document).querySelectorAll("[data-jump-back]").forEach((btn) => {
      if (btn.dataset.wired) return;
      btn.dataset.wired = "1";
      btn.addEventListener("click", () => {
        const id = btn.dataset.jumpBack;
        showConfirm("Restore to this point?", "Everything after this point is undone. Those entries stay in the log, greyed out and collapsed, and can be redone.", "Restore", () => {
          fetch(`/api/v1/audit/${id}/jump-back`, { method: "POST", headers: { "X-CSRF-Token": csrfToken() } })
            .then(async (r) => {
              if (!r.ok) {
                const b = await r.json().catch(() => ({}));
                toast((b.error && b.error.message) || "Restore failed");
                return;
              }
              toast("State restored to this point");
              location.reload();
            });
        });
      });
    });
    (root || document).querySelectorAll("[data-restore-path]").forEach((btn) => {
      if (btn.dataset.wired) return;
      btn.dataset.wired = "1";
      btn.addEventListener("click", (e) => {
        e.preventDefault(); // it sits in a <summary>; don't toggle the group
        e.stopPropagation();
        auditAction(null, `/api/v1/audit/paths/${encodeURIComponent(btn.dataset.restorePath)}/restore`, "POST",
          "Redo these changes?", "The undone changes are re-applied. Anything done since the restore is undone and collapsed instead.",
          "Redo", "Changes redone");
      });
    });
    (root || document).querySelectorAll("[data-audit-menu]").forEach((btn) => {
      if (btn.dataset.wired) return;
      btn.dataset.wired = "1";
      btn.addEventListener("click", (e) => {
        e.stopPropagation();
        openAuditMenu(btn);
      });
    });
  }
  window.wireAuditActions = wireAuditActions;
  wireAuditActions(document);
  function auditAction(id, url, method, title, body, confirmLabel, okMsg) {
    showConfirm(title, body, confirmLabel, () => {
      fetch(url, { method, headers: { "X-CSRF-Token": csrfToken() } })
        .then(async (r) => {
          if (!r.ok) {
            const b = await r.json().catch(() => ({}));
            toast((b.error && b.error.message) || "Action failed");
            return;
          }
          toast(okMsg);
          location.reload();
        });
    });
  }
  function openAuditMenu(btn) {
    closeAuditMenu();
    const id = btn.dataset.auditMenu;
    const menu = document.createElement("div");
    menu.className = "audit-menu";
    menu.id = "auditMenu";
    menu.innerHTML = '<div class="audit-menu-head">History actions</div>';
    menu.addEventListener("click", (e) => e.stopPropagation());
    fetch(`/api/v1/audit/${id}/capabilities`).then((r) => r.json()).then((caps) => {
      if (!caps || caps.error) { toast("Could not load actions"); return; }
      const items = [
        ["revert", "Revert change", "Undoes this change and records the revert in the history.", "Revert",
          () => auditAction(id, `/api/v1/audit/${id}/revert`, "POST",
            "Revert this change?", "The change is undone now; later entries stay collapsed in the timeline and remain restorable.", "Revert", "Change reverted")],
        ["forget", "Keep effect, remove record", "The result stays; this entry disappears from the history.", "Remove record",
          () => auditAction(id, `/api/v1/audit/${id}?mode=forget`, "DELETE",
            "Keep effect, remove record?", "The effect of this change stays in place, but the record is scrubbed from the history.", "Remove record", "Record removed")],
        ["delete", "Delete from history", "Undoes the change and scrubs the record; a tombstone notes the deletion.", "Delete",
          () => auditAction(id, `/api/v1/audit/${id}`, "DELETE",
            "Delete from history?", "The change is undone and its record scrubbed. A tombstone entry notes that a change was deleted here.", "Delete", "Deleted from history")],
      ];
      let any = false;
      for (const [cap, label, desc, cLabel, fn] of items) {
        const row = document.createElement("button");
        row.type = "button";
        row.className = "audit-menu-item" + (cap === "delete" ? " danger" : "");
        if (caps[cap]) {
          row.innerHTML = `<span class="audit-menu-label">${esc(label)}</span><span class="audit-menu-desc">${esc(desc)}</span>`;
          row.addEventListener("click", () => { closeAuditMenu(); fn(); });
          any = true;
        } else {
          row.disabled = true;
          row.innerHTML = `<span class="audit-menu-label">${esc(label)}</span><span class="audit-menu-desc">${esc(caps.reason || "Not available for this entry")}</span>`;
        }
        menu.appendChild(row);
      }
      if (!any) {
        const none = document.createElement("div");
        none.className = "audit-menu-desc";
        none.style.padding = "4px 14px 10px";
        none.textContent = caps.reason || "No actions available for this entry.";
        menu.appendChild(none);
      }
      document.body.appendChild(menu);
      const r = btn.getBoundingClientRect();
      const mw = menu.offsetWidth, mh = menu.offsetHeight;
      let left = Math.min(r.left, window.innerWidth - mw - 12);
      let top = r.bottom + 6;
      if (top + mh > window.innerHeight - 12) top = Math.max(12, r.top - mh - 6);
      menu.style.left = left + "px";
      menu.style.top = top + "px";
      requestAnimationFrame(() => menu.classList.add("open"));
    }).catch(() => toast("Could not load actions"));
  }
  function closeAuditMenu() {
    const m = document.getElementById("auditMenu");
    if (m) m.remove();
  }
  document.addEventListener("click", closeAuditMenu);
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeAuditMenu(); });
});


document.addEventListener("DOMContentLoaded", () => {
  const table = document.getElementById("transactionsTable");
  if (!table) return;
  let sentinel = document.getElementById("transactionsSentinel");
  if (!sentinel || table.dataset.txHasMore !== "1") return;
  const tbody = document.getElementById("transactionsBody");
  if (!tbody) return;

  let loading = false;
  const loadNext = () => {
    if (loading) return;
    if (table.dataset.txHasMore !== "1") return;
    loading = true;
    const params = new URLSearchParams();
    const put = (k, v) => { if (v) params.set(k, v); };
    put("q", table.dataset.txFilterQ);
    put("category", table.dataset.txFilterCategory);
    put("type", table.dataset.txFilterType);
    put("account", table.dataset.txFilterAccount);
    put("period", table.dataset.txFilterPeriod);
    put("from", table.dataset.txFilterFrom);
    put("to", table.dataset.txFilterTo);
    params.set("offset", table.dataset.txNextOffset || "0");
    fetch("/transactions/rows?" + params.toString(), { headers: { "X-Requested-With": "fetch" } })
      .then((r) => {
        if (!r.ok) throw new Error("rows " + r.status);
        const hasMore = r.headers.get("X-Has-More") === "1";
        const nextOffset = r.headers.get("X-Next-Offset");
        return r.text().then((html) => ({ html, hasMore, nextOffset }));
      })
      .then(({ html, hasMore, nextOffset }) => {
        if (html) {
          const tpl = document.createElement("template");
          tpl.innerHTML = html.trim();
          tbody.appendChild(tpl.content);
        }
        if (nextOffset) table.dataset.txNextOffset = nextOffset;
        if (!hasMore) {
          table.dataset.txHasMore = "0";
          if (sentinel) { sentinel.remove(); sentinel = null; }
          if (io) io.disconnect();
        }
        loading = false;
      })
      .catch(() => { loading = false; });
  };

  let io = null;
  if (window.IntersectionObserver) {
    io = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (entry.isIntersecting) loadNext();
      }
    }, { rootMargin: "200px" });
    io.observe(sentinel);
  } else {
    window.addEventListener("scroll", () => {
      if (!sentinel) return;
      const rect = sentinel.getBoundingClientRect();
      if (rect.top < window.innerHeight + 200) loadNext();
    }, { passive: true });
  }
});

document.addEventListener("DOMContentLoaded", () => {
  const body = document.getElementById("transactionsBody");
  if (!body) return;

  body.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-edit-tx]");
    if (!btn) return;
    const tr = btn.closest("tr");
    const id = btn.dataset.editTx;
    const open = document.getElementById("txEditorRow");
    if (open && open.dataset.txId === id) { open.remove(); tr.classList.remove("editing"); return; }
    if (open) open.remove();
    document.querySelectorAll("#transactionsBody tr.editing").forEach((r) => r.classList.remove("editing"));
    tr.classList.add("editing");
    openEditor(tr, id);
  });

  function openEditor(tr, id) {
    fetch(`/api/v1/transactions/${id}`).then((r) => r.json()).then((t) => {
      const editor = document.createElement("tr");
      editor.id = "txEditorRow";
      editor.dataset.txId = id;
      const linked = !!t.transfer_id;
      const cats = window.__CAT_OPTIONS__ || [];
      editor.innerHTML = `<td colspan="6">
        <div class="tx-editor">
          <label class="tx-field">Date
            <input class="field" type="date" id="txeDate" value="${esc(t.date)}" />
          </label>
          <label class="tx-field grow">Description
            <input class="field" type="text" id="txeDescription" value="${esc(t.description)}" placeholder="e.g. Tesco Express" />
          </label>
          <label class="tx-field">Category
            <select class="field" id="txeCategory" ${linked ? "disabled" : ""}>
              ${linked ? `<option selected>Internal transfer</option>` : `<option value="">Select category…</option>` + cats.map((c) => `<option value="${esc(c.id)}"${t.category && t.category.id === c.id ? " selected" : ""}>${esc(c.name)}</option>`).join("")}
            </select>
          </label>
          <label class="tx-field grow" id="txeTransferField">Transfer link
            ${linked
              ? `<select class="field" id="txeCounterpart" disabled><option selected>Linked: ${esc(t.counterpart_tx ? t.counterpart_tx.description + " · " + (t.counterpart_tx.account_name || "") : "transfer leg")}</option></select>`
              : `<select class="field" id="txeCounterpart"><option value="">Not a transfer</option></select>`}
          </label>
          <label class="tx-field">Amount
            <input class="field" type="number" step="0.01" id="txeAmount" value="${(t.amount_minor / 100).toFixed(2)}" />
          </label>
          <div class="tx-editor-actions">
            ${linked ? `<button class="btn btn-ghost btn-xs" id="txeUnlink" type="button">Break link</button>` : ""}
            <button class="btn btn-primary btn-xs" id="txeSave" type="button">Save</button>
            <button class="btn btn-ghost btn-xs" id="txeCancel" type="button">Cancel</button>
          </div>
        </div>
      </td>`;
      tr.after(editor);
      document.getElementById("txeDescription").focus();

      const cpField = document.getElementById("txeCounterpartField") || document.getElementById("txeTransferField");
      const catSel = document.getElementById("txeCategory");
      const loadTransferSuggestions = async () => {
        const sel = document.getElementById("txeCounterpart");
        if (!sel) return;
        try {
          const resp = await fetch(`/api/v1/transactions/${id}/transfer-suggestions`);
          const body = await resp.json();
          const items = body.items || [];
          if (!items.length) {
            sel.innerHTML = `<option value="">No candidates — import the other statement first</option>`;
            return;
          }
          sel.innerHTML = `<option value="">Not a transfer — choose a counterpart…</option>` + items.map((s) => {
            const tx = s.tx;
            return `<option value="${esc(tx.id)}">${esc(tx.date)} · ${esc(tx.description)} · ${fmtMoneyMinor(tx.amount_minor, tx.currency || "GBP")} (${esc(tx.account ? tx.account.name : "")})</option>`;
          }).join("");
        } catch (e2) {
          toast("Could not load transfer suggestions");
        }
      };
      if (!linked) {
        loadTransferSuggestions();
        const cpSel = document.getElementById("txeCounterpart");
        if (cpSel) {
          cpSel.addEventListener("change", () => {
            if (cpSel.value) {
              const internal = (window.__CAT_OPTIONS__ || []).find((c) => c.internal);
              if (internal) catSel.value = internal.id;
              catSel.disabled = true;
            } else {
              catSel.disabled = false;
            }
          });
        }
      }
      document.getElementById("txeCancel").addEventListener("click", () => { editor.remove(); tr.classList.remove("editing"); });
      document.getElementById("txeSave").addEventListener("click", () => {
        const date = document.getElementById("txeDate").value;
        const description = document.getElementById("txeDescription").value.trim();
        const amount = parseFloat(document.getElementById("txeAmount").value);
        if (!date) { toast("Date is required"); return; }
        if (!description) { toast("Description is required"); return; }
        if (isNaN(amount)) { toast("Amount is required"); return; }
        const cpSel = document.getElementById("txeCounterpart");
        if (cpSel && !linked && cpSel.value) {
          fetch(`/api/v1/transactions/${id}/link`, {
            method: "POST",
            headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
            body: JSON.stringify({ counterpart_id: cpSel.value }),
          }).then(async (r) => {
            if (!r.ok) {
              const b = await r.json().catch(() => ({}));
              toast((b.error && b.error.message) || "Could not link");
              return;
            }
            toast("Transfer linked");
            location.reload();
          });
          return;
        }
        const body = {
          date, description,
          amount_minor: Math.round(amount * 100),
          category_id: (!linked && document.getElementById("txeCategory").value) || null,
        };
        fetch(`/api/v1/transactions/${id}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
          body: JSON.stringify(body),
        }).then(async (r) => {
          if (!r.ok) {
            const b = await r.json().catch(() => ({}));
            toast((b.error && b.error.message) || "Could not save");
            return;
          }
          toast("Transaction updated");
          location.reload();
        }).catch(() => toast("Could not save"));
      });
      const unlink = document.getElementById("txeUnlink");
      if (unlink) {
        unlink.addEventListener("click", () => {
          if (!t.transfer_id) return;
          showConfirm("Break the link?", "Both transactions will stay; the transfer link will be removed.", "Break link", () => {
            fetch(`/api/v1/transactions/${id}/unlink`, {
              method: "POST",
              headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
              body: JSON.stringify({}),
            }).then((r) => {
              if (!r.ok) { toast("Could not unlink"); return; }
              toast("Transfer unlinked");
              location.reload();
            });
          });
        });
      }
    }).catch(() => toast("Could not load transaction"));
  }
});


document.addEventListener("DOMContentLoaded", () => {
  const dropzone = document.getElementById("dropzone");
  const fileInput = document.getElementById("fileInput");
  const flowBox = document.getElementById("importFlow");
  const view = document.getElementById("view-import");
  if (!dropzone || !flowBox) return;

  const openFlow = (html) => {
    view?.classList.add("import-active");
    view?.classList.remove("import-reviewing");
    flowBox.hidden = false;
    flowBox.innerHTML = html;
  };
  const closeFlow = () => {
    view?.classList.remove("import-active", "import-reviewing");
    flowBox.hidden = true;
    flowBox.innerHTML = "";
  };
  const flowMessage = (html, withRetry) => {
    openFlow(`<div class="thinking-card panel"><div class="head">${html}</div>
      ${withRetry ? '<button class="btn btn-ghost btn-xs" type="button" data-close-flow>Try another file</button>' : ""}</div>`);
  };
  flowBox.addEventListener("click", (e) => {
    if (e.target.closest("[data-close-flow]")) closeFlow();
  });
  const jsonHeaders = () => ({ "Content-Type": "application/json", "X-CSRF-Token": csrfToken() });
  const errMessage = (b, status) => (b && b.error && b.error.message) || `server error ${status}`;

  const upload = (file) => {
    const fd = new FormData();
    fd.append("file", file);
    const accSel = document.getElementById("importAccount");
    if (accSel && accSel.value) fd.append("account_id", accSel.value);
    flowMessage(`<span class="spinner"></span> Reading ${esc(file.name)}…`);
    fetch("/api/v1/imports", {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken() },
      body: fd,
    }).then(async (r) => {
      const b = await r.json().catch(() => ({}));
      if (!r.ok) {
        flowMessage(`Upload failed: ${esc(errMessage(b, r.status))}`, true);
        return;
      }
      renderBatch(b);
    }).catch((e) => {
      flowMessage(`Upload failed: ${esc(e && e.message ? e.message : "network error — is the server running?")}`, true);
    });
  };

  dropzone.addEventListener("dragover", (e) => { e.preventDefault(); dropzone.classList.add("drag"); });
  dropzone.addEventListener("dragleave", () => dropzone.classList.remove("drag"));
  dropzone.addEventListener("drop", (e) => {
    e.preventDefault(); dropzone.classList.remove("drag");
    if (e.dataTransfer.files.length) upload(e.dataTransfer.files[0]);
  });
  // The whole box opens the picker; the inner "browse files" button just bubbles here.
  dropzone.addEventListener("click", (e) => { if (e.target !== fileInput) fileInput.click(); });
  fileInput.addEventListener("change", () => {
    if (fileInput.files.length) upload(fileInput.files[0]);
    fileInput.value = "";
  });

  // The server parses Go reference layouts ("02/01/2006" = DD/MM/YYYY); users
  // only see the familiar codes.
  const GO_TO_HUMAN = [["January", "MMMM"], ["Jan", "MMM"], ["Monday", "dddd"], ["Mon", "ddd"],
    ["2006", "YYYY"], ["Z07:00", "Z"], ["15", "HH"], ["06", "YY"], ["01", "MM"], ["02", "DD"],
    ["03", "hh"], ["04", "mm"], ["05", "ss"], ["_2", "D"], ["PM", "A"], ["pm", "a"],
    ["1", "M"], ["2", "D"], ["3", "h"], ["4", "m"], ["5", "s"]];
  const HUMAN_TO_GO = [["YYYY", "2006"], ["YY", "06"], ["MMMM", "January"], ["MMM", "Jan"], ["MM", "01"], ["M", "1"],
    ["dddd", "Monday"], ["ddd", "Mon"], ["DD", "02"], ["D", "2"], ["HH", "15"], ["hh", "03"], ["h", "3"],
    ["mm", "04"], ["m", "4"], ["ss", "05"], ["s", "5"], ["A", "PM"], ["a", "pm"], ["Z", "Z07:00"]];
  const convertLayout = (str, table) => {
    let out = "";
    for (let i = 0; i < str.length;) {
      const hit = table.find(([from]) => str.startsWith(from, i));
      out += hit ? hit[1] : str[i];
      i += hit ? hit[0].length : 1;
    }
    return out;
  };
  const goLayoutToHuman = (layout) => convertLayout(layout || "", GO_TO_HUMAN);
  const humanToGoLayout = (fmt) => (!fmt || fmt.includes("2006") ? fmt : convertLayout(fmt, HUMAN_TO_GO));

  function mappingRowHTML(col, prevRole) {
    const roles = ["date","description","amount","amount_in","amount_out","currency","balance","type_hint","memo","source","destination","ignore"];
    const role = col.role || "ignore";
    const opts = roles.map((r) =>
      `<option value="${r}"${role === r ? " selected" : ""}>${r.replace(/_/g, " ")}</option>`).join("");
    const changed = prevRole !== undefined && prevRole !== role;
    return `<tr data-source="${esc(col.source)}"${changed ? ' class="mapping-changed"' : ""}><td>${esc(col.source)}</td>
      <td><select class="field select" style="min-width:150px">${opts}</select>
      ${changed ? `<span class="badge badge-transfer" title="Was “${esc((prevRole || "ignore").replace(/_/g, " "))}”">AI changed</span>` : ""}</td></tr>`;
  }

  const renderBatch = (b, prev) => {
    if (b.status === "failed") {
      const why = b.parse_error || "The file could not be read.";
      const kind = (b.mime || "").toLowerCase();
      const pdfHint = kind.includes("pdf")
        ? '<p class="login-error" style="margin-top:8px">Scanned PDFs have no text layer — the optional AI vision path (Settings → AI) can read them, or export a CSV from your bank.</p>'
        : "";
      openFlow(`
        <div class="panel category-form">
          <h3 class="category-form-title">Import failed</h3>
          <p class="login-error">${esc(why)}</p>
          ${pdfHint}
          <p class="login-error" style="color:var(--muted)">Nothing was imported, and this attempt does not appear in your history.</p>
          <button class="btn btn-primary" type="button" data-close-flow>Try another file</button>
        </div>`);
      return;
    }
    if (b.status === "awaiting_review") {
      openReview(b.id, true);
      return;
    }
    const d = b.detection || {};
    const aiProposal = !!d.ai_proposed;
    const prevRoles = {};
    if (aiProposal && prev) (prev.columns || []).forEach((c) => { prevRoles[c.source] = c.role || "ignore"; });
    const sheets = (d.sheets || []).map((s) => `<option value="${esc(s)}"${s === d.sheet ? " selected" : ""}>${esc(s)}</option>`).join("");
    const cols = (d.columns || []).map((c) => mappingRowHTML(c, aiProposal && prev ? (prevRoles[c.source] || "ignore") : undefined)).join("");
    const nChanged = aiProposal && prev ? (d.columns || []).filter((c) => (prevRoles[c.source] || "ignore") !== (c.role || "ignore")).length : 0;
    const sampleHeader = (d.sample && d.sample[0]) || [];
    const sampleBody = (d.sample || []).slice(1, 5);
    const headerHTML = sampleHeader.map((c) => `<th>${esc(c)}</th>`).join("");
    const sample = sampleBody.map((r) => `<tr>${r.map((c) => `<td>${esc(c)}</td>`).join("")}</tr>`).join("");
    const title = d.matched_schema ? `Matched saved schema “${esc(d.matched_schema)}”`
      : aiProposal ? `AI proposal${d.proposed_name ? `: ${esc(d.proposed_name)}` : ""}`
      : "Detected schema (local)";
    const dateColIdx = sampleHeader.findIndex((h) => {
      const col = (d.columns || []).find((c) => c.source === h);
      return col && col.role === "date";
    });
    let dateSample = "";
    if (dateColIdx >= 0) {
      for (const row of sampleBody) {
        if (row[dateColIdx]) { dateSample = row[dateColIdx]; break; }
      }
    }
    const intro = aiProposal
      ? `<div class="ai-review-banner">
          <strong>Review the AI's mapping before continuing.</strong>
          ${prev ? (nChanged ? `It changed ${nChanged} column role${nChanged === 1 ? "" : "s"} (highlighted).` : "It agreed with the existing roles.") : ""}
          Nothing is parsed or imported until you accept — fix any role first if it looks wrong.
        </div>`
      : `<p class="muted">Check the column roles below. If they look wrong, fix them inline or ask the AI to suggest a mapping (uses tokens).</p>`;
    openFlow(`
      <div class="panel category-form">
        <h3 class="category-form-title">${title}</h3>
        ${b.parse_error ? `<p class="login-error">${esc(b.parse_error)}</p>` : ""}
        ${intro}
        <div class="tx-table-wrap" style="overflow-x:auto">
        <table class="tx-table"><thead><tr><th>Column</th><th>Role</th></tr></thead>
        <tbody id="mappingRows">${cols}</tbody></table></div>
        <div class="field-row" style="margin-top:10px">
          <label>Date format
            <button type="button" class="info-btn" id="dateInfoBtn" aria-label="Date format help">i</button>
          </label>
          <input class="field" id="mapDateFormat" value="${esc(goLayoutToHuman(d.date_format))}" placeholder="leave blank to auto-detect, or e.g. DD/MM/YYYY" />
        </div>
        <div class="date-preview muted" id="datePreview" data-sample="${esc(dateSample)}">${dateSample ? "Checking date format…" : "No date column detected yet — set a column's role to “date”."}</div>
        <div id="dateInfoPopover" class="info-popover" hidden>
          <strong>How date formats work</strong>
          <p class="muted">Leave the field blank to let Glowworm auto-detect from ~15 common layouts.</p>
          <p class="muted">To pin a format, write it with these codes — anything else (<code>/</code>, <code>-</code>, spaces) must appear exactly as written:</p>
          <p class="muted"><code>DD</code> day 02 · <code>D</code> day 2 · <code>MM</code> month 06 · <code>MMM</code> Jun · <code>MMMM</code> June · <code>YYYY</code> 2026 · <code>YY</code> 26 · <code>HH</code> hour 0–23 · <code>hh</code> hour 1–12 · <code>mm</code> minutes · <code>ss</code> seconds · <code>A</code> AM/PM</p>
          <p class="muted">Click a format to use it:</p>
          <ul class="date-format-list">
            <li><button type="button" data-fmt="DD/MM/YYYY">DD/MM/YYYY</button> — UK, e.g. 02/06/2026</li>
            <li><button type="button" data-fmt="MM/DD/YYYY">MM/DD/YYYY</button> — US, e.g. 06/02/2026</li>
            <li><button type="button" data-fmt="YYYY-MM-DD">YYYY-MM-DD</button> — ISO, e.g. 2026-06-02</li>
            <li><button type="button" data-fmt="D MMM YYYY">D MMM YYYY</button> — e.g. 2 Jun 2026</li>
            <li><button type="button" data-fmt="D MMMM YYYY">D MMMM YYYY</button> — e.g. 2 June 2026</li>
            <li><button type="button" data-fmt="YYYY-MM-DD HH:mm:ss">YYYY-MM-DD HH:mm:ss</button> — ISO with time</li>
            <li><button type="button" data-fmt="DD/MM/YYYY HH:mm">DD/MM/YYYY HH:mm</button> — UK with time</li>
          </ul>
        </div>
        ${sheets ? `<div class="field-row"><label>Sheet</label><select class="field select" id="mapSheet">${sheets}</select></div>` : ""}
        <div class="section-head"><h2>Sample rows <span class="muted" style="font-weight:normal">(raw text from the file)</span></h2></div>
        <div style="overflow-x:auto"><table class="tx-table"><thead><tr>${headerHTML}</tr></thead><tbody>${sample}</tbody></table></div>
        <div class="category-form-actions">
          ${aiProposal
            ? (prev ? '<button class="btn btn-ghost" id="mapDiscardAI" type="button">Discard AI proposal</button>' : "")
            : '<button class="btn btn-ghost" id="mapAI" type="button">Ask AI to detect</button>'}
          <span class="spacer"></span>
          <button class="btn btn-ghost" id="mapCancel" type="button">Cancel</button>
          <button class="btn btn-primary" id="mapApply" type="button">${aiProposal ? "Accept &amp; continue" : "Parse &amp; continue"}</button>
        </div>
      </div>`);
    document.getElementById("mapCancel").addEventListener("click", closeFlow);
    document.getElementById("mapDiscardAI")?.addEventListener("click", () => {
      renderBatch(Object.assign({}, b, { detection: prev, parse_error: null }));
      toast("AI proposal discarded");
    });

    const infoBtn = document.getElementById("dateInfoBtn");
    const infoPop = document.getElementById("dateInfoPopover");
    infoBtn?.addEventListener("click", () => { infoPop.hidden = !infoPop.hidden; });
    infoPop?.addEventListener("click", (e) => {
      const btn = e.target.closest("button[data-fmt]");
      if (!btn) return;
      document.getElementById("mapDateFormat").value = btn.dataset.fmt;
      refreshDatePreview();
      infoPop.hidden = true;
    });

    const previewEl = document.getElementById("datePreview");
    const dateInput = document.getElementById("mapDateFormat");
    let previewToken = 0;
    function refreshDatePreview() {
      const sample = previewEl?.dataset.sample || "";
      if (!sample) return;
      const fmt = dateInput.value.trim();
      const myToken = ++previewToken;
      previewEl.textContent = "Checking date format…";
      fetch("/api/v1/imports/preview-date", {
        method: "POST",
        headers: jsonHeaders(),
        body: JSON.stringify({ sample, format: humanToGoLayout(fmt) }),
      }).then((r) => r.json()).then((body) => {
        if (myToken !== previewToken) return;
        if (!body.iso) {
          previewEl.innerHTML = `<span class="date-preview-fail">Couldn't read “${esc(sample)}”${fmt ? ` as ${esc(fmt)}` : ""} — pick a format from the “i” menu${fmt ? " or clear the field to auto-detect" : ""}.</span>`;
          return;
        }
        const detected = esc(goLayoutToHuman(body.layout));
        if (fmt && body.layout !== humanToGoLayout(fmt)) {
          // The parser falls back to auto-detection when a pinned format
          // doesn't match, so don't imply the pin worked.
          previewEl.innerHTML = `<span class="date-preview-fail">${esc(fmt)} doesn't match “${esc(sample)}”.</span>
            <span class="muted">Auto-detection would read it as ${esc(humanDate(body.iso))} (${detected}) — fix the format or clear it.</span>`;
          return;
        }
        const layoutNote = fmt ? "" : ` <span class="muted">· detected format ${detected}</span>`;
        previewEl.innerHTML = `<span class="date-preview-ok">Reads “${esc(sample)}” as ${esc(humanDate(body.iso))}</span>${layoutNote}`;
      }).catch(() => {
        if (myToken !== previewToken) return;
        previewEl.textContent = "Preview unavailable";
      });
    }
    dateInput?.addEventListener("input", () => {
      clearTimeout(dateInput._t);
      dateInput._t = setTimeout(refreshDatePreview, 250);
    });
    refreshDatePreview();

    document.getElementById("mapAI")?.addEventListener("click", (e) => {
      e.currentTarget.disabled = true;
      const ncols = (d.columns || []).length;
      const nrows = Math.max(0, (d.sample || []).length - 1);
      const panel = document.createElement("div");
      panel.className = "ai-progress panel";
      panel.innerHTML = `
        <div class="ai-progress-head"><span class="spinner"></span> Asking the AI to detect your schema</div>
        <ul class="ai-progress-steps">
          <li id="aiStep0" class="active">Preparing ${ncols} column${ncols === 1 ? "" : "s"} and ${nrows} sample row${nrows === 1 ? "" : "s"}…</li>
          <li id="aiStep1">Waiting for the model to respond…</li>
          <li id="aiStep2">Validating the proposed mapping…</li>
        </ul>
        <div class="muted" id="aiElapsed">Elapsed: 0.0s</div>`;
      flowBox.querySelector(".category-form").appendChild(panel);
      const t0 = Date.now();
      const tick = setInterval(() => {
        const dt = (Date.now() - t0) / 1000;
        panel.querySelector("#aiElapsed").textContent = `Elapsed: ${dt.toFixed(1)}s`;
        if (dt > 1.5) panel.querySelector("#aiStep0").classList.remove("active");
        if (dt > 1.5) panel.querySelector("#aiStep1").classList.add("active");
      }, 100);
      const fail = (msg) => {
        clearInterval(tick);
        panel.querySelector(".ai-progress-head").innerHTML = `<strong>AI detection failed</strong>`;
        toast(msg || "AI detection failed");
        setTimeout(() => panel.remove(), 1200);
        const btn = document.getElementById("mapAI");
        if (btn) btn.disabled = false;
      };
      fetch(`/api/v1/imports/${b.id}/schema/ai-detect`, {
        method: "POST",
        headers: { "X-CSRF-Token": csrfToken() },
      }).then(async (r) => {
        panel.querySelector("#aiStep1").classList.remove("active");
        panel.querySelector("#aiStep2").classList.add("active");
        const nb = await r.json().catch(() => ({}));
        if (!r.ok) { fail(errMessage(nb, r.status)); return; }
        clearInterval(tick);
        panel.querySelector(".ai-progress-head").innerHTML = `<strong>Got a proposal · loading it for your review…</strong>`;
        setTimeout(() => renderBatch(nb, d), 350);
      }).catch(() => fail());
    });
    document.getElementById("mapApply").addEventListener("click", (e) => {
      const btn = e.currentTarget;
      const rows = Array.from(document.querySelectorAll("#mappingRows tr")).map((tr) => ({
        source: tr.dataset.source,
        role: tr.querySelector("select").value,
        date_format: humanToGoLayout(document.getElementById("mapDateFormat").value.trim()),
      }));
      const m = {
        kind: d.kind || "csv", has_header: d.has_header !== false, delimiter: d.delimiter || ",",
        columns: rows, date_format: humanToGoLayout(document.getElementById("mapDateFormat").value.trim()),
        sheet: document.getElementById("mapSheet") ? document.getElementById("mapSheet").value : undefined,
        confidence: d.confidence || 0.5,
      };
      btn.disabled = true;
      fetch(`/api/v1/imports/${b.id}/schema`, {
        method: "POST",
        headers: jsonHeaders(),
        body: JSON.stringify({ mapping: m }),
      }).then(async (r) => {
        const nb = await r.json().catch(() => ({}));
        btn.disabled = false;
        if (!r.ok) { toast(errMessage(nb, r.status)); return; }
        renderBatch(nb);
      }).catch(() => { btn.disabled = false; toast("Re-parse failed"); });
    });
  };


  // Money in can be a refund, which belongs in the original spend category.
  const catsFor = (type) => (window.__IMPORT_CATS__ || []).filter((c) =>
    type === "income" || c.type === "internal" || c.type === "spend");
  const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;

  function sourceBadge(g) {
    if (g.source === "user") return '<span class="badge badge-income">You</span>';
    if (g.source === "ai") return `<span class="badge badge-transfer">AI</span>`;
    if (g.source === "local") return '<span class="badge badge-soft">Classifier</span>';
    return "";
  }

  function buildReview(body) {
    const R = {
      batchID: body.batch.id, batch: body.batch, aiAvailable: !!body.ai_available,
      groups: [], byKey: new Map(), dups: [], running: false, finished: false, aiFailed: false,
    };
    (body.rows || []).forEach((row) => {
      if (row.duplicate_of) { R.dups.push(row); return; }
      let g = R.byKey.get(row.group_key);
      if (!g) {
        g = {
          id: R.groups.length, key: row.group_key, type: row.type, description: row.description,
          destination: row.destination || "", currency: row.currency || "GBP", rows: [], total: 0, firstLine: row.line_no,
          categoryId: null, categoryName: "", source: "none", confidence: 0, suggestion: null, note: "",
        };
        R.byKey.set(row.group_key, g);
        R.groups.push(g);
      }
      g.rows.push(row);
      g.total += row.amount_minor;
      if (g.rows.length === 1 || row.category_source === "user") {
        g.categoryId = !row.needs_review && row.category_id ? row.category_id : null;
        g.categoryName = row.category_name || "";
        g.source = row.category_source || "none";
        g.confidence = row.category_confidence || 0;
        g.suggestion = row.suggested_category_id
          ? { id: row.suggested_category_id, name: row.suggested_category_name, conf: row.suggested_confidence || 0 } : null;
      }
    });
    R.groups.sort((a, b) => (b.rows.length - a.rows.length) || (a.firstLine - b.firstLine));
    R.groups.forEach((g, i) => { g.order = i; g.state = g.categoryId ? "done" : "review"; });
    R.byId = new Map(R.groups.map((g) => [String(g.id), g]));
    return R;
  }

  function groupHTML(g, R) {
    const n = g.rows.length;
    const dates = g.rows.map((r) => r.date).sort();
    const when = n > 1 && dates[0] !== dates[n - 1] ? `${dates[0]} – ${dates[n - 1]}` : dates[0];
    const opts = catsFor(g.type);
    if (g.categoryId && !opts.some((c) => c.id === g.categoryId)) {
      opts.push({ id: g.categoryId, name: g.categoryName || "Current category" });
    }
    const select = `<select class="field select" data-cat aria-label="Category for ${esc(g.description)}">
        <option value="">${g.state === "done" ? "Move back to review" : "Choose category…"}</option>
        ${opts.map((c) => `<option value="${esc(c.id)}"${c.id === g.categoryId ? " selected" : ""}>${esc(c.name)}</option>`).join("")}
      </select>`;
    let status = "";
    if (g.state === "pending") {
      status = `<span class="review-checking"><span class="spinner"></span>${R.phase === "ai" ? "Asking the AI…" : "Checking…"}</span>`;
    } else if (g.state === "review") {
      if (g.suggestion) {
        status = `<button type="button" class="review-suggest" data-accept title="Accept the AI's suggestion">
          AI suggests <strong>${esc(g.suggestion.name)}</strong> <svg class="icon"><use href="#i-check"/></svg></button>`;
      } else if (g.note) {
        status = `<span class="review-note">${esc(g.note)}</span>`;
      }
    } else {
      status = sourceBadge(g);
    }
    const rowsTable = n > 1 ? `<details class="review-group-rows"><summary>Show ${n} transactions</summary><table>
        ${g.rows.map((r) => `<tr><td>${esc(r.date)}</td><td class="num tx-amount ${r.amount_minor >= 0 ? "pos" : "neg"}">${fmtMoneySigned(r.amount_minor, r.currency || g.currency)}</td></tr>`).join("")}
      </table></details>` : "";
    return `<div class="review-group" data-group="${g.id}" data-order="${g.order}" data-state="${g.state}">
        <div class="review-group-main">
          <div class="review-group-desc" title="${esc(g.description)}">${esc(g.description)}${n > 1 ? ` <span class="badge badge-soft">×${n}</span>` : ""}</div>
          <div class="review-group-sub">${g.destination ? `→ ${esc(g.destination)} · ` : ""}${esc(when)}</div>
        </div>
        <div class="tx-amount ${g.total >= 0 ? "pos" : "neg"}" title="${n > 1 ? "Total of the group" : ""}">${fmtMoneySigned(g.total, g.currency)}</div>
        <div class="review-group-cat">${select}${status}</div>
        ${rowsTable}
      </div>`;
  }

  const openReview = (batchID, run) => {
    openFlow(`<div class="thinking-card panel"><div class="head"><span class="spinner"></span> Loading transactions…</div></div>`);
    fetch(`/api/v1/imports/${batchID}/review`).then(async (r) => {
      const body = await r.json().catch(() => ({}));
      if (!r.ok) { flowMessage(`Couldn't load the review: ${esc(errMessage(body, r.status))}`, true); return; }
      const R = buildReview(body);
      if (run) R.groups.forEach((g) => { if (g.source !== "user") { g.state = "pending"; g.suggestion = null; } });
      renderReview(R);
      if (run && R.groups.length) {
        startPipeline(R, `/api/v1/imports/${batchID}/categorise/stream`, "full");
        return;
      }
      setStep("local", "done", "Done");
      setStep("ai", R.aiAvailable ? "done" : "skipped", R.aiAvailable ? "Done" : "Not configured — add a provider in Settings → AI");
      finishPipeline(R);
    }).catch(() => flowMessage("Couldn't load the review — is the server running?", true));
  };

  function renderReview(R) {
    const b = R.batch;
    const d = b.detection || null;
    const kind = d && d.kind ? d.kind : "";
    const nRows = R.groups.reduce((s, g) => s + g.rows.length, 0);
    const accounts = window.__IMPORT_ACCOUNTS__ || [];
    const defaultAcc = b.account_id || document.getElementById("importAccount")?.value || (accounts[0] && accounts[0].id) || "";
    const remembersSchema = kind === "csv" || kind === "xlsx";
    const dupRows = R.dups.map((row) => `<tr data-row-id="${esc(row.id)}">
        <td class="cell-sub">${esc(row.date)}</td><td>${esc(row.description)}</td>
        <td class="num tx-amount ${row.amount_minor >= 0 ? "pos" : "neg"}">${fmtMoneySigned(row.amount_minor, row.currency || "GBP")}</td>
        <td><button class="btn btn-ghost btn-xs" type="button" data-force>Import anyway</button></td></tr>`).join("");
    openFlow(`
      <div class="panel import-review" id="importReview">
        <div class="import-review-head">
          <div>
            <h3 class="category-form-title">Review categories</h3>
            <p class="muted">${esc(b.filename || "")} · ${plural(nRows, "transaction")} in ${plural(R.groups.length, "group")}${R.dups.length ? ` · ${plural(R.dups.length, "duplicate")} skipped` : ""}</p>
          </div>
          ${b.schema_name ? `<span class="badge badge-income review-schema-badge" title="The column layout came from this saved schema">Saved schema · ${esc(b.schema_name)}</span>` : ""}
        </div>
        <ol class="cat-pipeline">
          <li data-step="local"><span class="step-num">1</span><div><strong>Quick match</strong><span class="step-note">Your rules and trained classifier</span></div></li>
          <li data-step="ai"><span class="step-num">2</span><div><strong>AI</strong><span class="step-note">${R.aiAvailable ? "Categorises what's left" : "Not configured — add a provider in Settings → AI"}</span></div></li>
          <li data-step="you"><span class="step-num">3</span><div><strong>Your review</strong><span class="step-note">Anything the AI wasn't sure about</span></div></li>
        </ol>
        <div class="progress-bar" id="catProgress"><div class="progress-fill" id="catBar" style="width:0%"></div></div>
        ${R.dups.length ? `<details class="review-dups" role="note">
          <summary><svg class="icon"><use href="#i-alert"/></svg>
            <span><strong>${plural(R.dups.length, "duplicate transaction")} skipped</strong>
            <span class="review-dups-sub">Already in your account, so they won't be imported again. Check them and choose “Import anyway” for any that are real.</span></span>
            <svg class="icon review-dups-chev"><use href="#i-chevron-down"/></svg></summary>
          <div style="overflow-x:auto"><table class="tx-table"><tbody>${dupRows}</tbody></table></div></details>` : ""}
        <div class="review-cols">
          <section class="review-col review-col-todo" aria-label="Needs your review">
            <header class="review-col-head"><h4>Needs your review</h4><span class="count" id="todoCount"></span>
              <button class="btn btn-ghost btn-xs" type="button" id="acceptAllSuggestions" hidden>Accept all AI suggestions</button></header>
            <div class="review-list" id="todoList"></div>
            <div class="review-empty" id="todoEmpty" hidden>Nothing left to review.</div>
          </section>
          <section class="review-col review-col-done" aria-label="Categorised">
            <header class="review-col-head"><h4>Categorised</h4><span class="count" id="doneCount"></span></header>
            <div class="review-list" id="doneList"></div>
            <div class="review-empty" id="doneEmpty" hidden>Categorised transactions will appear here.</div>
          </section>
        </div>
        <div class="review-footer">
          <div class="field-row">
            <label for="reviewAccount">Import into</label>
            <select class="field select" id="reviewAccount">
              ${accounts.map((a) => `<option value="${esc(a.id)}"${a.id === defaultAcc ? " selected" : ""}>${esc(a.name)}</option>`).join("")}
            </select>
          </div>
          ${remembersSchema ? `<div class="field-row review-schema-name">
            <label for="reviewSchemaName">Remember this format as</label>
            <input class="field" id="reviewSchemaName" maxlength="80" />
          </div>` : ""}
          <span class="spacer"></span>
          <button class="btn btn-ghost" id="reviewCancel" type="button">Cancel</button>
          <button class="btn btn-primary" id="reviewConfirm" type="button">Finalise import</button>
        </div>
      </div>`);

    view?.classList.add("import-reviewing");
    const todoList = document.getElementById("todoList");
    const doneList = document.getElementById("doneList");
    R.el = { todoList, doneList, root: document.getElementById("importReview") };
    const frag = { todo: document.createDocumentFragment(), done: document.createDocumentFragment() };
    R.groups.forEach((g) => {
      const tpl = document.createElement("template");
      tpl.innerHTML = groupHTML(g, R).trim();
      g.el = tpl.content.firstElementChild;
      (g.state === "done" ? frag.done : frag.todo).appendChild(g.el);
    });
    todoList.appendChild(frag.todo);
    doneList.appendChild(frag.done);
    updateCounts(R);

    const nameInput = document.getElementById("reviewSchemaName");
    const accSel = document.getElementById("reviewAccount");
    const defaultName = () => {
      if (b.schema_name) return b.schema_name;
      if (d && d.proposed_name) return d.proposed_name;
      const acc = accounts.find((a) => a.id === accSel.value);
      return acc ? `${acc.name} ${kind.toUpperCase()}` : (b.filename || "");
    };
    if (nameInput) {
      nameInput.value = defaultName();
      nameInput.addEventListener("input", () => { nameInput.dataset.touched = "1"; });
      accSel.addEventListener("change", () => { if (!nameInput.dataset.touched) nameInput.value = defaultName(); });
    }

    R.el.root.addEventListener("change", (e) => {
      const sel = e.target.closest("[data-cat]");
      if (!sel) return;
      const g = R.byId.get(sel.closest(".review-group").dataset.group);
      if (g) setGroupCategory(R, g, sel.value || null);
    });
    R.el.root.addEventListener("click", (e) => {
      const accept = e.target.closest("[data-accept]");
      if (accept) {
        const g = R.byId.get(accept.closest(".review-group").dataset.group);
        if (g && g.suggestion) setGroupCategory(R, g, g.suggestion.id, g.suggestion.name);
        return;
      }
      const force = e.target.closest("[data-force]");
      if (force) {
        force.disabled = true;
        patchReview(R, { force_import: [force.closest("tr").dataset.rowId] })
          .then(() => openReview(R.batchID, false))
          .catch((err) => { force.disabled = false; toast(err.message); });
        return;
      }
      if (e.target.closest("[data-retry-ai]")) {
        R.groups.forEach((g) => { if (g.state === "review" && g.source !== "user") { g.state = "pending"; g.note = ""; placeGroup(R, g); } });
        startPipeline(R, `/api/v1/imports/${R.batchID}/categorise/ai/stream`, "ai");
      }
    });
    document.getElementById("acceptAllSuggestions").addEventListener("click", () => {
      const targets = R.groups.filter((g) => g.state === "review" && g.suggestion);
      patchReview(R, { accept_suggestions: true }).then(() => {
        targets.forEach((g) => {
          Object.assign(g, { state: "done", categoryId: g.suggestion.id, categoryName: g.suggestion.name, source: "ai", confidence: g.suggestion.conf, suggestion: null });
          placeGroup(R, g);
        });
        updateCounts(R);
        toast(`Accepted ${plural(targets.length, "suggestion")}`);
      }).catch((err) => toast(err.message));
    });
    document.getElementById("reviewCancel").addEventListener("click", () => {
      if (R.es) R.es.close();
      fetch(`/api/v1/imports/${R.batchID}/cancel`, { method: "POST", headers: { "X-CSRF-Token": csrfToken() } });
      closeFlow();
    });
    document.getElementById("reviewConfirm").addEventListener("click", () => {
      const left = R.groups.filter((g) => g.state !== "done").reduce((s, g) => s + g.rows.length, 0);
      if (left > 0) {
        showConfirm(`${plural(left, "transaction")} still ${left === 1 ? "needs" : "need"} a category`,
          "They'll be imported as Uncategorised — you can categorise them later from Transactions.",
          "Import anyway", () => confirmImport(R), "primary");
      } else {
        confirmImport(R);
      }
    });
  }

  function patchReview(R, patch) {
    return fetch(`/api/v1/imports/${R.batchID}/review`, {
      method: "PATCH", headers: jsonHeaders(), body: JSON.stringify(patch),
    }).then(async (r) => {
      if (!r.ok) {
        const b = await r.json().catch(() => ({}));
        throw new Error(errMessage(b, r.status));
      }
    });
  }

  function setGroupCategory(R, g, catID, catName) {
    const before = { state: g.state, categoryId: g.categoryId, categoryName: g.categoryName, source: g.source, confidence: g.confidence };
    if (catID) {
      const c = (window.__IMPORT_CATS__ || []).find((x) => x.id === catID);
      Object.assign(g, { state: "done", categoryId: catID, categoryName: catName || (c ? c.name : ""), source: "user", confidence: 0 });
    } else {
      Object.assign(g, { state: "review", categoryId: null, categoryName: "", source: "none", confidence: 0 });
    }
    placeGroup(R, g);
    updateCounts(R);
    patchReview(R, { set_category: { row_ids: g.rows.map((r) => r.id), category_id: catID || "" } }).catch((err) => {
      Object.assign(g, before);
      placeGroup(R, g);
      updateCounts(R);
      toast(`Couldn't save: ${err.message}`);
    });
  }

  function placeGroup(R, g) {
    if (!R.el) return;
    const wasOpen = g.el && g.el.querySelector("details[open]");
    const tpl = document.createElement("template");
    tpl.innerHTML = groupHTML(g, R).trim();
    const el = tpl.content.firstElementChild;
    if (wasOpen) el.querySelector("details")?.setAttribute("open", "");
    const list = g.state === "done" ? R.el.doneList : R.el.todoList;
    let before = null;
    if (g.state === "done") {
      // Categorised reads in the order groups landed there, newest at the bottom.
      if (g.el && g.el.parentNode === list) before = g.el.nextSibling;
    } else {
      for (const child of list.children) {
        if (child !== g.el && Number(child.dataset.order) > g.order) { before = child; break; }
      }
    }
    if (g.el) g.el.remove();
    list.insertBefore(el, before);
    g.el = el;
  }

  function updateCounts(R) {
    const sum = (gs) => gs.reduce((s, g) => s + g.rows.length, 0);
    const todo = R.groups.filter((g) => g.state !== "done");
    const done = R.groups.filter((g) => g.state === "done");
    const label = (gs) => {
      const n = sum(gs);
      return n === gs.length ? plural(n, "transaction") : `${plural(n, "transaction")} · ${plural(gs.length, "group")}`;
    };
    document.getElementById("todoCount").textContent = todo.length ? label(todo) : "";
    document.getElementById("doneCount").textContent = done.length ? label(done) : "";
    document.getElementById("todoEmpty").hidden = todo.length > 0;
    document.getElementById("doneEmpty").hidden = done.length > 0;
    document.getElementById("acceptAllSuggestions").hidden = R.running || !todo.some((g) => g.state === "review" && g.suggestion);
    const btn = document.getElementById("reviewConfirm");
    const total = sum(R.groups);
    btn.disabled = R.running || total === 0;
    btn.textContent = R.running ? "Categorising…" : `Finalise import · ${plural(total, "transaction")}`;
  }

  function setStep(name, state, note) {
    const li = document.querySelector(`.cat-pipeline [data-step="${name}"]`);
    if (!li) return;
    li.dataset.state = state;
    if (note !== undefined) li.querySelector(".step-note").innerHTML = note;
  }
  function setBar(fraction) {
    const bar = document.getElementById("catBar");
    if (bar) bar.style.width = `${Math.max(0, Math.min(100, Math.round(fraction * 100)))}%`;
  }

  function startPipeline(R, url, mode) {
    R.running = true;
    R.finished = false;
    R.aiFailed = false;
    R.phase = mode === "ai" ? "ai" : "local";
    const localShare = R.aiAvailable ? 0.25 : 1;
    document.getElementById("catProgress").hidden = false;
    setBar(mode === "ai" ? localShare : 0);
    if (mode === "full") setStep("local", "active", "Matching your rules and classifier…");
    setStep("you", "", "Anything the AI wasn't sure about");
    updateCounts(R);
    const es = new EventSource(url);
    R.es = es;
    es.onmessage = (ev) => {
      let p;
      try { p = JSON.parse(ev.data); } catch { return; }
      switch (p.phase) {
        case "group": {
          const g = R.byKey.get(p.group_key);
          if (!g || g.source === "user") break;
          if (p.needs_review) {
            g.state = R.phase === "local" && R.aiAvailable ? "pending" : "review";
            g.categoryId = null; g.categoryName = ""; g.source = "none"; g.confidence = 0;
            g.suggestion = p.suggested_category_id ? { id: p.suggested_category_id, name: p.suggested_category, conf: p.suggested_confidence || 0 } : null;
            g.note = p.message || "";
          } else {
            Object.assign(g, { state: "done", categoryId: p.category_id, categoryName: p.category, source: p.source, confidence: p.confidence || 0, suggestion: null, note: "" });
          }
          placeGroup(R, g);
          if (R.phase === "local" && p.total) setBar(localShare * (p.index / p.total));
          break;
        }
        case "local_done":
          setStep("local", "done", p.categorised ? `Placed ${plural(p.categorised, "group")}` : "No matches yet — rules grow as you import");
          setBar(localShare);
          break;
        case "ai_start":
          R.phase = "ai";
          setStep("ai", "active", `Asking about ${plural(p.total || 0, "group")}${p.batches > 1 ? ` in ${p.batches} batches` : ""}…`);
          R.groups.forEach((g) => { if (g.state === "pending") placeGroup(R, g); });
          break;
        case "ai_progress":
          setBar(localShare + (1 - localShare) * (p.index / p.total));
          if (p.total > 1) setStep("ai", "active", `Batch ${p.index} of ${p.total} answered…`);
          break;
        case "ai_done":
          setStep("ai", R.aiFailed ? "failed" : "done", R.aiFailed
            ? `Stopped early — ${plural(p.categorised || 0, "group")} placed. <button class="link" type="button" data-retry-ai>Retry</button>`
            : `Placed ${plural(p.categorised || 0, "group")} confidently · ${p.pending || 0} unsure`);
          if (mode === "ai") finishPipeline(R);
          break;
        case "ai_skipped":
          setStep("ai", "skipped", p.message === "AI is not configured"
            ? "Not configured — add a provider in Settings → AI" : esc(p.message || "Skipped"));
          if (mode === "ai") finishPipeline(R);
          break;
        case "warning":
          R.aiFailed = true;
          toast(p.message || "AI problem");
          break;
        case "done":
          finishPipeline(R);
          break;
        case "error":
          toast(p.message || "Categorisation failed");
          setStep(R.phase === "ai" ? "ai" : "local", "failed", esc(p.message || "Failed"));
          finishPipeline(R);
          break;
      }
      updateCounts(R);
    };
    es.onerror = () => {
      es.close(); // never auto-reconnect: that re-runs the pipeline and spends AI tokens again
      if (!R.finished) {
        toast("Lost the connection while categorising");
        finishPipeline(R);
      }
    };
  }

  function finishPipeline(R) {
    if (R.es) { R.es.close(); R.es = null; }
    R.running = false;
    R.finished = true;
    R.phase = "done";
    R.groups.forEach((g) => { if (g.state === "pending") { g.state = "review"; placeGroup(R, g); } });
    const todo = R.groups.filter((g) => g.state !== "done");
    setStep("you", todo.length ? "active" : "done", todo.length
      ? `${plural(todo.length, "group")} for you to check`
      : "Nothing to check — ready to import");
    setBar(1);
    const prog = document.getElementById("catProgress");
    if (prog) setTimeout(() => { if (!R.running) prog.hidden = true; }, 600);
    updateCounts(R);
  }

  function confirmImport(R) {
    const btn = document.getElementById("reviewConfirm");
    const accountID = document.getElementById("reviewAccount").value;
    if (!accountID) { toast("Choose the account to import into"); return; }
    const nameInput = document.getElementById("reviewSchemaName");
    btn.disabled = true;
    btn.textContent = "Importing…";
    fetch(`/api/v1/imports/${R.batchID}/confirm`, {
      method: "POST",
      headers: jsonHeaders(),
      body: JSON.stringify({ account_id: accountID, schema_name: nameInput ? nameInput.value.trim() : "" }),
    }).then(async (r) => {
      const b = await r.json().catch(() => ({}));
      if (!r.ok) {
        toast(`Import failed: ${errMessage(b, r.status)}`);
        updateCounts(R);
        return;
      }
      const acc = (window.__IMPORT_ACCOUNTS__ || []).find((a) => a.id === accountID);
      const imported = R.groups.reduce((s, g) => s + g.rows.length, 0);
      openFlow(`<div class="thinking-card panel"><div class="head">
          <svg class="icon" style="color:var(--income)"><use href="#i-check"/></svg>
          Imported ${plural(imported, "transaction")}${acc ? ` into ${esc(acc.name)}` : ""} — see the <a href="/transactions">transactions</a> and <a href="/history?mode=audit">audit log</a>.</div>
        ${b.schema_name ? `<p class="muted">Saved the format as “${esc(b.schema_name)}”${acc ? ` for ${esc(acc.name)}` : ""} — next time this file shape skips straight to review.</p>` : ""}
        <button class="btn btn-ghost btn-xs" type="button" data-close-flow>Import another file</button></div>`);
      toast("Import complete");
    }).catch(() => { toast("Import failed: network error"); updateCounts(R); });
  }
});


function initHistoryTimeline() {
  const slider = document.getElementById("timelineSlider");
  if (!slider) return;
  let months = [];
  try { months = JSON.parse(slider.dataset.months || "[]"); } catch (e) { months = []; }
  if (!months.length) months = (window.__TIMELINE_MONTHS__ || []);
  const chartHost = document.getElementById("timelineChart");
  let fullSeries = null;
  if (chartHost && chartHost.dataset.chartData) {
    try { fullSeries = JSON.parse(chartHost.dataset.chartData); } catch (e) { fullSeries = null; }
  }
  let timer = null;
  const renderChart = (endDate) => {
    if (!chartHost || !fullSeries || !Array.isArray(fullSeries.labels)) return;
    let count = fullSeries.labels.length;
    for (let i = 0; i < fullSeries.labels.length; i++) {
      if (String(fullSeries.labels[i]) > endDate) { count = i; break; }
    }
    const series = (fullSeries.series || []).map((s) => Object.assign({}, s, { values: (s.values || []).slice(0, count) }));
    chartHost.dataset.chartData = JSON.stringify({ labels: fullSeries.labels.slice(0, count), series });
    if (window.redrawChartHost) window.redrawChartHost(chartHost);
  };
  const render = (idx) => {
    const end = months[idx];
    if (!end) return;
    document.getElementById("timelineDate").textContent = humanDate(end);
    const chip = document.getElementById("timelineChip");
    const stepsBack = months.length - 1 - idx;
    chip.textContent = stepsBack === 0 ? "Latest" : stepsBack === 1 ? "1 step back" : stepsBack + " steps back";
    slider.style.setProperty("--fill", (months.length > 1 ? (idx / (months.length - 1)) * 100 : 0) + "%");
    renderChart(end);
    fetch(`/api/v1/snapshot?date=${end}`).then((r) => r.json()).then((b) => {
      const bal = document.getElementById("timelineBalance");
      if (bal) bal.textContent = fmtMoneyMinor(b.total_minor, b.currency);
      const count = document.getElementById("timelineTxCount");
      if (count) count.textContent = b.tx_count;
      const wrap = document.getElementById("timelineAccounts");
      if (wrap) {
        wrap.innerHTML = (b.accounts || []).map((a) => `
          <div class="timeline-account">
            <span class="account-dot" style="background:${esc(a.color)}"></span>
            <span class="account-name">${esc(a.name)}</span>
            <span class="account-balance ${a.balance_minor < 0 ? "neg" : ""}">${fmtMoneyMinor(a.balance_minor, b.currency)}</span>
          </div>`).join("");
      }
    }).catch(() => {});
  };
  slider.addEventListener("input", (e) => {
    const idx = Number(e.target.value);
    clearTimeout(timer);
    timer = setTimeout(() => render(idx), 80);
  });
  const ticks = document.getElementById("timelineTicks");
  if (ticks && months.length) {
    const count = Math.min(months.length, 7);
    const denom = count > 1 ? count - 1 : 1;
    let html = "";
    for (let i = 0; i < count; i++) {
      const tIdx = Math.round((i * (months.length - 1)) / denom);
      const pos = months.length > 1 ? (tIdx / (months.length - 1)) * 100 : 0;
      const cls = i === 0 ? " first" : i === count - 1 ? " last" : "";
      html += `<div class="timeline-tick${cls}" style="left:${pos}%"><span>${esc(tickDateLabel(months[tIdx]))}</span></div>`;
    }
    ticks.innerHTML = html;
  }
  render(Number(slider.value));
}
document.addEventListener("DOMContentLoaded", initHistoryTimeline);

function tickDateLabel(iso) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(iso || "")) return shortMonthLabel(iso);
  const d = new Date(iso + "T00:00:00");
  if (isNaN(d)) return iso;
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "2-digit" });
}
function humanDate(iso) {
  const d = new Date(iso + "T00:00:00");
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric" });
}

let historyPaneToken = 0;

function historySkeletonHTML(mode) {
  const block = (h) => `<div class="sk-block" style="height:${h}px"></div>`;
  if (mode === "audit") {
    return `<div class="pane-skeleton">${block(46)}${Array.from({ length: 6 }, () => `<div class="sk-block sk-row"></div>`).join("")}</div>`;
  }
  return `<div class="pane-skeleton">${block(112)}<div class="stats-grid three">${block(84)}${block(84)}${block(84)}</div>${block(52)}${block(120)}${block(240)}</div>`;
}

function loadHistoryPane(url, push) {
  const host = document.getElementById("historyPaneHost");
  if (!host) return;
  const u = new URL(url, location.origin);
  const mode = u.searchParams.get("mode") === "audit" ? "audit" : "transactions";
  const filter = u.searchParams.get("filter") || "all";
  const period = u.searchParams.get("period") || "all";

  document.querySelectorAll(".history-mode .segmented-item").forEach((a) => {
    const on = a.dataset.historyMode === mode;
    a.classList.toggle("active", on);
    a.setAttribute("aria-selected", on);
  });
  host.innerHTML = historySkeletonHTML(mode);

  const token = ++historyPaneToken;
  fetch(`/history/pane?mode=${mode}&filter=${filter}&period=${period}`, { headers: { "X-Requested-With": "fetch" } })
    .then((r) => {
      // A redirect (e.g. to /setup) would otherwise be injected as pane HTML.
      if (!r.ok || r.redirected) throw new Error("pane " + r.status);
      return r.text();
    })
    .then((html) => {
      if (token !== historyPaneToken) return;
      host.innerHTML = html;
      if (mode === "audit") {
        if (window.wireAuditActions) window.wireAuditActions(host);
      } else {
        initHistoryTimeline();
        if (window.drawChartHosts) drawChartHosts(host);
      }
    })
    .catch(() => { location.href = u.href; });
  if (push) history.pushState({}, "", u.href);
}

document.addEventListener("DOMContentLoaded", () => {
  const view = document.getElementById("view-history");
  if (!view) return;
  view.addEventListener("click", (e) => {
    const a = e.target.closest && e.target.closest('a[href^="/history?"]');
    if (!a) return;
    e.preventDefault();
    loadHistoryPane(a.getAttribute("href"), true);
  });
  window.addEventListener("popstate", () => {
    if (document.getElementById("historyPaneHost")) loadHistoryPane(location.href, false);
  });
});
