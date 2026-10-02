/* ============================================================
   Glowworm — mock data (UI skeleton only)
   No AI or importers yet: everything below is sample content.
   ============================================================ */

const CATEGORIES = [
  { id: "transport", name: "Transport", color: "#4f5bc7", budget: 150, spent: 128.4 },
  { id: "food", name: "Food & Dining", color: "#c9822e", budget: 300, spent: 246.1 },
  { id: "bills", name: "Bills & Utilities", color: "#c0483f", budget: 400, spent: 382.0 },
  { id: "entertainment", name: "Entertainment", color: "#7a58b8", budget: 100, spent: 64.3 },
  { id: "shopping", name: "Shopping", color: "#2f7fb0", budget: 200, spent: 142.8 },
  { id: "health", name: "Health", color: "#0e7a4d", budget: 80, spent: 21.5 },
  { id: "income", name: "Income", color: "#0e7a4d", budget: null, spent: null },
  { id: "savings", name: "Internal transfer", color: "#2e9d8f", budget: null, spent: null },
];

const RECENT_TX = [
  { date: "2024-06-01", merchant: "Acme Ltd — Salary", description: "Acme Ltd — Salary", category: "Income", account: "Current", source: "Acme Ltd — Salary", destination: "Current", amount: 2450.00, type: "income", recurring: true, recurringKey: "Acme Ltd — Salary" },
  { date: "2024-06-01", merchant: "Transfer to Savings", description: "Transfer to Savings", category: "Internal transfer", account: "Current", source: "Current", destination: "Savings", amount: -500.00, type: "transfer", recurring: true, recurringKey: "Transfer to Savings", transferId: "tr-2024-06-01-current-savings" },
  { date: "2024-06-01", merchant: "Transfer from Current", description: "Transfer from Current", category: "Internal transfer", account: "Savings", source: "Current", destination: "Savings", amount: 500.00, type: "transfer", recurring: true, recurringKey: "Transfer to Savings", transferId: "tr-2024-06-01-current-savings" },
  { date: "2024-06-02", merchant: "Tesco Express", description: "Tesco Express", category: "Food & Dining", account: "Current", source: "Current", destination: "Tesco Express", amount: -42.35, type: "expense" },
  { date: "2024-06-03", merchant: "Octopus Energy", description: "Octopus Energy", category: "Bills & Utilities", account: "Current", source: "Current", destination: "Octopus Energy", amount: -86.20, type: "expense", recurring: true, recurringKey: "Octopus Energy" },
  { date: "2024-06-04", merchant: "Transport for London", description: "Transport for London", category: "Transport", account: "Current", source: "Current", destination: "Transport for London", amount: -12.50, type: "expense" },
  { date: "2024-06-05", merchant: "Netflix", description: "Netflix", category: "Entertainment", account: "Current", source: "Current", destination: "Netflix", amount: -10.99, type: "expense", recurring: true, recurringKey: "Netflix" },
  { date: "2024-06-06", merchant: "Pret a Manger", description: "Pret a Manger", category: "Food & Dining", account: "Current", source: "Current", destination: "Pret a Manger", amount: -8.75, type: "expense" },
  { date: "2024-06-07", merchant: "Amazon", description: "Amazon", category: "Shopping", account: "Current", source: "Current", destination: "Amazon", amount: -34.99, type: "expense" },
  { date: "2024-06-08", merchant: "Boots Pharmacy", description: "Boots Pharmacy", category: "Health", account: "Current", source: "Current", destination: "Boots Pharmacy", amount: -21.50, type: "expense" },
  { date: "2024-06-09", merchant: "Spotify", description: "Spotify", category: "Entertainment", account: "Current", source: "Current", destination: "Spotify", amount: -11.99, type: "expense", recurring: true, recurringKey: "Spotify" },
  { date: "2024-06-10", merchant: "Uber", description: "Uber", category: "Transport", account: "Current", source: "Current", destination: "Uber", amount: -18.40, type: "expense" },
  { date: "2024-06-10", merchant: "Payment to Credit card", description: "Payment to Credit card", category: "Internal transfer", account: "Current", source: "Current", destination: "Credit card", amount: -120.40, type: "transfer", recurring: true, recurringKey: "Credit card payment", transferId: "tr-2024-06-10-current-creditcard" },
  { date: "2024-06-10", merchant: "Payment from Current", description: "Payment from Current", category: "Internal transfer", account: "Credit card", source: "Current", destination: "Credit card", amount: 120.40, type: "transfer", recurring: true, recurringKey: "Credit card payment", transferId: "tr-2024-06-10-current-creditcard" },
  { date: "2024-06-11", merchant: "Waterstones", description: "Waterstones", category: "Shopping", account: "Current", source: "Current", destination: "Waterstones", amount: -24.00, type: "expense" },
  { date: "2024-06-12", merchant: "Vodafone", description: "Vodafone", category: "Bills & Utilities", account: "Current", source: "Current", destination: "Vodafone", amount: -45.00, type: "expense", recurring: true, recurringKey: "Vodafone" },
  { date: "2024-06-13", merchant: "Wagamama", description: "Wagamama", category: "Food & Dining", account: "Current", source: "Current", destination: "Wagamama", amount: -32.80, type: "expense" },
  { date: "2024-06-14", merchant: "Transport for London", description: "Transport for London", category: "Transport", account: "Current", source: "Current", destination: "Transport for London", amount: -9.60, type: "expense" },
  { date: "2024-06-15", merchant: "Zara", description: "Zara", category: "Shopping", account: "Current", source: "Current", destination: "Zara", amount: -83.81, type: "expense" },
];

