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
function showConfirm(title, message, okLabel, callback) {
  document.getElementById("confirmTitle").textContent = title;
  document.getElementById("confirmMessage").textContent = message;
  document.getElementById("confirmOk").textContent = okLabel || "Delete";
  confirmCallback = callback;
  document.getElementById("confirmModal").hidden = false;
}
let confirmCallback = null;

/* ---------- Widget drawer ---------- */
// Which widget types can be converted to which. Groups keep data shapes that
// actually make sense together: bar/line/table share labels+series, pie/table
// share single-value breakdowns, and number/text/transactions keep to
// themselves because their data shape doesn't translate elsewhere.
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
  // Only let the user switch between types that share a data shape with the
  // current widget (so pie <-> number can't blank the card).
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

// currentViewPeriod reports the masthead period selector state so chat
// answers respect it unless the question names its own range.
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

/* ---------- Sent-message recall (up-arrow, standard chat behaviour) ---------- */
let sentHistory = [];
let sentHistoryIdx = -1;

function pushSentHistory(text) {
  if (!text) return;
  if (sentHistory[sentHistory.length - 1] === text) return; // no consecutive dupes
  sentHistory.push(text);
  if (sentHistory.length > 100) sentHistory.shift();
  sentHistoryIdx = -1;
}

// Seed sentHistory from the user bubbles currently on the page. Oldest first
// (chronological) so ArrowUp walks backwards through time naturally. Called
// on initial load and whenever older messages are paged in at the top.
function seedSentHistoryFromDOM(box) {
  if (!box) return;
  const bubbles = box.querySelectorAll(".msg.user .msg-bubble");
  const texts = [];
  bubbles.forEach((b) => {
    const t = (b.textContent || "").trim();
    if (t) texts.push(t);
  });
  // Rebuild the history keeping relative order; dedupe consecutive repeats.
  const next = [];
  for (const t of texts) {
    if (next[next.length - 1] !== t) next.push(t);
  }
  sentHistory = next.slice(-100); // same cap as pushSentHistory
  sentHistoryIdx = -1;
}

// direction: "up" recalls older sent messages, "down" returns towards the
// empty composer. The first ArrowUp requires an empty composer; while
// recalling, up/down walk the history regardless of the caret position
// (typing always resets the position).
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

/* ---------- Sidebar day summaries (async catch-up poll) + day jump ---------- */
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

// Scroll to a day from a sidebar entry (?day=YYYY-MM-DD): page older
// messages in until the day is loaded, then align the first message of the
// day near the top of the viewport.
function jumpToDay(date) {
  const box = queriesScrollBox();
  if (!box) return;
  let tries = 0;
  const seek = () => {
    const hit = Array.from(box.querySelectorAll(".msg[data-created]")).find(
      (m) => localDayKey(m.dataset.created) === date);
    if (hit) {
      // getBoundingClientRect().top is viewport-relative, so add the current
      // scrollTop to get the hit's absolute offset within the scroll box
      // (otherwise this lands wherever the box happens to be scrolled).
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

/* ---------- Queries history: session scroll position + paging (doc 10 §8) ---------- */
const QUERIES_PAGE_SIZE = 20;

function queriesScrollBox() { return document.getElementById("queryMessages"); }
function queriesConvID() {
  const main = document.querySelector(".queries-main");
  return main ? main.dataset.conversationId || "" : "";
}

// Restore the conversation + scroll offset last viewed in this session.
// First load in a session (nothing saved) starts at the bottom.
function initQueriesScroll() {
  const box = queriesScrollBox();
  if (!box) return;
  const savedConv = sessionStorage.getItem("glowworm-queries-conv");
  const urlConv = new URLSearchParams(location.search).get("c");
  if (savedConv && !urlConv && savedConv !== queriesConvID()) {
    // Resume the conversation the user was reading when they left.
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
  // The debounce can drop the last 150ms of movement when the user navigates
  // away — pagehide fires reliably on soft nav and tab close, so save there
  // too to keep the restore point in sync with what the user actually saw.
  window.addEventListener("pagehide", saveQueriesScroll);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") saveQueriesScroll();
  });
  maybeLoadOlder(); // a restored position may already sit near the top
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

// Page older messages in as the user scrolls towards the top.
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
        seedSentHistoryFromDOM(box); // older user bubbles are now in the DOM
      }
      loadingOlder = false;
      maybeLoadOlder(); // still near the top → keep filling
    })
    .catch(() => { loader.remove(); loadingOlder = false; });
}

