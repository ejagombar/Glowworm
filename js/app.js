/* ============================================================
   Glowworm — app shell (UI skeleton)
   Navigation, mock rendering, setup wizard, AI chat with dynamic
   widgets. No real AI or importer logic yet.
   ============================================================ */

const state = {
  view: "dashboard",
  pinned: DEFAULT_PINNED_WIDGETS.map((w) => ({ ...w })),
  chat: [],
  historyFilter: "all",
  wizardStep: 0,
  wizardCats: CATEGORIES.filter((c) => c.budget != null).map((c) => ({ ...c })),
  editingId: null,
  period: "last-12m",
  editMode: false,
  dragId: null,
  accounts: ACCOUNTS.map((a) => ({ ...a })),
  selectedAccount: null,
  balanceView: "all",
  historyMode: "transactions",
  timelineAccount: null,
  timelineIdx: 11,
  privacy: false,
};

const PERIOD_LABELS = {
  all: "All time",
  ytd: "Year to date",
  "last-12m": "Last 12 months",
  "last-3m": "Last 3 months",
  "last-month": "Last month",
  "last-day": "Last day",
  custom: "Custom range",
};

const VIEW_TITLES = {
  dashboard: "Dashboard",
  queries: "Queries",
  transactions: "Transactions",
  categories: "Categories",
  import: "Import",
  timeline: "Timeline",
  history: "History",
  settings: "Settings",
};

const SUGGESTED_PROMPTS = [
  { label: "Transport spend", text: "How much have I spent on Transport over the past year?" },
  { label: "Where did my money go?", text: "Where did my money go this month?" },
  { label: "Balance over time", text: "How has my balance changed this year?" },
];

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
  initTheme();
  state.chat = [];
  renderPinned();
  renderRecent();
  renderTransactions();
  renderCategories();
  renderAccounts();
  renderDashboardTotals();
  populateAccountFilter();
  populateImportAccounts();
  renderSettingsAccounts();
  renderSchemas();
  renderHistory();
  renderTimeline(11);
  setHistoryMode("transactions");
  renderHeroChart();
  syncBalanceCards();
  populateCategoryFilter();
  renderWizard();
  renderChat();
  renderSuggestions();
  wireEvents();
  setTimeout(() => openWizard(), 500);
}

/* ============================== Theme ============================== */
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

/* ============================== Period ============================== */
function renderPeriod() {
  renderHeroChart();
}

