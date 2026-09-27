/* ============================================================
   Glowworm — app shell (UI skeleton)
   Navigation, mock rendering, setup wizard, AI chat with dynamic
   widgets. No real AI or importer logic yet.
   ============================================================ */

const state = {
  view: "dashboard",
  pinned: DEFAULT_PINNED_WIDGETS.map((w) => ({ ...w })),
  chat: [],
  chatMode: "closed", // "closed" | "floating" | "pinned"
  historyFilter: "all",
  wizardStep: 0,
  wizardCats: CATEGORIES.filter((c) => c.budget != null).map((c) => ({ ...c })),
  editingId: null,
};

const VIEW_TITLES = {
  dashboard: "Dashboard",
  transactions: "Transactions",
  categories: "Categories",
  import: "Import",
  timeline: "Timeline",
  history: "History",
  settings: "Settings",
};

const WELCOME_MESSAGE = {
  role: "assistant",
  text: "Hey, I'm Glowworm — your money, made clear. Ask me anything about your finances.",
  suggestions: [
    { label: "Transport spend", text: "How much have I spent on Transport over the past year?" },
    { label: "Where did my money go?", text: "Where did my money go this month?" },
    { label: "Balance over time", text: "How has my balance changed this year?" },
  ],
};

const TIMELINE_MONTHS = [
  { short: "Jul 23", date: "1 July 2023" },
  { short: "Aug 23", date: "1 August 2023" },
  { short: "Sep 23", date: "1 September 2023" },
  { short: "Oct 23", date: "1 October 2023" },
  { short: "Nov 23", date: "1 November 2023" },
  { short: "Dec 23", date: "1 December 2023" },
  { short: "Jan 24", date: "1 January 2024" },
  { short: "Feb 24", date: "1 February 2024" },
  { short: "Mar 24", date: "1 March 2024" },
  { short: "Apr 24", date: "1 April 2024" },
  { short: "May 24", date: "1 May 2024" },
  { short: "Jun 24", date: "1 June 2024" },
];

const MERCHANT_COLORS = ["#0e6f51", "#4f5bc7", "#c9822e", "#c0483f", "#2f7fb0", "#7a58b8", "#2e9d8f", "#6b7280"];

document.addEventListener("DOMContentLoaded", init);

function init() {
  state.chat = [{ ...WELCOME_MESSAGE, suggestions: [...WELCOME_MESSAGE.suggestions] }];
  renderPinned();
  renderRecent();
  renderTransactions();
  renderCategories();
  renderSchemas();
  renderHistory();
  renderTimeline(11);
  renderHeroChart();
  populateCategoryFilter();
  renderWizard();
  renderChat();
  setChatMode("closed");
  wireEvents();
  wireGlowwormReactions();
  setTimeout(() => openWizard(), 500);
}

/* ============================== Navigation ============================== */
function switchView(name) {
  state.view = name;
  document.querySelectorAll(".view").forEach((v) => v.classList.remove("active"));
  const el = document.getElementById("view-" + name);
  if (el) el.classList.add("active");
  document.querySelectorAll(".nav-item[data-view]").forEach((n) => {
    n.classList.toggle("active", n.dataset.view === name);
  });
  document.getElementById("topbarTitle").textContent = VIEW_TITLES[name] || "Dashboard";
  document.getElementById("sidebar").classList.remove("open");
  document.getElementById("sidebarScrim").classList.remove("show");
  window.scrollTo({ top: 0, behavior: "smooth" });
}

/* ============================== Widgets ============================== */
function spanFor(type) {
  if (type === "number" || type === "text" || type === "pie") return "span-4";
  return "span-8"; // bar, line, table
}

function renderPinned() {
  const grid = document.getElementById("pinnedGrid");
  grid.innerHTML = "";
  state.pinned.forEach((w) => {
    grid.appendChild(widgetEl(w, { span: spanFor(w.type) }));
  });
}

function renderHeroChart() {
  document.getElementById("heroChart").innerHTML = lineChartSVG({
    labels: BALANCE_SERIES.labels,
    series: [{ name: "Balance", color: "#0e6f51", values: BALANCE_SERIES.values }],
  }, 900);
}