// Render one persisted message (API shape: {role, text, steps, widgets, created_at}).
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

// Prepend an ascending (oldest→newest) batch of messages, keeping the
// viewport anchored and day badges correct across the new boundary.
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
  // The previously-first message loses its badge if the day now starts earlier.
  if (firstMsg && firstMsg.dataset.created) {
    const badgeEl = firstMsg.previousElementSibling;
    if (badgeEl && badgeEl.classList.contains("msg-day") &&
        dayKeyOf(firstMsg.dataset.created) === dayKeyOf(items[items.length - 1].created_at)) {
      badgeEl.remove();
    }
  }
}

/* Day keys/labels in the server's local timezone (the server computes the
   same way) — UTC grouping mislabels late-evening messages. */
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

  // User bubble + pending assistant with worm loader (prototype choreography).
  // The day badge shows only when the day changes — decided from the DOM's
  // last message, never from a cached format string (that mismatch produced
  // duplicate/incorrect Today badges).
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

/* ---------- Pin / unpin widgets (chat + dashboard cards) ---------- */
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
    if (grid && grid.contains(card)) card.remove(); // dashboard card
    toast("Removed from dashboard");
  }).catch(() => toast("Could not unpin widget"));
}

// adoptConversation binds the conversation id to the page so follow-up
// questions continue the same thread, and lists it in the history sidebar.
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

