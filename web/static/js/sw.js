// Glowworm's service worker does one thing: when a page can't be loaded
// because the server is down (or the proxy in front of it answers 502–504),
// it shows the cached "Can't connect" page instead of a blank screen. App
// pages and data are never cached. __VERSION__ is filled in by the server,
// so a change to the offline page or its styles re-caches them.
const CACHE = "glowworm-offline-__VERSION__";
const OFFLINE = "/static/offline.html";
const ASSETS = [OFFLINE, "/static/css/styles.css", "/static/fonts/inter-latin.woff2", "/static/fonts/inter-latin-ext.woff2"];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(ASSETS)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(caches.keys()
    .then((keys) => Promise.all(keys.filter((k) => k.startsWith("glowworm-offline-") && k !== CACHE).map((k) => caches.delete(k))))
    .then(() => self.clients.claim()));
});

const offline = () => caches.match(OFFLINE);

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (req.method !== "GET") return;
  if (req.mode === "navigate") {
    e.respondWith(fetch(req).then((r) => (r.status >= 502 && r.status <= 504 ? offline() : r)).catch(offline));
    return;
  }
  // The offline page's own styles and fonts, from the network when it's up.
  const url = new URL(req.url);
  if (url.origin === self.location.origin && ASSETS.includes(url.pathname)) {
    // A proxy in front answers 502 rather than failing, so any error falls back too.
    const cached = () => caches.match(url.pathname);
    e.respondWith(fetch(req).then((r) => (r.ok || r.status === 304 ? r : cached().then((c) => c || r))).catch(cached));
  }
});