/* ============================== Recent activity ============================== */
function renderRecent() {
  const list = document.getElementById("recentTransactions");
  list.innerHTML = "";
  TRANSACTIONS.slice(0, 7).forEach((t) => list.appendChild(txRowEl(t)));
}

function txRowEl(t) {
  const row = document.createElement("div");
  row.className = "tx-row";
  const cat = CATEGORIES.find((c) => c.name === t.category);
  const color = cat ? cat.color : merchantColor(t.merchant);
  row.innerHTML = `
    <div class="tx-icon" style="background:${color}">${esc(initial(t.merchant))}</div>
    <div class="tx-main">
      <div class="tx-merchant">${esc(t.merchant)}</div>
      <div class="tx-sub">${esc(t.category)} · ${esc(t.account)}</div>
    </div>
    <div class="tx-amount ${t.amount >= 0 ? "pos" : t.type === "transfer" ? "transfer" : "neg"}">
      ${t.type === "transfer" ? "⇄ " : ""}${t.amount >= 0 ? "+" : "−"}${fmtMoney(t.amount)}
    </div>`;
  return row;
}

function initial(s) {
  const words = String(s).trim().split(/\s+/);
  return (words[0]?.[0] || "?").toUpperCase();
}
function merchantColor(s) {
  let h = 0;
  for (const ch of String(s)) h = (h * 31 + ch.charCodeAt(0)) % 997;
  return MERCHANT_COLORS[h % MERCHANT_COLORS.length];
}

/* ============================== Transactions ============================== */
function typeBadge(t) {
  if (t === "income") return '<span class="badge badge-income">Income</span>';
  if (t === "transfer") return '<span class="badge badge-transfer">Transfer</span>';
  if (t === "bill") return '<span class="badge badge-bill">Bill</span>';
  return "";
}
function txAmountClass(t) {
  if (t.amount >= 0) return "pos";
  if (t.type === "transfer") return "transfer";
  return "neg";
}