// Minimal assistant-text formatting: escaped HTML, **bold** and line breaks
// (white-space: pre-wrap on .msg-text). Mirrors formatAssistantText in Go.
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
    // A clarification with suggestions isn't a failure — style it as normal text.
    const errCls = isError && !(suggestions && suggestions.length) ? " msg-error" : "";
    if (text) content += `<div class="msg-text${errCls}">${fmtAssistantText(text)}</div>`;
    // Errors never get a thinking section — nothing was analysed.
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
  // transactions rows carry signed minor units (amount_minor); older
  // payloads may use amount — normalise before rendering.
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
        // Dashboard widget: server owns the row (keeps plan + layout).
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

      // Chat widget: update the card in place, then honour the pin state.
      updateCardDOM();
      if (shouldPin) {
        const btn = card.querySelector("[data-action='toggle-pin']");
        if (btn) pinWidget(card, btn);
      }
      closeDrawer();
      toast("Widget updated");
    });
  }

  // Widget actions (delegated: works for live chat cards and server-rendered
  // dashboard/chat cards alike).
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

  // Suggestion chips (delegated so chips rendered mid-conversation also work).
  document.addEventListener("click", (e) => {
    const chip = e.target.closest("[data-chat-suggest]");
    if (chip) sendChat(chip.dataset.chatSuggest);
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
        // Swap both the DOM positions and the col/rows of the two cards —
        // matches the prototype behaviour (each card takes the other's slot
        // and size), then persist.
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
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(); return; }
      // Up/down recall the sent-message history (standard chat behaviour):
      // up loads the previous sent message when the composer is empty (or
      // already recalling), down walks back towards the empty composer.
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
      seedSentHistoryFromDOM(qbox); // ArrowUp recalls server-known history, not just this session
      pollDaySummaries();
      const jumpDay = document.querySelector(".queries-main")?.dataset.jumpDay;
      if (jumpDay) jumpToDay(jumpDay);
    }
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
      closeModal("setupWizard"); closeDrawer(); closeModal("recurringModal"); closeModal("accountModal"); closeModal("txModal"); closePasswordModal(); closeNameModal();
    }
  });

  // New transaction modal (manual entry, doc 04 §5.2).
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
      if (step === 2) {
        // Persist the remaining category chips (doc 12 §3 step 2).
        const chips = Array.from(document.querySelectorAll("#wizardCategories .wizard-cat"))
          .map((chip) => chip.textContent.replace(/[\u2715\u2717]|✕/g, "").trim())
          .filter((n) => n.length > 0);
        for (const name of chips) {
          try {
            await fetch("/api/v1/categories", {
              method: "POST",
              headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
              body: JSON.stringify({ name, type: "spend" }),
            });
          } catch (e2) {}
        }
        step++; render(); return;
      }
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
    document.querySelectorAll("#wizardCategories .wizard-cat .remove").forEach((b) =>
      b.addEventListener("click", () => { b.closest(".wizard-cat").remove(); }));
    document.getElementById("wizardAddCategory")?.addEventListener("click", () => {
      const name = window.prompt("New category name");
      if (name && name.trim()) {
        const wrap = document.getElementById("wizardCategories");
        const chip = document.createElement("span");
        chip.className = "wizard-cat";
        chip.innerHTML = `<span class="category-dot" style="background:var(--primary)"></span>${esc(name.trim())}<button class="remove" type="button" title="Remove">✕</button>`;
        wrap.appendChild(chip);
        chip.querySelector(".remove").addEventListener("click", () => chip.remove());
      }
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

/* ---------- Toast event hook ---------- */
document.addEventListener("glowworm-toast", (e) => {
  if (e.detail && e.detail.value) toast(e.detail.value);
});

/* ---------- Accounts modal (doc 04 §5.8) ---------- */
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
  // (The new-transaction modal is wired in the boot handler above, guarded by
  // #newTxBtn. A duplicate here — with an unguarded #txAddForm — threw on
  // every page without the tx modal and killed every binding below it:
  // settings, category form and audit actions.)
  document.getElementById("accDeleteBtn")?.addEventListener("click", () => {
    const form = document.getElementById("accountForm");
    const id = form.dataset.accountId;
    if (!id) return;
    showConfirm("Delete account?", "Delete this account and all of its transactions? This can't be undone.", "Delete account", () => {
      fetch(`/api/v1/accounts/${id}`, { method: "DELETE", headers: { "X-CSRF-Token": csrfToken() } })
        .then((r) => { if (!r.ok) { toast("Could not delete"); return; } closeModal("accountModal"); toast("Account deleted"); location.reload(); });
    });
  });

  // Account: change password (doc 14: >=10 chars, current verified) — opens in a modal.
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

  // Account: update your name — opens in a modal (shown on the dashboard greeting).
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

  // Appearance save (theme + currency; the name lives in the Account block).
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

  // AI provider save + test.
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
      aiKeyInput.value = KEY_MASK; // never re-display the real key
      aiKeyInput.type = "text";    // literal mask, not password dots
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
        aiKeyUnlocked = true; // typing a new key replaces the saved one
        aiKeyInput.value = "";
        aiKeyInput.placeholder = "Type a new key to replace the saved one";
        aiKeyInput.type = "password"; // hide the replacement key as it is typed
        aiKeyInput.readOnly = false;
      }
    });
    aiKeyInput.addEventListener("blur", () => {
      if (aiHasSavedKey && aiKeyUnlocked && aiKeyInput.value.trim() === "") {
        aiKeyUnlocked = false; // left empty — keep showing the saved key
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
  // Model list: fetch the provider's live catalogue and populate the select.
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
          api_key: (aiKeyInput.value.trim() === KEY_MASK ? "" : aiKeyInput.value.trim()), // empty falls back to the stored key
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
  if (aiProviderSel && aiProviderSel.value) loadAIModels(); // saved provider: populate on page load
  document.getElementById("aiSave")?.addEventListener("click", () => {
    const provider = aiProviderSel.value;
    const baseUrl = document.getElementById("aiBaseUrl")?.value.trim() || "";
    if ((provider === "custom" || provider === "ollama") && !baseUrl) {
      toast("Enter the endpoint URL for this provider");
      return;
    }
    // The masked placeholder means "keep the saved key" — never send the mask.
    // A freshly typed key is sent whether or not the field was "unlocked"
    // (first-time setup has no saved key, so there is nothing to unlock).
    const typedKey = aiKeyInput.value.trim();
    const key = typedKey === KEY_MASK ? "" : typedKey;
    const model = aiModelSel ? aiModelSel.value : "";
    const models = { default: model }; // always send: Auto ("") clears a stale selection
    fetch("/api/v1/settings/ai", {
      method: "PUT",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ provider, api_key: key || undefined, base_url: baseUrl || undefined, models }),
    }).then(async (r) => {
      const b = await r.json().catch(() => ({}));
      if (!r.ok) { toast((b.error && b.error.message) || "Could not save"); return; }
      if (key) { aiHasSavedKey = true; aiKeyUnlocked = false; syncAIKeyRow(); }
      toast("AI provider updated");
      if (aiProviderSel.value) loadAIModels(); // a fresh key may unlock the live catalogue
    }).catch(() => toast("Could not save"));
  });
  document.getElementById("aiTest")?.addEventListener("click", () => {
    fetch("/api/v1/settings/ai/test", { method: "POST", headers: { "X-CSRF-Token": csrfToken() } })
      .then((r) => r.json())
      .then((b) => toast(b.ok ? `Connection OK (${b.latency_ms}ms)` : `Failed: ${b.error || "unknown error"}`))
      .catch(() => toast("Test failed"));
  });

  // Re-run setup walkthrough.
  document.getElementById("runSetupBtn")?.addEventListener("click", () => {
    fetch("/api/v1/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ setup_completed: false }),
    }).then((r) => { if (r.ok) window.location = "/setup"; });
  });

  // Category form (create / edit / delete).
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

  // Audit entry actions (jump back / revert / forget / delete). Bound via
  // wireAuditActions so freshly injected audit panes get them too.
  function wireAuditActions(root) {
    (root || document).querySelectorAll("[data-jump-back]").forEach((btn) => {
      if (btn.dataset.wired) return;
      btn.dataset.wired = "1";
      btn.addEventListener("click", () => {
        const id = btn.dataset.jumpBack;
        showConfirm("Jump back?", "Everything after this point will be undone but kept collapsed in the history — restorable at any time.", "Jump back", () => {
          fetch(`/api/v1/audit/${id}/jump-back`, { method: "POST", headers: { "X-CSRF-Token": csrfToken() } })
            .then(async (r) => {
              if (!r.ok) {
                const b = await r.json().catch(() => ({}));
                toast((b.error && b.error.message) || "Jump back failed");
                return;
              }
              toast("State restored to this point");
              location.reload();
            });
        });
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
  window.wireAuditActions = wireAuditActions; // history pane switcher uses this
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


/* ---------- Transactions infinite scroll ---------- */
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
    // Older browsers: fall back to a scroll listener.
    window.addEventListener("scroll", () => {
      if (!sentinel) return;
      const rect = sentinel.getBoundingClientRect();
      if (rect.top < window.innerHeight + 200) loadNext();
    }, { passive: true });
  }
});

/* ---------- Inline transaction editor (doc 04 §5.2.4) ---------- */
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

      // Populate the transfer-link select (always visible; the type is
      // derived server-side from the link/category).
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
            // Picking a counterpart means this becomes a transfer: force the
            // Internal transfer category (the link endpoint does too).
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


/* ---------- Import flow (doc 06 §7) ---------- */
document.addEventListener("DOMContentLoaded", () => {
  const dropzone = document.getElementById("dropzone");
  const fileInput = document.getElementById("fileInput");
  const flowBox = document.getElementById("importFlow");
  if (!dropzone || !flowBox) return;

  const upload = (file) => {
    const fd = new FormData();
    fd.append("file", file);
    const accSel = document.getElementById("importAccount");
    if (accSel && accSel.value) fd.append("account_id", accSel.value);
    flowBox.hidden = false;
    flowBox.innerHTML = `<div class="thinking-card panel"><div class="head">
      <span class="spinner"></span> Reading ${esc(file.name)}…</div></div>`;
    fetch("/api/v1/imports", {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken() },
      body: fd,
    }).then(async (r) => {
      const b = await r.json().catch(() => ({}));
      if (!r.ok) {
        const why = (b.error && b.error.message) || `server error ${r.status}`;
        flowBox.innerHTML = `<div class="thinking-card panel"><div class="head">Upload failed: ${esc(why)}</div></div>`;
        return;
      }
      renderBatch(b);
    }).catch((e) => {
      flowBox.innerHTML = `<div class="thinking-card panel"><div class="head">Upload failed: ${esc(e && e.message ? e.message : "network error — is the server running?")}</div></div>`;
    });
  };

  dropzone.addEventListener("dragover", (e) => { e.preventDefault(); dropzone.classList.add("drag"); });
  dropzone.addEventListener("dragleave", () => dropzone.classList.remove("drag"));
  dropzone.addEventListener("drop", (e) => {
    e.preventDefault(); dropzone.classList.remove("drag");
    if (e.dataTransfer.files.length) upload(e.dataTransfer.files[0]);
  });
  document.getElementById("dropzoneBrowse")?.addEventListener("click", () => fileInput.click());
  fileInput.addEventListener("change", () => {
    if (fileInput.files.length) upload(fileInput.files[0]);
  });

  // One row of the column→role mapping editor (doc 06 §5.2). The collector
  // below reads tr.dataset.source and the select's value.
  function mappingRowHTML(col) {
    const roles = ["date","description","amount","amount_in","amount_out","currency","balance","type_hint","memo","source","destination","ignore"];
    const opts = roles.map((r) =>
      `<option value="${r}"${(col.role || "ignore") === r ? " selected" : ""}>${r.replace(/_/g, " ")}</option>`).join("");
    return `<tr data-source="${esc(col.source)}"><td>${esc(col.source)}</td><td><select class="field select" style="min-width:150px">${opts}</select></td></tr>`;
  }

  const renderBatch = (b) => {
    if (b.status === "failed") {
      const why = b.parse_error || "The file could not be read.";
      const kind = (b.mime || "").toLowerCase();
      const pdfHint = kind.includes("pdf")
        ? '<p class="login-error" style="margin-top:8px">Scanned PDFs have no text layer — the optional AI vision path (Settings → AI) can read them, or export a CSV from your bank.</p>'
        : "";
      flowBox.innerHTML = `
        <div class="panel category-form">
          <h3 class="category-form-title">Import failed</h3>
          <p class="login-error">${esc(why)}</p>
          ${pdfHint}
          <p class="login-error" style="color:var(--muted)">Nothing was imported, and this attempt does not appear in your history.</p>
          <button class="btn btn-primary" id="importRetry">Try another file</button>
        </div>`;
      const retry = document.getElementById("importRetry");
      if (retry) retry.addEventListener("click", () => location.reload());
      return;
    }
    if (b.status === "awaiting_review") {
      loadReview(b.id);
      return;
    }
    // awaiting_schema: show the detected schema + sample + mapping editor.
    const d = b.detection || {};
    const sheets = (d.sheets || []).map((s) => `<option value="${esc(s)}"${s === d.sheet ? " selected" : ""}>${esc(s)}</option>`).join("");
    const cols = (d.columns || []).map((c) => mappingRowHTML(c)).join("");
    const sample = (d.sample || []).slice(0, 4).map((r) => `<tr>${r.map((c) => `<td>${esc(c)}</td>`).join("")}</tr>`).join("");
    flowBox.innerHTML = `
      <div class="panel category-form">
        <h3 class="category-form-title">${d.matched_schema ? "Matched saved schema “" + esc(d.matched_schema) + "”" : "Detected schema"}${d.confidence ? ` <span class="schema-conf">· ${Math.round(d.confidence * 100)}% match</span>` : ""}</h3>
        ${b.parse_error ? `<p class="login-error">${esc(b.parse_error)}</p>` : ""}
        <div class="tx-table-wrap" style="overflow-x:auto">
        <table class="tx-table"><thead><tr><th>Column</th><th>Role</th></tr></thead>
        <tbody id="mappingRows">${cols}</tbody></table></div>
        <div class="field-row" style="margin-top:10px">
          <label>Date format</label>
          <input class="field" id="mapDateFormat" value="${esc(d.date_format || "")}" placeholder="e.g. 02/01/2006 (auto-detected)" />
        </div>
        ${sheets ? `<div class="field-row"><label>Sheet</label><select class="field select" id="mapSheet">${sheets}</select></div>` : ""}
        <div class="section-head"><h2>Sample rows</h2></div>
        <table class="tx-table"><tbody>${sample}</tbody></table>
        <div class="field-row" style="margin-top:12px">
          <label>Import into</label>
          <select class="field select" id="reviewAccount">
            ${(window.__IMPORT_ACCOUNTS__ || []).map((a) => `<option value="${esc(a.id)}">${esc(a.name)}</option>`).join("")}
          </select>
        </div>
        <div class="category-form-actions">
          <span class="spacer"></span>
          <button class="btn btn-ghost" id="mapCancel">Cancel</button>
          <button class="btn btn-primary" id="mapApply">Re-parse &amp; review</button>
        </div>
      </div>`;
    document.getElementById("mapCancel").addEventListener("click", () => { flowBox.hidden = true; flowBox.innerHTML = ""; });
    document.getElementById("mapApply").addEventListener("click", () => {
      const rows = Array.from(document.querySelectorAll("#mappingRows tr")).map((tr) => ({
        source: tr.dataset.source,
        role: tr.querySelector("select").value,
        date_format: document.getElementById("mapDateFormat").value.trim(),
      }));
      const m = {
        kind: d.kind || "csv", has_header: d.has_header !== false, delimiter: d.delimiter || ",",
        columns: rows, date_format: document.getElementById("mapDateFormat").value.trim(),
        sheet: document.getElementById("mapSheet") ? document.getElementById("mapSheet").value : undefined,
        confidence: d.confidence || 0.5,
      };
      fetch(`/api/v1/imports/${b.id}/schema`, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ mapping: m }),
      }).then(async (r) => {
        const nb = await r.json().catch(() => ({}));
        if (!r.ok) { toast((nb.error && nb.error.message) || "Re-parse failed"); return; }
        renderBatch(nb);
      });
    });
  };

  const loadReview = (batchID) => {
    flowBox.innerHTML = `<div class="thinking-card panel"><div class="head"><span class="spinner"></span> Loading review…</div></div>`;
    fetch(`/api/v1/imports/${batchID}/review`).then((r) => r.json()).then((body) => {
      const rows = body.rows || [];
      const needsReview = body.needs_review || 0;
      const confident = body.confident || 0;
      const duplicates = body.duplicates || 0;
      const rowHTML = rows.map((row) => {
        const status = row.duplicate_of ? "dup" : (row.needs_review ? "review" : "ok");
        const badge = status === "dup" ? `<span class="badge badge-stale">duplicate</span>`
          : status === "review" ? `<span class="badge badge-needsreview">needs review</span>`
          : `<span class="badge badge-income">${esc(row.category_source || "")}</span>`;
        const catName = row.category_name || "Uncategorised";
        return `<tr data-row-id="${esc(row.id)}" data-status="${status}">
          <td class="cell-sub">${esc(row.date)}</td>
          <td><div class="cell-merchant">${esc(row.description)} ${badge}</div></td>
          <td class="num tx-amount ${row.amount_minor >= 0 ? "pos" : "neg"}">${fmtMoneySigned(row.amount_minor, row.currency || "GBP")}</td>
          <td>
            <select class="field select" data-cat-select style="min-width:150px">
              <option value="">Uncategorised</option>
              ${(window.__IMPORT_CATS__ || []).map((c) => `<option value="${esc(c.id)}"${c.id === row.category_id ? " selected" : ""}>${esc(c.name)}</option>`).join("")}
            </select>
          </td>
          <td><button class="btn btn-ghost btn-xs" data-apply-similar>Apply to similar</button></td>
        </tr>`;
      }).join("");
      flowBox.innerHTML = `
        <div class="panel category-form">
          <h3 class="category-form-title">Review import</h3>
          <p class="muted">${rows.length} rows · ${confident} confident · ${needsReview} need review · ${duplicates} duplicates</p>
          <div style="overflow-x:auto"><table class="tx-table">
            <thead><tr><th>Date</th><th>Description</th><th class="num">Amount</th><th>Category</th><th></th></tr></thead>
            <tbody id="reviewRows">${rowHTML}</tbody>
          </table></div>
          <div class="field-row" style="margin-top:12px">
            <label>Import into</label>
            <select class="field select" id="reviewAccount">
              ${(window.__IMPORT_ACCOUNTS__ || []).map((a) => `<option value="${esc(a.id)}">${esc(a.name)}</option>`).join("")}
            </select>
          </div>
          <div class="category-form-actions">
            <button class="btn btn-ghost" id="reviewAccept" type="button">Accept ${confident} confident</button>
            <span class="spacer"></span>
            <button class="btn btn-ghost" id="reviewCancel" type="button">Cancel</button>
            <button class="btn btn-primary" id="reviewConfirm" type="button">Import ${rows.length - duplicates} transactions</button>
          </div>
        </div>`;
      document.getElementById("reviewCancel").addEventListener("click", () => {
        fetch(`/api/v1/imports/${batchID}/cancel`, { method: "POST", headers: { "X-CSRF-Token": csrfToken() } });
        flowBox.hidden = true; flowBox.innerHTML = "";
      });
      const acceptBtn = document.getElementById("reviewAccept");
      if (acceptBtn && confident > 0) {
        acceptBtn.addEventListener("click", () => {
          fetch(`/api/v1/imports/${batchID}/review`, {
            method: "PATCH",
            headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
            body: JSON.stringify({ accept_confident: true }),
          }).then(() => loadReview(batchID));
        });
      }
      // Per-row category change + apply-to-similar.
      document.getElementById("reviewRows").addEventListener("change", (e) => {
        const sel = e.target.closest("[data-cat-select]");
        if (!sel) return;
        const tr = sel.closest("tr");
        const rowID = tr.dataset.rowId;
        const catID = sel.value || null;
        const desc = tr.querySelector(".cell-merchant").textContent.trim();
        const similar = Array.from(document.querySelectorAll("#reviewRows tr")).filter((r2) => {
          const r2desc = r2.querySelector(".cell-merchant").textContent.trim();
          return r2 !== tr && normaliseForSimilar(r2desc) === normaliseForSimilar(desc);
        });
        const patch = { set_category: { row_ids: [rowID, ...similar.map((r2) => r2.dataset.rowId)], category_id: catID } };
        fetch(`/api/v1/imports/${batchID}/review`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
          body: JSON.stringify(patch),
        }).then(() => toast(similar.length ? `Applied to ${similar.length + 1} rows` : "Category set"));
      });
      function normaliseForSimilar(s) {
        return s.toLowerCase().replace(/[0-9]/g, "#").replace(/\s+/g, " ").trim();
      }
      const d2 = (row) => row.querySelector(".cell-merchant").textContent.trim();
      document.getElementById("reviewRows").addEventListener("click", (e) => {
        const btn = e.target.closest("[data-apply-similar]");
        if (!btn) return;
        const tr = btn.closest("tr");
        const sel = tr.querySelector("[data-cat-select]");
        const catID = sel.value || null;
        const desc = d2(tr);
        const similar = Array.from(document.querySelectorAll("#reviewRows tr")).filter((r2) =>
          r2 !== tr && normaliseForSimilar(d2(r2)) === normaliseForSimilar(desc));
        if (!similar.length) { toast("No similar rows found"); return; }
        const apply = () => {
          const patch = { set_category: { row_ids: [tr.dataset.rowId, ...similar.map((r) => r.dataset.rowId)], category_id: catID } };
          fetch(`/api/v1/imports/${batchID}/review`, {
            method: "PATCH",
            headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
            body: JSON.stringify(patch),
          }).then(async (r) => {
            if (!r.ok) {
              const b = await r.json().catch(() => ({}));
              toast((b.error && b.error.message) || "Could not apply");
              return;
            }
            similar.forEach((r2) => { const s2 = r2.querySelector("[data-cat-select]"); if (s2) s2.value = catID || ""; });
            toast(`Category applied to ${similar.length + 1} rows`);
          });
        };
        if (similar.length > 4) {
          showConfirm(`Apply to ${similar.length} similar rows?`, "Rows with a matching description (ignoring digits) will receive the same category.", "Apply", apply);
        } else {
          apply();
        }
      });
      document.getElementById("reviewConfirm").addEventListener("click", () => {
        const accountID = document.getElementById("reviewAccount").value;
        if (!accountID) { toast("Choose the account to import into"); return; }
        fetch(`/api/v1/imports/${batchID}/confirm`, {
          method: "POST",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
          body: JSON.stringify({ account_id: accountID }),
        }).then(async (r) => {
          const b = await r.json().catch(() => ({}));
          if (!r.ok) {
            toast(`Import failed: ${(b.error && b.error.message) || `server error ${r.status}`}`);
            return;
          }
          flowBox.innerHTML = `<div class="thinking-card panel"><div class="head">
            <svg class="icon" style="color:var(--income)"><use href="#i-check"/></svg>
            Imported ${b.rows || 0} transactions — see the <a href="/transactions">transactions</a> and <a href="/history?mode=audit">audit log</a>.</div></div>`;
          toast("Import complete");
        });
      });
    });
  };
});