/* ============================== Accounts ============================== */
function fmtBalance(v) {
  return (v < 0 ? "−£" : "£") + Math.abs(v).toLocaleString("en-GB", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
function accountTypeLabel(a) {
  return a.typeLabel || (a.type === "savings" ? "Savings" : a.type === "credit" ? "Credit" : "Debit");
}
function accountTypeBadge(a) {
  return a.type === "savings" ? "badge-soft" : a.type === "credit" ? "badge-expense" : "badge-income";
}
function accountTotals() {
  const spending = state.accounts.filter((a) => a.type !== "savings").reduce((s, a) => s + a.balance, 0);
  const savings = state.accounts.filter((a) => a.type === "savings").reduce((s, a) => s + a.balance, 0);
  return { spending, savings };
}
function renderDashboardTotals() {
  const t = accountTotals();
  const av = document.getElementById("statAvailable");
  if (av) av.textContent = fmtBalance(t.spending);
  const sv = document.getElementById("statSavings");
  if (sv) sv.textContent = fmtBalance(t.savings);
}
function renderAccounts() {
  const grid = document.getElementById("accountsGrid");
  grid.innerHTML = "";
  state.accounts.forEach((a) => {
    const card = document.createElement("div");
    card.className = "account-card";
    card.dataset.accountId = a.id;
    const txCount = TRANSACTIONS.filter((t) => t.account === (a.txKey || a.name)).length;
    card.innerHTML = `
      <div class="account-card-top">
        <span class="account-dot" style="background:${a.color}"></span>
        <span class="account-name">${esc(a.name)}</span>
        <span class="badge ${accountTypeBadge(a)}">${esc(accountTypeLabel(a))}</span>
      </div>
      <div class="account-balance ${a.balance < 0 ? "neg" : ""}">${fmtBalance(a.balance)}</div>
      <div class="account-sub">${esc(a.last4)} · ${txCount} transactions</div>`;
    card.addEventListener("click", () => {
      state.selectedAccount = state.selectedAccount === a.id ? null : a.id;
      const sel = document.getElementById("txAccount");
      if (sel) sel.value = state.selectedAccount || "";
      syncAccountSelection();
      renderTransactions();
    });
    grid.appendChild(card);
  });
  syncAccountSelection();
}
function syncAccountSelection() {
  document.querySelectorAll(".account-card").forEach((c) => {
    c.classList.toggle("selected", c.dataset.accountId === state.selectedAccount);
  });
  const sub = document.getElementById("accountsTxSub");
  if (sub) {
    const acc = state.accounts.find((a) => a.id === state.selectedAccount);
    sub.textContent = acc ? acc.name : "All accounts";
  }
}
function populateAccountFilter() {
  const sel = document.getElementById("txAccount");
  sel.innerHTML = '<option value="">All accounts</option>';
  state.accounts.forEach((a) => {
    const opt = document.createElement("option");
    opt.value = a.id;
    opt.textContent = a.name;
    sel.appendChild(opt);
  });
  sel.value = state.selectedAccount || "";
}
function populateImportAccounts() {
  const sel = document.getElementById("importAccount");
  sel.innerHTML = "";
  state.accounts.forEach((a) => {
    const opt = document.createElement("option");
    opt.value = a.id;
    opt.textContent = a.name + " — " + fmtBalance(a.balance);
    sel.appendChild(opt);
  });
}
function renderSettingsAccounts() {
  const wrap = document.getElementById("settingsAccounts");
  wrap.innerHTML = "";
  state.accounts.forEach((a) => {
    const row = document.createElement("div");
    row.className = "account-row";
    row.innerHTML = `
      <span class="account-dot" style="background:${a.color}"></span>
      <div><strong>${esc(a.name)}</strong><p class="muted">${esc(accountTypeLabel(a))} · ${fmtBalance(a.balance)}</p></div>
      <button class="widget-btn" data-action="edit-account" data-account-id="${a.id}" title="Edit account"><svg class="icon"><use href="#i-settings"/></svg></button>`;
    wrap.appendChild(row);
  });
}
let editingAccountId = null;
function openAccountForm(id) {
  editingAccountId = id || null;
  const form = document.getElementById("accountForm");
  form.hidden = false;
  const acc = id ? state.accounts.find((a) => a.id === id) : null;
  document.getElementById("accName").value = acc ? acc.name : "";
  document.getElementById("accType").value = acc ? acc.type : "debit";
  document.getElementById("accOpening").value = acc ? (acc.opening != null ? acc.opening : acc.balance) : "";
  document.getElementById("accSaveBtn").textContent = acc ? "Save changes" : "Add account";
  document.getElementById("accDeleteBtn").hidden = !acc;
}
function closeAccountForm() {
  document.getElementById("accountForm").hidden = true;
  editingAccountId = null;
}
function saveAccount() {
  const name = document.getElementById("accName").value.trim();
  if (!name) { toast("Account name is required"); return; }
  const type = document.getElementById("accType").value;
  const opening = parseFloat(document.getElementById("accOpening").value) || 0;
  const labels = { debit: "Debit", credit: "Credit", savings: "Savings" };
  const isEdit = !!editingAccountId;
  if (isEdit) {
    const a = state.accounts.find((x) => x.id === editingAccountId);
    if (a) { a.name = name; a.type = type; a.typeLabel = labels[type]; a.opening = opening; }
  } else {
    state.accounts.push({
      id: "a" + Date.now(),
      name,
      type,
      typeLabel: labels[type],
      txKey: name,
      balance: opening,
      opening,
      color: MERCHANT_COLORS[state.accounts.length % MERCHANT_COLORS.length],
      last4: "··· new",
    });
  }
  closeAccountForm();
  renderAccounts();
  renderSettingsAccounts();
  renderDashboardTotals();
  populateImportAccounts();
  populateAccountFilter();
  toast(isEdit ? "Account updated" : "Account added");
}
function deleteAccount(id) {
  const a = state.accounts.find((x) => x.id === id);
  if (!a) return;
  if (!window.confirm("Delete account “" + a.name + "”?")) return;
  state.accounts = state.accounts.filter((x) => x.id !== id);
  if (state.selectedAccount === id) state.selectedAccount = null;
  renderAccounts();
  renderSettingsAccounts();
  renderDashboardTotals();
  populateImportAccounts();
  populateAccountFilter();
  renderTransactions();
  toast("Account deleted");
}

/* ============================== Privacy ============================== */
function togglePrivacy() {
  state.privacy = !state.privacy;
  document.body.classList.toggle("privacy", state.privacy);
  const btn = document.getElementById("privacyToggle");
  if (btn) {
    btn.classList.toggle("active", state.privacy);
    btn.title = state.privacy ? "Show balances" : "Hide balances";
    btn.innerHTML = state.privacy
      ? '<svg class="icon"><use href="#i-eye-off"/></svg>'
      : '<svg class="icon"><use href="#i-eye"/></svg>';
  }
}

/* ============================== Navigation ============================== */
function switchView(name) {
  state.view = name;
  document.querySelectorAll(".view").forEach((v) => v.classList.remove("active"));
  const el = document.getElementById("view-" + name);
  if (el) el.classList.add("active");
  document.querySelectorAll(".tabnav-item[data-view]").forEach((n) => {
    n.classList.toggle("active", n.dataset.view === name);
  });
  window.scrollTo({ top: 0, behavior: "smooth" });
}

/* ============================== Widgets ============================== */
function spanCol(type) {
  if (type === "number" || type === "text" || type === "pie") return 4;
  return 8; // bar, line, table
}
function spanRows(type) {
  if (type === "number" || type === "text") return 2;
  if (type === "pie") return 3;
  return 4; // bar, line, table
}
function clamp(n, min, max) { return Math.max(min, Math.min(max, n)); }

// Must match the .widgets-grid CSS values
const GRID_GAP = 12;
const GRID_ROW = 72;

function renderPinned() {
  const grid = document.getElementById("pinnedGrid");
  grid.innerHTML = "";
  grid.classList.toggle("editing", state.editMode);
  state.pinned.forEach((w) => {
    grid.appendChild(widgetEl(w, {
      col: w.col || spanCol(w.type),
      rows: w.rows || spanRows(w.type),
      editMode: state.editMode,
    }));
  });
  drawChartHosts(grid);
}

function setEditMode(on) {
  state.editMode = on;
  const btn = document.getElementById("pinnedEditBtn");
  const hint = document.getElementById("pinnedHint");
  if (btn) {
    btn.innerHTML = on
      ? '<svg class="icon"><use href="#i-check"/></svg> Done'
      : '<svg class="icon"><use href="#i-settings"/></svg> Edit';
  }
  if (hint) hint.textContent = on ? "Drag onto another widget to swap · drag the edges to resize" : "Saved from your questions";
  renderPinned();
}

function swapWidgets(idA, idB) {
  const ia = state.pinned.findIndex((w) => w.id === idA);
  const ib = state.pinned.findIndex((w) => w.id === idB);
  if (ia < 0 || ib < 0 || ia === ib) return;
  const a = state.pinned[ia], b = state.pinned[ib];
  const aCol = a.col || spanCol(a.type), aRows = a.rows || spanRows(a.type);
  const bCol = b.col || spanCol(b.type), bRows = b.rows || spanRows(b.type);
  state.pinned[ia] = b;
  state.pinned[ib] = a;
  a.col = bCol; a.rows = bRows;
  b.col = aCol; b.rows = aRows;
}

function balanceScopeLabel() {
  if (state.balanceView === "savings") return "Savings";
  if (state.balanceView === "available") return "Available balance";
  return "All accounts";
}
function balanceSeriesFor(view) {
  if (view === "savings") {
    return { labels: BALANCE_SERIES.labels, series: [{ name: "Savings", color: "#4f5bc7", values: SAVINGS_BALANCE_SERIES.values }] };
  }
  if (view === "available") {
    const values = BALANCE_SERIES.values.map((v, i) => v - (SAVINGS_BALANCE_SERIES.values[i] || 0));
    return { labels: BALANCE_SERIES.labels, series: [{ name: "Available", color: "#0e6f51", values }] };
  }
  return { labels: BALANCE_SERIES.labels, series: [{ name: "Balance", color: "#0e6f51", values: BALANCE_SERIES.values }] };
}
function syncBalanceCards() {
  document.querySelectorAll("[data-balance-view]").forEach((c) => {
    c.classList.toggle("selected", c.dataset.balanceView === state.balanceView);
  });
}
function renderHeroChart() {
  document.getElementById("heroChart").innerHTML = lineChartSVG(balanceSeriesFor(state.balanceView), 900);
  const range = document.getElementById("heroRange");
  if (range) {
    const label = (PERIOD_LABELS[state.period] || "Last 12 months").toLowerCase();
    range.textContent = balanceScopeLabel() + " · " + label;
  }
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
  const acc = state.accounts.find((a) => a.id === state.selectedAccount);
  const rows = TRANSACTIONS.filter((t) => {
    if (q && !(t.merchant.toLowerCase().includes(q) || t.category.toLowerCase().includes(q))) return false;
    if (cat && t.category !== cat) return false;
    if (type && t.type !== type) return false;
    if (acc && t.account !== (acc.txKey || acc.name)) return false;
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
  CATEGORIES.filter((c) => c.id !== "income" && c.id !== "savings").forEach((c) => {
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
    const isSystem = c.id === "income" || c.id === "savings";
    const pct = c.budget ? Math.min(100, Math.round((c.spent / c.budget) * 100)) : 0;
    const badge = c.budget ? "Budgeted" : isSystem ? "System" : "Unbudgeted";
    const sub = c.budget
      ? "of " + fmtMoney(c.budget) + " budget"
      : c.id === "income" ? "Incoming" : c.id === "savings" ? "Internal transfer" : "No budget set";
    card.innerHTML = `
      <div class="cat-top">
        <span class="category-dot" style="background:${c.color}"></span>
        <span class="cat-name">${esc(c.name)}</span>
        <span class="cat-badge">${badge}</span>
      </div>
      <div class="cat-amount">${c.spent != null ? fmtMoney(c.spent) : "—"}</div>
      <div class="cat-budget">${sub}</div>
      ${c.budget ? `<div class="progress"><span style="width:${pct}%;background:${c.color}"></span></div>` : ""}`;
    grid.appendChild(card);
  });
}

function openCategoryForm() {
  const form = document.getElementById("categoryForm");
  form.hidden = false;
  document.getElementById("catName").value = "";
  document.getElementById("catBudget").value = "";
  document.getElementById("catName").focus();
}
function closeCategoryForm() {
  document.getElementById("categoryForm").hidden = true;
}
function saveCategory() {
  const name = document.getElementById("catName").value.trim();
  if (!name) { toast("Category name is required"); return; }
  const budget = parseFloat(document.getElementById("catBudget").value) || 0;
  CATEGORIES.push({
    id: "c" + Date.now(),
    name,
    color: MERCHANT_COLORS[CATEGORIES.length % MERCHANT_COLORS.length],
    budget: budget > 0 ? budget : null,
    spent: 0,
  });
  closeCategoryForm();
  renderCategories();
  populateCategoryFilter();
  toast("Category added");
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
      <div class="history-time">${esc(h.time)}</div>
      <button class="btn btn-ghost btn-xs history-restore" data-restore-history="${h.id}" title="Restore state to this point">Restore</button>`;
    list.appendChild(el);
  });
}

/* ============================== History page ============================== */
function setHistoryMode(mode) {
  state.historyMode = mode;
  document.querySelectorAll("[data-history-mode]").forEach((c) => {
    c.classList.toggle("active", c.dataset.historyMode === mode);
  });
  document.getElementById("historyTimelinePane").hidden = mode !== "transactions";
  document.getElementById("historyAuditPane").hidden = mode !== "audit";
}

/* ============================== Timeline ============================== */
function renderTimeline(idx) {
  idx = Math.max(0, Math.min(TIMELINE_MONTHS.length - 1, Number(idx)));
  state.timelineIdx = idx;
  const m = TIMELINE_MONTHS[idx];
  document.getElementById("timelineDate").textContent = m.date;
  document.getElementById("timelineBalance").textContent = fmtMoney(BALANCE_SERIES.values[idx]);
  document.getElementById("timelineTxCount").textContent = 30 + idx * 12;
  document.querySelectorAll(".timeline-labels span")[0].textContent = TIMELINE_MONTHS[0].short;
  document.querySelectorAll(".timeline-labels span")[1].textContent = TIMELINE_MONTHS[TIMELINE_MONTHS.length - 1].short;

  const labels = BALANCE_SERIES.labels.slice(0, idx + 1);
  const selected = state.accounts.find((a) => a.id === state.timelineAccount);
  let chartData;
  if (selected) {
    const total = BALANCE_SERIES.values[BALANCE_SERIES.values.length - 1] || 1;
    const share = selected.balance / total;
    chartData = {
      labels,
      series: [{ name: selected.name, color: selected.color, values: BALANCE_SERIES.values.slice(0, idx + 1).map((v) => v * share) }],
    };
  } else {
    chartData = {
      labels,
      series: [{ name: "Balance", color: "#0e6f51", values: BALANCE_SERIES.values.slice(0, idx + 1) }],
    };
  }
  document.getElementById("timelineChart").innerHTML = lineChartSVG(chartData, 900);
  renderTimelineAccounts(idx);
}
function renderTimelineAccounts(idx) {
  const wrap = document.getElementById("timelineAccounts");
  const latest = BALANCE_SERIES.values[BALANCE_SERIES.values.length - 1] || 1;
  const ratio = BALANCE_SERIES.values[idx] / latest;
  wrap.innerHTML = "";
  state.accounts.forEach((a) => {
    const bal = a.balance * ratio;
    const el = document.createElement("div");
    el.className = "timeline-account";
    el.classList.toggle("selected", state.timelineAccount === a.id);
    el.innerHTML = `
      <span class="account-dot" style="background:${a.color}"></span>
      <span class="account-name">${esc(a.name)}</span>
      <span class="account-balance ${bal < 0 ? "neg" : ""}">${fmtBalance(bal)}</span>`;
    el.addEventListener("click", () => {
      state.timelineAccount = state.timelineAccount === a.id ? null : a.id;
      renderTimeline(state.timelineIdx);
    });
    wrap.appendChild(el);
  });
}

/* ============================== AI chat ============================== */
function wormLoaderHTML() {
  return `<div class="worm-loader" role="status" aria-label="Glowworm is thinking">
    <svg class="worm-crawl" viewBox="0 0 140 44" aria-hidden="true">
      <g class="worm-body">
        <circle class="worm-tail" cx="10" cy="30" r="4.5" fill="#7dffb8"/>
        <circle cx="24" cy="26" r="7" fill="#2cc57a"/>
        <circle cx="42" cy="22" r="8.5" fill="#27b268"/>
        <circle cx="63" cy="20" r="9.5" fill="#22a75e"/>
        <circle cx="87" cy="21" r="11" fill="#1f9b57"/>
        <circle cx="83.5" cy="17" r="2.4" fill="#ffffff"/>
        <circle cx="93" cy="17" r="2.4" fill="#ffffff"/>
        <circle cx="84" cy="17.4" r="1.2" fill="#0c3a22"/>
        <circle cx="93.5" cy="17.4" r="1.2" fill="#0c3a22"/>
        <path d="M83 11 q-2 -5 3 -6" stroke="#1f9b57" stroke-width="1.6" fill="none" stroke-linecap="round"/>
        <circle cx="86.5" cy="6.5" r="1.4" fill="#7dffb8"/>
        <path d="M84 25 q3 2.5 6 0" stroke="#0c3a22" stroke-width="1.4" fill="none" stroke-linecap="round"/>
      </g>
    </svg>
    <span class="worm-spin-wrap">
      <svg class="worm-spin" viewBox="0 0 48 48" aria-hidden="true">
        <circle cx="24" cy="24" r="17" fill="none" style="stroke:var(--line)" stroke-width="3"/>
        <circle cx="24" cy="24" r="17" fill="none" stroke="#1f9b57" stroke-width="3" stroke-linecap="round" stroke-dasharray="80 107"/>
        <g class="worm-rotor">
          <circle cx="24" cy="7" r="5.5" fill="#1f9b57"/>
          <circle cx="23" cy="6" r="1.3" fill="#ffffff"/>
          <circle cx="26.4" cy="6" r="1.3" fill="#ffffff"/>
          <circle cx="23.3" cy="6.2" r="0.7" fill="#0c3a22"/>
          <circle cx="26.7" cy="6.2" r="0.7" fill="#0c3a22"/>
        </g>
      </svg>
    </span>
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

  let content = "";
  if (m.pending) {
    content = `<div class="msg-content">${wormLoaderHTML()}</div>`;
  } else {
    const text = m.text ? `<div class="msg-text">${esc(m.text)}</div>` : "";
    const reason = m.steps && m.steps.length ? msgReasonHTML(m.steps) : "";
    const widget = m.widget ? chatWidgetHTML(m.widget) : "";
    content = `<div class="msg-content">${text}${reason}${widget}</div>`;
  }
  el.innerHTML = content;
  return el;
}

function dayKey(ts) {
  const d = new Date(ts);
  return d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
}
function dayLabel(ts) {
  const d = new Date(ts);
  const today = new Date(); today.setHours(0, 0, 0, 0);
  const that = new Date(d); that.setHours(0, 0, 0, 0);
  const diff = Math.round((today - that) / 86400000);
  if (diff === 0) return "Today";
  if (diff === 1) return "Yesterday";
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric" });
}
function chatDays() {
  const days = [];
  const index = new Map();
  state.chat.forEach((m) => {
    const key = dayKey(m.ts || Date.now());
    if (!index.has(key)) {
      const entry = { key, label: dayLabel(m.ts || Date.now()), count: 0 };
      index.set(key, entry);
      days.push(entry);
    }
    index.get(key).count++;
  });
  return days;
}
function renderDays() {
  const list = document.getElementById("queriesDays");
  list.innerHTML = "";
  chatDays().forEach((d) => {
    const btn = document.createElement("button");
    btn.className = "queries-day";
    btn.dataset.day = d.key;
    btn.innerHTML = `<span>${esc(d.label)}</span><span class="queries-day-count">${d.count}</span>`;
    list.appendChild(btn);
  });
}
function renderSuggestions() {
  const wrap = document.getElementById("querySuggestions");
  wrap.innerHTML = SUGGESTED_PROMPTS
    .map((s) => `<button class="chip" data-chat-suggest="${esc(s.text)}">${esc(s.label)}</button>`)
    .join("");
}

function renderChat() {
  const box = document.getElementById("queryMessages");
  box.innerHTML = "";
  let lastKey = null;
  state.chat.forEach((m) => {
    const key = dayKey(m.ts || Date.now());
    if (key !== lastKey) {
      const sep = document.createElement("div");
      sep.className = "msg-day";
      sep.dataset.day = key;
      sep.textContent = dayLabel(m.ts || Date.now());
      box.appendChild(sep);
      lastKey = key;
    }
    box.appendChild(chatMessageEl(m));
  });
  box.scrollTop = box.scrollHeight;
  renderDays();
}

function sendChat(text) {
  text = (text || "").trim();
  if (!text) return;
  state.chat.push({ role: "user", text, ts: Date.now() });
  state.chat.push({ role: "assistant", pending: true, ts: Date.now() });
  renderChat();

  const resp = QUERY_RESPONSES.find((r) => r.match.test(text)) || DEFAULT_RESPONSE;
  setTimeout(() => {
    const idx = state.chat.findIndex((m) => m.pending);
    if (idx > -1) {
      state.chat[idx] = {
        role: "assistant",
        text: resp.answer,
        steps: resp.steps,
        widget: { id: widgetId(), ...JSON.parse(JSON.stringify(resp.widget)) },
        ts: Date.now(),
      };
    }
    renderChat();
  }, 2600);
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
    const bal = e.target.closest("[data-balance-view]");
    if (bal) {
      const view = bal.dataset.balanceView;
      state.balanceView = state.balanceView === view ? "all" : view;
      syncBalanceCards();
      renderHeroChart();
    }
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
    const hm = e.target.closest("[data-history-mode]");
    if (hm) setHistoryMode(hm.dataset.historyMode);
    const rest = e.target.closest("[data-restore-history]");
    if (rest) toast("State restored to this point");
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

  // pinned widgets: edit mode (drag to swap + resize)
  document.getElementById("pinnedEditBtn").addEventListener("click", () => setEditMode(!state.editMode));

  let move = null;

  document.addEventListener("pointerdown", (e) => {
    if (!state.editMode) return;
    if (e.target.closest(".resize-handle") || e.target.closest(".widget-btn")) return;
    const card = e.target.closest("[data-widget-id]");
    if (!card || !document.getElementById("pinnedGrid").contains(card)) return;

    e.preventDefault();
    const rect = card.getBoundingClientRect();
    const ghost = card.cloneNode(true);
    ghost.classList.add("drag-ghost");
    ghost.classList.remove("dragging", "drop-target");
    ghost.style.width = rect.width + "px";
    ghost.style.height = rect.height + "px";
    ghost.style.left = rect.left + "px";
    ghost.style.top = rect.top + "px";
    document.body.appendChild(ghost);

    move = {
      id: card.dataset.widgetId,
      startX: e.clientX,
      startY: e.clientY,
      card,
      ghost,
      target: null,
      moved: false,
    };
    card.classList.add("dragging");
    document.body.classList.add("dragging-widget");
  });

  window.addEventListener("pointermove", (e) => {
    if (!move) return;
    if (!move.moved) {
      if (Math.hypot(e.clientX - move.startX, e.clientY - move.startY) < 5) return;
      move.moved = true;
    }
    e.preventDefault();
    move.ghost.style.transform = "translate(" + (e.clientX - move.startX) + "px, " + (e.clientY - move.startY) + "px)";
    move.ghost.style.pointerEvents = "none";
    const under = document.elementFromPoint(e.clientX, e.clientY);
    move.ghost.style.pointerEvents = "";
    const target = under ? under.closest("[data-widget-id]") : null;

    if (move.target && move.target !== target) {
      move.target.classList.remove("drop-target");
      move.target = null;
    }
    if (target && target.dataset.widgetId !== move.id && document.getElementById("pinnedGrid").contains(target)) {
      target.classList.add("drop-target");
      move.target = target;
    }
  });

  window.addEventListener("pointerup", () => {
    if (!move) return;
    if (move.moved && move.target) swapWidgets(move.id, move.target.dataset.widgetId);
    if (move.card) move.card.classList.remove("dragging");
    if (move.target) move.target.classList.remove("drop-target");
    if (move.ghost) move.ghost.remove();
    document.body.classList.remove("dragging-widget");
    move = null;
    renderPinned();
  });

  // resize widgets by dragging their edges
  let resize = null;

  document.addEventListener("pointerdown", (e) => {
    const handle = e.target.closest(".resize-handle");
    if (!handle || !state.editMode) return;
    e.preventDefault();
    e.stopPropagation();
    const card = handle.closest("[data-widget-id]");
    const w = state.pinned.find((x) => x.id === card.dataset.widgetId);
    if (!w) return;
    const grid = document.getElementById("pinnedGrid");
    const colWidth = (grid.clientWidth - 11 * GRID_GAP) / 12;
    const rowStep = GRID_ROW + GRID_GAP;
    const col = w.col || spanCol(w.type);
    const rows = w.rows || spanRows(w.type);
    resize = {
      id: w.id, dir: handle.dataset.resizeDir,
      startX: e.clientX, startY: e.clientY,
      col, rows, colWidth, rowStep, card,
    };
    try { handle.setPointerCapture(e.pointerId); } catch (err) {}
    document.body.classList.add("resizing");
  });

  window.addEventListener("pointermove", (e) => {
    if (!resize) return;
    e.preventDefault();
    const dx = e.clientX - resize.startX;
    const dy = e.clientY - resize.startY;
    const w = state.pinned.find((x) => x.id === resize.id);
    if (!w) return;
    if (resize.dir === "e") {
      w.col = clamp(resize.col + Math.round(dx / resize.colWidth), 2, 12);
      resize.card.style.gridColumn = "span " + w.col;
    } else if (resize.dir === "w") {
      w.col = clamp(resize.col - Math.round(dx / resize.colWidth), 2, 12);
      resize.card.style.gridColumn = "span " + w.col;
    } else if (resize.dir === "s") {
      w.rows = clamp(resize.rows + Math.round(dy / resize.rowStep), 1, 8);
      resize.card.style.gridRow = "span " + w.rows;
    }
  });

  window.addEventListener("pointerup", () => {
    if (!resize) return;
    resize = null;
    document.body.classList.remove("resizing");
    renderPinned();
  });

  // queries page composer
  const queryInput = document.getElementById("queryInput");
  const submitQuery = () => {
    const text = queryInput.value.trim();
    if (!text) return;
    sendChat(text);
    queryInput.value = "";
    queryInput.style.height = "auto";
  };
  const autosizeQuery = () => {
    queryInput.style.height = "auto";
    queryInput.style.height = Math.min(queryInput.scrollHeight, 140) + "px";
  };
  document.getElementById("queryBtn").addEventListener("click", submitQuery);
  queryInput.addEventListener("input", autosizeQuery);
  queryInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submitQuery();
    }
  });

  // queries history sidebar
  const queriesSidebar = document.getElementById("queriesSidebar");
  const queriesSidebarExpand = document.getElementById("queriesSidebarExpand");
  document.getElementById("queriesSidebarToggle").addEventListener("click", () => {
    queriesSidebar.classList.add("collapsed");
    queriesSidebarExpand.hidden = false;
  });
  queriesSidebarExpand.addEventListener("click", () => {
    queriesSidebar.classList.remove("collapsed");
    queriesSidebarExpand.hidden = true;
  });
  document.getElementById("queriesDays").addEventListener("click", (e) => {
    const btn = e.target.closest(".queries-day");
    if (!btn) return;
    const sep = document.querySelector(`#queryMessages .msg-day[data-day="${btn.dataset.day}"]`);
    if (sep) sep.scrollIntoView({ behavior: "smooth", block: "start" });
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

  // transactions filters
  ["txSearch", "txCategory", "txType"].forEach((id) => {
    document.getElementById(id).addEventListener("input", renderTransactions);
    document.getElementById(id).addEventListener("change", renderTransactions);
  });
  document.getElementById("txAccount").addEventListener("change", (e) => {
    state.selectedAccount = e.target.value || null;
    syncAccountSelection();
    renderTransactions();
  });

  // privacy toggle
  document.getElementById("privacyToggle").addEventListener("click", togglePrivacy);

  // theme
  document.getElementById("themeSelect").addEventListener("change", (e) => {
    try { localStorage.setItem("glowworm-theme", e.target.value); } catch (err) {}
    applyTheme(e.target.value);
    toast("Theme updated");
  });

  // period
  document.getElementById("periodSelect").addEventListener("change", (e) => {
    state.period = e.target.value;
    if (state.period === "custom") {
      toast("Custom range picker coming soon");
    } else {
      toast("Showing: " + (PERIOD_LABELS[state.period] || state.period));
    }
    renderPeriod();
  });

  // categories form
  document.getElementById("addCategoryBtn").addEventListener("click", openCategoryForm);
  document.getElementById("catSaveBtn").addEventListener("click", saveCategory);
  document.getElementById("catCancelBtn").addEventListener("click", closeCategoryForm);

  // account settings form
  document.getElementById("settingsAddAccountBtn").addEventListener("click", () => openAccountForm(null));
  document.getElementById("accSaveBtn").addEventListener("click", saveAccount);
  document.getElementById("accCancelBtn").addEventListener("click", closeAccountForm);
  document.getElementById("accDeleteBtn").addEventListener("click", () => {
    if (editingAccountId) deleteAccount(editingAccountId);
  });
  document.addEventListener("click", (e) => {
    const edit = e.target.closest("[data-action='edit-account']");
    if (edit) openAccountForm(edit.dataset.accountId);
  });

  // import dropzone
  const dz = document.getElementById("dropzone");
  const importAccountName = () => {
    const sel = document.getElementById("importAccount");
    const acc = state.accounts.find((a) => a.id === sel.value);
    return acc ? acc.name : "selected account";
  };
  document.getElementById("dropzoneBrowse").addEventListener("click", () => document.getElementById("fileInput").click());
  document.getElementById("fileInput").addEventListener("change", () => toast("Importer coming soon — files selected for " + importAccountName()));
  ["dragenter", "dragover"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.add("drag"); }));
  ["dragleave", "drop"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.remove("drag"); }));
  dz.addEventListener("drop", () => toast("Importer coming soon — importing into " + importAccountName()));

  // timeline
  document.getElementById("timelineSlider").addEventListener("input", (e) => renderTimeline(e.target.value));

  // keyboard
  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      switchView("queries");
      setTimeout(() => document.getElementById("queryInput").focus(), 60);
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