/* ============================================================
   Simulated history — 30 months (Jan 2022 → Jun 2024).
   Generated deterministically (seeded) so the demo data is
   stable across reloads, and ends on today's balances.
   ============================================================ */

function seededRng(seed) {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 4294967296;
  };
}

const HISTORY_MONTHS = (() => {
  const long = ["January","February","March","April","May","June","July","August","September","October","November","December"];
  const sh = ["Jan","Feb","Mar","Apr","May","Jun","Jul","Aug","Sep","Oct","Nov","Dec"];
  const months = [];
  for (let i = 0; i < 30; i++) {
    const d = new Date(2022, i, 1);
    const y = d.getFullYear();
    const mo = d.getMonth();
    months.push({
      key: y + "-" + String(mo + 1).padStart(2, "0"),
      short: sh[mo] + " " + String(y).slice(2),
      date: "1 " + long[mo] + " " + y,
    });
  }
  return months;
})();

/* Total balance across accounts, ending on today's £8,412.66 */
const BALANCE_SERIES = (() => {
  const rng = seededRng(31);
  const raw = [1850];
  for (let i = 1; i < HISTORY_MONTHS.length; i++) {
    let delta = 230 + (rng() - 0.6) * 420;          /* salary less spending */
    if (i % 12 === 11) delta -= 170;               /* December splurge */
    if (i % 12 === 2 || i % 12 === 5) delta -= 80; /* spring dips */
    raw.push(raw[i - 1] + Math.max(delta, -260));
  }
  const n = raw.length;
  const drift = 8412.66 - raw[n - 1];
  return {
    labels: HISTORY_MONTHS.map((m) => m.short),
    values: raw.map((v, i) => Math.round((v + drift * (i / (n - 1))) * 100) / 100),
  };
})();

/* Savings balance, ending on today's £4,500 */
const SAVINGS_BALANCE_SERIES = (() => {
  const rng = seededRng(29);
  const raw = [280];
  for (let i = 1; i < HISTORY_MONTHS.length; i++) {
    let delta = rng() < 0.12 ? 0 : 110 + (rng() - 0.4) * 70;
    if (i % 12 === 11) delta *= 0.4;
    raw.push(raw[i - 1] + delta);
  }
  const n = raw.length;
  const drift = 4500 - raw[n - 1];
  return {
    labels: HISTORY_MONTHS.map((m) => m.short),
    values: raw.map((v, i) => Math.round((v + drift * (i / (n - 1))) * 100) / 100),
  };
})();

/* Historic transactions — salary, occasional savings transfer and a
   few everyday purchases per month, on top of the recent hand-written set */
