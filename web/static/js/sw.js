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

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(ASSETS)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(caches.keys()
    .then((keys) => Promise.all(keys.filter((k) => k.startsWith("glowworm-offline-") && k !== CACHE).map((k) => caches.delete(k))))
    .then(() => self.clients.claim()));
});

const offline = () => caches.match(OFFLINE);

// Off the VPN the server's address doesn't refuse the connection, it just never
// answers, and a page load would hang for a minute or more on a blank screen.
// Give up after this long and show the offline page; it reloads once Glowworm
// answers again.
const NAV_TIMEOUT_MS = 4000;

function fetchWithTimeout(req, ms) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), ms);
  return fetch(req, { signal: ctrl.signal }).finally(() => clearTimeout(timer));
}

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (req.method !== "GET") return;
  if (req.mode === "navigate") {
    e.respondWith(fetchWithTimeout(req, NAV_TIMEOUT_MS)
      .then((r) => (r.status >= 502 && r.status <= 504 ? offline() : r))
      .catch(offline));
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
