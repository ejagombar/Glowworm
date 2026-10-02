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
  history: HISTORY.map((h) => ({ ...h })),
  wizardStep: 0,
  wizardCats: CATEGORIES.filter((c) => c.id !== "income" && c.id !== "savings").map((c) => ({ ...c })),
  editingId: null,
  period: "last-12m",
  editMode: false,
  dragId: null,
  accounts: ACCOUNTS.map((a) => ({ ...a })),
  selectedAccount: null,
  recurringKey: null,
  balanceView: "all",
  historyMode: "transactions",
  timelineAccount: null,
  timelineIdx: HISTORY_MONTHS.length - 1,
  privacy: false,
  customFrom: null,
  customTo: null,
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
  { label: "List transactions", text: "List my recent transactions" },
];

const TIMELINE_MONTHS = HISTORY_MONTHS;

const MERCHANT_COLORS = ["#0e6f51", "#4f5bc7", "#c9822e", "#c0483f", "#2f7fb0", "#7a58b8", "#2e9d8f", "#6b7280"];

document.addEventListener("DOMContentLoaded", init);

function init() {
  initTheme();
  const periodSel = document.getElementById("periodSelect");
  if (periodSel) periodSel.value = state.period;
  state.chat = [
    { role: "user", text: "List my recent transactions", key: msgKey(), ts: Date.now() - 1000 },
    {
      role: "assistant",
      text: "Here are your most recent transactions.",
      key: msgKey(),
      steps: [
        "Detected a request to list transactions",
        "Assuming all accounts, most recent first",
        "Generated query: SELECT * FROM tx ORDER BY date DESC LIMIT 8",
      ],
      widget: { id: widgetId(), type: "transactions", title: "Recent transactions", data: { transactions: periodFilteredTransactions().slice(0, 8) } },
      ts: Date.now(),
    },
  ];
  renderPinned();
  renderRecent();
  renderTransactions();
  renderCategories();
  renderAccounts();
  renderDashboardTotals();
  renderSettingsAccounts();
  renderSchemas();
  renderHistory();
  renderTimeline(TIMELINE_MONTHS.length - 1);
  setHistoryMode("transactions");
  renderHeroChart();
  syncBalanceCards();
  populateCategoryFilter();
  renderWizard();
  renderChat();
  renderSuggestions();
  wireEvents();
  /* Welcome wizard is disabled for now — code kept for later.
     setTimeout(() => openWizard(), 500); */
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
  renderTransactions();
  renderRecent();
  renderAccounts();
  refreshTransactionWidgets();
  renderChat();
  renderPinned();
  if (state.recurringKey) openRecurring(state.recurringKey);
}
function formatShortDate(iso) {
  if (!iso) return "";
  const d = new Date(iso + "T00:00:00");
  if (isNaN(d)) return iso;
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" });
}
function periodLabel() {
  if (state.period === "custom") {
    if (state.customFrom && state.customTo) return formatShortDate(state.customFrom) + " – " + formatShortDate(state.customTo);
    return "Custom range";
  }
  return PERIOD_LABELS[state.period] || "Last 12 months";
}
function dataNow() {
  if (!TRANSACTIONS || !TRANSACTIONS.length) return new Date();
  let max = "";
  TRANSACTIONS.forEach((t) => { if (t.date && t.date > max) max = t.date; });
  return max ? new Date(max + "T00:00:00") : new Date();
}
function isoDate(d) {
  return d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
}
function periodRange() {
  if (state.period === "all") return null;
  if (state.period === "custom") {
    if (state.customFrom && state.customTo) return { start: state.customFrom, end: state.customTo };
    return null;
  }
  const now = dataNow();
  let start = new Date(now);
  let end = new Date(now);
  if (state.period === "ytd") {
    start = new Date(now.getFullYear(), 0, 1);
  } else if (state.period === "last-12m") {
    start = new Date(now);
    start.setMonth(start.getMonth() - 12);
  } else if (state.period === "last-3m") {
    start = new Date(now);
    start.setMonth(start.getMonth() - 3);
  } else if (state.period === "last-month") {
    start = new Date(now.getFullYear(), now.getMonth() - 1, 1);
    end = new Date(now.getFullYear(), now.getMonth(), 0);
  } else if (state.period === "last-day") {
    start = new Date(now);
    start.setDate(start.getDate() - 1);
  }
  return { start: isoDate(start), end: isoDate(end) };
}
function txInPeriod(t) {
  const r = periodRange();
  if (!r) return true;
  const d = t.date || "";
  return d >= r.start && d <= r.end;
}
function periodFilteredTransactions() {
  return TRANSACTIONS.filter(txInPeriod);
}
function refreshTransactionWidgets() {
  const txs = periodFilteredTransactions().slice(0, 8);
  state.chat.forEach((m) => { if (m.widget && m.widget.type === "transactions") m.widget.data.transactions = txs; });
  state.pinned.forEach((w) => { if (w.type === "transactions") w.data.transactions = txs; });
}
function fmtSignedMoney(v) {
  const n = Number(v) || 0;
  const sign = n < 0 ? "−" : n > 0 ? "+" : "";
  return sign + fmtMoney(n);
}
function openCustomRange() {
  const pop = document.getElementById("customRange");
  pop.hidden = false;
  const from = document.getElementById("customFrom");
  const to = document.getElementById("customTo");
  if (!from.value) {
    const d = dataNow();
    d.setMonth(d.getMonth() - 3);
    from.value = isoDate(d);
  }
  if (!to.value) to.value = isoDate(dataNow());
  from.focus();
}
function applyCustomRange() {
  const from = document.getElementById("customFrom").value;
  const to = document.getElementById("customTo").value;
  if (!from || !to) { toast("Choose a start and end date"); return; }
  state.customFrom = from;
  state.customTo = to;
  state.period = "custom";
  const sel = document.getElementById("periodSelect");
  const opt = sel.querySelector('option[value="custom"]');
  if (opt) opt.textContent = periodLabel();
  document.getElementById("customRange").hidden = true;
  renderPeriod();
  toast("Showing: " + periodLabel());
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
function accountRef(a) {
  if (a.accountNumber) {
    const num = String(a.accountNumber).replace(/\s+/g, "");
    return "···· " + num.slice(-4);
  }
  return a.last4 || "";
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
  const periodTxs = periodFilteredTransactions();
  state.accounts.forEach((a) => {
    const card = document.createElement("div");
    card.className = "account-card";
    card.dataset.accountId = a.id;
    const txCount = periodTxs.filter((t) => t.account === (a.txKey || a.name)).length;
    card.innerHTML = `
      <div class="account-card-top">
        <span class="account-dot" style="background:${a.color}"></span>
        <span class="account-name">${esc(a.name)}</span>
        <span class="badge ${accountTypeBadge(a)}">${esc(accountTypeLabel(a))}</span>
      </div>
      <div class="account-balance ${a.balance < 0 ? "neg" : ""}">${fmtBalance(a.balance)}</div>
      <div class="account-sub">${esc(accountRef(a))} · ${txCount} transactions</div>`;
    card.addEventListener("click", () => {
      state.selectedAccount = state.selectedAccount === a.id ? null : a.id;
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
    sub.textContent = acc ? acc.name + " · statement view" : "All accounts · transfers linked";
  }
}
function renderSettingsAccounts() {
  const wrap = document.getElementById("settingsAccounts");
  wrap.innerHTML = "";
  state.accounts.forEach((a) => {
    const card = document.createElement("div");
    card.className = "settings-account-card";
    const corrections = a.corrections ? a.corrections.length : 0;
    const opened = a.openingDate ? " · opened " + formatShortDate(a.openingDate) : "";
    const corr = corrections ? " · " + corrections + " correction" + (corrections === 1 ? "" : "s") : "";
    card.innerHTML = `
      <div class="settings-account-card-top">
        <span class="account-dot" style="background:${a.color}"></span>
        <div class="settings-account-name">${esc(a.name)}</div>
        <span class="badge ${accountTypeBadge(a)}">${esc(accountTypeLabel(a))}</span>
      </div>
      <div class="settings-account-balance ${a.balance < 0 ? "neg" : ""}">${fmtBalance(a.balance)}</div>
      <div class="settings-account-sub">${esc(accountRef(a))} · ${fmtBalance(a.opening)} at opening${opened}${corr}</div>`;
    card.addEventListener("click", () => openAccountForm(a.id));
    wrap.appendChild(card);
  });
}
let editingAccountId = null;
function syncAccountValuationField() {
  const row = document.getElementById("accValuationRow");
  if (row) row.hidden = document.getElementById("accType").value !== "savings";
}
function openAccountForm(id) {
  editingAccountId = id || null;
  const acc = id ? state.accounts.find((a) => a.id === id) : null;
  document.getElementById("accountModalTitle").textContent = acc ? "Account settings" : "Add account";
  document.getElementById("accountModalSub").textContent = acc ? acc.name : "Add a new account to Glowworm.";
  document.getElementById("accName").value = acc ? acc.name : "";
  document.getElementById("accType").value = acc ? acc.type : "debit";
  document.getElementById("accOpening").value = acc ? (acc.opening != null ? acc.opening : acc.balance) : "";
  document.getElementById("accOpeningDate").value = acc && acc.openingDate ? acc.openingDate : "";
  document.getElementById("accNumber").value = acc && acc.accountNumber ? acc.accountNumber : "";
  document.getElementById("accSortCode").value = acc && acc.sortCode ? acc.sortCode : "";
  document.getElementById("accIban").value = acc && acc.iban ? acc.iban : "";
  document.getElementById("accBic").value = acc && acc.bic ? acc.bic : "";
  document.getElementById("accValuation").value = acc && acc.balance != null ? acc.balance : "";
  document.getElementById("accValuationDate").value = isoDate(new Date());
  syncAccountValuationField();
  document.getElementById("accSaveBtn").textContent = acc ? "Save changes" : "Add account";
  document.getElementById("accDeleteBtn").hidden = !acc;
  document.getElementById("accountModal").hidden = false;
  document.getElementById("accName").focus();
}
function closeAccountForm() {
  document.getElementById("accountModal").hidden = true;
  editingAccountId = null;
}
function saveAccount() {
  const name = document.getElementById("accName").value.trim();
  if (!name) { toast("Account name is required"); return; }
  const type = document.getElementById("accType").value;
  const opening = parseFloat(document.getElementById("accOpening").value) || 0;
  const openingDate = document.getElementById("accOpeningDate").value;
  const accountNumber = document.getElementById("accNumber").value.trim();
  const sortCode = document.getElementById("accSortCode").value.trim();
  const iban = document.getElementById("accIban").value.trim();
  const bic = document.getElementById("accBic").value.trim();
  const valuationInput = type === "savings" ? document.getElementById("accValuation").value : "";
  const valuation = valuationInput === "" ? null : parseFloat(valuationInput);
  const valuationDate = document.getElementById("accValuationDate").value || isoDate(new Date());
  const labels = { debit: "Debit", credit: "Credit", savings: "Savings" };
  const isEdit = !!editingAccountId;
  if (isEdit) {
    const a = state.accounts.find((x) => x.id === editingAccountId);
    if (a) {
      const prevBalance = a.balance;
      a.name = name;
      a.type = type;
      a.typeLabel = labels[type];
      a.opening = opening;
      a.openingDate = openingDate;
      a.accountNumber = accountNumber;
      a.sortCode = sortCode;
      a.iban = iban;
      a.bic = bic;
      if (accountNumber) a.last4 = "···· " + accountNumber.replace(/\s+/g, "").slice(-4);
      if (valuation != null && !isNaN(valuation) && valuation !== a.balance) {
        a.corrections = a.corrections || [];
        a.corrections.push({ date: valuationDate, value: valuation, note: "Manual valuation" });
        a.balance = valuation;
        logAudit({
          title: "Updated account value",
          sub: `${name} · ${formatShortDate(valuationDate)}`,
          diff: [
            { line: `- ${fmtBalance(prevBalance)}`, cls: "diff-del" },
            { line: `+ ${fmtBalance(valuation)}`, cls: "diff-add" },
          ],
        });
      }
    }
  } else {
    const balance = valuation != null && !isNaN(valuation) ? valuation : opening;
    const acc = {
      id: "a" + Date.now(),
      name,
      type,
      typeLabel: labels[type],
      txKey: name,
      balance,
      opening,
      color: MERCHANT_COLORS[state.accounts.length % MERCHANT_COLORS.length],
      last4: accountNumber ? "···· " + accountNumber.replace(/\s+/g, "").slice(-4) : "··· new",
      openingDate,
      accountNumber,
      sortCode,
      iban,
      bic,
      corrections: [],
    };
    if (valuation != null && !isNaN(valuation) && valuation !== opening) {
      acc.corrections.push({ date: valuationDate, value: valuation, note: "Manual valuation" });
    }
    state.accounts.push(acc);
    logAudit({
      title: "Added account",
      sub: `${name} · opening ${fmtBalance(opening)}`,
    });
  }
  closeAccountForm();
  renderAccounts();
  renderSettingsAccounts();
  renderDashboardTotals();
  renderTransactions();
  renderRecent();
  toast(isEdit ? "Account updated" : "Account added");
}
let confirmCallback = null;
function showConfirm(title, message, okLabel, callback) {
  document.getElementById("confirmTitle").textContent = title;
  document.getElementById("confirmMessage").textContent = message;
  document.getElementById("confirmOk").textContent = okLabel || "Delete";
  confirmCallback = callback;
  document.getElementById("confirmModal").hidden = false;
}
function closeConfirm() {
  document.getElementById("confirmModal").hidden = true;
  confirmCallback = null;
}
function deleteAccount(id) {
  const a = state.accounts.find((x) => x.id === id);
  if (!a) return;
  showConfirm("Delete account?", `Delete "${a.name}"? This can't be undone.`, "Delete account", () => {
    state.accounts = state.accounts.filter((x) => x.id !== id);
    if (state.selectedAccount === id) state.selectedAccount = null;
    logAudit({ title: "Deleted account", sub: a.name });
    closeAccountForm();
    renderAccounts();
    renderSettingsAccounts();
    renderDashboardTotals();
    renderTransactions();
    renderRecent();
    toast("Account deleted");
  });
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
  if (name === "history") renderHistory();
  window.scrollTo({ top: 0, behavior: "smooth" });
}

/* ============================== Widgets ============================== */
function spanCol(type) {
  if (type === "number" || type === "text" || type === "pie") return 4;
  if (type === "transactions") return 12;
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
    range.textContent = balanceScopeLabel() + " · " + periodLabel().toLowerCase();
  }
}

/* ============================== Recent activity ============================== */
function renderRecent() {
  const list = document.getElementById("recentTransactions");
  list.innerHTML = "";
  displayRows({}).slice(0, 7).forEach((item) => list.appendChild(txRowEl(item)));
}

function txDescription(t) {
  return t.description || t.merchant || "";
}
function accountNameForKey(key) {
  const a = state.accounts.find((x) => (x.txKey || x.name) === key);
  return a ? a.name : key || "";
}
function txSource(t) {
  if (t.source) return t.source;
  if (t.type === "income") return txDescription(t);
  return t.account;
}
function txDestination(t) {
  if (t.destination) return t.destination;
  if (t.type === "income") return t.account;
  return txDescription(t);
}
function txLinkedPeer(t) {
  if (!t.transferId) return null;
  return TRANSACTIONS.find((x) => x.id !== t.id && x.transferId === t.transferId) || null;
}
function txCounterparty(t) {
  if (t.type === "transfer" && t.transferId) {
    if (t.account === t.source) return "To " + accountNameForKey(t.destination);
    return "From " + accountNameForKey(t.source);
  }
  if (t.amount < 0) return "To " + txDescription(t);
  return "From " + txDescription(t);
}
function passesTxFilters(t, q, cat, type) {
  const hay = (
    txDescription(t) + " " + (t.merchant || "") + " " + (t.category || "") + " " + (t.recurringKey || "") + " " +
    accountNameForKey(t.account) + " " + accountNameForKey(t.source) + " " + accountNameForKey(t.destination)
  ).toLowerCase();
  if (q && !hay.includes(q)) return false;
  if (cat && t.category !== cat) return false;
  if (type) {
    if (type === "recurring" ? !t.recurring : t.type !== type) return false;
  }
  return true;
}
function groupPassesTxFilters(members, q, cat, type) {
  if (cat && !members.some((m) => m.category === cat)) return false;
  if (type) {
    if (type === "recurring" ? !members.some((m) => m.recurring) : !members.some((m) => m.type === type)) return false;
  }
  if (q && !members.some((m) => {
    const hay = (
      txDescription(m) + " " + (m.merchant || "") + " " + (m.category || "") + " " + (m.recurringKey || "") + " " +
      accountNameForKey(m.account) + " " + accountNameForKey(m.source) + " " + accountNameForKey(m.destination)
    ).toLowerCase();
    return hay.includes(q);
  })) return false;
  return true;
}
function displayRows({ accountKey = null, q = "", cat = "", type = "" } = {}) {
  const rows = [];
  const grouped = new Map();
  periodFilteredTransactions().sort((a, b) => b.date.localeCompare(a.date)).forEach((t) => {
    if (accountKey) {
      if (t.account !== accountKey) return;
      if (passesTxFilters(t, q, cat, type)) rows.push({ kind: "single", tx: t });
      return;
    }
    if (t.type === "transfer" && t.transferId) {
      if (!grouped.has(t.transferId)) grouped.set(t.transferId, []);
      grouped.get(t.transferId).push(t);
      return;
    }
    if (passesTxFilters(t, q, cat, type)) rows.push({ kind: "single", tx: t });
  });
  if (!accountKey) {
    grouped.forEach((members) => {
      if (!groupPassesTxFilters(members, q, cat, type)) return;
      const rep = members.find((m) => m.amount < 0) || members[0];
      rows.push({ kind: "linked", members, rep });
    });
  }
  rows.sort((a, b) => (b.rep || b.tx).date.localeCompare((a.rep || a.tx).date));
  return rows;
}

function txRowEl(item) {
  const row = document.createElement("div");
  row.className = "tx-row";
  const t = item.rep || item.tx;
  const linked = item.kind === "linked";
  const cat = CATEGORIES.find((c) => c.name === t.category);
  const color = cat ? cat.color : merchantColor(txDescription(t));
  const iconText = linked || t.type === "transfer" ? "⇄" : initial(txDescription(t));
  const main = linked
    ? `<div class="tx-merchant">${esc(accountNameForKey(t.source))} → ${esc(accountNameForKey(t.destination))}</div>
       <div class="tx-sub">Transfer · ${esc(t.category)} ${linkedBadge()} ${typeBadge(t)}</div>`
    : `<div class="tx-merchant">${esc(txDescription(t))}</div>
       <div class="tx-sub">${esc(t.category)} · ${esc(accountNameForKey(t.account))} ${typeBadge(t)}</div>`;
  const amountCls = linked ? "transfer" : t.type === "transfer" ? "transfer" : t.amount >= 0 ? "pos" : "neg";
  const amountHtml = linked || t.type === "transfer"
    ? `⇄ ${fmtMoney(t.amount)}`
    : `${t.amount >= 0 ? "+" : "−"}${fmtMoney(t.amount)}`;
  row.innerHTML = `
    <div class="tx-icon" style="background:${color}">${esc(iconText)}</div>
    <div class="tx-main">${main}</div>
    <div class="tx-amount ${amountCls}">${amountHtml}</div>`;
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
  if (t && t.recurring) {
    const key = esc(t.recurringKey || txDescription(t));
    return `<button type="button" class="badge badge-recurring badge-btn" data-recurring-key="${key}" title="View all related transactions">Recurring</button>`;
  }
  return "";
}
function linkedBadge() {
  return '<span class="badge badge-linked" title="Matched with the other side of this transfer">Linked</span>';
}
function txAmountClass(t) {
  if (t.type === "transfer") return "transfer";
  if (t.amount >= 0) return "pos";
  return "neg";
}

function renderTransactionsHead(accountView) {
  const head = document.getElementById("transactionsHead");
  const cols = accountView
    ? ["Date", "Description", "Category", "To / From", "Amount", ""]
    : ["Date", "Description", "Category", "Account", "Amount", ""];
  head.innerHTML = `<tr>${cols.map((c) => `<th${c === "Amount" ? ' class="num"' : ""}>${c}</th>`).join("")}</tr>`;
}

function renderTransactions() {
  const q = (document.getElementById("txSearch").value || "").toLowerCase().trim();
  const cat = document.getElementById("txCategory").value;
  const type = document.getElementById("txType").value;
  const acc = state.accounts.find((a) => a.id === state.selectedAccount);
  const accountKey = acc ? (acc.txKey || acc.name) : null;
  const accountView = !!accountKey;

  const table = document.getElementById("transactionsTable");
  table.className = "tx-table" + (accountView ? " account-view" : "");
  renderTransactionsHead(accountView);

  const items = displayRows({ accountKey, q, cat, type });
  const body = document.getElementById("transactionsBody");
  body.innerHTML = "";

  items.forEach((item) => {
    const linked = item.kind === "linked";
    const t = item.rep || item.tx;
    const catObj = CATEGORIES.find((c) => c.name === t.category);
    const tr = document.createElement("tr");
    tr.dataset.txId = t.id;
    if (linked) tr.classList.add("linked");

    const badges = `${linked ? linkedBadge() : ""} ${typeBadge(t)}`.trim();
    const accountCell = accountView
      ? `<td class="cell-sub">${esc(txCounterparty(t))}</td>`
      : `<td>${linked ? `${esc(accountNameForKey(t.source))} → ${esc(accountNameForKey(t.destination))}` : esc(accountNameForKey(t.account))}</td>`;
    const amountCell = linked || t.type === "transfer"
      ? `<td class="num tx-amount transfer">⇄ ${fmtMoney(t.amount)}</td>`
      : `<td class="num tx-amount ${txAmountClass(t)}">${t.amount >= 0 ? "+" : "−"}${fmtMoney(t.amount)}</td>`;

    tr.innerHTML = `
      <td class="cell-sub">${esc(t.date.slice(8))} ${monthName(t.date.slice(5, 7))}</td>
      <td><div class="cell-merchant">${esc(txDescription(t))} ${badges}</div></td>
      <td><span class="category-dot" style="background:${catObj ? catObj.color : "#999"}"></span> ${esc(t.category)}</td>
      ${accountCell}
      ${amountCell}
      <td class="row-actions"><button class="widget-btn" data-edit-tx="${t.id}" title="Edit transaction" aria-label="Edit ${esc(txDescription(t))}"><svg class="icon"><use href="#i-edit"/></svg></button></td>`;
    body.appendChild(tr);
  });

  if (!items.length) {
    body.innerHTML = `<tr><td colspan="6" class="cell-sub" style="padding:28px;text-align:center">No transactions match your filters.</td></tr>`;
  }
}
function monthName(mm) {
  return ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"][Number(mm) - 1] || "";
}

/* ---------- Inline transaction editor ---------- */
function closeTxEditor() {
  const open = document.getElementById("txEditorRow");
  if (open) open.remove();
  document.querySelectorAll("#transactionsBody .editing").forEach((r) => r.classList.remove("editing"));
}

function openTxEditor(id) {
  const t = TRANSACTIONS.find((x) => x.id === id);
  if (!t) return;
  closeTxEditor();
  const row = document.querySelector(`#transactionsBody tr[data-tx-id="${id}"]`);
  if (!row) return;
  row.classList.add("editing");

  const linked = !!t.transferId;
  const catOpts = linked
    ? `<option value="Internal transfer" selected>Internal transfer</option>`
    : '<option value="">Select category…</option>' + CATEGORIES
        .filter((c) => c.id !== "savings")
        .map((c) => `<option value="${esc(c.name)}"${c.name === t.category ? " selected" : ""}>${esc(c.name)}</option>`)
        .join("");

  const accounts = state.accounts.map((a) => ({ value: a.txKey || a.name, label: a.name }));
  if (!accounts.some((a) => a.value === t.account)) {
    accounts.push({ value: t.account, label: accountNameForKey(t.account) || t.account });
  }
  const accOpts = linked
    ? `<option value="${esc(t.account)}" selected>${esc(accountNameForKey(t.account) || t.account)}</option>`
    : accounts
        .map((a) => `<option value="${esc(a.value)}"${a.value === t.account ? " selected" : ""}>${esc(a.label)}</option>`)
        .join("");

  const editor = document.createElement("tr");
  editor.id = "txEditorRow";
  editor.className = "tx-editor-row";
  editor.innerHTML = `<td colspan="6">
    <div class="tx-editor" data-tx-id="${esc(id)}">
      <label class="tx-field">Date
        <input class="field" type="date" id="txeDate" value="${esc(t.date)}" />
      </label>
      <label class="tx-field grow">Description
        <input class="field" type="text" id="txeDescription" value="${esc(txDescription(t))}" placeholder="e.g. Tesco Express" />
      </label>
      <label class="tx-field">Category
        <select class="field" id="txeCategory"${linked ? " disabled" : ""}>${catOpts}</select>
      </label>
      <label class="tx-field">Account
        <select class="field" id="txeAccount"${linked ? " disabled" : ""}>${accOpts}</select>
      </label>
      <label class="tx-field">Amount
        <input class="field" type="number" step="0.01" id="txeAmount" value="${t.amount}" />
      </label>
      <div class="tx-editor-actions">
        <button class="btn btn-primary btn-xs" id="txeSave">Save</button>
        <button class="btn btn-ghost btn-xs" id="txeCancel">Cancel</button>
      </div>
    </div>
  </td>`;
  row.after(editor);
  document.getElementById("txeDescription").focus();
}

function saveTxEditor() {
  const editor = document.querySelector(".tx-editor");
  if (!editor) return;
  const t = TRANSACTIONS.find((x) => x.id === editor.dataset.txId);
  if (!t) return;
  const date = document.getElementById("txeDate").value;
  const description = document.getElementById("txeDescription").value.trim();
  const category = document.getElementById("txeCategory").value;
  const account = document.getElementById("txeAccount").value;
  const amount = parseFloat(document.getElementById("txeAmount").value);

  if (!date) { toast("Date is required"); return; }
  if (!description) { toast("Description is required"); return; }
  if (isNaN(amount)) { toast("Amount is required"); return; }

  t.date = date;
  t.merchant = description;
  t.description = description;
  t.account = account;
  t.amount = amount;

  if (t.transferId) {
    /* Transfers are detected and linked automatically — keep the link intact. */
    const peer = txLinkedPeer(t);
    t.category = "Internal transfer";
    t.type = "transfer";
    if (peer) {
      const outbound = t.amount < 0;
      const source = outbound ? account : peer.account;
      const destination = outbound ? peer.account : account;
      t.source = source;
      t.destination = destination;
      peer.type = "transfer";
      peer.source = source;
      peer.destination = destination;
      peer.category = "Internal transfer";
    }
  } else {
    t.category = category || "Uncategorised";
    if (category === "Income") t.type = "income";
    else t.type = "expense";
    t.source = t.type === "income" ? description : account;
    t.destination = t.type === "income" ? account : description;
    delete t.transferId;
  }

  closeTxEditor();
  renderTransactions();
  renderRecent();
  renderAccounts();
  toast("Transaction updated");
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

/* ============================== Recurring series ============================== */
function recurringRowEl(item) {
  const linked = item.kind === "linked";
  const t = item.rep || item.tx;
  const row = document.createElement("div");
  row.className = "tx-row recurring-row";
  const cat = CATEGORIES.find((c) => c.name === t.category);
  const color = cat ? cat.color : merchantColor(txDescription(t));
  const iconText = linked || t.type === "transfer" ? "⇄" : initial(txDescription(t));
  const main = linked
    ? `<div class="tx-merchant">${esc(accountNameForKey(t.source))} → ${esc(accountNameForKey(t.destination))}</div>
       <div class="tx-sub">Transfer · ${esc(t.category)} ${linkedBadge()}</div>`
    : `<div class="tx-merchant">${esc(txDescription(t))}</div>
       <div class="tx-sub">${esc(t.category)} · ${esc(accountNameForKey(t.account))}</div>`;
  const amountCls = linked ? "transfer" : t.type === "transfer" ? "transfer" : t.amount >= 0 ? "pos" : "neg";
  const amountHtml = linked || t.type === "transfer"
    ? `⇄ ${fmtMoney(t.amount)}`
    : `${t.amount >= 0 ? "+" : "−"}${fmtMoney(t.amount)}`;
  row.innerHTML = `
    <div class="tx-icon" style="background:${color}">${esc(iconText)}</div>
    <div class="tx-main">${main}</div>
    <div class="tx-date">${esc(formatShortDate(t.date))}</div>
    <div class="tx-amount ${amountCls}">${amountHtml}</div>`;
  return row;
}

function openRecurring(key) {
  state.recurringKey = key;
  const txs = periodFilteredTransactions()
    .filter((t) => (t.recurringKey || txDescription(t)) === key)
    .sort((a, b) => a.date.localeCompare(b.date));

  const isTransferSeries = txs.length > 0 && txs.every((t) => t.type === "transfer" && t.transferId);
  const items = [];
  let total = 0;

  if (isTransferSeries) {
    const grouped = new Map();
    txs.forEach((t) => {
      if (!grouped.has(t.transferId)) grouped.set(t.transferId, []);
      grouped.get(t.transferId).push(t);
    });
    grouped.forEach((members) => {
      const rep = members.find((m) => m.amount < 0) || members[0];
      items.push({ kind: "linked", members, rep });
      total += rep.amount;
    });
    items.sort((a, b) => a.rep.date.localeCompare(b.rep.date));
  } else {
    txs.forEach((t) => {
      items.push({ kind: "single", tx: t });
      total += t.amount;
    });
  }

  document.getElementById("recurringTitle").textContent = key;
  const unit = isTransferSeries
    ? (items.length === 1 ? "transfer" : "transfers")
    : (items.length === 1 ? "transaction" : "transactions");
  document.getElementById("recurringSub").textContent =
    items.length + " " + unit + " · " + periodLabel().toLowerCase() + " · auto-detected series";

  const totalEl = document.getElementById("recurringTotal");
  const totalLabelEl = document.getElementById("recurringTotalLabel");
  if (isTransferSeries) {
    totalEl.textContent = fmtMoney(Math.abs(total));
    totalLabelEl.textContent = "Total transferred";
  } else {
    totalEl.textContent = fmtSignedMoney(total);
    totalLabelEl.textContent = total < 0 ? "Total spent" : total > 0 ? "Total received" : "Total";
  }

  const list = document.getElementById("recurringTxList");
  list.innerHTML = "";
  items.forEach((item) => list.appendChild(recurringRowEl(item)));
  document.getElementById("recurringModal").hidden = false;
}
function closeRecurring() {
  document.getElementById("recurringModal").hidden = true;
  state.recurringKey = null;
}

/* ============================== Categories ============================== */
let editingCategoryId = null;
let selectedCatColor = null;

function renderCategories() {
  const grid = document.getElementById("categoriesGrid");
  grid.innerHTML = "";
  CATEGORIES.forEach((c) => {
    const card = document.createElement("div");
    card.className = "category-card";
    const isSystem = c.id === "income" || c.id === "savings";
    const isFixed = c.id === "savings"; /* internal transfer: fully system-managed */
    const badge = isSystem ? "System" : "";
    const sub = c.id === "income" ? "Incoming" : c.id === "savings" ? "Between your own accounts" : "";
    card.innerHTML = `
      <div class="cat-top">
        <span class="category-dot" style="background:${c.color}"></span>
        <span class="cat-name">${esc(c.name)}</span>
        ${badge ? `<span class="cat-badge">${badge}</span>` : ""}
        ${isFixed ? "" : `<button class="icon-btn cat-edit" data-edit-cat="${c.id}" aria-label="Edit ${esc(c.name)}" title="Edit category">
          <svg class="icon"><use href="#i-edit"/></svg>
        </button>`}
      </div>
      <div class="cat-amount">${c.spent != null ? fmtMoney(c.spent) : "—"}</div>
      ${sub ? `<div class="cat-sub">${sub}</div>` : ""}`;
    grid.appendChild(card);
  });
}

function renderCatColors() {
  const wrap = document.getElementById("catColors");
  wrap.innerHTML = MERCHANT_COLORS
    .map((col) => `<button type="button" class="cat-color${col === selectedCatColor ? " selected" : ""}" style="background:${col}" data-cat-color="${col}" aria-label="Colour ${col}"></button>`)
    .join("");
}

function isSystemCategory(cat) {
  return !!cat && (cat.id === "income" || cat.id === "savings");
}

function openCategoryForm() {
  editingCategoryId = null;
  const form = document.getElementById("categoryForm");
  form.hidden = false;
  document.getElementById("catFormTitle").textContent = "New category";
  document.getElementById("catFormNote").hidden = true;
  const nameEl = document.getElementById("catName");
  nameEl.disabled = false;
  nameEl.value = "";
  selectedCatColor = MERCHANT_COLORS[CATEGORIES.length % MERCHANT_COLORS.length];
  renderCatColors();
  document.getElementById("catDeleteBtn").hidden = true;
  document.getElementById("catSaveBtn").textContent = "Add category";
  nameEl.focus();
}
function openCategoryEdit(id) {
  const cat = CATEGORIES.find((c) => c.id === id);
  if (!cat) return;
  editingCategoryId = id;
  const isSystem = isSystemCategory(cat);
  const form = document.getElementById("categoryForm");
  form.hidden = false;
  document.getElementById("catFormTitle").textContent = isSystem ? "Edit system category" : "Edit category";
  document.getElementById("catFormNote").hidden = !isSystem;
  const nameEl = document.getElementById("catName");
  nameEl.disabled = isSystem;   /* system categories can't be renamed */
  nameEl.value = cat.name;
  selectedCatColor = cat.color;
  renderCatColors();
  document.getElementById("catDeleteBtn").hidden = isSystem;
  document.getElementById("catSaveBtn").textContent = "Save changes";
  if (isSystem) document.getElementById("catSaveBtn").focus();
  else nameEl.focus();
}
function closeCategoryForm() {
  document.getElementById("categoryForm").hidden = true;
  editingCategoryId = null;
}
function refreshAfterCategoryChange() {
  renderCategories();
  populateCategoryFilter();
  renderTransactions();
  renderRecent();
}
function saveCategory() {
  const name = document.getElementById("catName").value.trim();
  if (!name) { toast("Category name is required"); return; }
  const isEdit = !!editingCategoryId;
  const clash = CATEGORIES.find((c) => c.name.toLowerCase() === name.toLowerCase() && c.id !== editingCategoryId);
  if (clash) { toast("A category with that name already exists"); return; }
  if (isEdit) {
    const cat = CATEGORIES.find((c) => c.id === editingCategoryId);
    if (cat) {
      if (!isSystemCategory(cat)) {
        if (cat.name !== name) {
          TRANSACTIONS.forEach((t) => { if (t.category === cat.name) t.category = name; });
        }
        cat.name = name;
      }
      cat.color = selectedCatColor || cat.color;
    }
    toast(isSystemCategory(cat) ? "Colour updated" : "Category updated");
  } else {
    CATEGORIES.push({
      id: "c" + Date.now(),
      name,
      color: selectedCatColor || MERCHANT_COLORS[CATEGORIES.length % MERCHANT_COLORS.length],
      spent: 0,
    });
    toast("Category added");
  }
  closeCategoryForm();
  refreshAfterCategoryChange();
}
function deleteCategory() {
  const idx = CATEGORIES.findIndex((c) => c.id === editingCategoryId);
  if (idx < 0) return;
  const cat = CATEGORIES[idx];
  if (cat.id === "income" || cat.id === "savings") { toast("System categories can't be deleted"); return; }
  if (!window.confirm(`Delete "${cat.name}"? Its transactions will be marked as Uncategorised.`)) return;
  TRANSACTIONS.forEach((t) => { if (t.category === cat.name) t.category = "Uncategorised"; });
  CATEGORIES.splice(idx, 1);
  closeCategoryForm();
  refreshAfterCategoryChange();
  toast("Category deleted");
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
function logAudit(entry) {
  state.history.unshift({
    id: Date.now(),
    time: "Just now",
    actor: "You",
    kind: "you",
    ...entry,
  });
  if (state.view === "history") renderHistory();
}
function renderHistory() {
  const list = document.getElementById("historyList");
  list.innerHTML = "";
  const f = state.historyFilter;
  const rows = state.history.filter((h) => (f === "all" ? true : h.kind === f));
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
    const on = c.dataset.historyMode === mode;
    c.classList.toggle("active", on);
    c.setAttribute("aria-selected", on);
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
  const n = TIMELINE_MONTHS.length;
  const chip = document.getElementById("timelineChip");
  const monthsBack = n - 1 - idx;
  chip.textContent = monthsBack === 0 ? "Latest" : monthsBack === 1 ? "1 month back" : monthsBack + " months back";
  const slider = document.getElementById("timelineSlider");
  slider.min = 0;
  slider.max = n - 1;
  slider.value = idx;
  slider.style.setProperty("--fill", (n > 1 ? (idx / (n - 1)) * 100 : 0) + "%");
  /* Tick labels: at most 7, evenly spread — stays readable with years of data */
  const count = Math.min(n, 7);
  const denom = count > 1 ? count - 1 : 1;
  let ticks = "";
  for (let i = 0; i < count; i++) {
    const tIdx = Math.round((i * (n - 1)) / denom);
    const pos = n > 1 ? (tIdx / (n - 1)) * 100 : 0;
    const cls = i === 0 ? " first" : i === count - 1 ? " last" : "";
    ticks += `<div class="timeline-tick${cls}" style="left:${pos}%"><span>${TIMELINE_MONTHS[tIdx].short}</span></div>`;
  }
  document.getElementById("timelineTicks").innerHTML = ticks;
  document.getElementById("timelineBalance").textContent = fmtMoney(BALANCE_SERIES.values[idx]);
  document.getElementById("timelineTxCount").textContent = TRANSACTIONS.filter((t) => t.date.slice(0, 7) <= m.key).length;

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

function chatWidgetHTML(widget) {
  const el = widgetEl(widget, { span: "span-12", pinnable: true });
  if (state.pinned.some((w) => w.id === widget.id)) {
    el.querySelector("[data-action='toggle-pin']")?.classList.add("pinned-on");
  }
  return el.outerHTML;
}

/* Chat render tracking: stable keys so re-renders don't replay entrance animations */
let renderedChatKeys = new Set();
let revealChatKey = null;
let msgSeq = 0;
function msgKey() { return "m" + (++msgSeq) + "-" + Math.random().toString(36).slice(2, 7); }

function chatMessageEl(m) {
  const el = document.createElement("div");
  el.className = "msg " + (m.role === "user" ? "user" : "assistant");
  if (m.key && (m.key === revealChatKey || !renderedChatKeys.has(m.key))) el.classList.add("msg-in");

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
  state.chat.forEach((m) => { if (m.key) renderedChatKeys.add(m.key); });
  revealChatKey = null;
  box.scrollTop = box.scrollHeight;
  renderDays();
}

function sendChat(text) {
  text = (text || "").trim();
  if (!text) return;
  state.chat.push({ role: "user", text, key: msgKey(), ts: Date.now() });
  state.chat.push({ role: "assistant", pending: true, key: msgKey(), ts: Date.now() });
  renderChat();

  const resp = QUERY_RESPONSES.find((r) => r.match.test(text)) || DEFAULT_RESPONSE;
  setTimeout(() => {
    const finish = () => {
      const idx = state.chat.findIndex((m) => m.pending);
      if (idx < 0) return;
      const widget = JSON.parse(JSON.stringify(resp.widget));
      if (widget.type === "transactions") {
        widget.data.transactions = periodFilteredTransactions().slice(0, 8);
      }
      state.chat[idx] = {
        role: "assistant",
        text: resp.answer,
        steps: resp.steps,
        widget: { id: widgetId(), ...widget },
        key: state.chat[idx].key,
        ts: Date.now(),
      };
      revealChatKey = state.chat[idx].key;
      renderChat();
    };
    /* Let the worm wriggle out first instead of vanishing, then reveal the answer */
    const loader = document.querySelector("#queryMessages .glowworm-loader");
    if (loader && !matchMedia("(prefers-reduced-motion: reduce)").matches) {
      loader.classList.add("worm-out");
      setTimeout(finish, 320);
    } else {
      finish();
    }
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
      state.wizardCats.push({ id: "c" + Date.now(), name: name.trim(), color: MERCHANT_COLORS[state.wizardCats.length % MERCHANT_COLORS.length], spent: 0 });
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

  // inline transaction editor
  document.getElementById("transactionsBody").addEventListener("click", (e) => {
    const editBtn = e.target.closest("[data-edit-tx]");
    if (editBtn) {
      const open = document.querySelector(".tx-editor");
      if (open && open.dataset.txId === editBtn.dataset.editTx) closeTxEditor();
      else openTxEditor(editBtn.dataset.editTx);
      return;
    }
    if (e.target.closest("#txeCancel")) { closeTxEditor(); return; }
    if (e.target.closest("#txeSave")) { saveTxEditor(); return; }
  });

  // clickable recurring tags (table and recent activity)
  document.addEventListener("click", (e) => {
    const rec = e.target.closest("[data-recurring-key]");
    if (rec) openRecurring(rec.dataset.recurringKey);
  });

  // brand mark wiggle (no navigation)
  let brandWiggleTimer;
  document.getElementById("brandMark").addEventListener("click", (e) => {
    e.preventDefault();
    const b = document.getElementById("brandMark");
    b.classList.remove("wiggle");
    void b.offsetWidth;
    b.classList.add("wiggle");
    clearTimeout(brandWiggleTimer);
    brandWiggleTimer = setTimeout(() => b.classList.remove("wiggle"), 1000);
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
    if (e.target.value === "custom") {
      openCustomRange();
    } else {
      state.period = e.target.value;
      document.getElementById("customRange").hidden = true;
      toast("Showing: " + periodLabel());
      renderPeriod();
    }
  });
  document.getElementById("customApply").addEventListener("click", applyCustomRange);

  // categories form
  document.getElementById("addCategoryBtn").addEventListener("click", openCategoryForm);
  document.getElementById("catSaveBtn").addEventListener("click", saveCategory);
  document.getElementById("catCancelBtn").addEventListener("click", closeCategoryForm);
  document.getElementById("catDeleteBtn").addEventListener("click", deleteCategory);
  document.getElementById("categoriesGrid").addEventListener("click", (e) => {
    const btn = e.target.closest("[data-edit-cat]");
    if (btn) openCategoryEdit(btn.dataset.editCat);
  });
  document.getElementById("catColors").addEventListener("click", (e) => {
    const sw = e.target.closest("[data-cat-color]");
    if (!sw) return;
    selectedCatColor = sw.dataset.catColor;
    document.querySelectorAll("#catColors .cat-color").forEach((el) => {
      el.classList.toggle("selected", el.dataset.catColor === selectedCatColor);
    });
  });

  // account settings form
  document.getElementById("settingsAddAccountBtn").addEventListener("click", () => openAccountForm(null));
  document.getElementById("accType").addEventListener("change", syncAccountValuationField);
  document.getElementById("accSaveBtn").addEventListener("click", saveAccount);
  document.getElementById("accCancelBtn").addEventListener("click", closeAccountForm);
  document.getElementById("accDeleteBtn").addEventListener("click", () => {
    if (editingAccountId) deleteAccount(editingAccountId);
  });

  // import dropzone
  const dz = document.getElementById("dropzone");
  document.getElementById("dropzoneBrowse").addEventListener("click", () => document.getElementById("fileInput").click());
  document.getElementById("fileInput").addEventListener("change", () => toast("Importer coming soon — files selected"));
  ["dragenter", "dragover"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.add("drag"); }));
  ["dragleave", "drop"].forEach((ev) => dz.addEventListener(ev, (e) => { e.preventDefault(); dz.classList.remove("drag"); }));
  dz.addEventListener("drop", () => toast("Importer coming soon — the AI will detect the account"));

  // timeline
  document.getElementById("timelineSlider").addEventListener("input", (e) => renderTimeline(e.target.value));

  // keyboard
  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      switchView("queries");
      setTimeout(() => document.getElementById("queryInput").focus(), 60);
    }
    if (!e.ctrlKey && !e.metaKey && !e.altKey && /^[0-6]$/.test(e.key)) {
      const editable = e.target && e.target.closest ? e.target.closest("input, textarea, select, [contenteditable]") : null;
      if (!editable) {
        const views = {
          "1": "dashboard",
          "2": "queries",
          "3": "transactions",
          "4": "categories",
          "5": "import",
          "6": "history",
          "0": "settings",
        };
        switchView(views[e.key]);
      }
    }
    if (e.key === "Escape") {
      if (!document.getElementById("confirmModal").hidden) {
        closeConfirm();
        return;
      }
      closeWizard();
      closeDrawer();
      closeRecurring();
      closeAccountForm();
    }
  });

  // recurring series modal backdrop
  document.getElementById("recurringModal").addEventListener("click", (e) => {
    if (e.target.id === "recurringModal") closeRecurring();
  });

  // account modal backdrop
  document.getElementById("accountModal").addEventListener("click", (e) => {
    if (e.target.id === "accountModal") closeAccountForm();
  });

  // confirm modal
  document.getElementById("confirmOk").addEventListener("click", () => {
    if (confirmCallback) confirmCallback();
    closeConfirm();
  });
  document.getElementById("confirmCancel").addEventListener("click", closeConfirm);
  document.getElementById("confirmModal").addEventListener("click", (e) => {
    if (e.target.id === "confirmModal") closeConfirm();
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
  if (action === "close-recurring") closeRecurring();
  if (action === "close-account") closeAccountForm();
  if (action === "close-confirm") closeConfirm();
}