const TX_POOL = [
  { merchant: "Tesco Express", category: "Food & Dining", account: "Current", amount: -42.35, type: "expense" },
  { merchant: "Octopus Energy", category: "Bills & Utilities", account: "Current", amount: -86.2, type: "expense", recurring: true },
  { merchant: "Transport for London", category: "Transport", account: "Current", amount: -12.5, type: "expense" },
  { merchant: "Netflix", category: "Entertainment", account: "Current", amount: -10.99, type: "expense", recurring: true },
  { merchant: "Pret a Manger", category: "Food & Dining", account: "Current", amount: -8.75, type: "expense" },
  { merchant: "Amazon", category: "Shopping", account: "Current", amount: -34.99, type: "expense" },
  { merchant: "Boots Pharmacy", category: "Health", account: "Current", amount: -21.5, type: "expense" },
  { merchant: "Spotify", category: "Entertainment", account: "Current", amount: -11.99, type: "expense", recurring: true },
  { merchant: "Uber", category: "Transport", account: "Current", amount: -18.4, type: "expense" },
  { merchant: "Waterstones", category: "Shopping", account: "Current", amount: -24.0, type: "expense" },
  { merchant: "Vodafone", category: "Bills & Utilities", account: "Current", amount: -45.0, type: "expense", recurring: true },
  { merchant: "Wagamama", category: "Food & Dining", account: "Current", amount: -32.8, type: "expense" },
  { merchant: "Zara", category: "Shopping", account: "Current", amount: -83.81, type: "expense" },
];

const TRANSACTIONS = (() => {
  const rng = seededRng(3);
  const rows = [];
  HISTORY_MONTHS.slice(0, -1).forEach((m) => {
    rows.push({ date: m.key + "-01", merchant: "Acme Ltd — Salary", description: "Acme Ltd — Salary", category: "Income", account: "Current", source: "Acme Ltd — Salary", destination: "Current", amount: 2400 + Math.round(rng() * 100), type: "income", recurring: true, recurringKey: "Acme Ltd — Salary" });
    if (rng() < 0.55) {
      const transferAmount = 400 + Math.round(rng() * 200);
      const transferId = "tr-" + m.key + "-current-savings";
      rows.push({ date: m.key + "-02", merchant: "Transfer to Savings", description: "Transfer to Savings", category: "Internal transfer", account: "Current", source: "Current", destination: "Savings", amount: -transferAmount, type: "transfer", recurring: true, recurringKey: "Transfer to Savings", transferId });
      rows.push({ date: m.key + "-02", merchant: "Transfer from Current", description: "Transfer from Current", category: "Internal transfer", account: "Savings", source: "Current", destination: "Savings", amount: transferAmount, type: "transfer", recurring: true, recurringKey: "Transfer to Savings", transferId });
    }
    TX_POOL
      .filter(() => rng() < 0.45)
      .slice(0, 3 + Math.floor(rng() * 3))
      .forEach((p) => {
        const day = 3 + Math.floor(rng() * 25);
        const row = {
          date: m.key + "-" + String(day).padStart(2, "0"),
          merchant: p.merchant,
          description: p.merchant,
          category: p.category,
          account: p.account,
          source: p.account,
          destination: p.merchant,
          amount: Math.round(p.amount * (0.85 + rng() * 0.3) * 100) / 100,
          type: p.type,
        };
        if (p.recurring) { row.recurring = true; row.recurringKey = p.merchant; }
        rows.push(row);
      });
  });
  return rows.concat(RECENT_TX)
    .sort((a, b) => b.date.localeCompare(a.date))
    .map((t, i) => ({ ...t, id: "t" + i }));
})();

const ACCOUNTS = [
  { id: "a-current", name: "Current account", type: "debit", typeLabel: "Debit", txKey: "Current", balance: 3912.66, opening: 3500.00, color: "#0e6f51", last4: "··· 4211" },
  { id: "a-savings", name: "Savings", type: "savings", typeLabel: "Savings", txKey: "Savings", balance: 4500.00, opening: 4000.00, color: "#4f5bc7", last4: "··· 8823" },
  { id: "a-credit", name: "Credit card", type: "credit", typeLabel: "Credit", txKey: "Credit card", balance: -120.40, opening: 0, color: "#c0483f", last4: "··· 1047" },
];

