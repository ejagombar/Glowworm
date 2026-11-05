
const WIDGET_COLORS = ["#0e6f51", "#4f5bc7", "#c9822e", "#c0483f", "#2f7fb0", "#7a58b8", "#2e9d8f", "#6b7280"];

let widgetSeq = 100;

function esc(str) {
  return String(str ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}
let WIDGET_CURRENCY = "GBP";
function fmtMoney(v) {
  const n = Number(v) / 100;
  const sym = ({ GBP: "£", USD: "$", EUR: "€" })[WIDGET_CURRENCY] || WIDGET_CURRENCY + " ";
  return sym + Math.abs(n).toLocaleString("en-GB", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
function fmtMoneySigned(v) {
  const n = Number(v);
  const sign = n < 0 ? "\u2212" : n > 0 ? "+" : "";
  return sign + fmtMoney(n);
}
// A chart's values are money in minor units unless its data names another
// format: percent (a ratio, 0.25 is 25%) or number.
function fmtPercent(v, dp) {
  const p = Number(v) * 100;
  const sign = p < 0 ? "\u2212" : "";
  const a = Math.abs(p);
  return sign + (a >= 10 || dp === 0 ? Math.round(a) : (Math.round(a * 10) / 10)) + "%";
}
function fmtNumber(v) {
  return Number(v).toLocaleString("en-GB", { maximumFractionDigits: 2 });
}
function fmtValue(v, format) {
  if (format === "percent") return fmtPercent(v);
  if (format === "number") return fmtNumber(v);
  return fmtMoney(v);
}
function fmtAxis(v, format) {
  if (format === "percent") return fmtPercent(v, 0);
  if (format === "number") return Math.abs(v) >= 1000 ? (Math.round(v / 100) / 10) + "k" : fmtNumber(v);
  return fmtMoneyAxis(v);
}
function fmtTip(v, format) {
  if (format === "percent") return fmtPercent(v);
  if (format === "number") return fmtNumber(v);
  return fmtMoneyTip(v);
}

function widgetId() {
  return "w-" + (++widgetSeq);
}

function toSeries(data) {
  if (data.series) return { labels: data.labels, series: data.series };
  if (data.values) return { labels: data.labels, series: [{ name: "Value", color: data.colors?.[0] || WIDGET_COLORS[0], values: data.values }] };
  return { labels: [], series: [] };
}

// Nothing to show: no rows, or a chart whose every value is zero (a fresh
// instance's example widgets, or a range with no transactions in it).
function widgetIsEmpty(type, data) {
  data = data || {};
  switch (type) {
    case "number": case "text": return false;
    case "table": return !(data.rows || []).length;
    case "transactions": return !(data.transactions || []).length;
    case "pie": return !(data.values || []).some((v) => Number(v) > 0);
    default: return !toSeries(data).series.some((s) => (s.values || []).some((v) => Number(v)));
  }
}
function widgetEmptyHTML() {
  return `<div class="widget-empty">No data yet</div>`;
}

function convertWidgetData(from, to, data) {
  if (from === to) return data;

  if (to === "number") return { value: data.value ?? "—", caption: data.caption ?? "", trend: data.trend ?? "", trendText: data.trendText ?? "" };
  if (to === "text") return { body: data.body ?? "No text available." };
  if (to === "transactions") return { transactions: data.transactions || [] };

  if (to === "table") {
    const s = toSeries(data);
    const columns = ["Label", ...s.series.map((x) => x.name)];
    const rows = s.labels.map((label, i) => [label, ...s.series.map((x) => fmtValue(x.values[i] ?? 0, data.format))]);
    return { columns, rows };
  }

  const s = toSeries(data);

  if (to === "pie") {
    const first = s.series[0];
    return {
      labels: s.labels,
      values: first ? first.values : [],
      colors: data.colors || WIDGET_COLORS,
      format: data.format,
    };
  }

  return { labels: s.labels, series: s.series, format: data.format, stacked: data.stacked };
}

// Ticks from 0 up to max, and down to min when the chart goes below zero.
function axisTicks(max, count = 4, min = 0) {
  const range = max - Math.min(min, 0);
  const step = range <= 0 ? 1 : niceStep(range / count);
  const ticks = [];
  for (let t = -step; t >= min * (1 + 1e-9); t -= step) ticks.unshift(Math.round(t * 1e6) / 1e6);
  for (let t = 0; t <= max * (1 + 1e-9); t += step) ticks.push(Math.round(t * 1e6) / 1e6);
  return ticks;
}
// A line chart's y axis: the range the data sits in, out to whole ticks.
// Zero is only included when the data crosses it or comes near it, where
// near means closer than the data's own spread.
function lineAxis(dmin, dmax, count = 4) {
  if (!isFinite(dmin)) return { min: 0, max: 1, ticks: [0, 1] };
  let lo = dmin, hi = dmax;
  const spread = hi - lo;
  if (lo > 0 && lo <= spread) lo = 0;
  else if (hi < 0 && -hi <= spread) hi = 0;
  if (hi === lo) {
    // A flat line sits mid-chart, not on the floor.
    const pad = Math.abs(hi) * 0.1 || 1;
    hi += pad; lo = lo === 0 ? 0 : lo - pad;
  }
  const step = niceStep((hi - lo) / count);
  const min = Math.floor(lo / step + 1e-9) * step;
  const max = Math.ceil(hi / step - 1e-9) * step;
  const ticks = [];
  for (let t = min; t <= max + step * 1e-6; t += step) ticks.push(Math.round(t * 1e6) / 1e6);
  return { min, max, ticks };
}
function niceStep(raw) {
  const pow = Math.pow(10, Math.floor(Math.log10(raw)));
  const n = raw / pow;
  const f = n >= 5 ? 5 : n >= 2.5 ? 2.5 : n >= 2 ? 2 : 1;
  return f * pow;
}

function fmtMoneyAxis(v) {
  const sym = ({ GBP: "\u00a3", USD: "$", EUR: "\u20ac" })[WIDGET_CURRENCY] || WIDGET_CURRENCY + " ";
  const n = Number(v) / 100;
  const sign = n < 0 ? "\u2212" : "";
  const a = Math.abs(n);
  if (a >= 1000) return sign + sym + (Math.round(a) / 1000).toFixed(a >= 100000 ? 0 : 1).replace(/\.0$/, "") + "k";
  return sign + sym + (Number.isInteger(a) ? a : Math.round(a * 10) / 10);
}

// Bar chart geometry, shared by drawing and the hover tooltip. Stacked
// bars put a label's series on top of each other in one bar; otherwise
// they stand side by side. Bars fill most of their slot, so a long run of
// them on a phone isn't mostly gaps.
function barGeom(data, W, H) {
  const s = data.series || [];
  const labels = data.labels || [];
  const stacked = !!data.stacked && s.length > 1;
  let max = 0;
  labels.forEach((_, i) => {
    let sum = 0;
    s.forEach((x) => {
      const v = Math.max(Number((x.values || [])[i]) || 0, 0);
      if (stacked) sum += v;
      else if (v > max) max = v;
    });
    if (sum > max) max = sum;
  });
  const scale = max || 1; // an empty chart would otherwise divide by zero
  const padL = 46, padB = 30, padT = 14, padR = 10;
  const innerW = W - padL - padR, innerH = H - padT - padB;
  const groupW = innerW / Math.max(labels.length, 1);
  const across = stacked ? 1 : Math.max(s.length, 1);
  const barW = Math.min(stacked || across === 1 ? 64 : 40, (groupW * 0.82) / across);
  const cx = (i) => padL + i * groupW + groupW / 2;
  const py = (v) => padT + innerH - (Math.max(Number(v) || 0, 0) / scale) * innerH;
  return { series: s, labels, stacked, max, padL, padB, padT, padR, innerW, innerH, groupW, barW, across, cx, py };
}

// Every step-th label, with step chosen so the widest label fits its slot.
// Names (not dates) are shortened to fit first, so every bar keeps one if it can.
function barTickLabels(labels, groupW) {
  let texts = labels.map(chartAxisLabel);
  const fits = Math.floor((groupW - 10) / 6); // ~6px a glyph at 10.5px, plus a gap
  const dates = labels.every((l) => /^\d{4}-\d{2}/.test(String(l ?? "")));
  if (!dates && fits >= 5) texts = texts.map((t) => (t.length > fits ? t.slice(0, fits - 1) + "…" : t));
  const widest = texts.reduce((m, t) => Math.max(m, t.length), 0);
  const step = Math.max(1, Math.ceil((widest * 6 + 10) / Math.max(groupW, 1)));
  const out = [];
  for (let i = 0; i < texts.length; i += step) out.push({ i, text: texts[i] });
  return out;
}

function barChartSVG(data, width = 640, height = 260) {
  const W = width, H = height;
  const g = barGeom(data, W, H);
  const { series: s, labels, max, padL, padR, innerW, groupW, barW, cx, py } = g;

  let parts = [];
  axisTicks(max).forEach((t) => {
    const y = py(t);
    parts.push(`<line x1="${padL}" y1="${y}" x2="${W - padR}" y2="${y}" style="stroke:var(--chart-grid)" stroke-width="1"/>`);
    parts.push(`<text x="${padL - 8}" y="${y + 3.5}" text-anchor="end" class="chart-tooltip chart-y-label">${fmtAxis(t, data.format)}</text>`);
  });

  const base = g.padT + g.innerH;
  // Side-by-side bars keep a hairline apart; stacked parts share one bar.
  const gap = !g.stacked && g.across > 1 && barW > 6 ? 1 : 0;
  labels.forEach((label, i) => {
    const x = cx(i);
    let top = base;
    s.forEach((series, si) => {
      const color = series.color || WIDGET_COLORS[si % WIDGET_COLORS.length];
      const v = Math.max(Number((series.values || [])[i]) || 0, 0);
      if (g.stacked) {
        const h = base - py(v);
        if (h <= 0) return;
        parts.push(`<rect x="${x - barW / 2}" y="${top - h}" width="${Math.max(barW, 1)}" height="${h}" fill="${color}"/>`);
        top -= h;
        return;
      }
      const by = py(v);
      const bx = x - (g.across * barW) / 2 + si * barW + gap / 2;
      parts.push(`<rect x="${bx}" y="${by}" width="${Math.max(barW - gap, 1)}" height="${Math.max(base - by, 0)}" rx="1" fill="${color}"/>`);
    });
  });

  barTickLabels(labels, groupW).forEach(({ i, text }) => {
    const x = cx(i);
    const anchor = labels.length === 1 ? "middle" : x - padL < 30 ? "start" : padL + innerW - x < 30 ? "end" : "middle";
    parts.push(`<text x="${x}" y="${H - 8}" text-anchor="${anchor}" class="chart-tooltip">${esc(text)}</text>`);
  });
  parts.push(`<rect class="chart-hover-layer" x="0" y="0" width="${W}" height="${H}" fill="transparent"/>`);
  return `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="bar chart">${parts.join("")}</svg>`;
}

// The bar group under the pointer's x, and where its tip goes.
function barAt(host, svg, clientX) {
  const data = chartDataFor(host);
  const vb = svg.viewBox && svg.viewBox.baseVal;
  const W = vb && vb.width ? vb.width : 640;
  const H = vb && vb.height ? vb.height : 260;
  const g = barGeom(data, W, H);
  const n = g.labels.length;
  const rect = svg.getBoundingClientRect();
  if (!n || !rect.width) return null;
  const x = ((clientX - rect.left) / rect.width) * W;
  if (x < g.padL || x > g.padL + g.innerW) return null;
  const idx = Math.min(n - 1, Math.max(0, Math.floor((x - g.padL) / g.groupW)));
  let rows = g.series.map((x, si) => ({ name: x.name || "Value", color: x.color || WIDGET_COLORS[si % WIDGET_COLORS.length], value: Number((x.values || [])[idx]) || 0 }));
  const total = rows.reduce((a, r) => a + Math.max(r.value, 0), 0);
  const top = g.stacked ? g.py(total) : Math.min(...rows.map((r) => g.py(r.value)));
  // A stack reads top down, and its empty parts say nothing.
  if (g.stacked) rows = rows.filter((r) => r.value).reverse();
  return { idx, label: g.labels[idx], rows, total: g.stacked ? total : null, format: data.format, x: g.cx(idx), y: top };
}

// Line chart geometry, shared by drawing and the hover tooltip. view is an
// optional {lo, hi} window of fractional point indices for an x-axis zoom.
// A value of null is a gap: that series has nothing at that label yet.
function lineGeom(data, W, H, view) {
  const series = (data.series && data.series.length) ? data.series : [{ name: "Value", color: WIDGET_COLORS[0], values: [] }];
  const labels = data.labels || [];
  const n = labels.length;
  const padL = 46, padB = 30, padT = 14, padR = 14;
  const innerW = W - padL - padR, innerH = H - padT - padB;
  const lo = view ? view.lo : 0;
  const hi = view ? view.hi : Math.max(n - 1, 0);
  // The points drawn: the window plus one either side, so the line runs off the clipped edge.
  const i0 = Math.max(0, Math.floor(lo) - (view ? 1 : 0));
  const i1 = Math.min(n - 1, Math.ceil(hi) + (view ? 1 : 0));
  // Loop, not spread: Math.max(...vals) throws RangeError on very long series.
  let dmax = -Infinity, dmin = Infinity;
  series.forEach((x) => {
    const vals = x.values || [];
    for (let i = Math.max(0, Math.floor(lo)); i <= Math.min(vals.length - 1, Math.ceil(hi)); i++) {
      if (vals[i] == null) continue;
      const v = Number(vals[i]);
      if (v > dmax) dmax = v;
      if (v < dmin) dmin = v;
    }
  });
  const { min, max, ticks } = lineAxis(dmin, dmax);
  const span = (max - min) || 1;
  const px = (i) => (n <= 1 ? padL + innerW / 2 : padL + ((i - lo) / (hi - lo)) * innerW);
  const py = (v) => padT + innerH - ((v - min) / span) * innerH;
  return { series, labels, n, padL, padB, padT, padR, innerW, innerH, lo, hi, i0, i1, max, min, ticks, px, py };
}

// The runs of points with a value, for each to be drawn as one line.
function lineRuns(vals, i0, i1) {
  const runs = [];
  let run = null;
  for (let i = i0; i <= i1; i++) {
    if (vals[i] == null) { run = null; continue; }
    if (!run) runs.push(run = []);
    run.push(i);
  }
  return runs;
}

function lineChartSVG(data, width = 640, height = 260, view = null) {
  const W = width, H = height;
  const g = lineGeom(data, W, H, view);
  const { series, labels, n, padL, padR, padT, innerW, innerH, i0, i1, min, ticks, px, py } = g;
  const one = series.length === 1;

  let parts = [];
  ticks.forEach((t) => {
    const y = py(t);
    parts.push(`<line x1="${padL}" y1="${y}" x2="${W - padR}" y2="${y}" style="stroke:var(--chart-grid)" stroke-width="1"/>`);
    parts.push(`<text x="${padL - 8}" y="${y + 3.5}" text-anchor="end" class="chart-tooltip chart-y-label">${fmtAxis(t, data.format)}</text>`);
  });

  const fillId = Math.random().toString(36).slice(2, 8);
  const defs = [`<clipPath id="clip-${fillId}"><rect x="${padL - 4}" y="0" width="${innerW + 8}" height="${padT + innerH + 4}"/></clipPath>`];
  const body = [];
  // Dots only while there's room for them: fewer as lines are added.
  const dots = (i1 - i0 + 1) * series.length <= 80;
  series.forEach((s, si) => {
    const color = s.color || WIDGET_COLORS[si % WIDGET_COLORS.length];
    const vals = s.values || [];
    lineRuns(vals, i0, i1).forEach((run) => {
      const points = run.map((i) => `${px(i)},${py(vals[i])}`).join(" ");
      // A lone series gets the area under it: a fading gradient with a grid
      // like the page background over it, in its colour. Several would muddy.
      if (one) {
        const area = `${px(run[0])},${py(min)} ${points} ${px(run[run.length - 1])},${py(min)}`;
        body.push(`<polygon points="${area}" fill="url(#grad-${fillId})"/>`);
        body.push(`<polygon points="${area}" fill="url(#grid-${fillId})"/>`);
      }
      body.push(`<polyline points="${points}" fill="none" stroke="${color}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`);
      if (dots) {
        run.forEach((i) => {
          body.push(`<circle cx="${px(i)}" cy="${py(vals[i])}" r="${one ? 3 : 2.5}" style="fill:var(--surface)" stroke="${color}" stroke-width="2"><title>${esc(labels[i] || "")}${one ? "" : " · " + esc(s.name || "")}: ${fmtValue(vals[i], data.format)}</title></circle>`);
        });
      }
    });
    if (one) {
      defs.push(`<linearGradient id="grad-${fillId}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${color}" stop-opacity="0.14"/><stop offset="1" stop-color="${color}" stop-opacity="0"/></linearGradient>` +
        `<pattern id="grid-${fillId}" width="8" height="8" patternUnits="userSpaceOnUse"><path d="M8 0V8M0 8H8" fill="none" stroke="${color}" stroke-opacity="0.22" stroke-width="1"/></pattern>`);
    }
  });
  parts.push(`<defs>${defs.join("")}</defs><g clip-path="url(#clip-${fillId})">${body.join("")}</g>`);

  const xTickCount = width < 420 ? 2 : width < 700 ? 4 : 6;
  const first = Math.max(0, Math.ceil(g.lo - 1e-9)), last = Math.min(n - 1, Math.floor(g.hi + 1e-9));
  xTickLabels(labels.slice(first, last + 1), xTickCount).forEach(({ i: j, text }) => {
    const i = first + j;
    const x = px(i);
    const anchor = n === 1 ? "middle" : x - padL < 30 ? "start" : padL + innerW - x < 30 ? "end" : "middle";
    parts.push(`<text x="${x}" y="${H - 8}" text-anchor="${anchor}" class="chart-tooltip">${esc(text)}</text>`);
  });
  parts.push(`<rect class="chart-hover-layer" x="0" y="0" width="${W}" height="${H}" fill="transparent"/>`);
  return `<svg viewBox="0 0 ${W} ${H}" role="img" aria-label="line chart">${parts.join("")}</svg>`;
}

let chartTipEl = null;
function ensureChartTip() {
  if (chartTipEl || !document.body) return chartTipEl;
  chartTipEl = document.createElement("div");
  chartTipEl.className = "chart-hover-tip";
  chartTipEl.setAttribute("role", "status");
  chartTipEl.style.display = "none";
  document.body.appendChild(chartTipEl);
  return chartTipEl;
}
function fmtMoneyTip(v) {
  const n = Number(v);
  const sym = ({ GBP: "\u00a3", USD: "$", EUR: "\u20ac" })[WIDGET_CURRENCY] || WIDGET_CURRENCY + " ";
  const sign = n < 0 ? "\u2212" : "";
  return sign + sym + Math.abs(n / 100).toLocaleString("en-GB", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
// The point nearest the pointer. With several lines, every line's value at
// that label goes in the tip, largest first, the nearest line's marked.
function nearestLinePoint(host, svg, clientX, clientY) {
  const data = chartDataFor(host);
  const vb = svg.viewBox && svg.viewBox.baseVal;
  const W = vb && vb.width ? vb.width : 640;
  const H = vb && vb.height ? vb.height : 260;
  const g = lineGeom(data, W, H, chartZoomFor(host, data));
  if (!g.n) return null;
  const rect = svg.getBoundingClientRect();
  if (!rect.width || !rect.height) return null;
  const x = ((clientX - rect.left) / rect.width) * W;
  const y = ((clientY - rect.top) / rect.height) * H;
  let idx = -1, near = -1, best = Infinity;
  for (let i = Math.max(0, Math.ceil(g.lo - 1e-9)); i <= Math.min(g.n - 1, Math.floor(g.hi + 1e-9)); i++) {
    g.series.forEach((s, si) => {
      const v = (s.values || [])[i];
      if (v == null) return;
      const dx = g.px(i) - x, dy = g.py(v) - y;
      const d = dx * dx + dy * dy;
      if (d < best) { best = d; idx = i; near = si; }
    });
  }
  if (idx < 0) return null;
  const nearV = g.series[near].values[idx];
  const pt = { label: g.labels[idx], value: nearV, format: data.format, name: g.series[near].name || "Value", x: g.px(idx), y: g.py(nearV) };
  if (g.series.length > 1) {
    pt.rows = g.series.map((s, si) => ({ name: s.name || "Value", color: s.color || WIDGET_COLORS[si % WIDGET_COLORS.length], value: (s.values || [])[idx], near: si === near }))
      .filter((r) => r.value != null).sort((a, b) => Number(b.value) - Number(a.value));
  }
  return pt;
}

const chartDataCache = new WeakMap();
function chartDataFor(host) {
  const src = host.dataset.chartData || "";
  const cached = chartDataCache.get(host);
  if (cached && cached.src === src) return cached.data;
  let data;
  try { data = JSON.parse(src || "{}"); } catch (e) { data = {}; }
  chartDataCache.set(host, { src, data });
  return data;
}
function showChartTip(tip, svg, pt) {
  placeChartTip(tip, svg, pt.x, pt.y, chartTipHTML(pt));
}
// A tip's label and value, or with several series a row per series: a
// swatch in its colour, its name and value, and a stack's total.
function chartTipHTML(pt) {
  const head = `<div class="chart-tip-label">${esc(chartTooltipLabel(pt.label))}</div>`;
  if (!pt.rows) return head + `<div class="chart-tip-value">${esc(fmtTip(pt.value, pt.format))}</div>`;
  return head + pt.rows.map((r) => `<div class="chart-tip-row${r.near ? " near" : ""}"><span class="chart-tip-dot" style="background:${esc(r.color)}"></span>`
    + `<span class="chart-tip-name">${esc(r.name)}</span><span class="chart-tip-value">${esc(fmtTip(r.value, pt.format))}</span></div>`).join("")
    + (pt.total != null ? `<div class="chart-tip-row chart-tip-total"><span class="chart-tip-name">Total</span><span class="chart-tip-value">${esc(fmtTip(pt.total, pt.format))}</span></div>` : "");
}
// Puts the tip above (or below) the point x, y given in the svg's viewBox units.
function placeChartTip(tip, svg, x, y, html) {
  const rect = svg.getBoundingClientRect();
  const vb = svg.viewBox && svg.viewBox.baseVal;
  const W = vb && vb.width ? vb.width : 640;
  const H = vb && vb.height ? vb.height : 260;
  if (!rect.width || !rect.height) return;
  tip.style.display = "block";
  tip.innerHTML = html;
  const pt = { x, y };
  const sx = rect.left + (pt.x / W) * rect.width;
  const sy = rect.top + (pt.y / H) * rect.height;
  const gap = 10;
  const r = tip.getBoundingClientRect();
  let left = sx - r.width / 2;
  let top = sy - r.height - gap;
  let below = false;
  if (top < 8) { top = sy + gap; below = true; }
  if (left < 8) left = 8;
  if (left + r.width > window.innerWidth - 8) left = window.innerWidth - 8 - r.width;
  tip.classList.toggle("below", below);
  tip.style.left = left + "px";
  tip.style.top = top + "px";
}
function ensureChartHover() {
  if (ensureChartHover.done || !document.body) return;
  ensureChartHover.done = true;
  const tip = ensureChartTip();
  if (!tip) return;
  // Pie slices: hover on desktop, tap on a phone; the slice stays picked
  // until the pointer leaves it or another tap lands elsewhere.
  let activeSlice = null;
  const pickSlice = (slice) => {
    if (activeSlice === slice) return;
    if (activeSlice) activeSlice.classList.remove("active");
    activeSlice = slice;
    if (!slice) { tip.style.display = "none"; return; }
    slice.classList.add("active");
    const d = slice.dataset;
    placeChartTip(tip, slice.ownerSVGElement, Number(d.x), Number(d.y),
      `<div class="chart-tip-label">${esc(d.label)}</div>`
      + `<div class="chart-tip-value">${esc(fmtTip(d.value, d.format))} <span class="chart-tip-pct">· ${esc(d.pct)}%</span></div>`);
  };
  // Bars: the whole column under the pointer is picked, so thin bars are easy to hit.
  const pickBar = (host, svg, clientX) => {
    const pt = host && svg ? barAt(host, svg, clientX) : null;
    if (!pt) { if (host) tip.style.display = "none"; return; }
    const one = pt.rows.length === 1 && pt.total == null;
    placeChartTip(tip, svg, pt.x, pt.y, chartTipHTML(one ? { label: pt.label, value: pt.rows[0].value, format: pt.format } : pt));
  };
  const sliceAt = (e) => (e.target && e.target.closest ? e.target.closest(".pie-slice") : null);
  document.addEventListener("click", (e) => pickSlice(sliceAt(e)));
  document.addEventListener("mousemove", (e) => {
    if (sliceAt(e) || activeSlice) {
      pickSlice(sliceAt(e));
      if (activeSlice) return;
    }
    const bars = e.target && e.target.closest ? e.target.closest('.chart-host[data-chart-type="bar"]') : null;
    pickBar(bars, bars && bars.querySelector("svg"), e.clientX);
    if (bars) return;
    const host = e.target && e.target.closest ? e.target.closest('.chart-host[data-chart-type="line"]') : null;
    if (!host || chartGesture) { tip.style.display = "none"; return; }
    const svg = host.querySelector("svg");
    if (!svg) { tip.style.display = "none"; return; }
    const pt = nearestLinePoint(host, svg, e.clientX, e.clientY);
    if (!pt) { tip.style.display = "none"; return; }
    showChartTip(tip, svg, pt);
  });
  document.addEventListener("mouseleave", () => { tip.style.display = "none"; });
  // Fixed-position tip would go stale when the page scrolls under the pointer.
  window.addEventListener("scroll", () => { tip.style.display = "none"; pickSlice(null); }, { passive: true });
}
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", ensureChartHover);
} else {
  ensureChartHover();
}

function pieChartSVG(data) {
  const labels = data.labels || [];
  const values = (data.values || []).map(Number);
  const colors = data.colors || WIDGET_COLORS;
  const sum = values.reduce((a, b) => a + b, 0);
  const total = sum || 1; // only to divide by: an empty chart would otherwise divide by zero
  const W = 260, H = 260, cx = 130, cy = 130, r = 92, inner = 56;
  let angle = -Math.PI / 2;
  const parts = [];
  values.forEach((v, i) => {
    const sweep = (v / total) * Math.PI * 2;
    const end = angle + sweep;
    const large = sweep > Math.PI ? 1 : 0;
    const p1 = polar(cx, cy, r, angle);
    const p2 = polar(cx, cy, r, end);
    // The tooltip sits at the middle of the slice's ring.
    const mid = polar(cx, cy, (r + inner) / 2, angle + sweep / 2);
    parts.push(`<path class="pie-slice" d="M ${p1.x} ${p1.y} A ${r} ${r} 0 ${large} 1 ${p2.x} ${p2.y} L ${cx} ${cy} Z" fill="${colors[i % colors.length]}"` +
      ` data-label="${esc(labels[i])}" data-value="${v}" data-format="${esc(data.format || "")}" data-pct="${((v / total) * 100).toFixed(0)}" data-x="${mid.x.toFixed(1)}" data-y="${mid.y.toFixed(1)}"/>`);
    angle = end;
  });
  parts.push(`<circle cx="${cx}" cy="${cy}" r="${inner}" style="fill:var(--surface)"/>`);
  // Shrink the type if even the short form would reach the ring (~0.6em a glyph).
  const label = data.format === "number" ? fmtNumber(sum) : fmtMoneyCompact(sum);
  const size = Math.min(23, Math.floor(((inner - 8) * 2) / (label.length * 0.6)));
  parts.push(`<text x="${cx}" y="${cy}" dominant-baseline="central" text-anchor="middle" font-size="${size}" font-weight="600" class="pie-total" style="fill:var(--text)" font-family="Inter, sans-serif">${esc(label)}</text>`);
  return { svg: `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="xMidYMid meet" role="img" aria-label="pie chart">${parts.join("")}</svg>`, legend: legendHTML(labels, values, colors, total) };
}
// Short enough for the middle of a pie: pence only under £1,000, whole pounds
// to £100,000, then £123K and £1.24M.
function fmtMoneyCompact(v) {
  const sym = ({ GBP: "\u00a3", USD: "$", EUR: "\u20ac" })[WIDGET_CURRENCY] || WIDGET_CURRENCY + " ";
  const n = Number(v) / 100;
  const sign = n < 0 ? "\u2212" : "";
  const a = Math.abs(n);
  let s;
  if (a >= 999995000) s = (a / 1e9).toFixed(2).replace(/\.?0+$/, "") + "B";
  else if (a >= 999500) s = (a / 1e6).toFixed(2).replace(/\.?0+$/, "") + "M";
  else if (a >= 1e5) s = Math.round(a / 1e3) + "K";
  else if (a >= 1e3) s = Math.round(a).toLocaleString("en-GB");
  else s = a.toLocaleString("en-GB", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  return sign + sym + s;
}
function polar(cx, cy, r, a) {
  return { x: cx + r * Math.cos(a), y: cy + r * Math.sin(a) };
}
function legendHTML(labels, values, colors, total) {
  return `<div class="pie-legend">${labels.map((l, i) => `
    <div class="pie-legend-row">
      <span class="pie-legend-dot" style="background:${colors[i % colors.length]}"></span>
      <span>${esc(l)}</span>
      <span class="pie-legend-val">${((values[i] / total) * 100).toFixed(0)}%</span>
    </div>`).join("")}</div>`;
}
function shortLabel(l) {
  const s = String(l);
  return s.length > 8 ? s.slice(0, 7) + "…" : s;
}

function chartAxisLabel(l) {
  const s = String(l ?? "");
  if (/^\d{4}-\d{2}-\d{2}/.test(s)) {
    const d = new Date(s.slice(0, 10) + "T00:00:00");
    if (!isNaN(d)) return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "2-digit" });
  }
  if (/^\d{4}-\d{2}$/.test(s)) {
    const d = new Date(s + "-01T00:00:00");
    if (!isNaN(d)) return d.toLocaleDateString("en-GB", { month: "short", year: "2-digit" });
  }
  return s.length > 12 ? s.slice(0, 11) + "…" : s;
}
function chartTooltipLabel(l) {
  const s = String(l ?? "");
  let m;
  if (/^\d{4}-\d{2}-\d{2}/.test(s)) {
    const d = new Date(s.slice(0, 10) + "T00:00:00");
    if (!isNaN(d)) {
      const datePart = d.toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric" });
      if ((m = /[T\s](\d{1,2}:\d{2})/.exec(s))) return datePart + ", " + m[1];
      return datePart;
    }
  }
  if (/^\d{4}-\d{2}$/.test(s)) {
    const d = new Date(s + "-01T00:00:00");
    if (!isNaN(d)) return d.toLocaleDateString("en-GB", { month: "long", year: "numeric" });
  }
  return s;
}
function xTickLabels(labels, count = 6) {
  const n = labels.length;
  if (!n) return [];
  const maxTicks = Math.max(2, count);
  if (n <= maxTicks) return labels.map((text, i) => ({ i, text: chartAxisLabel(text) }));
  const out = [];
  const seen = new Set();
  for (let k = 0; k < maxTicks; k++) {
    const i = Math.round((k * (n - 1)) / (maxTicks - 1));
    if (seen.has(i)) continue;
    seen.add(i);
    out.push({ i, text: chartAxisLabel(labels[i]) });
  }
  return out;
}

let chartObserver = null;

function redrawChartHost(host) {
  const type = host.dataset.chartType;
  if (!type) return;
  let data;
  try { data = JSON.parse(host.dataset.chartData || "{}"); } catch (e) { return; }
  if (widgetIsEmpty(type, data)) {
    host.innerHTML = widgetEmptyHTML();
    const legend = type === "pie" && host.parentElement && host.parentElement.querySelector(":scope > .pie-legend");
    if (legend) legend.remove();
    return;
  }
  if (type === "pie") {
    const rendered = pieChartSVG(data);
    host.innerHTML = rendered.svg;
    const wrap = host.parentElement;
    if (wrap && wrap.classList.contains("pie-wrap")) {
      const tpl = document.createElement("template");
      tpl.innerHTML = rendered.legend;
      const legendEl = tpl.content.firstElementChild;
      const existing = wrap.querySelector(":scope > .pie-legend");
      if (existing) existing.replaceWith(legendEl);
      else wrap.appendChild(legendEl);
    }
    return;
  }
  let w = host.clientWidth;
  let h = host.clientHeight;
  // Hosts not laid out yet report 0; without defaults they'd stay empty.
  if (!w || w < 80) w = 640;
  if (!h || h < 80) h = 260;
  // Several series get a key above the chart, which takes its height.
  const legend = chartLegendHTML(type, data);
  if (legend) {
    host.innerHTML = legend;
    h = Math.max(h - host.firstElementChild.offsetHeight, 80);
  }
  const sized = (svg) => legend ? svg.replace("<svg ", `<svg style="height:${h}px" `) : svg;
  if (type !== "line") {
    host.innerHTML = legend + sized(barChartSVG(data, w, h));
    return;
  }
  const view = chartZoomFor(host, data);
  host.innerHTML = legend + sized(lineChartSVG(data, w, h, view)) +
    (view ? `<button type="button" class="chart-zoom-reset" data-chart-zoom-reset>Reset zoom</button>` : "");
}

function chartLegendHTML(type, data) {
  const series = data.series || [];
  if (series.length < 2) return "";
  return `<div class="chart-legend">${series.map((s, i) => `<span class="chart-legend-item">`
    + `<span class="chart-legend-swatch ${type === "line" ? "line" : ""}" style="background:${esc(s.color || WIDGET_COLORS[i % WIDGET_COLORS.length])}"></span>`
    + `${esc(s.name || "Series " + (i + 1))}</span>`).join("")}</div>`;
}

// ---- Line charts zoom along the x axis: Ctrl + scroll or a trackpad pinch
// on desktop, two fingers on a phone; drag (or scroll sideways) to move along,
// double-click or "Reset zoom" to go back. The zoom lives only in memory, so
// a reload or a redraw with different data starts from the full range.
const chartZooms = new WeakMap();
let chartGesture = null;

function chartZoomFor(host, data) {
  const z = chartZooms.get(host);
  if (!z) return null;
  if (z.src !== host.dataset.chartData) { chartZooms.delete(host); return null; }
  return z;
}

function setChartZoom(host, lo, hi) {
  const n = (chartDataFor(host).labels || []).length;
  const full = n - 1;
  if (full < 2) return;
  const minSpan = Math.min(full, 3);
  let span = Math.min(Math.max(hi - lo, minSpan), full);
  if (span >= full - 1e-6) chartZooms.delete(host);
  else {
    lo = Math.min(Math.max(lo, 0), full - span);
    chartZooms.set(host, { src: host.dataset.chartData, lo, hi: lo + span });
  }
  if (!host.zoomFrame) {
    host.zoomFrame = requestAnimationFrame(() => { host.zoomFrame = 0; redrawChartHost(host); });
  }
}

// Where the plot area sits on screen, and the visible index window.
function chartPlot(host) {
  const svg = host.querySelector("svg");
  if (!svg) return null;
  const rect = svg.getBoundingClientRect();
  const vb = svg.viewBox.baseVal;
  if (!rect.width || !vb || !vb.width) return null;
  const data = chartDataFor(host);
  const n = (data.labels || []).length;
  const z = chartZoomFor(host, data);
  const k = rect.width / vb.width;
  const g = lineGeom(data, vb.width, vb.height, z);
  return { left: rect.left + g.padL * k, width: g.innerW * k, lo: z ? z.lo : 0, hi: z ? z.hi : n - 1, n };
}

function zoomableChart(target) {
  const host = target && target.closest ? target.closest('.chart-host[data-chart-type="line"]') : null;
  if (!host || host.closest(".widgets-grid.editing")) return null;
  return host;
}

function ensureChartZoom() {
  if (ensureChartZoom.done || !document.body) return;
  ensureChartZoom.done = true;
  // iOS Safari ignores user-scalable=no; its gesture events still let a pinch be refused.
  ["gesturestart", "gesturechange"].forEach((t) => document.addEventListener(t, (e) => e.preventDefault()));
  document.addEventListener("wheel", (e) => {
    const host = zoomableChart(e.target);
    if (!host) return;
    const p = chartPlot(host);
    if (!p || p.n < 3) return;
    const span = p.hi - p.lo;
    if (e.ctrlKey || e.metaKey) {
      // Ctrl + wheel, and a trackpad pinch, which browsers send as one.
      e.preventDefault();
      const f = Math.min(Math.max((e.clientX - p.left) / p.width, 0), 1);
      const at = p.lo + f * span;
      // A mouse notch is ~100, a trackpad pinch step a few: cap so a notch zooms ~25%.
      const next = span * Math.exp(Math.min(Math.max(e.deltaY, -40), 40) * 0.007);
      setChartZoom(host, at - f * next, at - f * next + next);
    } else if (chartZooms.has(host) && Math.abs(e.deltaX) > Math.abs(e.deltaY)) {
      e.preventDefault();
      const shift = (e.deltaX / p.width) * span;
      setChartZoom(host, p.lo + shift, p.hi + shift);
    }
  }, { passive: false });

  const pointers = new Map();
  const begin = (host) => {
    const p = chartPlot(host);
    if (!p) { chartGesture = null; return; }
    const pts = [...pointers.values()];
    const x = pts.reduce((a, q) => a + q.x, 0) / pts.length;
    const dist = pts.length > 1 ? Math.abs(pts[0].x - pts[1].x) : 0;
    chartGesture = { host, p, x, dist, moved: false };
  };
  document.addEventListener("pointerdown", (e) => {
    if (e.target.closest && e.target.closest("[data-chart-zoom-reset]")) return;
    const host = zoomableChart(e.target);
    if (!host || (chartGesture && chartGesture.host !== host)) return;
    if (e.pointerType === "mouse" && (e.button !== 0 || !chartZooms.has(host))) return;
    pointers.set(e.pointerId, { x: e.clientX });
    begin(host);
  });
  document.addEventListener("pointermove", (e) => {
    if (!chartGesture || !pointers.has(e.pointerId)) return;
    pointers.get(e.pointerId).x = e.clientX;
    const { host, p } = chartGesture;
    const pts = [...pointers.values()];
    const x = pts.reduce((a, q) => a + q.x, 0) / pts.length;
    const span = p.hi - p.lo;
    let next = span;
    if (pts.length > 1 && chartGesture.dist > 10) {
      next = span * chartGesture.dist / Math.max(Math.abs(pts[0].x - pts[1].x), 1);
    } else if (!chartZooms.has(host)) {
      return; // one finger on an unzoomed chart: leave it to the page
    }
    if (Math.abs(x - chartGesture.x) > 3 || next !== span) {
      chartGesture.moved = true;
      if (!host.hasPointerCapture(e.pointerId)) host.setPointerCapture(e.pointerId);
      host.classList.add("chart-panning");
    }
    // Keep the index that was under the fingers (or cursor) under them.
    const f0 = (chartGesture.x - p.left) / p.width;
    const at = p.lo + f0 * span;
    const f = (x - p.left) / p.width;
    setChartZoom(host, at - f * next, at - f * next + next);
  });
  const end = (e) => {
    if (!pointers.delete(e.pointerId) || !chartGesture) return;
    const host = chartGesture.host;
    if (pointers.size) begin(host);
    else { chartGesture = null; host.classList.remove("chart-panning"); }
  };
  document.addEventListener("pointerup", end);
  document.addEventListener("pointercancel", end);

  const reset = (host) => { chartZooms.delete(host); redrawChartHost(host); };
  document.addEventListener("dblclick", (e) => {
    const host = zoomableChart(e.target);
    if (host && chartZooms.has(host)) reset(host);
  });
  document.addEventListener("click", (e) => {
    const btn = e.target.closest && e.target.closest("[data-chart-zoom-reset]");
    if (btn) reset(btn.closest(".chart-host"));
  });
}
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", ensureChartZoom);
} else {
  ensureChartZoom();
}

const CHART_HOST_SELECTOR = ".chart-host[data-chart-type], .pie-chart[data-chart-type]";

function drawChartHosts(scope) {
  if (!window.ResizeObserver) {
    scope.querySelectorAll(CHART_HOST_SELECTOR).forEach(redrawChartHost);
    return;
  }
  if (!chartObserver) {
    chartObserver = new ResizeObserver((entries) => {
      requestAnimationFrame(() => entries.forEach((entry) => redrawChartHost(entry.target)));
    });
  }
  chartObserver.disconnect();
  scope.querySelectorAll(CHART_HOST_SELECTOR).forEach((host) => {
    redrawChartHost(host);
    chartObserver.observe(host);
  });
}

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
    const links = data.links || [];
    const cell = (v, ri, i) => (i === 0 && links[ri]
      ? `<a class="table-link" href="${esc(links[ri])}" title="See these transactions">${esc(v)}</a>` : esc(v));
    return `<div class="table-widget"><table>
      <thead><tr>${cols.map((c, i) => `<th${i === cols.length - 1 ? ' class="num"' : ""}>${esc(c)}</th>`).join("")}</tr></thead>
      <tbody>${rows.map((r, ri) => `<tr>${r.map((v, i) => `<td${i === r.length - 1 ? ' class="num"' : ""}>${cell(v, ri, i)}</td>`).join("")}</tr>`).join("")}</tbody>
    </table></div>`;
  }
  if (t === "transactions") {
    const rows = (data.transactions || []).map((tx) => `<tr>
      <td class="cell-sub">${esc(String(tx.date).slice(8))} ${esc(monthName(String(tx.date).slice(5, 7)))}</td>
      <td><div class="cell-merchant">${esc(tx.description || tx.merchant)}</div></td>
      <td>${esc(tx.category)}</td>
      <td class="num tx-amount ${tx.type === "transfer" ? "transfer" : tx.amount >= 0 ? "pos" : "neg"}">${tx.type === "transfer" ? "⇄ " + fmtMoney(tx.amount) : (tx.amount >= 0 ? "+" : "−") + fmtMoney(tx.amount)}</td>
    </tr>`).join("");
    return `<div class="table-widget tx-widget"><table class="tx-table">
      <thead><tr><th>Date</th><th>Description</th><th>Category</th><th class="num">Amount</th></tr></thead>
      <tbody>${rows}</tbody>
    </table></div>`;
  }
  if (t === "pie") {
    const rendered = pieChartSVG(data);
    return `<div class="pie-wrap"><div class="pie-chart">${rendered.svg}</div>${rendered.legend}</div>`;
  }
  if (t === "line") return `<div class="chart-host" data-chart-type="line" data-chart-data="${esc(JSON.stringify(data))}">${lineChartSVG(data)}</div>`;
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

function ensureWidgetStyles() {
  if (document.getElementById("widget-extra-css")) return;
  const style = document.createElement("style");
  style.id = "widget-extra-css";
  style.textContent = `
    .pie-wrap { display: flex; align-items: center; gap: 18px; flex-wrap: wrap; }
    .pie-wrap svg { flex: none; width: 168px; height: 168px; }
    .pie-legend { flex: 1; min-width: 120px; max-width: 100%; display: flex; flex-direction: column; gap: 7px; }
    .pie-slice { opacity: 0.92; cursor: pointer; transition: opacity 0.12s; }
    .pie-chart svg:has(.pie-slice.active) .pie-slice:not(.active) { opacity: 0.45; }
    .pie-slice.active { opacity: 1; }
    .chart-tip-pct { color: var(--muted); font-weight: 500; }
    .pie-legend-dot { width: 8px; height: 8px; border-radius: 1px; flex: none; }
    .pie-legend-row { display: flex; align-items: center; gap: 8px; font-size: 12.5px; }
    .pie-legend-row span:nth-child(2) { flex: 1; color: var(--text-2); }
    .chart-legend { display: flex; flex-wrap: wrap; gap: 4px 14px; padding: 0 4px 8px; font-size: 12px; color: var(--text-2); }
    .chart-legend-item { display: inline-flex; align-items: center; gap: 6px; min-width: 0; max-width: 100%; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .chart-legend-swatch { width: 9px; height: 9px; border-radius: 2px; flex: none; }
    .chart-legend-swatch.line { width: 14px; height: 3px; border-radius: 2px; }
    .chart-hover-tip .chart-tip-row { display: flex; align-items: center; gap: 6px; font-size: 12px; }
    .chart-hover-tip .chart-tip-row.near .chart-tip-name { color: var(--text); font-weight: 600; }
    .chart-hover-tip .chart-tip-dot { width: 8px; height: 8px; border-radius: 2px; flex: none; }
    .chart-hover-tip .chart-tip-name { flex: 1; color: var(--text-2); margin-right: 10px; max-width: 160px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .chart-hover-tip .chart-tip-total { border-top: 1px solid var(--line); margin-top: 3px; padding-top: 3px; padding-left: 14px; }
    .pie-legend-val { color: var(--muted); font-weight: 500; font-variant-numeric: tabular-nums; }
  `;
  document.head.appendChild(style);
}
ensureWidgetStyles();