function renderTransactions() {
  const q = (document.getElementById("txSearch").value || "").toLowerCase();
  const cat = document.getElementById("txCategory").value;
  const type = document.getElementById("txType").value;
  const rows = TRANSACTIONS.filter((t) => {
    if (q && !(t.merchant.toLowerCase().includes(q) || t.category.toLowerCase().includes(q))) return false;
    if (cat && t.category !== cat) return false;
    if (type && t.type !== type) return false;
    return true;
  });
  const body = document.getElementById("transactionsBody");
  body.innerHTML = "";
  rows.forEach((t) => {
    const catObj = CATEGORIES.find((c) => c.name === t.category);
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td class="cell-sub">${esc(t.date.slice(8))} ${monthName(t.date.slice(5, 7))}</td>
      <td>
        <div class="cell-merchant">${esc(t.merchant)} ${typeBadge(t.type)}</div>
        <div class="cell-sub">${esc(t.account)} account</div>
      </td>
      <td><span class="category-dot" style="background:${catObj ? catObj.color : "#999"}"></span> ${esc(t.category)}</td>
      <td>${esc(t.account)}</td>
      <td class="num tx-amount ${txAmountClass(t)}">${t.type === "transfer" ? "⇄ " : ""}${t.amount >= 0 ? "+" : "−"}${fmtMoney(t.amount)}</td>
      <td class="row-actions"><button class="widget-btn"><svg class="icon"><use href="#i-more"/></svg></button></td>`;
    body.appendChild(tr);
  });
  if (!rows.length) {
    body.innerHTML = `<tr><td colspan="6" class="cell-sub" style="padding:28px;text-align:center">No transactions match your filters.</td></tr>`;
  }
}
function monthName(mm) {
  return ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"][Number(mm) - 1] || "";
}

function populateCategoryFilter() {
  const sel = document.getElementById("txCategory");
  sel.innerHTML = '<option value="">All categories</option>';
  CATEGORIES.filter((c) => c.budget != null).forEach((c) => {
    const opt = document.createElement("option");
    opt.value = c.name;
    opt.textContent = c.name;
    sel.appendChild(opt);
  });
}

/* ============================== Categories ============================== */
function renderCategories() {
  const grid = document.getElementById("categoriesGrid");
  grid.innerHTML = "";
  CATEGORIES.forEach((c) => {
    const card = document.createElement("div");
    card.className = "category-card";
    const pct = c.budget ? Math.min(100, Math.round((c.spent / c.budget) * 100)) : 0;
    card.innerHTML = `
      <div class="cat-top">
        <span class="category-dot" style="background:${c.color}"></span>
        <span class="cat-name">${esc(c.name)}</span>
        <span class="cat-badge">${c.budget ? "Budgeted" : "System"}</span>
      </div>
      <div class="cat-amount">${c.spent != null ? fmtMoney(c.spent) : "—"}</div>
      <div class="cat-budget">${c.budget ? "of " + fmtMoney(c.budget) + " budget" : c.id === "income" ? "Incoming" : "Internal transfer"}</div>
      ${c.budget ? `<div class="progress"><span style="width:${pct}%;background:${c.color}"></span></div>` : ""}`;
    grid.appendChild(card);
  });
}

/* ============================== Schemas ============================== */
function renderSchemas() {
  const list = document.getElementById("schemasList");
  list.innerHTML = "";
  SCHEMAS.forEach((s) => {
    const el = document.createElement("div");
    el.className = "schema-card";
    el.innerHTML = `
      <div class="schema-logo" style="background:${s.color}">${esc(s.initials)}</div>
      <div class="schema-main">
        <div class="schema-name">${esc(s.name)} <span class="schema-conf">· ${s.confidence}% match</span></div>
        <div class="schema-cols">${s.cols.map(esc).join(" · ")}</div>
      </div>
      <button class="widget-btn" title="Use this schema"><svg class="icon"><use href="#i-chevron"/></svg></button>`;
    list.appendChild(el);
  });
}

/* ============================== History ============================== */
function renderHistory() {
  const list = document.getElementById("historyList");
  list.innerHTML = "";
  const f = state.historyFilter;
  const rows = HISTORY.filter((h) => (f === "all" ? true : h.kind === f));
  rows.forEach((h) => {
    const el = document.createElement("div");
    el.className = "history-item";
    const icon = h.kind === "you"
      ? "Y"
      : `<svg class="icon"><use href="${h.kind === "ai" ? "#i-sparkle" : "#i-import"}"/></svg>`;
    const diff = h.diff ? `<div class="history-diff">${h.diff.map((d) => `<span class="${d.cls}">${esc(d.line)}</span>`).join("")}</div>` : "";
    el.innerHTML = `
      <div class="history-icon ${h.kind}">${icon}</div>
      <div class="history-main">
        <div class="history-title"><span class="who ${h.kind}">${esc(h.actor)}</span> ${esc(h.title)}</div>
        <div class="history-sub">${esc(h.sub)}</div>
        ${diff}
      </div>
      <div class="history-time">${esc(h.time)}</div>`;
    list.appendChild(el);
  });
}

/* ============================== Timeline ============================== */
function renderTimeline(idx) {
  idx = Math.max(0, Math.min(TIMELINE_MONTHS.length - 1, Number(idx)));
  const m = TIMELINE_MONTHS[idx];
  document.getElementById("timelineDate").textContent = m.date;
  document.getElementById("timelineBalance").textContent = fmtMoney(BALANCE_SERIES.values[idx]);
  document.getElementById("timelineTxCount").textContent = 30 + idx * 12;
  document.querySelectorAll(".timeline-labels span")[0].textContent = TIMELINE_MONTHS[0].short;
  document.querySelectorAll(".timeline-labels span")[1].textContent = TIMELINE_MONTHS[TIMELINE_MONTHS.length - 1].short;

  const sliced = {
    labels: BALANCE_SERIES.labels.slice(0, idx + 1),
    series: [{ name: "Balance", color: "#0e6f51", values: BALANCE_SERIES.values.slice(0, idx + 1) }],
  };
  document.getElementById("timelineChart").innerHTML = lineChartSVG(sliced, 900);
}

/* ============================== AI chat ============================== */
let loaderSeq = 0;

