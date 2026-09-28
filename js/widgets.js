/* ============================================================
   Glowworm — dynamic widget engine (UI skeleton)
   Widgets are JSON-defined and frontend agnostic. This renderer
   maps that JSON to HTML/CSS + SVG. Charts are drawn with plain
   SVG — no external libraries.
   ============================================================ */

const WIDGET_COLORS = ["#0e6f51", "#4f5bc7", "#c9822e", "#c0483f", "#2f7fb0", "#7a58b8", "#2e9d8f", "#6b7280"];

let widgetSeq = 100;

/* ---- helpers ---- */
function esc(str) {
  return String(str ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}
function fmtMoney(v) {
  const n = Number(v);
  return "£" + Math.abs(n).toLocaleString("en-GB", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
function widgetId() {
  return "w-" + (++widgetSeq);
}

/* ---- data conversion (bar <-> line <-> pie <-> table) ---- */
function toSeries(data) {
  if (data.series) return { labels: data.labels, series: data.series };
  if (data.values) return { labels: data.labels, series: [{ name: "Value", color: data.colors?.[0] || WIDGET_COLORS[0], values: data.values }] };
  return { labels: [], series: [] };
}

function convertWidgetData(from, to, data) {
  if (from === to) return data;

  if (to === "number") return { value: data.value ?? "—", caption: data.caption ?? "", trend: data.trend ?? "", trendText: data.trendText ?? "" };
  if (to === "text") return { body: data.body ?? "No text available." };
  if (to === "transactions") return { transactions: data.transactions || [] };

  // Anything chart-like -> table
  if (to === "table") {
    const s = toSeries(data);
    const columns = ["Label", ...s.series.map((x) => x.name)];
    const rows = s.labels.map((label, i) => [label, ...s.series.map((x) => fmtMoney(x.values[i] ?? 0))]);
    return { columns, rows };
  }

  const s = toSeries(data);

  // -> pie (use first series)
  if (to === "pie") {
    const first = s.series[0];
    return {
      labels: s.labels,
      values: first ? first.values : [],
      colors: data.colors || WIDGET_COLORS,
    };
  }

  // -> bar or line
  return { labels: s.labels, series: s.series };
}

/* ---- SVG chart builders ---- */
function axisTicks(max, count = 4) {
  const step = max <= 0 ? 1 : niceStep(max / count);
  const ticks = [];
  for (let t = 0; t <= max + 0.0001; t += step) ticks.push(Math.round(t * 100) / 100);
  return ticks;
}
function niceStep(raw) {
  const pow = Math.pow(10, Math.floor(Math.log10(raw)));
  const n = raw / pow;
  const f = n >= 5 ? 5 : n >= 2.5 ? 2.5 : n >= 2 ? 2 : 1;
  return f * pow;
}

function barChartSVG(data, width = 640, height = 260) {
  const s = data.series || [];
  const labels = data.labels || [];
  const all = s.flatMap((x) => x.values || []);
  const max = Math.max(0, ...all);
  const W = width, H = height, padL = 46, padB = 30, padT = 14, padR = 10;
  const innerW = W - padL - padR, innerH = H - padT - padB;
  const groupW = innerW / Math.max(labels.length, 1);
  const barW = Math.min(34, (groupW * 0.6) / Math.max(s.length, 1));

  let parts = [];
  const ticks = axisTicks(max);
  ticks.forEach((t) => {
    const y = padT + innerH - (t / max) * innerH;
    parts.push(`<line x1="${padL}" y1="${y}" x2="${W - padR}" y2="${y}" style="stroke:var(--chart-grid)" stroke-width="1"/>`);
    parts.push(`<text x="${padL - 8}" y="${y + 3.5}" text-anchor="end" class="chart-tooltip chart-y-label">${t >= 1000 ? (t / 1000) + "k" : t}</text>`);
  });

  labels.forEach((label, i) => {
    const x = padL + i * groupW + groupW / 2;
    s.forEach((series, si) => {
      const v = series.values[i] || 0;
      const h = (v / max) * innerH;
      const bx = x - (s.length * barW) / 2 + si * barW + 2;
      const by = padT + innerH - h;
      parts.push(`<rect x="${bx}" y="${by}" width="${Math.max(barW - 4, 2)}" height="${Math.max(h, 0)}" rx="1" fill="${series.color || WIDGET_COLORS[si % WIDGET_COLORS.length]}"><title>${esc(series.name)}: ${fmtMoney(v)}</title></rect>`);
    });
    parts.push(`<text x="${x}" y="${H - 8}" text-anchor="middle" class="chart-tooltip">${esc(label)}</text>`);
  });

  return `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="bar chart">${parts.join("")}</svg>`;
}

function lineChartSVG(data, width = 640, height = 260) {
  const s = (data.series && data.series[0]) ? data.series[0] : { name: "Value", color: WIDGET_COLORS[0], values: [] };
  const labels = data.labels || [];
  const vals = s.values || [];
  const max = Math.max(0, ...vals);
  const min = Math.min(0, ...vals);
  const W = width, H = height, padL = 46, padB = 30, padT = 14, padR = 14;
  const innerW = W - padL - padR, innerH = H - padT - padB;
  const span = (max - min) || 1;
  const n = labels.length;
  const px = (i) => (n <= 1 ? padL + innerW / 2 : padL + (i / (n - 1)) * innerW);
  const py = (v) => padT + innerH - ((v - min) / span) * innerH;

  let parts = [];
  axisTicks(max, 4).forEach((t) => {
    const y = py(t);
    parts.push(`<line x1="${padL}" y1="${y}" x2="${W - padR}" y2="${y}" style="stroke:var(--chart-grid)" stroke-width="1"/>`);
    parts.push(`<text x="${padL - 8}" y="${y + 3.5}" text-anchor="end" class="chart-tooltip chart-y-label">${t >= 1000 ? (t / 1000) + "k" : t}</text>`);
  });

  const points = vals.map((v, i) => `${px(i)},${py(v)}`).join(" ");
  const area = `${padL},${py(min)} ${points} ${px(n - 1)},${py(min)}`;
  const gradId = "grad-" + Math.random().toString(36).slice(2, 8);
  parts.push(`<defs><linearGradient id="${gradId}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${s.color}" stop-opacity="0.14"/><stop offset="1" stop-color="${s.color}" stop-opacity="0"/></linearGradient></defs>`);
  parts.push(`<polygon points="${area}" fill="url(#${gradId})"/>`);
  parts.push(`<polyline points="${points}" fill="none" stroke="${s.color}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`);

  vals.forEach((v, i) => {
    parts.push(`<circle cx="${px(i)}" cy="${py(v)}" r="3" style="fill:var(--surface)" stroke="${s.color}" stroke-width="2"><title>${esc(labels[i] || "")}: ${fmtMoney(v)}</title></circle>`);
  });
  if (n > 1) {
    parts.push(`<text x="${padL}" y="${H - 8}" class="chart-tooltip">${esc(shortLabel(labels[0]))}</text>`);
    parts.push(`<text x="${W - padR}" y="${H - 8}" text-anchor="end" class="chart-tooltip">${esc(shortLabel(labels[n - 1]))}</text>`);
  }
  return `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="line chart">${parts.join("")}</svg>`;
}

function pieChartSVG(data) {
  const labels = data.labels || [];
  const values = (data.values || []).map(Number);
  const colors = data.colors || WIDGET_COLORS;
  const total = values.reduce((a, b) => a + b, 0) || 1;
  const W = 260, H = 260, cx = 130, cy = 130, r = 92, inner = 56;
  let angle = -Math.PI / 2;
  const parts = [];
  values.forEach((v, i) => {
    const sweep = (v / total) * Math.PI * 2;
    const end = angle + sweep;
    const large = sweep > Math.PI ? 1 : 0;
    const p1 = polar(cx, cy, r, angle);
    const p2 = polar(cx, cy, r, end);
    parts.push(`<path d="M ${p1.x} ${p1.y} A ${r} ${r} 0 ${large} 1 ${p2.x} ${p2.y} L ${cx} ${cy} Z" fill="${colors[i % colors.length]}" opacity="0.92"><title>${esc(labels[i])}: ${((v / total) * 100).toFixed(0)}%</title></path>`);
    angle = end;
  });
  parts.push(`<circle cx="${cx}" cy="${cy}" r="${inner}" style="fill:var(--surface)"/>`);
  parts.push(`<text x="${cx}" y="${cy - 3}" text-anchor="middle" font-size="23" font-weight="600" class="pie-total" style="fill:var(--text)" font-family="Inter, sans-serif">${fmtMoney(total)}</text>`);
  parts.push(`<text x="${cx}" y="${cy + 15}" text-anchor="middle" font-size="10.5" style="fill:var(--muted)" font-family="Inter, sans-serif">total</text>`);
  return { svg: `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="xMidYMid meet" role="img" aria-label="pie chart">${parts.join("")}</svg>`, legend: legendHTML(labels, values, colors, total) };
}
function polar(cx, cy, r, a) {
  return { x: cx + r * Math.cos(a), y: cy + r * Math.sin(a) };
}
function legendHTML(labels, values, colors, total) {
  return `<div class="pie-legend">${labels.map((l, i) => `
    <div class="pie-legend-row">
      <span class="category-dot" style="background:${colors[i % colors.length]}"></span>
      <span>${esc(l)}</span>
      <span class="pie-legend-val">${((values[i] / total) * 100).toFixed(0)}%</span>
    </div>`).join("")}</div>`;
}
function shortLabel(l) {
  const s = String(l);
  return s.length > 8 ? s.slice(0, 7) + "…" : s;
}

/* ---- responsive chart redraw (fills container without distortion) ---- */
let chartObserver = null;

function redrawChartHost(host) {
  const type = host.dataset.chartType;
  if (!type) return;
  const w = host.clientWidth;
  const h = host.clientHeight;
  if (!w || !h || w < 80 || h < 80) return;
  let data;
  try { data = JSON.parse(host.dataset.chartData || "{}"); } catch (e) { return; }
  host.innerHTML = type === "line" ? lineChartSVG(data, w, h) : barChartSVG(data, w, h);
}

function drawChartHosts(scope) {
  if (!window.ResizeObserver) {
    scope.querySelectorAll(".chart-host[data-chart-type]").forEach(redrawChartHost);
    return;
  }
  if (!chartObserver) {
    chartObserver = new ResizeObserver((entries) => {
      requestAnimationFrame(() => entries.forEach((entry) => redrawChartHost(entry.target)));
    });
  }
  chartObserver.disconnect();
  scope.querySelectorAll(".chart-host[data-chart-type]").forEach((host) => {
    redrawChartHost(host);
    chartObserver.observe(host);
  });
}

/* ---- widget DOM ---- */
function widgetBody(widget) {
  const t = widget.type;
  const data = widget.data || {};

  if (t === "number") {
    const trend = data.trend === "up" ? "up" : data.trend === "down" ? "down" : "";
    return `<div class="num-widget">
      <div class="num-value">${esc(data.value)}</div>
      ${data.caption ? `<div class="num-caption">${esc(data.caption)}</div>` : ""}
      ${data.trendText ? `<div class="num-trend ${trend}">${trend === "up" ? "▲" : trend === "down" ? "▼" : ""} ${esc(data.trendText)}</div>` : ""}
    </div>`;
  }
  if (t === "text") return `<div class="text-widget"><p>${esc(data.body ?? "")}</p></div>`;
  if (t === "table") {
    const cols = data.columns || [];
    const rows = data.rows || [];
    return `<div class="table-widget"><table>
      <thead><tr>${cols.map((c) => `<th>${esc(c)}</th>`).join("")}</tr></thead>
      <tbody>${rows.map((r) => `<tr>${r.map((cell, i) => `<td class="${i === r.length - 1 ? "num" : ""}">${esc(cell)}</td>`).join("")}</tr>`).join("")}</tbody>
    </table></div>`;
  }
  if (t === "transactions") {
    const rows = (data.transactions || []).map((tx) => `<tr>
      <td class="cell-sub">${esc(String(tx.date).slice(8))} ${esc(monthName(String(tx.date).slice(5, 7)))}</td>
      <td><div class="cell-merchant">${esc(tx.merchant)}</div></td>
      <td>${esc(tx.category)}</td>
      <td class="num tx-amount ${tx.amount >= 0 ? "pos" : "neg"}">${tx.amount >= 0 ? "+" : "−"}${fmtMoney(tx.amount)}</td>
    </tr>`).join("");
    return `<div class="table-widget tx-widget"><table class="tx-table">
      <thead><tr><th>Date</th><th>Merchant</th><th>Category</th><th class="num">Amount</th></tr></thead>
      <tbody>${rows}</tbody>
    </table></div>`;
  }
  if (t === "pie") {
    const rendered = pieChartSVG(data);
    return `<div class="pie-wrap"><div class="pie-chart">${rendered.svg}</div>${rendered.legend}</div>`;
  }
  if (t === "line") return `<div class="chart-host" data-chart-type="line" data-chart-data="${esc(JSON.stringify(data))}">${lineChartSVG(data)}</div>`;
  // default bar
  return `<div class="chart-host" data-chart-type="bar" data-chart-data="${esc(JSON.stringify(data))}">${barChartSVG(data)}</div>`;
}

function widgetEl(widget, opts = {}) {
  const el = document.createElement("article");
  el.className = "widget";
  if (opts.span) el.classList.add(opts.span);
  el.dataset.widgetId = widget.id;
  if (opts.col) el.style.gridColumn = "span " + opts.col;
  if (opts.rows) el.style.gridRow = "span " + opts.rows;

  const resizeHandles = opts.editMode ? `
    <span class="resize-handle rh-w" draggable="false" data-resize-dir="w"></span>
    <span class="resize-handle rh-e" draggable="false" data-resize-dir="e"></span>
    <span class="resize-handle rh-s" draggable="false" data-resize-dir="s"></span>` : "";

  el.innerHTML = `
    <div class="widget-head">
      <div class="widget-title">${esc(widget.title || "Widget")}</div>
      <div class="widget-actions">
        ${opts.pinnable ? `<button class="widget-btn" draggable="false" data-action="toggle-pin" title="Pin to dashboard"><svg class="icon"><use href="#i-pin"/></svg></button>` : ""}
        <button class="widget-btn" draggable="false" data-action="edit-widget" title="Widget settings"><svg class="icon"><use href="#i-settings"/></svg></button>
      </div>
    </div>
    <div class="widget-body">${widgetBody(widget)}</div>
    ${resizeHandles}`;
  return el;
}

/* ---- pie legend style (injected once) ---- */
function ensureWidgetStyles() {
  if (document.getElementById("widget-extra-css")) return;
  const style = document.createElement("style");
  style.id = "widget-extra-css";
  style.textContent = `
    .pie-wrap { display: flex; align-items: center; gap: 18px; flex-wrap: wrap; }
    .pie-wrap svg { flex: none; width: 168px; height: 168px; }
    .pie-legend { flex: 1; min-width: 120px; max-width: 100%; display: flex; flex-direction: column; gap: 7px; }
    .pie-legend-row { display: flex; align-items: center; gap: 8px; font-size: 12.5px; }
    .pie-legend-row span:nth-child(2) { flex: 1; color: var(--text-2); }
    .pie-legend-val { color: var(--muted); font-weight: 500; font-variant-numeric: tabular-nums; }
  `;
  document.head.appendChild(style);
}
ensureWidgetStyles();
