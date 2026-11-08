// Glowworm's service worker does one thing: when a page can't be loaded
// because the server is down (or the proxy in front of it answers 502–504),
// it shows the cached "Can't connect" page instead of a blank screen. App
// pages and data are never cached. __VERSION__ is filled in by the server,
// so a change to the offline page or its styles re-caches them.
const CACHE = "glowworm-offline-__VERSION__";
const OFFLINE = "/static/offline.html";
// The offline page asks for its stylesheet as styles.css?offline, which only
// this worker answers (from the cache), so app pages always get the live one.
const STYLES = "/static/css/styles.css?offline";
const FONTS = ["/static/fonts/inter-latin.woff2", "/static/fonts/inter-latin-ext.woff2"];
const ASSETS = [OFFLINE, STYLES, ...FONTS];

// Pages fetched ahead when the pointer goes down on (or hovers over) a link,
// so the page is often already on its way when the click arrives. Only used
// within a few seconds, and dropped as soon as anything is saved, so a page
// never shows data from before a change.
const PREFETCH_TTL_MS = 10000;
const prefetched = new Map(); // url -> { at, res: Promise<Response|null> }

self.addEventListener("message", (e) => {
  const d = e.data || {};
  if (d.type !== "prefetch" || typeof d.url !== "string") return;
  const url = new URL(d.url);
  if (url.origin !== self.location.origin || prefetched.has(url.href)) return;
  const res = fetchWithTimeout(new Request(url.href, { credentials: "same-origin" }), PREFETCH_TTL_MS)
    .then((r) => (r.ok && !r.redirected ? r : null))
    .catch(() => null);
  prefetched.set(url.href, { at: Date.now(), res });
  setTimeout(() => prefetched.delete(url.href), PREFETCH_TTL_MS);
  e.waitUntil(res);
});

function takePrefetched(url) {
  const hit = prefetched.get(url);
  prefetched.delete(url);
  return hit && Date.now() - hit.at < PREFETCH_TTL_MS ? hit.res : null;
}

// Diagnostics: what happened to recent page loads, kept in the cache and sent
// to the server log (POST /sw-log) on the next load that reaches it. It's
// the only way to see what a home screen app did while it was offline.
const LOG_KEY = "/__sw-log";
const LOG_CACHE = "glowworm-sw-log";
let logChain = Promise.resolve();
function swLog(e) {
  logChain = logChain.then(async () => {
    const c = await caches.open(LOG_CACHE);
    const r = await c.match(LOG_KEY);
    const events = r ? await r.json() : [];
    events.push({ t: Date.now(), e });
    await c.put(LOG_KEY, new Response(JSON.stringify(events.slice(-60))));
  }).catch(() => {});
  return logChain;
}
function flushLog() {
  logChain = logChain.then(async () => {
    const c = await caches.open(LOG_CACHE);
    const r = await c.match(LOG_KEY);
    if (!r) return;
    const events = await r.json();
    if (!events.length) return;
    const res = await fetch("/sw-log", { method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ua: self.navigator.userAgent, events }) });
    if (res.ok) await c.delete(LOG_KEY);
  }).catch(() => {});
  return logChain;
}
swLog("worker started (" + CACHE + ")");

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(ASSETS)).then(() => swLog("installed")).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(caches.keys()
    .then((keys) => Promise.all(keys.filter((k) => k.startsWith("glowworm-offline-") && k !== CACHE).map((k) => caches.delete(k))))
    .then(() => swLog("activated"))
    .then(() => self.clients.claim()));
});

const offline = () => caches.match(OFFLINE);

// Off the VPN the server's address doesn't refuse the connection, it just never
// answers, and a page load would hang for a minute or more on a blank screen.
// Give up after this long and show the offline page; it reloads once Glowworm
// answers again. A load that fails straight away (a dropped connection, the
// server restarting) is tried once more before the offline page shows.
const NAV_TIMEOUT_MS = 8000;
const NAV_RETRY_MS = 1500;

function fetchWithTimeout(req, ms) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), ms);
  return fetch(req, { signal: ctrl.signal }).finally(() => clearTimeout(timer));
}

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (req.method !== "GET") {
    prefetched.clear(); // something is being saved: pages fetched ahead may be stale
    return;
  }
  if (req.mode === "navigate") {
    const t0 = Date.now(), path = new URL(req.url).pathname;
    const showOffline = (why) => {
      e.waitUntil(swLog(`load ${path}: offline page (${why}) after ${Date.now() - t0}ms`));
      return offline().then((r) => {
        if (!r) e.waitUntil(swLog("offline page missing from the cache"));
        return r || Response.error();
      });
    };
    const early = takePrefetched(req.url);
    const first = early ? early.then((r) => r || fetchWithTimeout(req, NAV_TIMEOUT_MS)) : fetchWithTimeout(req, NAV_TIMEOUT_MS);
    const retry = () => new Promise((ok) => setTimeout(ok, NAV_RETRY_MS)).then(() => fetchWithTimeout(req, NAV_TIMEOUT_MS));
    const load = first.then(
      (r) => (r.status >= 502 && r.status <= 504 ? retry() : r),
      (err) => (err && err.name === "AbortError" ? Promise.reject(err) : retry()));
    e.respondWith(load
      .then((r) => {
        if (r.status >= 502 && r.status <= 504) return showOffline("status " + r.status);
        e.waitUntil(flushLog());
        return r;
      })
      .catch((err) => showOffline(err && err.name === "AbortError" ? "timed out" : "network error")));
    return;
  }
  // The offline page's stylesheet and the fonts come straight from the cache:
  // waiting on a server that isn't answering would hold the page blank.
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  const key = url.pathname + url.search;
  if (key === STYLES || FONTS.includes(key)) {
    e.respondWith(caches.match(key).then((c) => c || fetch(req)));
  }
});