function wormLoaderHTML() {
  const n = ++loaderSeq;
  const gid = (name) => `${name}-${n}`;
  const g = (name) => `url(#${gid(name)})`;
  return `<div class="worm-loader-stage" role="status" aria-label="Glowworm is thinking">
    <svg class="worm-loader-svg" viewBox="0 0 200 70">
      <defs>
        <linearGradient id="${gid("wl-body")}" x1="0%" y1="0%" x2="100%" y2="100%">
          <stop offset="0%" stop-color="#4ade80"/>
          <stop offset="50%" stop-color="#22c55e"/>
          <stop offset="100%" stop-color="#15803d"/>
        </linearGradient>
        <radialGradient id="${gid("wl-tail")}" cx="45%" cy="45%" r="55%">
          <stop offset="0%" stop-color="#ffffff"/>
          <stop offset="30%" stop-color="#fef08a"/>
          <stop offset="70%" stop-color="#4ade80"/>
          <stop offset="100%" stop-color="#16a34a"/>
        </radialGradient>
        <radialGradient id="${gid("wl-glow")}" cx="50%" cy="50%" r="50%">
          <stop offset="0%" stop-color="#a7f3d0" stop-opacity="0.9"/>
          <stop offset="45%" stop-color="#4ade80" stop-opacity="0.45"/>
          <stop offset="100%" stop-color="#22c55e" stop-opacity="0"/>
        </radialGradient>
      </defs>

      <!-- Bioluminescent trail (revealed while spinning) -->
      <g class="worm-glow-trail">
        <circle cx="100" cy="35" r="15" fill="none" stroke="${g("wl-tail")}" stroke-width="2.5" stroke-dasharray="32 62" stroke-linecap="round"/>
      </g>

      <!-- The worm: crawls in, curls into a ball, then this group spins -->
      <g class="worm-spin-group">
        <g class="wl-seg4">
          <circle cx="108.6" cy="47.3" r="4.8" fill="${g("wl-body")}"/>
          <circle cx="107.8" cy="48.5" r="3.2" fill="#86efac" opacity="0.35"/>
        </g>
        <g class="wl-tail">
          <circle class="tail-bulb" cx="115" cy="35" r="10" fill="${g("wl-glow")}"/>
          <circle cx="115" cy="35" r="4.3" fill="${g("wl-tail")}"/>
          <circle cx="114.2" cy="34.2" r="1.6" fill="#ffffff" opacity="0.85"/>
        </g>
        <g class="wl-seg3">
          <circle cx="94.9" cy="49.1" r="5.2" fill="${g("wl-body")}"/>
          <circle cx="94" cy="50.5" r="3.5" fill="#86efac" opacity="0.35"/>
        </g>
        <g class="wl-seg2">
          <circle cx="85.5" cy="38.9" r="5.6" fill="${g("wl-body")}"/>
          <circle cx="84.5" cy="40" r="3.8" fill="#86efac" opacity="0.35"/>
        </g>
        <g class="wl-seg1">
          <circle cx="88.5" cy="25.4" r="6" fill="${g("wl-body")}"/>
          <circle cx="87.5" cy="26.5" r="4" fill="#86efac" opacity="0.35"/>
        </g>
        <g class="wl-head">
          <circle cx="100" cy="20" r="6.8" fill="${g("wl-body")}"/>
          <circle cx="96.5" cy="21.5" r="1.5" fill="#f472b6" opacity="0.55"/>
          <circle cx="103.5" cy="21.5" r="1.5" fill="#f472b6" opacity="0.55"/>
          <circle cx="97.5" cy="18.5" r="1.9" fill="#0f291e"/>
          <circle cx="97" cy="18" r="0.8" fill="#ffffff"/>
          <circle cx="102.5" cy="18.5" r="1.9" fill="#0f291e"/>
          <circle cx="102" cy="18" r="0.8" fill="#ffffff"/>
          <g class="wl-antenna">
            <path d="M 98 14 Q 95 9 92 10" stroke="#16a34a" stroke-width="1.3" fill="none" stroke-linecap="round"/>
            <circle cx="91.5" cy="10" r="1.2" fill="#86efac"/>
            <path d="M 102 14 Q 105 9 108 10" stroke="#16a34a" stroke-width="1.3" fill="none" stroke-linecap="round"/>
            <circle cx="108.5" cy="10" r="1.2" fill="#86efac"/>
          </g>
          <path d="M 98.8 22.5 Q 100 24 101.2 22.5" stroke="#0f291e" stroke-width="1.1" fill="none" stroke-linecap="round"/>
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

function chatWidgetHTML(widget) {
  const el = widgetEl(widget, { span: "span-12", pinnable: true });
  if (state.pinned.some((w) => w.id === widget.id)) {
    el.querySelector("[data-action='toggle-pin']")?.classList.add("pinned-on");
  }
  return el.outerHTML;
}

function chatMessageEl(m) {
  const el = document.createElement("div");
  el.className = "msg " + (m.role === "user" ? "user" : "assistant");

  if (m.role === "user") {
    el.innerHTML = `<div class="msg-content"><div class="msg-bubble">${esc(m.text)}</div></div>`;
    return el;
  }

  const avatar = `<div class="msg-avatar"><svg class="gw"><use href="#i-glowworm"/></svg></div>`;
  let content = "";
  if (m.pending) {
    content = `<div class="msg-content">${wormLoaderHTML()}</div>`;
  } else {
    const text = m.text ? `<div class="msg-text">${esc(m.text)}</div>` : "";
    const reason = m.steps && m.steps.length ? msgReasonHTML(m.steps) : "";
    const widget = m.widget ? chatWidgetHTML(m.widget) : "";
    const suggestions = m.suggestions && m.suggestions.length
      ? `<div class="msg-suggestions">${m.suggestions.map((s) => `<button class="chip" data-chat-suggest="${esc(s.text)}">${esc(s.label)}</button>`).join("")}</div>`
      : "";
    content = `<div class="msg-content">${text}${reason}${widget}${suggestions}</div>`;
  }
  el.innerHTML = avatar + content;
  return el;
}

function renderChat() {
  const box = document.getElementById("chatMessages");
  box.innerHTML = "";
  state.chat.forEach((m) => box.appendChild(chatMessageEl(m)));
  box.scrollTop = box.scrollHeight;
}

function sendChat(text) {
  text = (text || "").trim();
  if (!text) return;
  state.chat.push({ role: "user", text });
  state.chat.push({ role: "assistant", pending: true });
  renderChat();
  nodChatAvatar();

  const resp = QUERY_RESPONSES.find((r) => r.match.test(text)) || DEFAULT_RESPONSE;
  setTimeout(() => {
    const idx = state.chat.findIndex((m) => m.pending);
    if (idx > -1) {
      state.chat[idx] = {
        role: "assistant",
        text: resp.answer,
        steps: resp.steps,
        widget: { id: widgetId(), ...JSON.parse(JSON.stringify(resp.widget)) },
      };
    }
    renderChat();
  }, 2850);
}

/* ============================== Glowworm reactions ============================== */
function nodChatAvatar() {
  const avatar = document.querySelector(".chat-head .chat-avatar");
  if (!avatar) return;
  avatar.classList.remove("gw-nod");
  void avatar.offsetWidth; // restart the animation
  avatar.classList.add("gw-nod");
}

function wireGlowwormReactions() {
  // Eyes follow the cursor when it's near a mascot
  document.querySelectorAll(".brand-logo, .chat-avatar, .chat-toggle").forEach((host) => {
    host.addEventListener("mousemove", (e) => {
      const svg = host.querySelector(".gw");
      if (!svg) return;
      const r = svg.getBoundingClientRect();
      const dx = (e.clientX - (r.left + r.width / 2)) / Math.max(r.width, 1);
      const dy = (e.clientY - (r.top + r.height / 2)) / Math.max(r.height, 1);
      svg.style.setProperty("--gw-look-x", clamp(dx * 1.6, -0.6, 0.6).toFixed(2) + "px");
      svg.style.setProperty("--gw-look-y", clamp(dy * 1.6, -0.5, 0.5).toFixed(2) + "px");
    });
    host.addEventListener("mouseleave", () => {
      const svg = host.querySelector(".gw");
      svg?.style.removeProperty("--gw-look-x");
      svg?.style.removeProperty("--gw-look-y");
    });
  });

  // Any glowworm does a joyful hop when clicked
  document.addEventListener("click", (e) => {
    const gw = e.target.closest(".gw");
    if (!gw) return;
    gw.classList.remove("gw-hop");
    void gw.offsetWidth; // restart the animation
    gw.classList.add("gw-hop");
  });

  // Chat avatar perks up while the user is typing
  const input = document.getElementById("chatInput");
  const avatar = document.querySelector(".chat-head .chat-avatar");
  input.addEventListener("focus", () => avatar?.classList.add("gw-listening"));
  input.addEventListener("blur", () => avatar?.classList.remove("gw-listening"));
}

function clamp(v, min, max) {
  return Math.max(min, Math.min(max, v));
}

function setChatMode(mode) {
  state.chatMode = mode;
  const panel = document.getElementById("chatPanel");
  const shell = document.getElementById("dashShell");
  const fab = document.getElementById("chatFab");
  const toggle = document.getElementById("chatToggle");
  const pinBtn = document.getElementById("chatPinBtn");

  panel.classList.remove("floating", "open", "docked");

  if (mode === "pinned") {
    shell.classList.add("docked");
    shell.appendChild(panel);
    panel.classList.add("docked");
    switchView("dashboard");
  } else {
    shell.classList.remove("docked");
    if (panel.parentElement !== document.body) document.body.appendChild(panel);
    panel.classList.add("floating");
    if (mode === "floating") panel.classList.add("open");
  }

  fab.hidden = mode !== "closed";
  toggle.classList.toggle("active", mode !== "closed");
  pinBtn.classList.toggle("active", mode === "pinned");
  renderChat();
}

function openChat() {
  setChatMode("floating");
  setTimeout(() => document.getElementById("chatInput").focus(), 260);
}
function toggleChat() {
  setChatMode(state.chatMode === "closed" ? "floating" : "closed");
}
function togglePinChat() {
  setChatMode(state.chatMode === "pinned" ? "floating" : "pinned");
}

/* ============================== Widget config drawer ============================== */
function findWidget(id) {
  const p = state.pinned.find((w) => w.id === id);
  if (p) return { widget: p, store: "pinned" };
  for (const m of state.chat) {
    if (m.widget && m.widget.id === id) return { widget: m.widget, store: "chat" };
  }
  return null;
}

function openDrawer(id) {
  const found = findWidget(id);
  if (!found) return;
  state.editingId = id;
  document.getElementById("wTitle").value = found.widget.title;
  document.getElementById("wType").value = found.widget.type;
  document.getElementById("wPinned").checked = state.pinned.some((w) => w.id === id);
  updateJsonPreview();
  document.getElementById("widgetDrawer").hidden = false;
  document.getElementById("drawerBackdrop").hidden = false;
  requestAnimationFrame(() => {
    document.getElementById("widgetDrawer").classList.add("show");
    document.getElementById("drawerBackdrop").classList.add("show");
  });
}

function closeDrawer() {
  document.getElementById("widgetDrawer").classList.remove("show");
  document.getElementById("drawerBackdrop").classList.remove("show");
  setTimeout(() => {
    document.getElementById("widgetDrawer").hidden = true;
    document.getElementById("drawerBackdrop").hidden = true;
  }, 220);
  state.editingId = null;
}

function updateJsonPreview() {
  const found = findWidget(state.editingId);
  if (!found) return;
  const preview = { type: document.getElementById("wType").value, title: document.getElementById("wTitle").value, data: found.widget.data };
  document.getElementById("wJsonPreview").textContent = JSON.stringify(preview, null, 2);
}

function applyWidget() {
  const id = state.editingId;
  const found = findWidget(id);
  if (!found) return;
  const newType = document.getElementById("wType").value;
  const newTitle = document.getElementById("wTitle").value.trim() || found.widget.title;
  const shouldPin = document.getElementById("wPinned").checked;

  found.widget.type = newType;
  found.widget.title = newTitle;
  found.widget.data = convertWidgetData(found.widget.type, newType, found.widget.data);

  const alreadyPinned = state.pinned.some((w) => w.id === id);
  if (shouldPin && !alreadyPinned) {
    state.pinned.push({ ...found.widget });
  } else if (!shouldPin && alreadyPinned) {
    state.pinned = state.pinned.filter((w) => w.id !== id);
  }

  renderPinned();
  renderChat();
  closeDrawer();
  toast("Widget updated");
}

function togglePin(id, btn) {
  const found = findWidget(id);
  if (!found) return;
  const isPinned = state.pinned.some((w) => w.id === id);
  if (isPinned) {
    state.pinned = state.pinned.filter((w) => w.id !== id);
    toast("Removed from dashboard");
  } else {
    state.pinned.push({ ...found.widget });
    toast("Pinned to dashboard");
  }
  renderPinned();
  renderChat();
}

/* ============================== Setup wizard ============================== */
const WIZARD_STEPS = 5;

function openWizard() {
  state.wizardStep = 0;
  document.getElementById("setupWizard").hidden = false;
  renderWizard();
}
function closeWizard() {
  document.getElementById("setupWizard").hidden = true;
}
function renderWizard() {
  const step = state.wizardStep;
  document.querySelectorAll(".wizard-step").forEach((s) => {
    s.hidden = s.dataset.step !== String(step);
  });
  const prog = document.getElementById("wizardProgress");
  prog.innerHTML = "";
  for (let i = 0; i < WIZARD_STEPS; i++) {
    const span = document.createElement("span");
    if (i <= step) span.classList.add("done");
    prog.appendChild(span);
  }
  document.getElementById("wizardBack").hidden = step === 0;
  document.getElementById("wizardSkip").hidden = step === WIZARD_STEPS - 1;
  document.getElementById("wizardNext").innerHTML =
    step === WIZARD_STEPS - 1
      ? "Finish <svg class='icon'><use href='#i-check'/></svg>"
      : "Continue <svg class='icon'><use href='#i-arrow-right'/></svg>";
  if (step === 1) renderWizardCategories();
}
function renderWizardCategories() {
  const wrap = document.getElementById("wizardCategories");
  wrap.innerHTML = "";
  state.wizardCats.forEach((c, i) => {
    const chip = document.createElement("span");
    chip.className = "wizard-cat";
    chip.innerHTML = `<span class="category-dot" style="background:${c.color}"></span>${esc(c.name)}<button class="remove" data-wizcat="${i}" title="Remove">✕</button>`;
    wrap.appendChild(chip);
  });
}
function wizardNext() {
  if (state.wizardStep < WIZARD_STEPS - 1) {
    state.wizardStep++;
    renderWizard();
  } else {
    closeWizard();
    toast("Welcome to Glowworm — you're all set");
  }
}
function wizardBack() {
  if (state.wizardStep > 0) {
    state.wizardStep--;
    renderWizard();
  }
}

/* ============================== Toast ============================== */
let toastTimer;
function toast(msg) {
  const el = document.getElementById("toast");
  el.hidden = false;
  el.innerHTML = `<svg class="icon"><use href="#i-check"/></svg> ${esc(msg)}`;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (el.hidden = true), 2600);
}

/* ============================== Events ============================== */
function wireEvents() {
  document.addEventListener("click", (e) => {
    const nav = e.target.closest("[data-view]");
    if (nav) switchView(nav.dataset.view);
    const link = e.target.closest("[data-view-link]");
    if (link) switchView(link.dataset.viewLink);
    const action = e.target.closest("[data-action]");
    if (action) handleAction(action.dataset.action);
    const suggest = e.target.closest("[data-chat-suggest]");
    if (suggest) sendChat(suggest.dataset.chatSuggest);
    const wcat = e.target.closest("[data-wizcat]");
    if (wcat) {
      state.wizardCats.splice(Number(wcat.dataset.wizcat), 1);
      renderWizardCategories();
    }
    const prov = e.target.closest(".provider-pick");
    if (prov) {
      document.querySelectorAll(".provider-pick").forEach((p) => p.classList.remove("selected"));
      prov.classList.add("selected");
    }
    const hf = e.target.closest("[data-history-filter]");
    if (hf) {
      document.querySelectorAll("[data-history-filter]").forEach((c) => c.classList.remove("active"));
      hf.classList.add("active");
      state.historyFilter = hf.dataset.historyFilter;
      renderHistory();
    }
  });

  // widget actions (delegated)
  document.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-action='edit-widget']");
    if (btn) {
      const widget = btn.closest("[data-widget-id]");
      openDrawer(widget.dataset.widgetId);
      return;
    }
    const pin = e.target.closest("[data-action='toggle-pin']");
    if (pin) {
      const widget = pin.closest("[data-widget-id]");
      togglePin(widget.dataset.widgetId, pin);
    }
  });

  // chat controls
  document.getElementById("chatToggle").addEventListener("click", toggleChat);
  document.getElementById("chatFab").addEventListener("click", openChat);
  document.getElementById("chatCloseBtn").addEventListener("click", () => setChatMode("closed"));
  document.getElementById("chatPinBtn").addEventListener("click", togglePinChat);
  document.getElementById("chatSendBtn").addEventListener("click", () => {
    const input = document.getElementById("chatInput");
    sendChat(input.value);
    input.value = "";
  });
  document.getElementById("chatInput").addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      sendChat(e.target.value);
      e.target.value = "";
    }
  });

  // wizard footer
  document.getElementById("wizardNext").addEventListener("click", wizardNext);
  document.getElementById("wizardBack").addEventListener("click", wizardBack);
  document.getElementById("wizardSkip").addEventListener("click", closeWizard);
  document.getElementById("wizardAddCategory").addEventListener("click", () => {
    const name = window.prompt("New category name");
    if (name && name.trim()) {
      state.wizardCats.push({ id: "c" + Date.now(), name: name.trim(), color: MERCHANT_COLORS[state.wizardCats.length % MERCHANT_COLORS.length], budget: 100, spent: 0 });
      renderWizardCategories();
    }
  });
  document.getElementById("wizardDropzone").addEventListener("click", () => {
    toast("Statement import is coming soon");
  });

  // drawer
  document.getElementById("wApply").addEventListener("click", applyWidget);
  document.getElementById("wTitle").addEventListener("input", updateJsonPreview);
  document.getElementById("wType").addEventListener("change", updateJsonPreview);

  // transactions filters
  ["txSearch", "txCategory", "txType"].forEach((id) => {
    document.getElementById(id).addEventListener("input", renderTransactions);
    document.getElementById(id).addEventListener("change", renderTransactions);
  });

  // categories add
  document.getElementById("addCategoryBtn").addEventListener("click", () => {
    const name = window.prompt("New category name");
    if (name && name.trim()) {
      CATEGORIES.push({ id: "c" + Date.now(), name: name.trim(), color: MERCHANT_COLORS[CATEGORIES.length % MERCHANT_COLORS.length], budget: 100, spent: 0 });
      renderCategories();
      populateCategoryFilter();
      toast("Category added");
    }
  });

  // import dropzone
  const dz = document.getElementById("dropzone");
  document.getElementById("dropzoneBrowse").addEventListener("click", () => document.getElementById("fileInput").click());
  document.getElementById("fileInput").addEventListener("change", () => toast("Importer coming soon — files selected"));
  ["dragenter", "dragover"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.add("drag"); }));
  ["dragleave", "drop"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.remove("drag"); }));
  dz.addEventListener("drop", () => toast("Importer coming soon — this is a UI skeleton"));

  // timeline
  document.getElementById("timelineSlider").addEventListener("input", (e) => renderTimeline(e.target.value));

  // mobile sidebar
  document.getElementById("menuToggle").addEventListener("click", () => {
    document.getElementById("sidebar").classList.toggle("open");
    document.getElementById("sidebarScrim").classList.toggle("show");
  });
  document.getElementById("sidebarScrim").addEventListener("click", () => {
    document.getElementById("sidebar").classList.remove("open");
    document.getElementById("sidebarScrim").classList.remove("show");
  });

  // keyboard
  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      openChat();
    }
    if (e.key === "Escape") {
      closeWizard();
      closeDrawer();
    }
  });

  // backdrop / close buttons
  document.getElementById("drawerBackdrop").addEventListener("click", closeDrawer);
  document.querySelectorAll('[data-action="close-drawer"]').forEach((b) => b.addEventListener("click", closeDrawer));
  document.querySelectorAll('[data-action="close-wizard"]').forEach((b) => b.addEventListener("click", closeWizard));
}

function handleAction(action) {
  if (action === "run-setup") openWizard();
  if (action === "close-wizard") closeWizard();
  if (action === "close-drawer") closeDrawer();
}