const SCHEMAS = [
  { name: "Monzo CSV", bank: "Monzo", initials: "MO", color: "#c03b44", cols: ["Date", "Description", "Amount"], confidence: 98, source: "monzo-may-2024.csv" },
  { name: "NatWest PDF", bank: "NatWest", initials: "NW", color: "#4f5bc7", cols: ["Date", "Details", "Paid out", "Paid in"], confidence: 94, source: "natwest-apr-2024.pdf" },
  { name: "Barclays OFX", bank: "Barclays", initials: "BA", color: "#2f7fb0", cols: ["DTPOSTED", "NAME", "TRNAMT"], confidence: 91, source: "barclays-q1.ofx" },
];

const HISTORY = [
  {
    id: 1, time: "Today, 10:42", actor: "AI", kind: "ai",
    title: "Categorised 12 transactions",
    sub: "From Monzo May statement",
    diff: [
      { line: "+ 6 → Food & Dining", cls: "diff-add" },
      { line: "+ 4 → Transport", cls: "diff-add" },
      { line: "+ 2 → Bills & Utilities", cls: "diff-add" },
    ],
  },
  {
    id: 2, time: "Today, 10:40", actor: "AI", kind: "import",
    title: "Imported 34 transactions",
    sub: "monzo-may-2024.csv · schema “Monzo CSV”",
  },
  {
    id: 3, time: "Yesterday, 18:03", actor: "You", kind: "you",
    title: "Edited schema “Monzo CSV”",
    sub: "Renamed column and saved for reuse",
    diff: [
      { line: "- Amount", cls: "diff-del" },
      { line: "+ Value (currency)", cls: "diff-add" },
    ],
  },
  {
    id: 4, time: "Yesterday, 17:55", actor: "AI", kind: "ai",
    title: "Detected schema “Monzo CSV”",
    sub: "Confidence 98% · 4 columns detected",
  },
  {
    id: 5, time: "Mon, 15:20", actor: "You", kind: "you",
    title: "Moved £500 to Savings",
    sub: "Detected as an internal transfer",
    diff: [
      { line: "- £500 · Current account", cls: "diff-del" },
      { line: "+ £500 · Savings", cls: "diff-add" },
    ],
  },
  {
    id: 6, time: "Mon, 09:12", actor: "AI", kind: "ai",
    title: "Detected recurring bill: Netflix",
    sub: "Monthly payment · labelled automatically",
  },
];

const CATEGORY_SPEND_SERIES = {
  labels: ["Bills", "Food", "Shopping", "Transport", "Entertainment", "Health"],
  values: [382.0, 246.1, 142.8, 128.4, 64.3, 21.5],
  colors: ["#c0483f", "#c9822e", "#2f7fb0", "#4f5bc7", "#7a58b8", "#0e7a4d"],
};

const TRANSPORT_SERIES = {
  labels: ["Jul", "Aug", "Sep", "Oct", "Nov", "Dec", "Jan", "Feb", "Mar", "Apr", "May", "Jun"],
  values: [96, 88, 104, 91, 122, 110, 98, 105, 118, 101, 131, 128],
};

const FOOD_SERIES = {
  labels: ["Groceries", "Eating out", "Coffee", "Takeaway"],
  values: [118.2, 74.6, 31.3, 22.0],
  colors: ["#c9822e", "#a96e1f", "#d59a4a", "#8a6a33"],
};

const TOP_MERCHANTS_TABLE = {
  columns: ["Merchant", "Category", "Total"],
  rows: [
    ["Tesco Express", "Food & Dining", "£142.20"],
    ["Transport for London", "Transport", "£96.40"],
    ["Octopus Energy", "Bills & Utilities", "£86.20"],
    ["Amazon", "Shopping", "£74.15"],
    ["Vodafone", "Bills & Utilities", "£45.00"],
  ],
};