/* ---------- Timeline slider (doc 04 §5.7) ---------- */
/* Initialised on load and re-run whenever the transactions pane is injected
   by the history mode switcher (the old nodes — and their listeners — die
   with the replaced innerHTML). */
function initHistoryTimeline() {
  const slider = document.getElementById("timelineSlider");
  if (!slider) return;
  // Steps are every day the balance can change (opening balances,
  // valuations, transactions) plus today — one slider stop per data point.
  // Months travel with the pane (data-months) so fragment swaps work too.
  let months = [];
  try { months = JSON.parse(slider.dataset.months || "[]"); } catch (e) { months = []; }
  if (!months.length) months = (window.__TIMELINE_MONTHS__ || []);
  // Full balance series (server-rendered into #timelineChart). Sliced per
  // slider position so the chart shows only the selected period.
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
  // Ticks (≤7 evenly spread, prototype logic).
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

/* ---------- History mode switch (doc 04 §5.7) ----------
   Switching Transactions ⇄ Audit log (and audit filter chips) swaps the pane
   fragment in place: the page shows a skeleton instantly while /history/pane
   loads, instead of a slow full-page reload. */
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

  // Immediate feedback: segmented state + skeleton while the fragment loads.
  document.querySelectorAll(".history-mode .segmented-item").forEach((a) => {
    const on = a.dataset.historyMode === mode;
    a.classList.toggle("active", on);
    a.setAttribute("aria-selected", on);
  });
  host.innerHTML = historySkeletonHTML(mode);

  const token = ++historyPaneToken;
  fetch(`/history/pane?mode=${mode}&filter=${filter}&period=${period}`, { headers: { "X-Requested-With": "fetch" } })
    .then((r) => {
      // A redirect (e.g. to /setup while not initialised) would otherwise be
      // injected as pane HTML — treat it as a failure and navigate fully.
      if (!r.ok || r.redirected) throw new Error("pane " + r.status);
      return r.text();
    })
    .then((html) => {
      if (token !== historyPaneToken) return; // a newer switch superseded this
      host.innerHTML = html;
      if (mode === "audit") {
        if (window.wireAuditActions) window.wireAuditActions(host);
      } else {
        initHistoryTimeline();
        if (window.drawChartHosts) drawChartHosts(host);
      }
    })
    .catch(() => { location.href = u.href; }); // fragment failed — fall back to full navigation
  if (push) history.pushState({}, "", u.href);
}

document.addEventListener("DOMContentLoaded", () => {
  const view = document.getElementById("view-history");
  if (!view) return;
  // Segmented tabs + audit filter chips are plain /history? links; intercept.
  view.addEventListener("click", (e) => {
    const a = e.target.closest && e.target.closest('a[href^="/history?"]');
    if (!a) return;
    e.preventDefault();
    loadHistoryPane(a.getAttribute("href"), true);
  });
  // Back/forward between modes restores the right pane.
  window.addEventListener("popstate", () => {
    if (document.getElementById("historyPaneHost")) loadHistoryPane(location.href, false);
  });
});
