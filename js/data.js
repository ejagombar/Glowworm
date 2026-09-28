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
  { id: "savings", name: "Savings", color: "#2e9d8f", budget: null, spent: null },
];

const TRANSACTIONS = [
  { date: "2024-06-01", merchant: "Acme Ltd — Salary", category: "Income", account: "Current", amount: 2450.00, type: "income" },
  { date: "2024-06-01", merchant: "Transfer to Savings", category: "Savings", account: "Savings", amount: -500.00, type: "transfer" },
  { date: "2024-06-02", merchant: "Tesco Express", category: "Food & Dining", account: "Current", amount: -42.35, type: "expense" },
  { date: "2024-06-03", merchant: "Octopus Energy", category: "Bills & Utilities", account: "Current", amount: -86.20, type: "bill" },
  { date: "2024-06-04", merchant: "Transport for London", category: "Transport", account: "Current", amount: -12.50, type: "expense" },
  { date: "2024-06-05", merchant: "Netflix", category: "Entertainment", account: "Current", amount: -10.99, type: "bill" },
  { date: "2024-06-06", merchant: "Pret a Manger", category: "Food & Dining", account: "Current", amount: -8.75, type: "expense" },
  { date: "2024-06-07", merchant: "Amazon", category: "Shopping", account: "Current", amount: -34.99, type: "expense" },
  { date: "2024-06-08", merchant: "Boots Pharmacy", category: "Health", account: "Current", amount: -21.50, type: "expense" },
  { date: "2024-06-09", merchant: "Spotify", category: "Entertainment", account: "Current", amount: -11.99, type: "bill" },
  { date: "2024-06-10", merchant: "Uber", category: "Transport", account: "Current", amount: -18.40, type: "expense" },
  { date: "2024-06-11", merchant: "Waterstones", category: "Shopping", account: "Current", amount: -24.00, type: "expense" },
  { date: "2024-06-12", merchant: "Vodafone", category: "Bills & Utilities", account: "Current", amount: -45.00, type: "bill" },
  { date: "2024-06-13", merchant: "Wagamama", category: "Food & Dining", account: "Current", amount: -32.80, type: "expense" },
  { date: "2024-06-14", merchant: "Transport for London", category: "Transport", account: "Current", amount: -9.60, type: "expense" },
  { date: "2024-06-15", merchant: "Zara", category: "Shopping", account: "Current", amount: -83.81, type: "expense" },
];

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

const BALANCE_SERIES = {
  labels: ["Jul", "Aug", "Sep", "Oct", "Nov", "Dec", "Jan", "Feb", "Mar", "Apr", "May", "Jun"],
  values: [3050, 3680, 3540, 4110, 4650, 5020, 4740, 5480, 6120, 6890, 7560, 8412],
};

const SAVINGS_BALANCE_SERIES = {
  labels: ["Jul", "Aug", "Sep", "Oct", "Nov", "Dec", "Jan", "Feb", "Mar", "Apr", "May", "Jun"],
  values: [600, 980, 1180, 1420, 1750, 2060, 2240, 2510, 2890, 3340, 3860, 4500],
};

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
      data: { labels: BALANCE_SERIES.labels, series: [{ name: "Balance", color: "#0e6f51", values: BALANCE_SERIES.values }] },
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