const DEFAULT_PINNED_WIDGETS = [
  {
    id: "w-spend",
    type: "bar",
    title: "Spending by category — this month",
    data: {
      labels: CATEGORY_SPEND_SERIES.labels,
      series: [{ name: "Spend", color: "#0e6f51", values: CATEGORY_SPEND_SERIES.values }],
    },
  },
  {
    id: "w-rate",
    type: "number",
    title: "Savings rate",
    data: { value: "22%", caption: "Of income this month", trend: "up", trendText: "3% vs last month" },
  },
  {
    id: "w-mix",
    type: "pie",
    title: "Spending mix — this month",
    data: { labels: CATEGORY_SPEND_SERIES.labels, values: CATEGORY_SPEND_SERIES.values, colors: CATEGORY_SPEND_SERIES.colors },
  },
  {
    id: "w-top",
    type: "table",
    title: "Top merchants",
    data: TOP_MERCHANTS_TABLE,
  },
];

const QUERY_RESPONSES = [
  {
    match: /transport/i,
    answer: "Here's your transport spending over the last 12 months.",
    steps: [
      "Detected “Transport” is a category ✓",
      "Assuming all bank accounts",
      "Generated query: SELECT month, SUM(amount) FROM tx WHERE category = 'Transport' GROUP BY month",
    ],
    widget: {
      type: "bar",
      title: "Transport spend over the past year",
      data: { labels: TRANSPORT_SERIES.labels, series: [{ name: "Transport", color: "#4f5bc7", values: TRANSPORT_SERIES.values }] },
    },
  },
  {
    match: /food|groceries|dining/i,
    answer: "Here's how your food and dining spending breaks down.",
    steps: [
      "Detected “Food & Dining” is a category ✓",
      "Breaking down by merchant sub-type",
      "Generated query: SELECT subcategory, SUM(amount) FROM tx WHERE category = 'Food & Dining' GROUP BY subcategory",
    ],
    widget: {
      type: "pie",
      title: "Food & Dining breakdown",
      data: { labels: FOOD_SERIES.labels, values: FOOD_SERIES.values, colors: FOOD_SERIES.colors },
    },
  },
  {
    match: /balance|savings rate|net worth/i,
    answer: "Here's how your balance has moved over the last year.",
    steps: [
      "Detected a time-series question",
      "Aggregating all accounts over 12 months",
      "Generated query: SELECT month, SUM(balance) OVER (ORDER BY month) FROM accounts",
    ],
    widget: {
      type: "line",
      title: "Balance over the last year",
      data: { labels: BALANCE_SERIES.labels.slice(-12), series: [{ name: "Balance", color: "#0e6f51", values: BALANCE_SERIES.values.slice(-12) }] },
    },
  },
  {
    match: /where did my money go|spending|spent/i,
    answer: "Here's where your money went this month.",
    steps: [
      "Detected a spending breakdown question",
      "Assuming this month, all accounts",
      "Generated query: SELECT category, SUM(amount) FROM tx WHERE amount < 0 GROUP BY category",
    ],
    widget: {
      type: "bar",
      title: "Where your money went this month",
      data: {
        labels: CATEGORY_SPEND_SERIES.labels,
        series: [{ name: "Spend", color: "#0e6f51", values: CATEGORY_SPEND_SERIES.values }],
      },
    },
  },
  {
    match: /transactions|recent|list|payments|activity/i,
    answer: "Here are your most recent transactions.",
    steps: [
      "Detected a request to list transactions",
      "Assuming all accounts, most recent first",
      "Generated query: SELECT * FROM tx ORDER BY date DESC LIMIT 8",
    ],
    widget: {
      type: "transactions",
      title: "Recent transactions",
      data: { transactions: TRANSACTIONS.slice(0, 8) },
    },
  },
];

const DEFAULT_RESPONSE = {
  answer: "Here's a breakdown of your spending by category.",
  steps: [
    "Analysing your question",
    "Assuming all bank accounts",
    "Generated query: SELECT category, SUM(amount) FROM tx WHERE amount < 0 GROUP BY category",
  ],
  widget: {
    type: "bar",
    title: "Spending by category",
    data: {
      labels: CATEGORY_SPEND_SERIES.labels,
      series: [{ name: "Spend", color: "#0e6f51", values: CATEGORY_SPEND_SERIES.values }],
    },
  },
};
