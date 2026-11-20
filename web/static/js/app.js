
function applyTheme(t) {
  const dark = t === "dark" || (t === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
  // The status bar follows the masthead (see layout.html).
  document.querySelectorAll('meta[name="theme-color"]').forEach((m) => { m.content = dark ? "#14181d" : "#ffffff"; });
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

let toastTimer;
// A message that reports a failure gets the alert icon, not a tick.
const FAILURE_RE = /^(could ?n[o']t|can['’]?t|cannot|failed|not |no longer|nothing |too many|invalid|something went wrong)|\b(required|is incorrect|must be|doesn't exist|out of date|isn't answering|was changed somewhere else)\b/i;
function toast(msg, kind) {
  const el = document.getElementById("toast");
  if (!el) return;
  const failed = kind === "error" || (kind !== "ok" && FAILURE_RE.test(String(msg)));
  el.hidden = false;
  el.classList.toggle("toast-error", failed);
  el.innerHTML = `<svg class="icon"><use href="#i-${failed ? "alert" : "check"}"/></svg> ${esc(msg)}`;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (el.hidden = true), Math.max(2600, String(msg).length * 70));
}
function reloadWithToast(msg) {
  try { sessionStorage.setItem("glowworm-toast", msg); } catch (e) {}
  location.reload();
}
async function apiErrorText(r, fallback) {
  return apiErrorFrom(r, await r.json().catch(() => ({})), fallback);
}
// jsonOk reads a response's JSON, or throws the server's explanation when
// the request failed, so a failure never renders as an empty result.
async function jsonOk(r, fallback) {
  if (!r.ok) throw new Error(await apiErrorText(r, fallback || "The server couldn't do that"));
  return r.json();
}
// apiErrorFrom is apiErrorText for a body that has already been read.
function apiErrorFrom(r, b, fallback) {
  const err = (b && b.error) || {};
  if (r.status === 401) return "You've been signed out. Sign in again to carry on.";
  if (r.status >= 502 && r.status <= 504) return "Glowworm isn't answering right now (it may be restarting). Try again in a moment.";
  if (r.status === 403 && err.code === "csrf") return "This page is out of date. Reload it and try again.";
  return err.message || fallback;
}

// ---- Connection. Every request to the server goes through this fetch: it
// gives up on one that hasn't started answering after 30s, shows a banner
// while the server can't be reached (and checks until it can), says so when
// the session has ended, and notes the data version each response carries.
const nativeFetch = window.fetch.bind(window);
const FETCH_TIMEOUT_MS = 30000;
// These wait on the AI, a file or a long job, so they get no time limit.
const SLOW_PATH_RE = /^\/api\/v1\/(query|imports|instance\/backup|settings\/ai|transfers\/detect|accounts\/archives)/;
window.fetch = function (input, init) {
  init = init || {};
  const url = new URL(typeof input === "string" ? input : input.url, location.href);
  if (url.origin !== location.origin) return nativeFetch(input, init);
  const method = (init.method || (input && input.method) || "GET").toUpperCase();
  let ctrl = null, timer = null;
  if (!init.signal && !SLOW_PATH_RE.test(url.pathname)) {
    ctrl = new AbortController();
    init = Object.assign({}, init, { signal: ctrl.signal });
    timer = setTimeout(() => ctrl.abort(), FETCH_TIMEOUT_MS);
  }
  dataVersion.started();
  return nativeFetch(input, init).then((r) => {
    dataVersion.finished();
    clearTimeout(timer);
    if (r.status >= 502 && r.status <= 504) connection.lost("server");
    else connection.ok();
    if (r.status === 401 && url.pathname.startsWith("/api/")) connection.signedOut();
    const v = r.headers.get("X-Glowworm-Version");
    if (v && method !== "GET" && method !== "HEAD") dataVersion.ownChange(v);
    return r;
  }, (err) => {
    dataVersion.finished();
    clearTimeout(timer);
    const timedOut = ctrl && ctrl.signal.aborted;
    if (err && err.name === "AbortError" && !timedOut) throw err; // the caller cancelled it
    connection.lost(timedOut ? "timeout" : "network");
    throw new Error(timedOut ? "Glowworm took too long to answer." : "Can't reach Glowworm.");
  });
};

const connection = (() => {
  // A failed request only shows the banner if the server still can't be
  // reached a couple of seconds later: requests cut off by leaving the page
  // (Safari reports those as network errors) or a moment's blip don't count.
  const CONFIRM_MS = 2500;
  let banner = null, state = "ok", poll = null, pending = null, leaving = false;
  addEventListener("pagehide", () => { leaving = true; });
  addEventListener("pageshow", () => { leaving = false; });
  const show = (html, cls) => {
    if (!banner) {
      banner = document.createElement("div");
      banner.className = "conn-banner";
      banner.setAttribute("role", "status");
      (document.querySelector(".app.shell") || document.body).prepend(banner);
    }
    banner.className = "conn-banner " + (cls || "");
    banner.innerHTML = html;
    banner.hidden = false;
  };
  const hide = () => { if (banner) banner.hidden = true; };
  const reachable = () => {
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), 4000);
    return nativeFetch("/healthz", { cache: "no-store", signal: ctrl.signal })
      .then((r) => r.ok, () => false)
      .finally(() => clearTimeout(t));
  };
  const check = () => reachable().then((up) => { if (up) api.ok(); });
  const confirmLost = (why) => {
    pending = null;
    if (leaving || state !== "ok") return;
    reachable().then((up) => { if (!up && !leaving && state === "ok") api.showLost(why); });
  };
  const api = {
    lost(why) {
      if (state === "lost") api.showLost(why);
      else if (state === "ok" && !leaving && !pending) pending = setTimeout(() => confirmLost(why), CONFIRM_MS);
    },
    showLost(why) {
      if (state === "signedOut") return;
      state = "lost";
      show(`<svg class="icon"><use href="#i-alert"/></svg> ${why === "server"
        ? "Glowworm isn't answering (it may be restarting)."
        : why === "timeout" ? "Glowworm is taking too long to answer." : "Can't reach Glowworm: check your connection."}
        Changes won't save until it's back. <button class="btn btn-ghost btn-xs" type="button" data-conn-retry>Try now</button>`, "conn-lost");
      if (!poll) poll = setInterval(check, 5000);
    },
    ok() {
      clearTimeout(pending);
      pending = null;
      if (state !== "lost") return;
      state = "ok";
      clearInterval(poll);
      poll = null;
      hide();
      toast("Connected again", "ok");
      dataVersion.check();
    },
    signedOut() {
      if (state === "signedOut") return;
      state = "signedOut";
      clearInterval(poll);
      poll = null;
      show(`<svg class="icon"><use href="#i-alert"/></svg> You've been signed out (perhaps the password was changed on another device).
        <a class="btn btn-primary btn-xs" href="/login">Sign in</a>`, "conn-lost");
    },
    check,
  };
  window.addEventListener("offline", () => api.lost("network"));
  window.addEventListener("online", check);
  document.addEventListener("click", (e) => { if (e.target.closest("[data-conn-retry]")) check(); });
  return api;
})();

// ---- Other devices. The server's data version changes whenever anything is
// saved. Every 20s while this page is in view, and when it comes back into
// view, it compares the version it was built from; if another device or tab
// has saved something since, the page reloads itself with the new data,
// keeping its scroll position. It waits while reloading would lose anything:
// something typed, an open pop-up, a request still going, rows loaded
// further down, or the person mid-gesture.
const dataVersion = (() => {
  const meta = document.querySelector('meta[name="glowworm-version"]');
  let known = meta ? meta.content : "";
  let stale = false;
  let seen = 0; // the newer version a check found
  let inflight = 0;
  let lastTouch = 0;
  let holds = 0;
  const SCROLL_KEY = "glowworm-refresh-scroll";

  // Form values when the page finished setting itself up; anything that
  // differs since was changed by the person.
  const fields = () => document.querySelectorAll("input:not([type=hidden]), textarea, select");
  const valueOf = (el) => (el.type === "checkbox" || el.type === "radio" ? String(el.checked)
    : el.tagName === "SELECT" && el.multiple ? [...el.selectedOptions].map((o) => o.value).join("\n") : el.value);
  let initial = new Map();
  const snapshot = () => { initial = new Map([...fields()].map((el) => [el, valueOf(el)])); };

  const busy = () => {
    if (inflight > 0 || holds > 0 || state.editMode) return true;
    if (Date.now() - lastTouch < 1500) return true;
    const a = document.activeElement;
    if (a && (a.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName))) return true;
    if (document.querySelector(".modal-backdrop:not([hidden]), .drawer.show, .chat-panel.floating.open, .info-popover:not([hidden])")) return true;
    const sel = window.getSelection && window.getSelection();
    if (sel && !sel.isCollapsed) return true;
    for (const el of fields()) {
      if (el.type === "password" && el.value) return true;
      if (initial.has(el) ? initial.get(el) !== valueOf(el) : el.value) return true;
    }
    return false;
  };

  const refresh = () => {
    if (!stale || document.hidden || busy()) return;
    stale = false;
    try { sessionStorage.setItem(SCROLL_KEY, JSON.stringify({ url: location.href, y: pageScroller().scrollTop })); } catch (e) {}
    // A replace, not reload(), so it gets the same crossfade as moving
    // between pages instead of a blank frame.
    const reload = () => location.replace(location.href);
    // The service worker's kept copies of the tabs are out of date too:
    // have them dropped first, or the reload would get this one again.
    const sw = navigator.serviceWorker && navigator.serviceWorker.controller;
    if (!sw || !window.MessageChannel) { reload(); return; }
    const ch = new MessageChannel();
    const t = setTimeout(reload, 1000);
    ch.port1.onmessage = () => { clearTimeout(t); reload(); };
    sw.postMessage({ type: "drop-copies" }, [ch.port2]);
  };

  const api = {
    // A save made by this page: its new version isn't news. A check can see
    // it before the save's own response arrives; if this save accounts for
    // the change the check found, there's nothing to refresh for.
    ownChange(v) {
      known = v;
      if (stale && Number(v) >= seen) stale = false;
    },
    // Requests through the fetch wrapper; a reload would cut them off.
    started() { inflight++; },
    finished() { inflight = Math.max(0, inflight - 1); },
    // hold is for state a reload would drop: rows loaded further down, an
    // answer still streaming in, an import under way. It returns the release.
    hold() {
      holds++;
      let held = true;
      return () => { if (held) { held = false; holds--; } };
    },
    check() {
      if (!known || document.hidden) return;
      if (stale) { refresh(); return; }
      nativeFetch("/api/v1/version", { cache: "no-store" })
        .then((r) => (r.ok ? r.json() : null))
        .then((b) => {
          if (!b || !b.version || b.version === known || Number(b.version) <= Number(known)) return;
          seen = Number(b.version);
          stale = true;
          refresh();
        })
        .catch(() => {});
    },
  };

  for (const ev of ["pointerdown", "touchstart", "keydown", "wheel", "scroll"]) {
    addEventListener(ev, () => { lastTouch = Date.now(); }, { passive: true, capture: true });
  }
  document.addEventListener("DOMContentLoaded", () => {
    try {
      const saved = JSON.parse(sessionStorage.getItem(SCROLL_KEY) || "null");
      sessionStorage.removeItem(SCROLL_KEY);
      if (saved && saved.url === location.href) pageScroller().scrollTop = saved.y;
    } catch (e) {}
    setTimeout(snapshot, 0); // after the page's own setup has filled its fields
    // A copy of the page the service worker kept: has anything been saved
    // since it was made?
    if (document.querySelector('meta[name="glowworm-copy"]')) setTimeout(api.check, 0);
  });
  document.addEventListener("visibilitychange", () => { if (!document.hidden) api.check(); });
  window.addEventListener("pageshow", (e) => { if (e.persisted) api.check(); });
  setInterval(api.check, 20000);
  // Once a change is waiting, try again soon after the page is left alone.
  setInterval(() => { if (stale) refresh(); }, 2000);
  return api;
})();
// ---- Feeling like an app on a phone.
//
// History: in a home screen app, swiping in from the left edge goes back.
// Moving between the tabs, changing the date range or sort, and signing in
// or out replace the page instead of adding to its history, so there's
// nothing to swipe back to (least of all the sign-in page). Going into
// something from a page, such as a dashboard link to filtered
// transactions, still adds to it, so swiping back returns from it.
//
// In the home screen app nothing adds to history at all: there's no back
// button, and swiping in from the edge would fight with swiping between the
// tabs (below). Every link and filter replaces the page, so the app always
// has just the one page and an edge swipe has nothing to go back to.
const homeScreenApp = window.matchMedia("(display-mode: standalone)").matches || navigator.standalone === true;
const replaceLink = (e, a) => {
  if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  if (a.target || a.hasAttribute("download")) return;
  const u = new URL(a.href, location.href);
  if (u.origin !== location.origin || /^\/(api|static)\//.test(u.pathname)) return;
  if (u.pathname === location.pathname && u.search === location.search && u.hash) return; // a jump within the page
  e.preventDefault();
  if (a.matches(".tabnav-item")) { markTabSwitch(); location.replace(tabHref(u.href)); return; }
  location.replace(u.href);
};
document.addEventListener("click", (e) => replaceLink(e, e.target.closest && e.target.closest(".masthead a[href]")));
// On window, so it runs after the page's own link handlers have had their say.
if (homeScreenApp) window.addEventListener("click", (e) => replaceLink(e, e.target.closest && e.target.closest("a[href]")));
document.addEventListener("submit", (e) => {
  const form = e.target;
  if (!homeScreenApp || e.defaultPrevented || !form.matches || !form.matches("form") || (form.method || "get").toLowerCase() !== "get") return;
  e.preventDefault();
  const u = new URL(form.action || location.href, location.href);
  u.search = new URLSearchParams(new FormData(form)).toString();
  location.replace(u.href);
});
// A select marked data-autosubmit applies as soon as something is chosen.
document.addEventListener("change", (e) => {
  const el = e.target;
  if (el && el.matches && el.matches("select[data-autosubmit]") && el.form) {
    if (el.form.requestSubmit) el.form.requestSubmit(); else el.form.submit();
  }
});
// Forms marked data-replace post in the background and then replace the
// page with wherever the server sent them.
document.addEventListener("submit", (e) => {
  const form = e.target.closest && e.target.closest("form[data-replace]");
  if (!form || e.defaultPrevented) return;
  e.preventDefault();
  const btn = form.querySelector("[type=submit]");
  if (btn) btn.disabled = true;
  fetch(form.action, { method: "POST", body: new URLSearchParams(new FormData(form)) })
    .then((r) => {
      if (!r.redirected) throw new Error("no redirect");
      location.replace(r.url);
    })
    .catch(() => {
      if (btn) btn.disabled = false;
      HTMLFormElement.prototype.submit.call(form); // the ordinary way
    });
});

// Swiping between tabs, in the home screen app on a phone: a sideways
// swipe across the page goes to the tab beside this one, the page following
// the finger with the next tab right beside it. Swipes that start where a
// sideways drag already means something are left alone: a text box, a line
// chart (it pans), anything that scrolls sideways, an open pop-up, and the
// screen's edges (the system's own gestures).
//
// The tab coming in is shown as it looked when it was last left (kept in
// sessionStorage); a tab not seen yet is fetched when the finger lands and
// shown from that (without its charts, which scripts draw). Once the swipe
// ends the real page loads in its place. On a slow connection a spinner shows
// until it arrives. Both neighbouring tabs are fetched ahead as soon as a
// finger lands on the page, so by the time a swipe ends the page is often
// already here (and the service worker keeps recent ones).
//
// Swiping again before that page has arrived carries on from the tab now
// showing: its load is paused (so it can't land mid-swipe), and whichever
// tab the swipes end on is the one loaded.
(() => {
  if (!homeScreenApp || !window.matchMedia("(pointer: coarse)").matches) return;
  const EDGE = 24, LOCK = 10, SLIDE_MS = 180;
  const SNAP = "glowworm-snap:", SNAP_MAX = 1500000; // characters
  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
  let s = null;
  // Each tab as { href, link, active } (href: see tabHref).
  const tabs = () => [...document.querySelectorAll(".tabnav .tabnav-item")]
    .filter((a) => a.getClientRects().length)
    .map((a) => ({ href: tabHref(a.href), link: a, active: a.classList.contains("active") }));
  // The tab bar shows the tab swiped to straight away.
  const markActive = (tab) => {
    document.querySelectorAll(".tabnav .tabnav-item.active").forEach((a) => a.classList.remove("active"));
    tab.link.classList.add("active");
  };
  const leftAlone = (el) => {
    if (document.documentElement.classList.contains("scroll-locked")) return true;
    if (document.querySelector(".modal-backdrop:not([hidden]), .drawer.show, .chat-panel.floating.open")) return true;
    if (!el.closest || el.closest('input, textarea, select, [contenteditable], .chart-host[data-chart-type="line"], .widgets-grid.editing, [data-no-swipe]')) return true;
    for (let n = el; n && n !== document.body; n = n.parentElement) {
      const ox = getComputedStyle(n).overflowX;
      if ((ox === "auto" || ox === "scroll") && n.scrollWidth > n.clientWidth + 1) return true;
    }
    const sel = window.getSelection && window.getSelection();
    return !!(sel && !sel.isCollapsed);
  };
  const content = () => document.getElementById("content");
  const pageKey = (href) => { const u = new URL(href, location.href); return SNAP + u.pathname + u.search; };

  // This page as it looks now, for the next time it's swiped in.
  const snapshot = () => {
    const c = content();
    if (!c) return;
    // Fields as they show now (a date or a choice the page filled in), not
    // as the page arrived; text typed in is left out.
    const copy = c.cloneNode(true);
    const from = c.querySelectorAll("input, select"), to = copy.querySelectorAll("input, select");
    from.forEach((el, i) => {
      const d = to[i];
      if (!d) return;
      if (el.tagName === "SELECT") {
        [...el.options].forEach((o, j) => { if (d.options[j]) d.options[j].toggleAttribute("selected", o.selected); });
      } else if (el.type === "checkbox" || el.type === "radio") {
        d.toggleAttribute("checked", el.checked);
      } else if (/^(date|month|time|datetime-local|range|color)$/.test(el.type)) {
        d.setAttribute("value", el.value);
      }
    });
    const html = copy.innerHTML;
    if (html.length > SNAP_MAX) return;
    const key = pageKey(location.href);
    try {
      sessionStorage.setItem(key, html);
    } catch (e) {
      // Out of room: keep only this one.
      try {
        Object.keys(sessionStorage).filter((k) => k.startsWith(SNAP)).forEach((k) => sessionStorage.removeItem(k));
        sessionStorage.setItem(key, html);
      } catch (e2) {}
    }
  };
  addEventListener("pagehide", snapshot);

  // The tab coming in, beside this one: a still copy over the content's
  // area, after it in the page so its ids never shadow the real ones.
  let peek = null; // { el, side, href }
  const showPeek = (to, side) => {
    if (peek && peek.href === to.href && peek.side === side) return;
    hidePeek();
    const c = content();
    if (!c || !s) return;
    const r = s.frame;
    const el = document.createElement("div");
    el.className = "content swipe-peek";
    el.inert = true;
    el.setAttribute("aria-hidden", "true");
    Object.assign(el.style, { top: r.top + "px", left: r.left + "px", width: r.width + "px", height: r.height + "px" });
    let html = null;
    try { html = sessionStorage.getItem(pageKey(to.href)); } catch (e) {}
    if (html) el.innerHTML = html;
    (going ? going.el : c).after(el);
    peek = { el, side, href: to.href, width: r.width };
    if (html) peekScroll();
  };
  // The tab coming in scrolled to where its page will open (see
  // restoreScroll and initQueriesScroll).
  const peekScroll = () => {
    if (!peek) return;
    peek.el.scrollTop = savedScroll(peek.href);
    const q = peek.el.querySelector("#queryMessages");
    if (q) {
      const conv = (peek.el.querySelector(".queries-main") || { dataset: {} }).dataset.conversationId;
      let pos = null;
      try { pos = conv ? sessionStorage.getItem("glowworm-qpos-" + conv) : null; } catch (e) {}
      q.scrollTop = pos !== null && pos !== "bottom" ? Number(pos) || 0 : q.scrollHeight;
    }
  };
  const hidePeek = () => { if (peek) { peek.el.remove(); peek = null; } };

  // After a swipe, until its page loads: the tab now showing in the page's
  // place, and where it's going. { href, list, at, frame, el, timer, loading }
  let going = null;
  const go = () => {
    if (!going) return;
    going.loading = true;
    location.replace(going.href);
  };
  // dx: how far the page (or the tab in its place) has moved; the tab
  // beside it keeps alongside.
  const place = (dx, animate) => {
    const c = going ? going.el : content();
    if (!c) return;
    const t = animate ? `transform ${SLIDE_MS}ms ease-out` : "none";
    c.style.transition = t;
    c.style.transform = dx ? `translateX(${dx}px)` : "";
    if (peek) {
      peek.el.style.transition = t;
      peek.el.style.transform = `translateX(${dx + peek.side * peek.width}px)`;
    }
  };
  const prefetch = (a) => {
    if (a && navigator.serviceWorker && navigator.serviceWorker.controller) {
      navigator.serviceWorker.controller.postMessage({ type: "prefetch", url: new URL(a.href, location.href).href });
    }
  };
  // A neighbouring tab, fetched ahead. One with no picture yet is fetched
  // here, marked so the service worker answers from its kept copy or keeps
  // the response for the page load that follows; its content becomes the
  // picture.
  const fetching = new Set();
  const ahead = (a) => {
    if (!a) return;
    let snap = null;
    try { snap = sessionStorage.getItem(pageKey(a.href)); } catch (e) {}
    if (snap) { prefetch(a); return; }
    if (fetching.has(a.href)) return;
    fetching.add(a.href);
    nativeFetch(a.href, { credentials: "same-origin", headers: { "X-Glowworm-Peek": "1" } })
      .then((r) => (r.ok && !r.redirected ? r.text() : null))
      .then((html) => {
        if (!html) return;
        const c = new DOMParser().parseFromString(html, "text/html").getElementById("content");
        if (!c) return;
        try { if (!sessionStorage.getItem(pageKey(a.href))) sessionStorage.setItem(pageKey(a.href), c.innerHTML); } catch (e) {}
        if (peek && peek.href === a.href && !peek.el.childElementCount) { peek.el.innerHTML = c.innerHTML; peekScroll(); }
      })
      .catch(() => {})
      .finally(() => fetching.delete(a.href));
  };
  // Shown while the next page is on its way; it fades in only after a
  // moment (CSS), so a quick load never flashes it.
  const waiting = (on) => {
    let w = document.getElementById("swipeWait");
    if (!on) { if (w) w.remove(); return; }
    if (w) return;
    w = document.createElement("div");
    w.id = "swipeWait";
    w.className = "swipe-wait";
    w.setAttribute("role", "status");
    w.setAttribute("aria-label", "Loading");
    w.innerHTML = '<div class="spinner"></div>';
    document.body.appendChild(w);
  };
  const reset = () => {
    if (s && s.locked === "x") {
      place(0, true);
      setTimeout(() => {
        if (s) return;
        hidePeek();
        const c = content();
        if (c && !going) c.style.transition = "";
        if (going && !going.loading) go();
      }, SLIDE_MS + 20);
    } else if (going && !going.loading) {
      go();
    }
    s = null;
  };
  document.addEventListener("touchstart", (e) => {
    s = null;
    if (e.touches.length !== 1) return;
    const t = e.touches[0];
    let list, at, frame;
    if (going) {
      // A swipe is still on its way: hold its page until this touch is done.
      clearTimeout(going.timer);
      if (going.loading) { going.loading = false; window.stop(); }
      ({ list, at, frame } = going);
      if (t.clientX < EDGE || t.clientX > innerWidth - EDGE) return;
    } else {
      if (t.clientX < EDGE || t.clientX > innerWidth - EDGE || leftAlone(e.target)) return;
      list = tabs();
      at = list.findIndex((a) => a.active);
      const c = content();
      if (at < 0 || !c) return;
      const r = c.getBoundingClientRect();
      frame = { top: r.top, left: r.left, width: r.width, height: r.height };
    }
    s = { x: t.clientX, y: t.clientY, time: Date.now(), dx: 0, locked: null, list, at, frame, to: null };
    ahead(list[at - 1]);
    ahead(list[at + 1]);
  }, { passive: true });
  document.addEventListener("touchmove", (e) => {
    if (!s) return;
    if (e.touches.length !== 1) { reset(); return; }
    const t = e.touches[0];
    const dx = t.clientX - s.x, dy = t.clientY - s.y;
    if (!s.locked) {
      if (Math.abs(dx) < LOCK && Math.abs(dy) < LOCK) return;
      // Sideways only when clearly so, and only while the page can still be
      // held: once iOS has started scrolling it can't be.
      if (Math.abs(dx) < Math.abs(dy) * 1.5 || !e.cancelable) { s = null; if (going && !going.loading) go(); return; }
      s.locked = "x";
    }
    e.preventDefault();
    const to = s.list[s.at + (dx < 0 ? 1 : -1)] || null;
    s.to = to;
    s.dx = dx;
    if (to) showPeek(to, dx < 0 ? 1 : -1); else hidePeek();
    // Past the first or last tab the page only gives a little.
    place(to ? dx : dx / 5, false);
  }, { passive: false });
  const end = () => {
    if (!s || s.locked !== "x") {
      s = null;
      if (going && !going.loading) go();
      return;
    }
    const { dx, to, time, list, frame } = s;
    const fast = Math.abs(dx) > 30 && Math.abs(dx) / Math.max(Date.now() - time, 1) > 0.3;
    if (!to || !(Math.abs(dx) > innerWidth * 0.2 || fast)) { reset(); return; }
    const toAt = s.at + (dx < 0 ? 1 : -1);
    s = null;
    if (!going) snapshot();
    markTabSwitch();
    markActive(to);
    waiting(true);
    if (reduced.matches || !peek) {
      going = { href: to.href, list, at: toAt, frame, el: going ? going.el : content(), timer: null, loading: false };
      go();
      return;
    }
    // The next tab slides the rest of the way in, then its page loads in
    // the same place.
    place(dx < 0 ? -peek.width : peek.width, true);
    const before = going && going.el;
    going = { href: to.href, list, at: toAt, frame, el: peek.el, timer: null, loading: false };
    peek = null;
    going.timer = setTimeout(() => {
      if (before) before.remove();
      go();
    }, SLIDE_MS);
  };
  document.addEventListener("touchend", end, { passive: true });
  document.addEventListener("touchcancel", reset, { passive: true });
  // Coming back to a page kept in memory: put it straight.
  window.addEventListener("pageshow", () => {
    s = null;
    if (going) { clearTimeout(going.timer); if (going.el !== content()) going.el.remove(); going = null; }
    hidePeek();
    place(0, false);
    waiting(false);
  });
})();

// Pop-ups: while one is open the page behind stays put. iOS ignores
// overflow: hidden for touch scrolling, so a drag, wherever it starts
// (a text box included), only scrolls something inside the pop-up that can
// scroll, such as the pop-up itself when it's taller than the screen, and
// not past its top or bottom; anything else is held. Once a scroll is under
// way iOS no longer lets a drag be held, so overscroll-behavior: contain
// keeps it from carrying on to the page.
const scrollLock = (() => {
  const OPEN = ".modal-backdrop:not([hidden]), .drawer.show";
  const phone = window.matchMedia("(max-width: 760px)");
  let locked = false;
  const sync = () => {
    // On a phone the chat panel covers the page too; on a wider screen it
    // sits beside it and the page still scrolls.
    locked = !!document.querySelector(OPEN) || (phone.matches && !!document.querySelector(".chat-panel.floating.open"));
    document.documentElement.classList.toggle("scroll-locked", locked);
  };
  // The nearest thing under the finger that can scroll, short of the page.
  const scroller = (el) => {
    for (; el && el !== document.body && el !== document.documentElement; el = el.parentElement) {
      const oy = getComputedStyle(el).overflowY;
      if ((oy === "auto" || oy === "scroll") && el.scrollHeight > el.clientHeight + 1) return el;
    }
    return null;
  };
  let startY = 0;
  document.addEventListener("touchstart", (e) => {
    if (e.touches.length === 1) startY = e.touches[0].clientY;
  }, { passive: true });
  document.addEventListener("touchmove", (e) => {
    if (!locked || e.touches.length !== 1) return;
    if (e.target.type === "range") return; // dragging a slider
    const el = scroller(e.target);
    const y = e.touches[0].clientY;
    const dy = y - startY; // > 0: finger moving down, scrolling up
    startY = y;
    // At its top or bottom, iOS would carry the drag on to the page.
    const atTop = el && el.scrollTop <= 0 && dy > 0;
    const atEnd = el && el.scrollTop + el.clientHeight >= el.scrollHeight - 1 && dy < 0;
    if (!el || atTop || atEnd) e.preventDefault();
  }, { passive: false });
  document.addEventListener("DOMContentLoaded", () => {
    sync();
    new MutationObserver(sync).observe(document.body, { subtree: true, attributes: true, attributeFilter: ["hidden", "class"] });
  });
  return { sync };
})();

// (i) buttons: [data-info-toggle] names a hidden .info-popover, which opens as
// a speech bubble floating by the button. While open it lives in <body> so no
// overflow or transform above it can clip it; closing puts it back.
let openInfo = null; // { btn, pop, home: placeholder comment }
function placeInfoPopover() {
  if (!openInfo) return;
  const { btn, pop } = openInfo;
  if (!btn.isConnected) { closeInfoPopover(); return; }
  const gutter = 16, gap = 10;
  const b = btn.getBoundingClientRect();
  const w = pop.offsetWidth, h = pop.offsetHeight;
  const mid = b.left + b.width / 2;
  const left = Math.max(gutter, Math.min(mid - 18, window.innerWidth - gutter - w));
  const below = b.bottom + gap + h <= window.innerHeight - gutter || b.top - gap - h < gutter;
  pop.classList.toggle("above", !below);
  pop.style.left = `${left}px`;
  pop.style.top = `${below ? b.bottom + gap : b.top - gap - h}px`;
  pop.style.setProperty("--arrow-x", `${Math.max(12, Math.min(mid - left, w - 12))}px`);
}
function closeInfoPopover() {
  if (!openInfo) return;
  const { btn, pop, home } = openInfo;
  openInfo = null;
  pop.hidden = true;
  btn.setAttribute("aria-expanded", "false");
  // The page may have been re-rendered under it; then the old note just goes.
  if (home.isConnected) home.replaceWith(pop); else { pop.remove(); home.remove(); }
}
function openInfoPopover(btn, pop) {
  closeInfoPopover();
  const home = document.createComment("info-popover");
  pop.replaceWith(home);
  document.body.appendChild(pop);
  openInfo = { btn, pop, home };
  pop.hidden = false;
  btn.setAttribute("aria-expanded", "true");
  placeInfoPopover();
}
document.addEventListener("click", (e) => {
  const btn = e.target.closest("[data-info-toggle]");
  if (btn) {
    const pop = document.getElementById(btn.dataset.infoToggle);
    if (!pop) return;
    e.preventDefault(); // the date one sits inside a <label>
    if (openInfo && openInfo.pop === pop) closeInfoPopover(); else openInfoPopover(btn, pop);
    return;
  }
  if (openInfo && !openInfo.pop.contains(e.target)) closeInfoPopover();
});
// Capture, so Escape shuts just the bubble and not a dialog it sits in.
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || !openInfo) return;
  e.stopImmediatePropagation();
  const b = openInfo.btn;
  closeInfoPopover();
  b.focus();
}, true);
window.addEventListener("resize", placeInfoPopover);
document.addEventListener("scroll", placeInfoPopover, true);

// App pages scroll inside .content rather than the window (see .app.shell).
function pageScroller() {
  return document.querySelector(".app.shell .content") || document.scrollingElement || document.documentElement;
}
// Scroll positions: each page's is kept as it's left, and put back on Back
// and when moving between the tabs (a swipe, a tab, a number key), so a
// long page is where it was. Within this session only.
const SCROLL_PREFIX = "glowworm-scroll:";
const TAB_SWITCH = "glowworm-tab-switch";
const scrollKey = (href) => { const u = new URL(href, location.href); return SCROLL_PREFIX + u.pathname + u.search; };
const savedScroll = (href) => { try { return Number(sessionStorage.getItem(scrollKey(href))) || 0; } catch (e) { return 0; } };
// Called just before going to another tab.
function markTabSwitch() {
  try { sessionStorage.setItem(TAB_SWITCH, "1"); } catch (e) {}
}
// The tab link to follow: Queries goes straight to the conversation last
// open, which it would otherwise load only to redirect to.
function tabHref(href) {
  const u = new URL(href, location.href);
  if (u.pathname === "/queries" && !u.searchParams.has("c")) {
    try {
      const conv = sessionStorage.getItem("glowworm-queries-conv");
      if (conv) u.searchParams.set("c", conv);
    } catch (e) {}
  }
  return u.href;
}
// Puts box back at y, and keeps doing so for a moment while the page is
// still growing (charts drawn, more rows loaded) unless the person scrolls.
function restoreScroll(box, y) {
  const until = Date.now() + 3000;
  let stop = false;
  const quit = () => { stop = true; };
  for (const t of ["touchstart", "wheel", "keydown", "pointerdown"]) addEventListener(t, quit, { once: true, passive: true, capture: true });
  const step = () => {
    if (stop) return;
    box.scrollTop = y;
    if (Math.abs(box.scrollTop - y) > 2 && Date.now() < until) requestAnimationFrame(step);
  };
  step();
}
document.addEventListener("DOMContentLoaded", () => {
  const box = document.querySelector(".app.shell .content");
  if (!box) return;
  // Space, Page Down and the arrow keys scroll whatever has focus.
  box.tabIndex = -1;
  if (document.activeElement === document.body) box.focus({ preventScroll: true });
  const key = scrollKey(location.href);
  addEventListener("pagehide", () => {
    try { sessionStorage.setItem(key, String(box.scrollTop)); } catch (e) {}
  });
  let tabSwitch = false;
  try {
    tabSwitch = sessionStorage.getItem(TAB_SWITCH) === "1";
    sessionStorage.removeItem(TAB_SWITCH);
  } catch (e) {}
  let back = false;
  try { back = performance.getEntriesByType("navigation")[0].type === "back_forward"; } catch (e) {}
  const y = savedScroll(location.href);
  if ((tabSwitch || back) && y > 0) restoreScroll(box, y);
});

// The keyboard: while it's up iOS lets the page scroll further, and in a
// home screen app it can leave the page scrolled into space that's no
// longer there once it goes. When it goes, put the page back within its
// content.
(() => {
  const field = (el) => el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName));
  const settle = () => {
    if (field(document.activeElement)) return;
    const max = Math.max(0, document.documentElement.scrollHeight - document.documentElement.clientHeight);
    window.scrollTo(window.scrollX, Math.min(window.scrollY, max));
  };
  document.addEventListener("focusout", () => setTimeout(settle, 120));
  const vv = window.visualViewport;
  if (vv) {
    let up = false;
    vv.addEventListener("resize", () => {
      const now = vv.height < window.innerHeight - 120;
      if (up && !now) setTimeout(settle, 60);
      up = now;
    });
  }
})();

// ---- Passkeys. The server sends WebAuthn options as JSON with
// binary fields in base64url; the browser wants ArrayBuffers, and back.
const passkeys = (() => {
  const dec = (s) => {
    const b = atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4));
    return Uint8Array.from(b, (c) => c.charCodeAt(0)).buffer;
  };
  const enc = (buf) => btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  const post = (url, body) => fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: body ? JSON.stringify(body) : "{}",
  }).then((r) => jsonOk(r));
  const supported = () => !!(window.PublicKeyCredential && navigator.credentials);
  return {
    supported,
    // create sets up a passkey on this device; rejects with the browser's
    // InvalidStateError if this device already has one.
    async create() {
      const { publicKey: o } = await post("/api/v1/passkeys/register/begin");
      o.challenge = dec(o.challenge);
      o.user.id = dec(o.user.id);
      (o.excludeCredentials || []).forEach((c) => (c.id = dec(c.id)));
      const cred = await navigator.credentials.create({ publicKey: o });
      return post("/api/v1/passkeys/register/finish", {
        id: cred.id, rawId: enc(cred.rawId), type: cred.type,
        authenticatorAttachment: cred.authenticatorAttachment || undefined,
        clientExtensionResults: cred.getClientExtensionResults(),
        response: {
          clientDataJSON: enc(cred.response.clientDataJSON),
          attestationObject: enc(cred.response.attestationObject),
          transports: cred.response.getTransports ? cred.response.getTransports() : [],
        },
      });
    },
    // signIn asks for any passkey this browser can reach for Glowworm and
    // signs in with it. conditional offers them in the username field's
    // autofill instead of asking straight away; signal cancels the request.
    async signIn({ conditional = false, signal } = {}) {
      const { publicKey: o } = await post("/login/passkey/begin");
      o.challenge = dec(o.challenge);
      (o.allowCredentials || []).forEach((c) => (c.id = dec(c.id)));
      const cred = await navigator.credentials.get({ publicKey: o, signal, mediation: conditional ? "conditional" : undefined });
      return post("/login/passkey/finish", {
        id: cred.id, rawId: enc(cred.rawId), type: cred.type,
        authenticatorAttachment: cred.authenticatorAttachment || undefined,
        clientExtensionResults: cred.getClientExtensionResults(),
        response: {
          clientDataJSON: enc(cred.response.clientDataJSON),
          authenticatorData: enc(cred.response.authenticatorData),
          signature: enc(cred.response.signature),
          userHandle: cred.response.userHandle ? enc(cred.response.userHandle) : undefined,
        },
      });
    },
    conditionalSupported: async () => !!(window.PublicKeyCredential && PublicKeyCredential.isConditionalMediationAvailable &&
      await PublicKeyCredential.isConditionalMediationAvailable().catch(() => false)),
    // cancelled is the error the browser gives when the passkey prompt is
    // dismissed (or not allowed without a tap).
    cancelled: (err) => err && (err.name === "NotAllowedError" || err.name === "AbortError"),
  };
})();

function restoredMessage(res) {
  const n = res.transactions || 0;
  let msg = `Restored “${res.name}” · ${n} transaction${n === 1 ? "" : "s"}`;
  if (res.categories_created && res.categories_created.length) {
    msg += ` · added categor${res.categories_created.length === 1 ? "y" : "ies"} ${res.categories_created.join(", ")}`;
  }
  return msg;
}
function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

function openModal(id) {
  const m = document.getElementById(id);
  if (!m) return;
  m.hidden = false;
  // Start typing in the first text field. Not a select or date field: focusing
  // those opens their picker on a phone. And not at all on touch screens,
  // where focus would throw up the keyboard before anything is tapped.
  if (window.matchMedia("(pointer: coarse)").matches) return;
  const field = m.querySelector("input:not([type=hidden]):not([type=date]):not([type=checkbox]):not([type=radio]), textarea");
  if (field) field.focus();
}
function closeModal(id) {
  const m = document.getElementById(id);
  if (!m) return;
  const wasOpen = !m.hidden;
  m.hidden = true;
  // A balance recorded from the account pop-up shows on the page behind.
  if (wasOpen && m.dataset.changed) { delete m.dataset.changed; location.reload(); }
}
function closePasswordModal() {
  closeModal("passwordModal");
  ["pwCurrent", "pwNew", "pwConfirm"].forEach((id) => {
    const el = document.getElementById(id);
    if (el) el.value = "";
  });
}
function closeNameModal() {
  closeModal("nameModal");
}
function showConfirm(title, message, okLabel, callback, tone) {
  document.getElementById("confirmTitle").textContent = title;
  document.getElementById("confirmMessage").textContent = message;
  const ok = document.getElementById("confirmOk");
  ok.textContent = okLabel || "Delete";
  ok.classList.toggle("btn-danger", tone !== "primary");
  ok.classList.toggle("btn-primary", tone === "primary");
  confirmCallback = callback;
  document.getElementById("confirmModal").hidden = false;
}
let confirmCallback = null;

const WIDGET_CONVERSIONS = {
  bar: ["bar", "line", "table"],
  line: ["bar", "line", "table"],
  pie: ["pie", "table"],
  table: ["bar", "line", "pie", "table"],
  number: ["number"],
  progress: ["progress", "table"],
  text: ["text"],
  transactions: ["transactions", "table"],
};

function openDrawer(id) {
  const card = document.querySelector(`[data-widget-id="${id}"]`);
  if (!card) return;
  const drawer = document.getElementById("widgetDrawer");
  if (!drawer || !document.getElementById("drawerBackdrop") || !document.getElementById("wTitle")) return;
  state.editingId = id;
  document.getElementById("wTitle").value = card.dataset.widgetTitle || "";
  const sel = document.getElementById("wType");
  const typ = card.dataset.widgetType || "bar";
  const allowed = WIDGET_CONVERSIONS[typ] || [typ];
  Array.from(sel.options).forEach((o) => {
    o.hidden = !allowed.includes(o.value);
    o.disabled = !allowed.includes(o.value);
  });
  if (!allowed.includes(typ)) {
    const opt = document.createElement("option");
    opt.value = typ;
    opt.textContent = typ.charAt(0).toUpperCase() + typ.slice(1);
    sel.appendChild(opt);
  }
  sel.value = typ;
  state.editingPlan = card.dataset.widgetPlan ? JSON.parse(card.dataset.widgetPlan) : null;
  fillProjectionFields(state.editingPlan && state.editingPlan.project);
  state.editingFormat = (() => { try { return JSON.parse(card.dataset.widgetData || "{}").format || "money"; } catch (e) { return "money"; } })();
  fillTargetFields(state.editingPlan && state.editingPlan.targets);
  syncProjectionFields();
  drawer.hidden = false;
  document.getElementById("drawerBackdrop").hidden = false;
  requestAnimationFrame(() => {
    drawer.classList.add("show");
    document.getElementById("drawerBackdrop").classList.add("show");
  });
}
// ---- Projection settings in the widget drawer. They edit the "project"
// part of a widget's saved query (see SpecProjection in the server).
const PROJECTABLE = ["line", "bar", "number"];

function fillProjectionFields(p) {
  const $ = (id) => document.getElementById(id);
  if (!$("wProjMethod")) return;
  state.projHorizonTouched = false;
  $("wProjMethod").value = p ? (p.method || "trend") : "";
  const span = p && /^\+(\d+)\s*([dwmy])$/i.exec(p.until || "");
  if (span && "my".includes(span[2].toLowerCase())) {
    $("wProjHorizon").value = span[1];
    $("wProjUnit").value = span[2].toLowerCase();
  } else {
    // A date or a number of periods: shown as a year, kept unless changed.
    $("wProjHorizon").value = p ? "" : "1";
    $("wProjHorizon").placeholder = p && p.until ? `To ${p.until}` : p && p.periods ? `${p.periods} periods` : "";
    $("wProjUnit").value = "y";
  }
  $("wProjBasis").value = p && p.basis ? p.basis : "";
  $("wProjRate").value = p && p.rate ? +(p.rate * 100).toFixed(4) : "";
  // A rate the widget's query works out is kept while this is left empty.
  $("wProjRate").placeholder = p && p.rate_sql ? "From its query" : "0";
  $("wProjContrib").value = p && p.contribution ? +(p.contribution / 100).toFixed(2) : "";
  $("wProjError").hidden = true;
}

function syncProjectionFields() {
  const box = document.getElementById("wProjection");
  if (!box) return;
  const typ = document.getElementById("wType").value;
  box.hidden = !(state.editingPlan && PROJECTABLE.includes(typ));
  const method = document.getElementById("wProjMethod").value;
  box.querySelectorAll("[data-proj-show]").forEach((row) => {
    row.hidden = !method || !row.dataset.projShow.split(" ").includes(method);
  });
  const targets = document.getElementById("wTargets");
  if (targets) targets.hidden = !(state.editingPlan && TARGETABLE.includes(typ));
}

// ---- Target lines in the widget drawer: the "targets" part of a saved
// query. A fixed line has a label and a value; a line from a query (written
// by the AI) is kept as it is.
const TARGETABLE = ["line", "bar"];

function targetRowHTML(t) {
  const money = state.editingFormat === "money";
  if (t.sql) {
    const name = t.label || "a query";
    return `<div class="target-row" data-target-sql="${esc(t.sql)}" data-target-label="${esc(t.label || "")}" data-target-color="${esc(t.color || "")}">`
      + `<span class="target-from">Follows ${esc(name)}</span>`
      + `<button class="icon-btn" type="button" data-target-remove aria-label="Remove line"><svg class="icon"><use href="#i-close"/></svg></button></div>`;
  }
  const v = t.value == null ? "" : money ? +(Number(t.value) / 100).toFixed(2) : Number(t.value);
  return `<div class="target-row" data-target-color="${esc(t.color || "")}">`
    + `<input class="field" data-target-label-input type="text" maxlength="40" placeholder="Label" value="${esc(t.label || "")}" aria-label="Line label" />`
    + `<input class="field" data-target-value type="number" step="any" inputmode="decimal" placeholder="Value" value="${esc(String(v))}" aria-label="Line value" />`
    + `<button class="icon-btn" type="button" data-target-remove aria-label="Remove line"><svg class="icon"><use href="#i-close"/></svg></button></div>`;
}

function fillTargetFields(targets) {
  const list = document.getElementById("wTargetList");
  if (!list) return;
  list.innerHTML = (targets || []).map(targetRowHTML).join("");
}

// collectTargets reads the target rows back; a fixed line with no value is
// left out.
function collectTargets() {
  const money = state.editingFormat === "money";
  const out = [];
  document.querySelectorAll("#wTargetList .target-row").forEach((row) => {
    const color = row.dataset.targetColor || undefined;
    if (row.dataset.targetSql) {
      out.push({ sql: row.dataset.targetSql, label: row.dataset.targetLabel || undefined, color });
      return;
    }
    const raw = row.querySelector("[data-target-value]").value;
    if (raw === "" || !isFinite(Number(raw))) return;
    const label = row.querySelector("[data-target-label-input]").value.trim();
    out.push({ label: label || undefined, value: money ? Math.round(Number(raw) * 100) : Number(raw), color });
  });
  return out;
}

// withProjection is plan with the drawer's projection settings applied.
function withProjection(plan, type) {
  const $ = (id) => document.getElementById(id);
  const out = { ...plan };
  const method = $("wProjMethod") ? $("wProjMethod").value : "";
  if (!method || !PROJECTABLE.includes(type)) {
    delete out.project;
    return out;
  }
  const old = plan.project || {};
  const p = { method };
  const n = parseInt($("wProjHorizon").value, 10);
  if (n > 0) p.until = `+${n}${$("wProjUnit").value}`;
  else if (!state.projHorizonTouched && (old.until || old.periods)) {
    if (old.until) p.until = old.until;
    if (old.periods) p.periods = old.periods;
  }
  const basis = parseInt($("wProjBasis").value, 10);
  if (method !== "compound" && basis > 1) p.basis = basis;
  if (method === "compound") {
    const rate = parseFloat($("wProjRate").value);
    const contrib = parseFloat($("wProjContrib").value);
    if (rate) p.rate = rate / 100;
    else if (old.rate_sql) p.rate_sql = old.rate_sql;
    if (contrib) p.contribution = Math.round(contrib * 100);
    if (old.contribution_sql) p.contribution_sql = old.contribution_sql;
  }
  if (old.series) p.series = old.series;
  out.project = p;
  return out;
}

function closeDrawer() {
  const d = document.getElementById("widgetDrawer"), b = document.getElementById("drawerBackdrop");
  if (!d || !b) return;
  d.classList.remove("show"); b.classList.remove("show");
  setTimeout(() => { d.hidden = true; b.hidden = true; }, 220);
  state.editingId = null;
}

const state = {
  editMode: false,
  editingId: null,
  editingPlan: null,
  editingFormat: "money",
  projHorizonTouched: false,
  balanceView: "all",
  privacy: false,
  customFrom: null,
  customTo: null,
};

const GRID_GAP = 12, GRID_ROW = 72;

function setEditMode(on) {
  state.editMode = on;
  const grid = document.getElementById("pinnedGrid");
  if (grid) grid.classList.toggle("editing", on);
  const btn = document.getElementById("pinnedEditBtn");
  const hint = document.getElementById("pinnedHint");
  if (btn) {
    btn.innerHTML = on
      ? '<svg class="icon"><use href="#i-check"/></svg> Done'
      : '<svg class="icon"><use href="#i-settings"/></svg> Edit';
  }
  if (hint) {
    hint.textContent = on ? "Drag onto another widget to swap · drag the edges to resize" : "";
    hint.hidden = !on;
  }
}

function persistWidgets(order) {
  fetch("/api/v1/widgets/pinned", {
    method: "PUT",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify({ items: order }),
  }).then((r) => { if (!r.ok) toast("Could not save layout"); }).catch(() => toast("Could not save layout"));
}

function pinnedOrder() {
  const grid = document.getElementById("pinnedGrid");
  if (!grid) return [];
  return Array.from(grid.querySelectorAll("[data-widget-id]")).map((card) => ({
    id: card.dataset.widgetId,
    col: Number(card.dataset.widgetCol || 8),
    rows: Number(card.dataset.widgetRows || 4),
    position: Array.from(grid.children).indexOf(card),
  }));
}

function clamp(n, min, max) { return Math.max(min, Math.min(max, n)); }

function csrfToken() {
  const c = document.cookie.match(/(?:^|; )glowworm_csrf=([^;]*)/);
  return c ? decodeURIComponent(c[1]) : "";
}

function currentViewPeriod() {
  const sel = document.getElementById("periodSelect");
  if (!sel) return {};
  const out = { period: sel.value };
  if (sel.value === "custom") {
    out.from = document.getElementById("customFrom")?.value || "";
    out.to = document.getElementById("customTo")?.value || "";
    if (!out.from && !out.to) delete out.period;
  }
  return out;
}

let sentHistory = [];
let sentHistoryIdx = -1;

function pushSentHistory(text) {
  if (!text) return;
  if (sentHistory[sentHistory.length - 1] === text) return;
  sentHistory.push(text);
  if (sentHistory.length > 100) sentHistory.shift();
  sentHistoryIdx = -1;
}

function seedSentHistoryFromDOM(box) {
  if (!box) return;
  const bubbles = box.querySelectorAll(".msg.user .msg-bubble");
  const texts = [];
  bubbles.forEach((b) => {
    const t = (b.textContent || "").trim();
    if (t) texts.push(t);
  });
  const next = [];
  for (const t of texts) {
    if (next[next.length - 1] !== t) next.push(t);
  }
  sentHistory = next.slice(-100);
  sentHistoryIdx = -1;
}

function recallSent(direction, input) {
  if (!input || !sentHistory.length) return false;
  if (direction === "up") {
    if (sentHistoryIdx === -1 && input.value !== "") return false;
    if (sentHistoryIdx === -1) sentHistoryIdx = sentHistory.length;
    if (sentHistoryIdx > 0) {
      sentHistoryIdx--;
      input.value = sentHistory[sentHistoryIdx];
      input.setSelectionRange(input.value.length, input.value.length);
      return true;
    }
    return false;
  } else {
    if (sentHistoryIdx === -1) return false;
    if (sentHistoryIdx < sentHistory.length - 1) {
      sentHistoryIdx++;
      input.value = sentHistory[sentHistoryIdx];
    } else {
      sentHistoryIdx = -1;
      input.value = "";
    }
    input.setSelectionRange(input.value.length, input.value.length);
    return true;
  }
  return false;
}

function pollDaySummaries() {
  if (!document.querySelector('[data-day-summarising="1"]')) return;
  setTimeout(() => {
    fetch("/api/v1/queries/days").then((r) => (r.ok ? r.json() : null)).then((out) => {
      if (!out || !out.items || !out.items.length) return;
      renderDaySidebar(out.items);
      if (out.items.some((d) => !d.summary)) setTimeout(pollDaySummaries, 5000);
    }).catch(() => {});
  }, 3000);
}

function renderDaySidebar(entries) {
  const wrap = document.getElementById("queriesDays");
  if (!wrap) return;
  wrap.innerHTML = entries.map((d) => {
    const conv = esc(d.conversation_id || "");
    return `<a class="queries-day" href="/queries?c=${conv}&day=${esc(d.date)}">` +
      `<span class="queries-day-top"><span>${esc(d.label)}</span><span class="queries-day-count">${esc(d.count)}</span></span>` +
      (d.summary ? `<span class="queries-day-summary">${esc(d.summary)}</span>` : "") +
      `</a>`;
  }).join("");
}

function jumpToDay(date) {
  const box = queriesScrollBox();
  if (!box) return;
  let tries = 0;
  const seek = () => {
    const hit = Array.from(box.querySelectorAll(".msg[data-created]")).find(
      (m) => localDayKey(m.dataset.created) === date);
    if (hit) {
      // Land on the day's date badge, a little below the top.
      const target = hit.previousElementSibling?.classList.contains("msg-day") ? hit.previousElementSibling : hit;
      // getBoundingClientRect is viewport-relative; add scrollTop to get the
      // offset within the box.
      const offset = (target.getBoundingClientRect().top - box.getBoundingClientRect().top) + box.scrollTop;
      box.scrollTop = Math.max(0, offset - 10);
      return;
    }
    if (tries++ >= 15 || box.dataset.hasMore !== "1") return;
    const before = box.dataset.oldest;
    if (!before) return;
    fetch(`/api/v1/conversations/${queriesConvID()}/messages?before=${encodeURIComponent(before)}&limit=${QUERIES_PAGE_SIZE}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((out) => {
        if (!out || !out.items || !out.items.length) { box.dataset.hasMore = "0"; return; }
        if (!out.has_more) box.dataset.hasMore = "0";
        box.dataset.oldest = out.items[0].created_at + "|" + out.items[0].id;
        prependMessages(box, out.items);
        seek();
      }).catch(() => {});
  };
  requestAnimationFrame(seek);
}

const QUERIES_PAGE_SIZE = 20;

function queriesScrollBox() { return document.getElementById("queryMessages"); }
function queriesConvID() {
  const main = document.querySelector(".queries-main");
  return main ? main.dataset.conversationId || "" : "";
}

function initQueriesScroll() {
  const box = queriesScrollBox();
  if (!box) return;
  const savedConv = sessionStorage.getItem("glowworm-queries-conv");
  const urlConv = new URLSearchParams(location.search).get("c");
  if (savedConv && !urlConv && savedConv !== queriesConvID()) {
    location.replace("/queries?c=" + encodeURIComponent(savedConv));
    return;
  }
  if (queriesConvID()) {
    try { sessionStorage.setItem("glowworm-queries-conv", queriesConvID()); } catch (e) {}
  }
  const key = "glowworm-qpos-" + queriesConvID();
  const saved = sessionStorage.getItem(key);
  // Now, before the page is first drawn, and again once it has settled.
  const seek = () => {
    if (saved !== null && saved !== "bottom") box.scrollTop = Number(saved) || 0;
    else scrollQueriesToBottom();
  };
  seek();
  requestAnimationFrame(seek);
  let saveTimer;
  box.addEventListener("scroll", () => {
    clearTimeout(saveTimer);
    saveTimer = setTimeout(saveQueriesScroll, 150);
    maybeLoadOlder();
  }, { passive: true });
  // The debounce can miss the last movement before navigating away; pagehide
  // always fires.
  window.addEventListener("pagehide", saveQueriesScroll);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") saveQueriesScroll();
  });
  maybeLoadOlder();
}

function scrollQueriesToBottom() {
  const box = queriesScrollBox();
  if (box) box.scrollTop = box.scrollHeight;
}

function saveQueriesScroll() {
  const box = queriesScrollBox();
  const key = "glowworm-qpos-" + queriesConvID();
  if (!box || !queriesConvID()) return;
  const atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
  try { sessionStorage.setItem(key, atBottom ? "bottom" : String(box.scrollTop)); } catch (e) {}
}

let loadingOlder = false;
function maybeLoadOlder() {
  const box = queriesScrollBox();
  if (!box || loadingOlder) return;
  if (box.dataset.hasMore !== "1" || box.scrollTop > 80) return;
  loadOlderMessages();
}

function loadOlderMessages() {
  const box = queriesScrollBox();
  const convID = queriesConvID();
  const cursor = box.dataset.oldest;
  if (!convID || !cursor) { box.dataset.hasMore = "0"; return; }
  loadingOlder = true;
  const loader = document.createElement("div");
  loader.className = "history-loading";
  loader.textContent = "Loading earlier messages…";
  box.prepend(loader);
  fetch(`/api/v1/conversations/${convID}/messages?before=${encodeURIComponent(cursor)}&limit=${QUERIES_PAGE_SIZE}`)
    .then((r) => (r.ok ? r.json() : Promise.reject(new Error("load failed"))))
    .then((out) => {
      loader.remove();
      const items = out.items || [];
      if (!items.length || !out.has_more) box.dataset.hasMore = "0";
      if (items.length) {
        box.dataset.oldest = items[0].created_at + "|" + items[0].id;
        prependMessages(box, items);
        dataVersion.hold();
        if (window.drawChartHosts) drawChartHosts(box);
        seedSentHistoryFromDOM(box);
      }
      loadingOlder = false;
      maybeLoadOlder();
    })
    .catch(() => { loader.remove(); loadingOlder = false; });
}

function messageHTML(m) {
  const created = esc(m.created_at || "");
  if (m.role === "user") {
    return `<div class="msg user" data-created="${created}"><div class="msg-content"><div class="msg-bubble">${esc(m.text)}</div></div></div>`;
  }
  let content = "";
  if (m.text) content += `<div class="msg-text">${fmtAssistantText(m.text)}</div>`;
  if (m.steps && m.steps.length) content += msgReasonHTML(m.steps);
  for (const w of m.widgets || []) content += widgetCardHTML(w);
  return `<div class="msg assistant" data-created="${created}"><div class="msg-content">${content}</div></div>`;
}

function prependMessages(box, items) {
  const firstMsg = box.querySelector(".msg");
  const prevHeight = box.scrollHeight;
  const prevTop = box.scrollTop;
  let runningDay = null;
  const frag = document.createDocumentFragment();
  for (const m of items) {
    const day = dayKeyOf(m.created_at);
    if (day && day !== runningDay) {
      const badge = document.createElement("div");
      badge.className = "msg-day";
      badge.textContent = dayLabelOf(m.created_at);
      frag.appendChild(badge);
      runningDay = day;
    }
    const tpl = document.createElement("template");
    tpl.innerHTML = messageHTML(m);
    frag.appendChild(tpl.content.firstElementChild);
  }
  box.prepend(frag);
  box.scrollTop = prevTop + (box.scrollHeight - prevHeight);
  if (firstMsg && firstMsg.dataset.created) {
    const badgeEl = firstMsg.previousElementSibling;
    if (badgeEl && badgeEl.classList.contains("msg-day") &&
        dayKeyOf(firstMsg.dataset.created) === dayKeyOf(items[items.length - 1].created_at)) {
      badgeEl.remove();
    }
  }
}

// Local-timezone day keys, matching the server: UTC grouping mislabels
// late-evening messages.
function localDayKey(ts) {
  const d = new Date(ts || "");
  if (isNaN(d)) return "";
  const p = (n) => String(n).padStart(2, "0");
  return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate());
}
function dayKeyOf(ts) { return localDayKey(ts); }
function dayLabelOf(ts) {
  const d = new Date(ts || "");
  if (isNaN(d)) return "";
  const key = (x) => x.getFullYear() + "." + x.getMonth() + "." + x.getDate();
  const now = new Date();
  if (key(d) === key(now)) return "Today";
  const yest = new Date(); yest.setDate(yest.getDate() - 1);
  if (key(d) === key(yest)) return "Yesterday";
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric" });
}

function sendChat(text) {
  text = (text || "").trim();
  if (!text) return;
  // The starter suggestions are only for an empty conversation.
  const starters = document.getElementById("querySuggestions");
  if (starters) starters.hidden = true;
  pushSentHistory(text);
  const box = document.getElementById("queryMessages");
  const convEl = document.querySelector("[data-conversation-id]");
  const conversationID = convEl ? convEl.dataset.conversationId : "";

  // The day badge is decided from the DOM's last message, never a cached
  // format string (that produced duplicate Today badges).
  const user = document.createElement("div");
  user.className = "msg user msg-in";
  user.innerHTML = `<div class="msg-content"><div class="msg-bubble">${esc(text)}</div></div>`;
  const pending = document.createElement("div");
  pending.className = "msg assistant msg-in";
  pending.innerHTML = `<div class="msg-content">${wormLoaderHTML()}<details class="msg-reason" hidden><summary>How I worked this out</summary><div class="reason-steps"></div></details></div>`;
  const msgs = box.querySelectorAll(".msg[data-created]");
  const lastCreated = msgs.length ? msgs[msgs.length - 1].dataset.created : null;
  const nowTs = new Date().toISOString();
  if (!lastCreated || localDayKey(lastCreated) !== localDayKey(nowTs)) {
    const day = document.createElement("div");
    day.className = "msg-day";
    day.textContent = dayLabelOf(nowTs);
    box.appendChild(day);
  }
  box.appendChild(user); box.appendChild(pending);
  box.scrollTop = box.scrollHeight;

  const release = dataVersion.hold();
  fetch("/api/v1/query/stream", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify({ text, conversation_id: conversationID, ...currentViewPeriod() }),
  }).then(async (resp) => {
    if (!resp.ok || !resp.body) {
      finishAssistant(pending, { text: await apiErrorText(resp, "Sorry - the query engine is unavailable right now."), isError: true });
      return;
    }
    const reader = resp.body.getReader();
    const dec = new TextDecoder();
    let buf = "", out = { text: "", steps: [], widgets: [], isError: false };
    let finished = false;
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let idx;
        while ((idx = buf.indexOf("\n")) >= 0) {
          const line = buf.slice(0, idx).trim();
          buf = buf.slice(idx + 1);
          if (!line) continue;
          let ev;
          try { ev = JSON.parse(line); } catch (e) { continue; }
          if (ev.event === "steps") {
            out.steps = out.steps.concat(ev.items || []);
            renderSteps(pending, out.steps);
          }
          else if (ev.event === "text") { out.text = ev.text || out.text; }
          else if (ev.event === "widget" && ev.widget) { out.widgets.push(ev.widget); }
          else if (ev.event === "done") {
            finished = true;
            if (ev.conversation_id) adoptConversation(ev.conversation_id, text);
          }
          else if (ev.event === "error") {
            out.text = ev.message || "Query failed.";
            out.detail = ev.detail || "";
            out.isError = true;
            out.steps = [];
          }
        }
      }
    } catch (e) {
      // The connection dropped part way; reading further is pointless.
    }
    if (!finished) {
      // The server carries on and saves the answer whether or not anyone is
      // still listening, so it'll be there after a reload.
      finishAssistant(pending, {
        text: "The connection dropped before the answer arrived. Glowworm carries on with it: reload this page in a moment to see it.",
        isError: true,
      });
      return;
    }
    finishAssistant(pending, out);
  }).catch(() => {
    finishAssistant(pending, {
      text: "Couldn't reach Glowworm. Check your connection and ask again; if the question did get through, its answer appears after a reload.",
      isError: true,
    });
  }).finally(release);
}

function pinWidget(card, btn) {
  const payload = {
    title: card.dataset.widgetTitle || "",
    type: card.dataset.widgetType || "bar",
    data: card.dataset.widgetData ? JSON.parse(card.dataset.widgetData) : null,
    plan: card.dataset.widgetPlan ? JSON.parse(card.dataset.widgetPlan) : null,
  };
  fetch("/api/v1/widgets/pinned", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify(payload),
  }).then((r) => jsonOk(r)).then((out) => {
    if (!out || !out.id) { toast("Could not pin widget"); return; }
    card.dataset.pinnedId = out.id;
    btn.classList.add("pinned-on");
    btn.title = "Unpin from dashboard";
    toast("Pinned to dashboard");
  }).catch((e) => toast(e.message || "Could not pin widget"));
}

function unpinWidget(card, btn) {
  fetch(`/api/v1/widgets/pinned/${card.dataset.pinnedId}`, {
    method: "DELETE",
    headers: { "X-CSRF-Token": csrfToken() },
  }).then((r) => {
    if (!r.ok) { toast("Could not unpin widget"); return; }
    delete card.dataset.pinnedId;
    btn.classList.remove("pinned-on");
    btn.title = "Pin to dashboard";
    const grid = document.getElementById("pinnedGrid");
    if (grid && grid.contains(card)) card.remove();
    toast("Removed from dashboard");
  }).catch(() => toast("Could not unpin widget"));
}

function adoptConversation(id, firstQuestion) {
  if (!id) return;
  const main = document.querySelector(".queries-main");
  if (main) main.dataset.conversationId = id;
  try { sessionStorage.setItem("glowworm-queries-conv", id); } catch (e) {}
  const sidebar = document.getElementById("queriesDays");
  if (sidebar && !sidebar.querySelector(`a[href="/queries?c=${id}"]`)) {
    const a = document.createElement("a");
    a.className = "queries-day";
    a.href = `/queries?c=${id}`;
    const t = document.createElement("span");
    t.textContent = (firstQuestion || "New conversation").slice(0, 60);
    a.appendChild(t);
    sidebar.prepend(a);
  }
}

// A step is a heading line, optionally followed by the full SQL it ran.
function reasonStepHTML(step) {
  const nl = String(step).indexOf("\n");
  const text = nl < 0 ? step : step.slice(0, nl);
  const code = nl < 0 ? "" : step.slice(nl + 1).trim();
  return `<div class="reason-step"><span class="tick"><svg class="icon"><use href="#i-check"/></svg></span><div class="reason-body"><span>${esc(text)}</span>${
    code ? `<pre class="reason-sql"><code>${esc(code)}</code></pre>` : ""}</div></div>`;
}

function renderSteps(pending, steps) {
  const reason = pending.querySelector(".msg-reason");
  if (!reason) return;
  reason.hidden = false;
  reason.querySelector(".reason-steps").innerHTML = steps
    .map(reasonStepHTML)
    .join("");
}

function fmtAssistantText(text) {
  const escd = esc(text || "");
  return escd.split("**").map((p, i) => (i % 2 ? `<strong>${p}</strong>` : p)).join("");
}

function finishAssistant(pending, out) {
  out = out || {};
  const { text = "", detail = "", steps = [], widgets = [], isError = false } = out;
  const loader = pending.querySelector(".glowworm-loader");
  const reveal = () => {
    let content = "";
    if (text) content += `<div class="msg-text${isError ? " msg-error" : ""}">${fmtAssistantText(text)}</div>`;
    // A provider's own words about an error, quieter, as in Settings.
    if (isError && detail) content += `<div class="msg-error-detail">${esc(detail)}</div>`;
    if (!isError && steps && steps.length) content += msgReasonHTML(steps);
    if (!isError && widgets) {
      for (const w of widgets) content += widgetCardHTML(w);
    }
    pending.innerHTML = `<div class="msg-content">${content}</div>`;
    if (widgets && widgets.length) {
      const host = pending.querySelector(".chart-host[data-chart-type]");
      if (host && window.drawChartHosts) drawChartHosts(pending);
    }
    const box = document.getElementById("queryMessages");
    if (box) box.scrollTop = box.scrollHeight;
  };
  if (loader && !matchMedia("(prefers-reduced-motion: reduce)").matches) {
    loader.classList.add("worm-out");
    setTimeout(reveal, 320);
  } else {
    reveal();
  }
}

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
    steps.map(reasonStepHTML).join("")
  }</div></details>`;
}
function widgetCardHTML(widget) {
  const el = document.createElement("article");
  el.className = "widget span-12";
  el.dataset.widgetId = widget.id || "";
  el.dataset.widgetType = widget.type || "";
  el.dataset.widgetTitle = widget.title || "";
  el.dataset.widgetData = JSON.stringify(widget.data || {});
  if (widget.plan) el.dataset.widgetPlan = JSON.stringify(widget.plan);
  el.innerHTML = `
    <div class="widget-head">
      <div class="widget-title">${esc(widget.title || "Widget")}</div>
      ${isRulesWidget(widget.type) ? "" : `<div class="widget-actions">
        ${widget.plan || widget.type === "text" ? `<button class="widget-btn${widget.pinned ? " pinned-on" : ""}" data-action="toggle-pin" title="Pin to dashboard"><svg class="icon"><use href="#i-pin"/></svg></button>` : ""}
        <button class="widget-btn" data-action="edit-widget" title="Widget settings"><svg class="icon"><use href="#i-settings"/></svg></button>
      </div>`}
    </div>
    <div class="widget-body">${widgetBodyHTML(widget)}</div>`;
  return el.outerHTML;
}
function widgetBodyHTML(widget) {
  const data = widget.data || {};
  if (widget.type === "group_rules") return `<div class="rules-host" data-rules-kind="list"></div>`;
  if (widget.type === "group_rule") return `<div class="rules-host" data-rules-kind="draft" data-draft="${esc(JSON.stringify(data))}"></div>`;
  if (widget.type === "change") return `<div class="rules-host" data-rules-kind="change" data-draft="${esc(JSON.stringify(data))}"></div>`;
  if (widget.type === "valuation") return `<div class="rules-host" data-rules-kind="valuation" data-draft="${esc(JSON.stringify(data))}"></div>`;
  if (widget.type === "link_transfers") return `<div class="rules-host" data-rules-kind="link_transfers"></div>`;
  if (data.transactions) {
    data.transactions = data.transactions.map((tx) => {
      const v = tx.amount_minor ?? tx.amount ?? 0;
      return { ...tx, amount: Number(v) };
    });
  }
  if (window.widgetIsEmpty && widgetIsEmpty(widget.type, data)) return widgetEmptyHTML();
  if (widget.type === "progress" && window.progressHTML) return progressHTML(data);
  if (widget.type === "number") {
    return `<div class="num-widget"><div class="num-value">${esc(data.value)}</div>
      ${data.caption ? `<div class="num-caption">${esc(data.caption)}</div>` : ""}</div>`;
  }
  if (widget.type === "text") return `<div class="text-widget"><p>${esc(data.body)}</p></div>`;
  if (widget.type === "table") {
    return `<div class="table-widget"><table><thead><tr>${(data.columns || []).map((c) => `<th>${esc(c)}</th>`).join("")}</tr></thead>
      <tbody>${(data.rows || []).map((r) => `<tr>${r.map((c, i) => `<td class="${i === r.length - 1 ? "num" : ""}">${esc(c)}</td>`).join("")}</tr>`).join("")}</tbody></table></div>`;
  }
  if (widget.type === "transactions") {
    const rows = (data.transactions || []).map((tx) => {
      const cur = tx.currency || "GBP";
      const sym = ({ GBP: "£", USD: "$", EUR: "€" })[cur] || cur + " ";
      const amt = Number(tx.amount || 0) / 100;
      const amtStr = (tx.type === "transfer" ? "⇄ " : tx.amount >= 0 ? "+" : "−") +
        sym + Math.abs(amt).toLocaleString("en-GB", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
      const cls = tx.type === "transfer" ? "transfer" : tx.amount >= 0 ? "pos" : "neg";
      return `<tr><td class="cell-sub cell-date">${dateHTML(String(tx.date || "").slice(0, 10))}</td>` +
        `<td><div class="cell-merchant">${esc(tx.description || tx.merchant || "")}</div></td>` +
        `<td>${esc(tx.category || "")}</td>` +
        `<td class="num tx-amount ${cls}">${esc(amtStr)}</td></tr>`;
    }).join("");
    return `<div class="table-widget tx-widget"><table class="tx-table">
      <thead><tr><th>Date</th><th>Description</th><th>Category</th><th class="num">Amount</th></tr></thead>
      <tbody>${rows}</tbody>
    </table></div>`;
  }
  if (widget.type === "pie" && window.pieChartSVG) {
    const p = pieChartSVG(data);
    return `<div class="pie-wrap"><div class="pie-chart">${p.svg}</div>${p.legend}</div>`;
  }
  if (widget.type === "line" && window.lineChartSVG) {
    return `<div class="chart-host" data-chart-type="line" data-chart-data="${esc(JSON.stringify(data))}"></div>`;
  }
  if (window.barChartSVG) {
    return `<div class="chart-host" data-chart-type="bar" data-chart-data="${esc(JSON.stringify(data))}"></div>`;
  }
  return "";
}

document.addEventListener("DOMContentLoaded", () => {
  initTheme();
  try {
    const pending = sessionStorage.getItem("glowworm-toast");
    if (pending) { sessionStorage.removeItem("glowworm-toast"); toast(pending); }
  } catch (e) {}

  const privacyBtn = document.getElementById("privacyToggle");
  const privacySwitch = document.getElementById("privacySwitch");
  const applyPrivacy = (on) => {
    state.privacy = on;
    document.body.classList.toggle("privacy", on);
    if (privacySwitch) privacySwitch.checked = on;
    if (!privacyBtn) return;
    privacyBtn.classList.toggle("active", on);
    privacyBtn.setAttribute("aria-pressed", on ? "true" : "false");
    privacyBtn.title = on ? "Show balances" : "Hide balances";
    privacyBtn.innerHTML = on
      ? '<svg class="icon"><use href="#i-eye-off"/></svg>'
      : '<svg class="icon"><use href="#i-eye"/></svg>';
  };
  const privacyCookie = () => {
    const m = document.cookie.match(/(?:^|;\s*)glowworm_privacy=([^;]*)/);
    return m ? m[1] === "1" : null;
  };
  state.privacy = document.body.classList.contains("privacy");
  const savePrivacy = (on) => {
    applyPrivacy(on);
    document.cookie = `glowworm_privacy=${on ? "1" : "0"}; Path=/; Max-Age=31536000; SameSite=Lax`;
  };
  if (privacyBtn) privacyBtn.addEventListener("click", () => savePrivacy(!state.privacy));
  if (privacySwitch) privacySwitch.addEventListener("change", () => savePrivacy(privacySwitch.checked));
  // bfcache restores the old state; resync with a choice made on another page.
  window.addEventListener("pageshow", (e) => {
    const saved = privacyCookie();
    if (e.persisted && saved !== null && saved !== state.privacy) applyPrivacy(saved);
  });

  const themeSel = document.getElementById("themeSelect");
  if (themeSel) {
    themeSel.addEventListener("change", (e) => {
      try { localStorage.setItem("glowworm-theme", e.target.value); } catch (err) {}
      applyTheme(e.target.value);
      toast("Theme updated");
    });
  }

  if (window.drawChartHosts) drawChartHosts(document);

  const brand = document.getElementById("brandMark");
  if (brand) {
    let timer;
    brand.addEventListener("click", (e) => {
      e.preventDefault();
      brand.classList.remove("wiggle");
      void brand.offsetWidth;
      brand.classList.add("wiggle");
      clearTimeout(timer);
      timer = setTimeout(() => brand.classList.remove("wiggle"), 1000);
    });
  }

  const confirmOk = document.getElementById("confirmOk");
  const confirmCancel = document.getElementById("confirmCancel");
  if (confirmOk) confirmOk.addEventListener("click", () => { const cb = confirmCallback; confirmCallback = null; document.getElementById("confirmModal").hidden = true; if (cb) cb(); });
  if (confirmCancel) confirmCancel.addEventListener("click", () => { confirmCallback = null; document.getElementById("confirmModal").hidden = true; });
  document.querySelectorAll("[data-action='close-confirm']").forEach((b) => b.addEventListener("click", () => { confirmCallback = null; document.getElementById("confirmModal").hidden = true; }));

  document.getElementById("drawerBackdrop")?.addEventListener("click", closeDrawer);
  document.querySelectorAll("[data-action='close-drawer']").forEach((b) => b.addEventListener("click", closeDrawer));
  const wApply = document.getElementById("wApply");
  if (wApply) {
    document.getElementById("wProjMethod")?.addEventListener("change", syncProjectionFields);
    document.getElementById("wTargetAdd")?.addEventListener("click", () => {
      document.getElementById("wTargetList").insertAdjacentHTML("beforeend", targetRowHTML({}));
      document.querySelector("#wTargetList .target-row:last-child [data-target-label-input]")?.focus();
    });
    document.getElementById("wTargetList")?.addEventListener("click", (e) => {
      const rm = e.target.closest("[data-target-remove]");
      if (rm) rm.closest(".target-row").remove();
    });
    document.getElementById("wType")?.addEventListener("change", syncProjectionFields);
    ["wProjHorizon", "wProjUnit"].forEach((fid) => document.getElementById(fid)?.addEventListener("input", () => { state.projHorizonTouched = true; }));
    wApply.addEventListener("click", () => {
      const id = state.editingId;
      const card = document.querySelector(`[data-widget-id="${id}"]`);
      if (!card) { closeDrawer(); return; }
      const newType = document.getElementById("wType").value;
      const newTitle = document.getElementById("wTitle").value.trim() || card.dataset.widgetTitle;
      const wasPinned = !!card.dataset.pinnedId;
      let data = card.dataset.widgetData ? JSON.parse(card.dataset.widgetData) : null;
      let plan = card.dataset.widgetPlan ? JSON.parse(card.dataset.widgetPlan) : null;
      const errEl = document.getElementById("wProjError");
      if (errEl) errEl.hidden = true;

      const updateCardDOM = () => {
        card.dataset.widgetType = newType;
        card.dataset.widgetTitle = newTitle;
        card.dataset.widgetData = JSON.stringify(data || {});
        if (plan) card.dataset.widgetPlan = JSON.stringify(plan);
        card.querySelector(".widget-title").textContent = newTitle;
        const body = card.querySelector(".widget-body");
        if (body) body.innerHTML = widgetBodyHTML({ type: newType, data: data || {} });
        if (window.drawChartHosts) drawChartHosts(card);
      };
      const save = () => {
        if (!wasPinned) {
          updateCardDOM();
          closeDrawer();
          toast("Widget updated");
          return;
        }
        fetch(`/api/v1/widgets/pinned/${card.dataset.pinnedId}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
          body: JSON.stringify({ title: newTitle, type: newType, data, plan }),
        }).then((r) => {
          if (!r.ok) { toast("Could not update widget"); return; }
          updateCardDOM();
          toast("Widget updated");
          closeDrawer();
        }).catch(() => toast("Could not update widget"));
      };

      // A widget with a saved query is run again with its new settings, so
      // the card shows exactly what the dashboard will.
      if (plan) {
        plan = withProjection(plan, newType);
        const targets = TARGETABLE.includes(newType) ? collectTargets() : [];
        if (targets.length) plan.targets = targets;
        else delete plan.targets;
        fetch("/api/v1/widgets/preview", {
          method: "POST",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
          body: JSON.stringify({ type: newType, plan, ...currentViewPeriod() }),
        }).then((r) => jsonOk(r)).then((out) => {
          data = out.data;
          save();
        }).catch((e) => {
          if (errEl) { errEl.textContent = e.message || "That didn't work."; errEl.hidden = false; }
          else toast(e.message || "Could not update widget");
        });
        return;
      }
      if (data && window.convertWidgetData) {
        data = convertWidgetData(card.dataset.widgetType, newType, data);
      }
      save();
    });
  }

  document.addEventListener("click", (e) => {
    const edit = e.target.closest("[data-action='edit-widget']");
    if (edit) {
      const card = edit.closest("[data-widget-id]");
      if (card) openDrawer(card.dataset.widgetId);
      return;
    }
    const pin = e.target.closest("[data-action='toggle-pin']");
    if (pin) {
      const card = pin.closest("[data-widget-id]");
      if (!card) return;
      if (card.dataset.pinnedId) {
        showConfirm(
          "Unpin widget?",
          "Remove this widget from your dashboard?",
          "Unpin",
          () => unpinWidget(card, pin),
        );
      } else {
        pinWidget(card, pin);
      }
    }
  });

  document.addEventListener("click", (e) => {
    const chip = e.target.closest("[data-chat-suggest]");
    if (chip) sendChat(chip.dataset.chatSuggest);
  });

  // Available balance / Savings cards swap the overview chart to that slice;
  // clicking the selected card again goes back to all accounts.
  const heroChart = document.getElementById("heroChart");
  const balanceCards = document.querySelectorAll("[data-balance-view]");
  if (heroChart && balanceCards.length) {
    const HERO_TITLES = { all: "Balance overview", available: "Available balance", savings: "Savings" };
    const HERO_RANGES = { all: "All accounts", available: "Current and other non-savings accounts", savings: "Savings accounts" };
    // The page opens on Available balance (rendered server-side).
    state.balanceView = document.querySelector("[data-balance-view].selected")?.dataset.balanceView || "all";
    const setBalanceView = (view) => {
      state.balanceView = view;
      balanceCards.forEach((card) => {
        const on = card.dataset.balanceView === view;
        card.classList.toggle("selected", on);
        card.setAttribute("aria-pressed", on ? "true" : "false");
      });
      heroChart.dataset.chartData = heroChart.dataset[`series${view[0].toUpperCase()}${view.slice(1)}`] || heroChart.dataset.seriesAll;
      const title = document.getElementById("heroTitle");
      if (title) title.textContent = HERO_TITLES[view];
      const range = document.getElementById("heroRange");
      if (range) range.textContent = `${HERO_RANGES[view]} · all time`;
      if (typeof redrawChartHost === "function") redrawChartHost(heroChart);
    };
    balanceCards.forEach((card) => {
      const pick = () => setBalanceView(state.balanceView === card.dataset.balanceView ? "all" : card.dataset.balanceView);
      card.addEventListener("click", pick);
      card.addEventListener("keydown", (e) => {
        if (e.key === "Enter" || e.key === " ") { e.preventDefault(); pick(); }
      });
    });
  }

  const pinnedEditBtn = document.getElementById("pinnedEditBtn");
  if (pinnedEditBtn) {
    pinnedEditBtn.addEventListener("click", () => setEditMode(!state.editMode));
    let move = null, resize = null;

    document.addEventListener("pointerdown", (e) => {
      if (!state.editMode) return;
      const grid = document.getElementById("pinnedGrid");
      if (!grid) return;
      if (e.target.closest(".resize-handle")) {
        const handle = e.target.closest(".resize-handle");
        const card = handle.closest("[data-widget-id]");
        resize = {
          id: card.dataset.widgetId, dir: handle.dataset.resizeDir,
          startX: e.clientX, startY: e.clientY,
          col: Number(card.dataset.widgetCol || 8), rows: Number(card.dataset.widgetRows || 4),
          colWidth: (grid.clientWidth - 11 * GRID_GAP) / 12, rowStep: GRID_ROW + GRID_GAP, card,
        };
        try { handle.setPointerCapture(e.pointerId); } catch (err) {}
        document.body.classList.add("resizing");
        return;
      }
      if (e.target.closest(".widget-btn")) return;
      const card = e.target.closest("[data-widget-id]");
      if (!card || !grid.contains(card)) return;
      e.preventDefault();
      const rect = card.getBoundingClientRect();
      const ghost = card.cloneNode(true);
      ghost.classList.add("drag-ghost");
      ghost.style.width = rect.width + "px";
      ghost.style.height = rect.height + "px";
      ghost.style.left = rect.left + "px";
      ghost.style.top = rect.top + "px";
      document.body.appendChild(ghost);
      move = { id: card.dataset.widgetId, startX: e.clientX, startY: e.clientY, card, ghost, target: null, moved: false };
      card.classList.add("dragging");
      document.body.classList.add("dragging-widget");
    });

    window.addEventListener("pointermove", (e) => {
      if (resize) {
        e.preventDefault();
        const dx = e.clientX - resize.startX, dy = e.clientY - resize.startY;
        if (resize.dir === "e") {
          const c = clamp(resize.col + Math.round(dx / resize.colWidth), 2, 12);
          resize.card.style.gridColumn = "span " + c;
          resize.card.dataset.widgetCol = c;
        } else if (resize.dir === "w") {
          const c = clamp(resize.col - Math.round(dx / resize.colWidth), 2, 12);
          resize.card.style.gridColumn = "span " + c;
          resize.card.dataset.widgetCol = c;
        } else if (resize.dir === "s") {
          const rw = clamp(resize.rows + Math.round(dy / resize.rowStep), 1, 8);
          resize.card.style.gridRow = "span " + rw;
          resize.card.dataset.widgetRows = rw;
        }
        if (window.drawChartHosts) drawChartHosts(resize.card);
        return;
      }
      if (!move) return;
      if (!move.moved && Math.hypot(e.clientX - move.startX, e.clientY - move.startY) < 5) return;
      move.moved = true;
      e.preventDefault();
      move.ghost.style.transform = `translate(${e.clientX - move.startX}px, ${e.clientY - move.startY}px)`;
      move.ghost.style.pointerEvents = "none";
      const under = document.elementFromPoint(e.clientX, e.clientY);
      move.ghost.style.pointerEvents = "";
      const target = under ? under.closest("[data-widget-id]") : null;
      if (move.target && move.target !== target) move.target.classList.remove("drop-target");
      const grid = document.getElementById("pinnedGrid");
      if (target && target.dataset.widgetId !== move.id && grid.contains(target)) {
        target.classList.add("drop-target");
        move.target = target;
      }
    });

    window.addEventListener("pointerup", () => {
      if (resize) {
        resize = null;
        document.body.classList.remove("resizing");
        persistWidgets(pinnedOrder());
        return;
      }
      if (!move) return;
      if (move.moved && move.target) {
        const a = move.card, b = move.target;
        const aCol = a.dataset.widgetCol || "8", aRows = a.dataset.widgetRows || "4";
        const bCol = b.dataset.widgetCol || "8", bRows = b.dataset.widgetRows || "4";
        a.dataset.widgetCol = bCol; a.dataset.widgetRows = bRows;
        a.style.gridColumn = "span " + bCol; a.style.gridRow = "span " + bRows;
        b.dataset.widgetCol = aCol; b.dataset.widgetRows = aRows;
        b.style.gridColumn = "span " + aCol; b.style.gridRow = "span " + aRows;
        const placeholder = document.createElement("span");
        a.replaceWith(placeholder);
        b.replaceWith(a);
        placeholder.replaceWith(b);
        if (window.drawChartHosts) {
          drawChartHosts(a);
          drawChartHosts(b);
        }
        persistWidgets(pinnedOrder());
      }
      if (move.card) move.card.classList.remove("dragging");
      if (move.target) move.target.classList.remove("drop-target");
      if (move.ghost) move.ghost.remove();
      document.body.classList.remove("dragging-widget");
      move = null;
    });
  }

  const queryInput = document.getElementById("queryInput");
  const queryBtn = document.getElementById("queryBtn");
  // iOS keeps the page its full height when the keyboard opens and, if the
  // field would be under it, slides the visible part of the page up to show
  // it, taking the masthead out of view. Two things stop that here:
  //  - A tap on the composer first shortens the app to the height the
  //    keyboard will leave, and only then focuses the field. The field is
  //    already clear of the keyboard, so iOS has nothing to slide.
  //  - While the field has focus, the app is kept to the visible part of
  //    the screen frame by frame (iOS doesn't always send the viewport's
  //    resize and scroll events), so it follows whatever iOS does anyway.
  const vv = window.visualViewport;
  const composer = queryInput && queryInput.closest(".queries-composer");
  if (composer && vv) {
    const root = document.documentElement;
    const list = document.getElementById("queryMessages");
    const phone = window.matchMedia("(max-width: 760px) and (pointer: coarse)");
    const KB_KEY = "glowworm-kb";
    let raf = 0, lastH = -1, lastTop = -1;
    let guess = 0, guessFrom = 0, guessUntil = 0; // height held until the keyboard arrives
    const fit = () => {
      let h = vv.height;
      if (guess) {
        if (performance.now() > guessUntil || h < guessFrom - 120) guess = 0;
        else h = Math.min(h, guess);
      }
      const top = vv.offsetTop;
      if (h === lastH && top === lastTop) return;
      const atEnd = list && list.scrollHeight - list.scrollTop - list.clientHeight < 40;
      lastH = h; lastTop = top;
      root.style.setProperty("--app-h", h + "px");
      root.style.setProperty("--app-top", top + "px");
      if (atEnd) list.scrollTop = list.scrollHeight;
      // Remember what the keyboard leaves, for the next tap's guess.
      if (!guess && h < window.innerHeight - 120) {
        try { localStorage.setItem(KB_KEY, JSON.stringify({ screen: window.innerHeight, h })); } catch (e) {}
      }
    };
    const loop = () => { fit(); raf = requestAnimationFrame(loop); };
    const start = () => {
      if (raf) return;
      root.classList.add("kb-fit");
      loop();
    };
    const stop = () => {
      cancelAnimationFrame(raf);
      raf = 0; guess = 0; lastH = lastTop = -1;
      root.classList.remove("kb-fit");
      root.style.removeProperty("--app-h");
      root.style.removeProperty("--app-top");
    };
    queryInput.addEventListener("focus", start);
    queryInput.addEventListener("blur", stop);

    let downY = 0;
    composer.addEventListener("touchstart", (e) => { downY = e.touches[0].clientY; }, { passive: true });
    composer.addEventListener("touchend", (e) => {
      if (!phone.matches || document.activeElement === queryInput) return; // typing: taps place the caret
      if (e.target.closest("button") || !e.cancelable) return;
      if (Math.abs(e.changedTouches[0].clientY - downY) > 10) return; // a drag
      e.preventDefault(); // we focus it ourselves, once it's out of the keyboard's way
      let left = 0;
      try {
        const saved = JSON.parse(localStorage.getItem(KB_KEY) || "null");
        if (saved && saved.screen === window.innerHeight) left = saved.h;
      } catch (err) {}
      guess = left || Math.round(window.innerHeight * 0.45);
      guessFrom = vv.height;
      guessUntil = performance.now() + 1000;
      start();
      composer.getBoundingClientRect(); // lay out at the new height before focusing
      queryInput.focus({ preventScroll: true });
      const end = queryInput.value.length;
      queryInput.setSelectionRange(end, end);
    }, { passive: false });
  }
  if (queryInput) {
    const submit = () => {
      const text = queryInput.value.trim();
      if (!text) return;
      sendChat(text);
      queryInput.value = "";
      queryInput.style.height = "auto";
    };
    queryBtn?.addEventListener("click", submit);
    // With the keyboard up, a tap on Send would first take focus from the
    // text box: the keyboard drops, the page grows back under the finger
    // and the tap misses the button, so it only hid the keyboard. Instead
    // the text box keeps its focus (and the keyboard stays up, as in a
    // messaging app) and the tap sends there and then.
    queryBtn?.addEventListener("touchend", (e) => {
      if (document.activeElement !== queryInput || !e.cancelable) return;
      e.preventDefault(); // no blur, and no click to follow
      submit();
    }, { passive: false });
    queryBtn?.addEventListener("mousedown", (e) => {
      if (document.activeElement === queryInput) e.preventDefault(); // keep the caret
    });
    queryInput.addEventListener("input", () => {
      queryInput.style.height = "auto";
      queryInput.style.height = Math.min(queryInput.scrollHeight, 140) + "px";
    });
    queryInput.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(); return; }
      if (e.key === "ArrowUp") {
        if (queryInput.value === "" || sentHistoryIdx !== -1) {
          if (recallSent("up", queryInput)) e.preventDefault();
        }
      } else if (e.key === "ArrowDown") {
        if (sentHistoryIdx !== -1 && recallSent("down", queryInput)) e.preventDefault();
      }
    });

    const qbox = document.getElementById("queryMessages");
    if (qbox) {
      initQueriesScroll();
      seedSentHistoryFromDOM(qbox);
      pollDaySummaries();
      const jumpDay = document.querySelector(".queries-main")?.dataset.jumpDay;
      if (jumpDay) jumpToDay(jumpDay);
    }
    const sidebar = document.getElementById("queriesSidebar");
    document.getElementById("queriesSidebarToggle")?.addEventListener("click", () => {
      sidebar.classList.add("collapsed");
      document.getElementById("queriesSidebarExpand").hidden = false;
    });
    document.getElementById("queriesSidebarExpand")?.addEventListener("click", () => {
      sidebar.classList.remove("collapsed");
      document.getElementById("queriesSidebarExpand").hidden = true;
    });
  }

  document.addEventListener("click", (e) => {
    const rec = e.target.closest("[data-recurring-key]");
    if (rec) openRecurring(rec.dataset.recurringKey);
    const closers = e.target.closest("[data-action='close-recurring']");
    if (closers) closeModal("recurringModal");
    if (e.target.id === "recurringModal") closeModal("recurringModal");
    if (e.target.id === "accountModal") closeModal("accountModal");
  });

  const periodSelect = document.getElementById("periodSelect");
  const periodTrigger = document.getElementById("periodTrigger");
  const periodMenu = document.getElementById("periodMenu");
  if (periodSelect && periodTrigger && periodMenu) {
    const fromIn = document.getElementById("customFrom");
    const toIn = document.getElementById("customTo");
    const customForm = document.getElementById("customRange");
    // The button's label comes from the server (base view), so it's right on first paint.
    const current = periodSelect.value || "last-12m";
    periodMenu.querySelectorAll("[role=option]").forEach((b) => b.setAttribute("aria-selected", b.dataset.period === current ? "true" : "false"));
    customForm.classList.toggle("selected", current === "custom");

    const setOpen = (open) => {
      periodMenu.hidden = !open;
      periodTrigger.setAttribute("aria-expanded", open ? "true" : "false");
      if (open) {
        if (!fromIn.value) {
          const d = new Date(); d.setMonth(d.getMonth() - 3);
          fromIn.value = d.toISOString().slice(0, 10);
        }
        if (!toIn.value) toIn.value = new Date().toISOString().slice(0, 10);
        (periodMenu.querySelector('[aria-selected="true"]') || periodMenu.querySelector("[role=option]")).focus();
      }
    };
    const go = (params) => {
      const url = new URL(window.location);
      url.searchParams.delete("from");
      url.searchParams.delete("to");
      Object.entries(params).forEach(([k, v]) => url.searchParams.set(k, v));
      location.replace(url.toString());
    };
    periodTrigger.addEventListener("click", (e) => { e.stopPropagation(); setOpen(periodMenu.hidden); });
    periodMenu.addEventListener("click", (e) => {
      e.stopPropagation();
      const b = e.target.closest("[role=option]");
      if (b) go({ period: b.dataset.period });
    });
    periodMenu.addEventListener("keydown", (e) => {
      const opts = [...periodMenu.querySelectorAll("[role=option]")];
      const i = opts.indexOf(document.activeElement);
      if (e.key === "ArrowDown" && i >= 0) { e.preventDefault(); (opts[i + 1] || fromIn).focus(); }
      else if (e.key === "ArrowUp" && i > 0) { e.preventDefault(); opts[i - 1].focus(); }
      else if (e.key === "Escape") { setOpen(false); periodTrigger.focus(); }
    });
    customForm.addEventListener("submit", (e) => {
      e.preventDefault();
      if (!fromIn.value || !toIn.value) { toast("Choose a start and end date"); return; }
      if (fromIn.value > toIn.value) { toast("The start date is after the end date"); return; }
      go({ period: "custom", from: fromIn.value, to: toIn.value });
    });
    document.addEventListener("click", () => { if (!periodMenu.hidden) setOpen(false); });
  }

  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
      e.preventDefault();
      location.replace("/queries");
      return;
    }
    if (!e.ctrlKey && !e.metaKey && !e.altKey && /^[0-9]$/.test(e.key)) {
      const editable = e.target && e.target.closest ? e.target.closest("input, textarea, select, [contenteditable]") : null;
      if (!editable) {
        // The keys follow the tabs' own numbers (01 Dashboard … 06 History);
        // 0 is Settings.
        const tab = e.key === "0" ? { href: "/settings" } : document.querySelectorAll(".tabnav .tabnav-item")[Number(e.key) - 1];
        if (tab) { markTabSwitch(); location.replace(tabHref(tab.href)); }
      }
    }
    if (e.key === "Escape") {
      const cm = document.getElementById("confirmModal");
      if (cm && !cm.hidden) { cm.hidden = true; confirmCallback = null; return; }
      closeDrawer(); closeModal("recurringModal"); closeModal("accountModal"); closeModal("txModal"); closeModal("categoryModal"); closePasswordModal(); closeNameModal();
    }
  });

  const newTxBtn = document.getElementById("newTxBtn");
  if (newTxBtn) {
    newTxBtn.addEventListener("click", () => openModal("txModal"));
    document.querySelectorAll("[data-action='close-tx-modal']").forEach((b) =>
      b.addEventListener("click", () => closeModal("txModal")));
    document.getElementById("txModal")?.addEventListener("click", (e) => {
      if (e.target.id === "txModal") closeModal("txModal");
    });
    const txDirections = document.querySelectorAll("[data-tx-direction]");
    txDirections.forEach((b) => b.addEventListener("click", () => {
      txDirections.forEach((o) => {
        o.classList.toggle("active", o === b);
        o.setAttribute("aria-checked", o === b ? "true" : "false");
      });
    }));
    document.getElementById("txAddForm").addEventListener("submit", (e) => {
      e.preventDefault();
      const date = document.getElementById("txDate").value;
      const description = document.getElementById("txDesc").value.trim();
      // The amount is typed unsigned; the Outgoing/Income switch sets the sign.
      const income = document.querySelector("[data-tx-direction].active")?.dataset.txDirection === "in";
      const amount = Math.abs(parseFloat(document.getElementById("txAmount").value)) * (income ? 1 : -1);
      const accountID = document.getElementById("txAccount").value;
      const categoryID = document.getElementById("txCategory").value || null;
      if (!accountID) { toast("Add an account first (Settings → Accounts)"); return; }
      if (!date) { toast("Date is required"); return; }
      if (!description) { toast("Description is required"); return; }
      if (isNaN(amount) || amount === 0) { toast("Amount is required"); return; }
      fetch("/api/v1/transactions", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ account_id: accountID, date, description, amount_minor: Math.round(amount * 100), category_id: categoryID }),
      }).then(async (r) => {
        if (!r.ok) {
          const b = await r.json().catch(() => ({}));
          toast(apiErrorFrom(r, b, "Could not add transaction"));
          return;
        }
        toast("Transaction added");
        location.reload();
      }).catch(() => toast("Could not add transaction"));
    });
  }
});

// A searchable model list: type to filter, sorted alphabetically, with
// "Auto" (empty value) first. Keeps its value even before models load.
// A searchable model menu. Each model can carry the provider it's from
// ({key, id, name, provider, short}); the menu marks it, and the value is the
// model's key ("provider:model"), or "" for the default.
function modelPicker(root) {
  if (!root) return null;
  const input = root.querySelector("input");
  const list = root.querySelector(".model-picker-list");
  const AUTO = { key: "", id: "", name: "Auto (default model)" };
  const label = (m) => m.name || m.id;
  const keyOf = (m) => (m.key !== undefined ? m.key : m.id);
  const marker = (m) => (m.short ? `<span class="model-provider" data-provider="${esc(m.provider || "")}">${esc(m.short)}</span>` : "");
  let models = [];
  let shown = [];
  let active = -1;
  const api = { value: root.dataset.value || "" };

  const display = () => {
    const m = models.find((x) => keyOf(x) === api.value);
    input.value = !api.value ? "" : m ? label(m) + (m.short ? ` (${m.short})` : "") : api.value;
  };
  const close = () => {
    list.hidden = true;
    input.setAttribute("aria-expanded", "false");
    display();
  };
  const render = (q) => {
    const needle = (q || "").trim().toLowerCase();
    shown = [AUTO, ...models].filter((m) => !needle || keyOf(m) === "" ||
      label(m).toLowerCase().includes(needle) || m.id.toLowerCase().includes(needle) ||
      (m.short || "").toLowerCase().includes(needle));
    if (needle) shown = shown.filter((m) => keyOf(m) !== "" || "auto".includes(needle));
    active = Math.max(0, shown.findIndex((m) => keyOf(m) === api.value));
    list.innerHTML = shown.length
      ? shown.map((m, i) => `<li role="option" data-i="${i}" aria-selected="${keyOf(m) === api.value}"${i === active ? ' class="active"' : ""}>
          ${marker(m)}<span class="model-name">${esc(label(m))}</span>${m.id && m.name && m.name !== m.id ? `<span class="model-id">${esc(m.id)}</span>` : ""}</li>`).join("")
      : `<li class="model-picker-empty">No models match “${esc(q)}”</li>`;
    list.hidden = false;
    input.setAttribute("aria-expanded", "true");
  };
  const highlight = (i) => {
    if (!shown.length) return;
    active = (i + shown.length) % shown.length;
    list.querySelectorAll("li[data-i]").forEach((li) => li.classList.toggle("active", Number(li.dataset.i) === active));
    list.querySelector(`li[data-i="${active}"]`)?.scrollIntoView({ block: "nearest" });
  };
  const choose = (i) => {
    const m = shown[i];
    if (!m) return;
    api.value = keyOf(m);
    close();
  };

  input.addEventListener("focus", () => { input.select(); render(""); });
  input.addEventListener("input", () => render(input.value));
  input.addEventListener("blur", () => setTimeout(close, 120));
  input.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown") { e.preventDefault(); if (list.hidden) render(""); else highlight(active + 1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); highlight(active - 1); }
    else if (e.key === "Enter") { if (!list.hidden) { e.preventDefault(); choose(active); } }
    else if (e.key === "Escape") { close(); input.blur(); }
  });
  list.addEventListener("mousedown", (e) => {
    const li = e.target.closest("li[data-i]");
    if (!li) return;
    e.preventDefault();
    choose(Number(li.dataset.i));
  });

  // Grouped by provider, then by name. A saved choice that isn't listed (its
  // provider couldn't be reached, say) stays, so saving doesn't lose it.
  api.setModels = (next, fallback) => {
    models = (next || []).slice().sort((x, y) => (x.short || "").localeCompare(y.short || "") ||
      label(x).localeCompare(label(y), undefined, { sensitivity: "base" }));
    if (api.value && !models.some((m) => keyOf(m) === api.value)) models.unshift(fallback ? fallback(api.value) : { id: api.value, name: api.value });
    display();
  };
  api.setModels([]);
  return api;
}

// Settings → AI providers. Several providers can be connected at once. The
// menu picks which one the key and endpoint boxes are for; what's typed is
// kept per provider until Save, and the model menus list every connected
// provider's models, each marked with its provider.
function setupAISettings(root) {
  const KEY_MASK = "*********";
  let providers = [];
  try { providers = JSON.parse(root.dataset.providers || "[]"); } catch (e) {}
  const byId = (id) => providers.find((p) => p.id === id);
  const short = (id) => byId(id)?.short || id;
  const drafts = {}; // provider id → { key, baseUrl } typed but not saved
  const sel = document.getElementById("aiProvider");
  const keyRow = document.getElementById("aiKeyRow");
  const keyInput = document.getElementById("aiKey");
  const keyLabel = document.getElementById("aiKeyLabel");
  const urlRow = document.getElementById("aiBaseUrlRow");
  const urlInput = document.getElementById("aiBaseUrl");
  const chips = document.getElementById("aiConnected");
  const statusList = document.getElementById("aiModelsStatus");
  const testList = document.getElementById("aiTestResults");
  const queryPicker = modelPicker(document.getElementById("aiModelQueries"));
  const catPicker = modelPicker(document.getElementById("aiModelCat"));
  let current = "";
  let keyUnlocked = false;

  const draft = (id) => (drafts[id] ||= { key: "", baseUrl: null });
  const hasDraft = (id) => !!drafts[id] && (drafts[id].key !== "" || (drafts[id].baseUrl || "") !== "");
  const unsaved = () => Object.keys(drafts).filter(hasDraft);

  const renderChips = () => {
    const connected = providers.filter((p) => p.connected);
    const pending = unsaved().filter((id) => !byId(id)?.connected);
    sel.querySelectorAll("option[value]").forEach((o) => {
      const p = byId(o.value);
      if (p) o.textContent = p.label + (p.connected ? " ✓" : "");
    });
    if (!connected.length && !pending.length) {
      chips.innerHTML = `<span class="muted">No providers connected yet.</span>`;
      return;
    }
    chips.innerHTML = connected.map((p) => `<span class="ai-chip${p.id === current ? " current" : ""}">
        <button type="button" class="ai-chip-name" data-ai-pick="${esc(p.id)}" title="Edit ${esc(p.short)}">${esc(p.short)}</button>
        <button type="button" class="ai-chip-x" data-ai-disconnect="${esc(p.id)}" aria-label="Disconnect ${esc(p.short)}" title="Disconnect">×</button>
      </span>`).join("") +
      pending.map((id) => `<span class="ai-chip pending${id === current ? " current" : ""}">
        <button type="button" class="ai-chip-name" data-ai-pick="${esc(id)}" title="Not saved yet">${esc(short(id))} · unsaved</button>
      </span>`).join("");
  };

  // Show the boxes for one provider: none at all for "None", an endpoint
  // where it has one, and a key where it takes one. A saved key shows as
  // stars until the box is clicked; no key shows "None".
  const show = (id) => {
    current = id;
    sel.value = id;
    const p = byId(id);
    keyUnlocked = false;
    urlRow.hidden = !p || !p.url;
    keyRow.hidden = !p || !(p.needs_key || p.id === "custom");
    if (p) {
      const d = drafts[id];
      urlInput.value = d && d.baseUrl !== null ? d.baseUrl : p.base_url || "";
      urlInput.placeholder = p.default_url || "https://your-endpoint.example.com/v1";
      keyLabel.textContent = `${p.short} API key`;
      if (d && d.key) {
        keyInput.readOnly = false; keyInput.type = "password"; keyInput.value = d.key;
      } else if (p.has_key) {
        keyInput.readOnly = true; keyInput.type = "text"; keyInput.value = KEY_MASK;
      } else {
        keyInput.readOnly = false; keyInput.type = "password"; keyInput.value = "";
      }
      keyInput.placeholder = p.id === "custom" ? "None (optional)" : "None";
    }
    renderChips();
  };

  keyInput.addEventListener("focus", () => {
    if (keyInput.readOnly && keyInput.value === KEY_MASK) {
      keyUnlocked = true;
      keyInput.value = "";
      keyInput.type = "password";
      keyInput.readOnly = false;
      keyInput.placeholder = "Type a new key to replace the saved one";
    }
  });
  keyInput.addEventListener("blur", () => {
    if (keyUnlocked && keyInput.value.trim() === "") show(current);
  });
  keyInput.addEventListener("input", () => { if (current) draft(current).key = keyInput.value.trim(); });
  keyInput.addEventListener("change", () => { renderChips(); loadModels(); });
  urlInput.addEventListener("input", () => { if (current) draft(current).baseUrl = urlInput.value.trim(); });
  urlInput.addEventListener("change", () => { renderChips(); loadModels(); });
  sel.addEventListener("change", () => show(sel.value));

  chips.addEventListener("click", (e) => {
    const pick = e.target.closest("[data-ai-pick]");
    if (pick) { show(pick.dataset.aiPick); return; }
    const x = e.target.closest("[data-ai-disconnect]");
    if (!x) return;
    const id = x.dataset.aiDisconnect;
    showConfirm(`Disconnect ${short(id)}?`,
      "Its saved key is removed. A job using one of its models goes back to the default model of another connected provider.",
      "Disconnect", () => save({ keys: { [id]: { clear: true } } }, `${short(id)} disconnected`, [id]), "danger");
  });

  const pickerFallback = (value) => {
    const i = value.indexOf(":");
    const provider = value.slice(0, i), id = value.slice(i + 1);
    return { key: value, id, name: id, provider, short: short(provider) };
  };

  // The model menus fill by themselves: when the page opens and whenever a
  // key or endpoint changes. Nothing is said unless a provider couldn't be
  // reached.
  let modelsToken = 0;
  const loadModels = async () => {
    const token = ++modelsToken;
    const keys = {};
    for (const id of unsaved()) keys[id] = { api_key: drafts[id].key, base_url: drafts[id].baseUrl || "" };
    const setStatus = (items) => {
      statusList.innerHTML = items.join("");
      statusList.hidden = !items.length;
    };
    try {
      const r = await fetch("/api/v1/settings/ai/models", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ keys }),
      });
      const b = await r.json().catch(() => ({}));
      if (token !== modelsToken) return; // a later change is loading
      if (!r.ok || !b.ok) throw new Error(apiErrorFrom(r, b, "Could not list the models"));
      const models = (b.models || []).map((m) => ({ ...m, key: `${m.provider}:${m.id}`, short: short(m.provider) }));
      queryPicker?.setModels(models, pickerFallback);
      catPicker?.setModels(models, pickerFallback);
      setStatus((b.sources || []).filter((src) => src.error).map((src) =>
        aiProblemHTML(src.label, src.error, "Showing a built-in list of its models instead.")));
    } catch (e) {
      if (token === modelsToken) setStatus([`<li class="ai-status-bad"><span class="ai-status-text">Couldn't load the models: ${esc(e.message)}</span></li>`]);
    }
  };

  const roleModels = () => {
    const out = {};
    for (const [role, picker] of [["queries", queryPicker], ["categorisation", catPicker]]) {
      const v = picker ? picker.value : "";
      const i = v.indexOf(":");
      out[role] = i > 0 ? v.slice(i + 1) : "";
      out[role + "_provider"] = i > 0 ? v.slice(0, i) : "";
    }
    out.default = out.categorisation;
    return out;
  };

  // done names the providers whose drafts the save settles.
  const save = (body, message, done) => fetch("/api/v1/settings/ai", {
    method: "PUT",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify(body),
  }).then(async (r) => {
    const b = await r.json().catch(() => ({}));
    if (!r.ok) { toast(apiErrorFrom(r, b, "Could not save")); return; }
    providers = b.providers || providers;
    (done || []).forEach((id) => { delete drafts[id]; });
    show(current);
    toast(message);
    loadModels();
  }).catch(() => toast("Could not save"));

  document.getElementById("aiSave")?.addEventListener("click", () => {
    const keys = {};
    for (const id of unsaved()) {
      const p = byId(id), d = drafts[id];
      if (p.needs_key && !d.key && !p.has_key) { show(id); toast(`Enter an API key for ${p.short} to connect it`); return; }
      if (p.url === "required" && !(d.baseUrl ?? p.base_url)) { show(id); toast(`Enter the endpoint URL for ${p.short}`); return; }
      keys[id] = { api_key: d.key || undefined, base_url: p.url && d.baseUrl !== null ? d.baseUrl : undefined };
    }
    // Ollama needs neither key nor URL, so picking it is enough to connect it.
    const p = byId(current);
    if (p && !p.connected && !p.needs_key && p.url !== "required" && !keys[current]) keys[current] = {};
    save({ keys, models: roleModels() }, "AI settings saved", Object.keys(keys));
  });

  document.getElementById("aiTest")?.addEventListener("click", (e) => {
    const btn = e.currentTarget;
    btn.disabled = true;
    testList.hidden = false;
    testList.innerHTML = `<li class="muted">Testing…</li>`;
    fetch("/api/v1/settings/ai/test", { method: "POST", headers: { "X-CSRF-Token": csrfToken() } })
      .then((r) => jsonOk(r))
      .then((b) => {
        const results = b.results || [];
        const items = results.map((res) => res.ok
          ? `<li class="ai-status-ok"><span class="ai-status-icon" aria-hidden="true">✓</span>
              <span class="ai-status-text"><strong>${esc(res.label)}</strong> works${res.model ? ` · <span class="mono">${esc(res.model)}</span>` : ""} · ${res.latency_ms} ms</span></li>`
          : aiProblemHTML(res.label, res.error, res.model ? `Model: ${res.model}` : ""));
        if (!results.length) items.push(`<li class="muted">Connect a provider and save, then test it.</li>`);
        if (unsaved().length) items.push(`<li class="muted">Unsaved changes aren't tested. Save first to test them.</li>`);
        testList.innerHTML = items.join("");
      })
      .catch((err) => { testList.innerHTML = `<li class="ai-status-bad"><span class="ai-status-text">${esc(err.message || "Test failed")}</span></li>`; })
      .finally(() => { btn.disabled = false; });
  });

  show(providers.find((p) => p.connected)?.id || "");
  if (providers.some((p) => p.connected)) loadModels();
}

// One provider's problem, from the server's explained error.
function aiProblemHTML(label, error, note) {
  const err = error || { summary: "Unknown error" };
  return `<li class="ai-status-bad"><span class="ai-status-icon" aria-hidden="true">!</span>
    <span class="ai-status-text"><strong>${esc(label)}</strong>: ${esc(err.summary)}
    ${err.detail ? `<span class="ai-status-detail">${esc(err.detail)}</span>` : ""}
    ${note ? `<span class="ai-status-detail">${esc(note)}</span>` : ""}</span></li>`;
}

function openRecurring(key) {
  fetch(`/api/v1/recurring/${encodeURIComponent(key)}/transactions`)
    .then((r) => jsonOk(r))
    .then((body) => {
      const txs = body.transactions || [];
      const list = document.getElementById("recurringTxList");
      list.innerHTML = txs.map((t) => recurringRowHTML(t)).join("");
      document.getElementById("recurringTitle").textContent = (body.series && body.series.label) || "Recurring series";
      document.getElementById("recurringSub").textContent =
        txs.length + (txs.length === 1 ? " transaction" : " transactions") + " · auto-detected series";
      let total = 0;
      txs.forEach((t) => { total += t.amount_minor || 0; });
      document.getElementById("recurringTotal").textContent =
        (total < 0 ? "\u2212" : total > 0 ? "+" : "") + fmtMoneyMinor(total, body.currency || "GBP");
      document.getElementById("recurringTotalLabel").textContent = total < 0 ? "Total spent" : total > 0 ? "Total received" : "Total";
      document.getElementById("recurringSearch").href = recurringSearchHref(txs);
      openModal("recurringModal");
    })
    .catch(() => toast("Could not load the series"));
}
// A Transactions search for the series, like Top merchants' links: the text
// its descriptions share ("Pure Gym Ltd - 02410…0002" and "…0003" share
// "Pure Gym Ltd"), cut back to a whole word, plus their type when they agree.
function recurringSearchHref(txs) {
  const descs = txs.map((t) => t.description || "");
  let common = descs[0] || "";
  for (const d of descs) {
    let i = 0;
    while (i < common.length && i < d.length && common[i].toLowerCase() === d[i].toLowerCase()) i++;
    common = common.slice(0, i);
  }
  if (descs.some((d) => d.length > common.length && /[\p{L}\p{N}]/u.test(d[common.length]))) {
    common = common.replace(/[\p{L}\p{N}]+$/u, "");
  }
  common = common.replace(/[^\p{L}\p{N})]+$/u, "").trim();
  const params = new URLSearchParams();
  if (common) params.set("q", common);
  const types = new Set(txs.map((t) => t.type));
  if (types.size === 1 && (types.has("expense") || types.has("income"))) params.set("type", [...types][0]);
  const qs = params.toString();
  return "/transactions" + (qs ? "?" + qs : "");
}
// ---- Grouping rules, shown only in the query window. Both widgets read the
// rules live, so an old answer still shows what's true now.
function isRulesWidget(type) { return type === "group_rule" || type === "group_rules" || type === "change" || type === "valuation" || type === "link_transfers"; }
function ruleText(r) {
  return `${r.match === "is" ? "is" : "contains"} “${r.text}”`;
}
function hydrateRules(root) {
  (root || document).querySelectorAll(".rules-host").forEach((host) => {
    if (host.dataset.rulesKind === "list") loadRulesList(host);
    else if (host.dataset.rulesKind === "change") loadChange(host);
    else if (host.dataset.rulesKind === "valuation") loadValuationDraft(host);
    else if (host.dataset.rulesKind === "link_transfers") loadPendingTransfers(host);
    else loadRuleDraft(host);
  });
}
function loadRulesList(host) {
  fetch("/api/v1/group-rules").then((r) => jsonOk(r)).then((body) => {
    const items = body.items || [];
    if (!items.length) {
      host.innerHTML = `<p class="rules-empty">No grouping rules yet. Ask to group transactions that should count as one merchant.</p>`;
      return;
    }
    host.innerHTML = `<div class="table-widget"><table class="rules-table">
      <thead><tr><th>Group</th><th>Description</th><th class="num">Transactions</th><th></th></tr></thead>
      <tbody>${items.map((r) => `<tr>
        <td>${esc(r.group)}</td><td class="cell-sub">${esc(ruleText(r))}</td><td class="num">${r.count}</td>
        <td class="row-actions"><button class="widget-btn" data-delete-rule="${esc(r.id)}" data-rule-label="${esc(r.group + " · " + ruleText(r))}" title="Delete rule" aria-label="Delete rule"><svg class="icon"><use href="#i-close"/></svg></button></td>
      </tr>`).join("")}</tbody></table></div>`;
  }).catch((e) => { host.innerHTML = `<p class="rules-empty">${esc(e.message || "Could not load the rules.")}</p>`; });
}
function loadRuleDraft(host) {
  let draft;
  try { draft = JSON.parse(host.dataset.draft || "{}"); } catch (e) { draft = {}; }
  fetch("/api/v1/group-rules/preview", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify(draft),
  }).then(async (r) => {
    const p = await r.json();
    if (!r.ok) throw new Error((p.error && p.error.message) || "preview failed");
    const total = (p.matched || []).reduce((n, m) => n + m.count, 0);
    host.innerHTML = `<div class="rule-draft">
      <p class="rule-draft-head">Transactions whose description ${(p.rules || []).map((r) => `<strong>${esc(ruleText(r))}</strong>`).join(" or ")}
        go in the group <strong>${esc(p.group)}</strong>:</p>
      <div class="table-widget"><table class="rules-table">
        <thead><tr><th>Description</th><th class="num">Transactions</th></tr></thead>
        <tbody>${(p.matched || []).map((m) => `<tr><td>${esc(m.description)}${m.current ? ` <span class="cell-sub">· now in ${esc(m.current)}</span>` : ""}</td><td class="num">${m.count}</td></tr>`).join("")}</tbody>
      </table></div>
      <div class="rule-draft-foot">
        <span class="cell-sub">${total} transaction${total === 1 ? "" : "s"}${p.created ? " · rule created" : ""}</span>
        ${p.created ? "" : `<button class="btn btn-primary btn-xs" data-create-rule>Create rule</button>`}
      </div></div>`;
  }).catch((e) => { host.innerHTML = `<p class="rules-empty">${esc(e.message || "Could not check the rule.")}</p>`; });
}
// A change the AI proposed: every transaction it would touch, and Apply.
// The preview is read live, so an old answer shows whether it was applied.
function loadChange(host) {
  fetch("/api/v1/changes/preview", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: host.dataset.draft,
  }).then(async (r) => {
    const p = await r.json();
    if (!r.ok) throw new Error((p.error && p.error.message) || "preview failed");
    const rows = p.rows || [];
    const skipped = p.applied ? 0 : rows.length - p.ready;
    host.innerHTML = `<div class="rule-draft">
      <p class="rule-draft-head">${p.applied ? `<strong>Applied:</strong> ${esc(p.done)}. Now:`
        : `<strong>${esc(p.summary)}</strong>${skipped ? `, leaving ${skipped} as ${skipped === 1 ? "it is" : "they are"}` : ""}:`}</p>
      <div class="table-widget change-rows"><table class="rules-table">
        <thead><tr><th>Date</th><th>Description</th><th>Category</th><th class="num">Amount</th></tr></thead>
        <tbody>${rows.map((t) => `<tr class="${t.skip ? "change-skip" : ""}">
          <td class="cell-sub cell-date">${dateHTML(t.date)}</td>
          <td>${esc(t.description)} <span class="cell-sub">· ${esc(t.account)}</span></td>
          <td class="cell-sub">${t.skip ? `Skipped: ${esc(t.skip)}` : t.deleted ? "Deleted" : esc(t.category || (t.linked ? "Transfer" : "Uncategorised"))}</td>
          <td class="num">${fmtMoneySigned(t.amount_minor, t.currency)}</td></tr>`).join("")}</tbody>
      </table></div>
      <div class="rule-draft-foot">
        <span class="cell-sub">${p.applied ? `Applied · <a href="/history#audit">undo in History</a>` : "Nothing changes until you apply it."}</span>
        ${p.applied || !p.ready ? "" : `<button class="btn btn-primary btn-xs" data-apply-change data-change-action="${esc(p.proposal.action)}" data-count="${p.ready}">Apply</button>`}
      </div></div>`;
  }).catch((e) => { host.innerHTML = `<p class="rules-empty">${esc(e.message || "Could not check the change.")}</p>`; });
}
// An account's balance, drafted from a question ("my ISA is 50,000").
// It's read against the account each time, so an old answer shows whether
// it was recorded.
function loadValuationDraft(host) {
  let d;
  try { d = JSON.parse(host.dataset.draft || "{}"); } catch (e) { d = {}; }
  const id = encodeURIComponent(d.account_id || "");
  Promise.all([
    fetch(`/api/v1/accounts/${id}`).then((r) => jsonOk(r)),
    fetch(`/api/v1/accounts/${id}/valuations`).then((r) => jsonOk(r)),
  ]).then(([acct, vals]) => {
    const cc = acct.currency || d.currency || "GBP";
    host.dataset.accountType = acct.type || "";
    const money = (m) => (m < 0 ? "\u2212" : "") + fmtMoneyMinor(m, cc);
    const recorded = (vals.items || []).some((v) => v.date === d.date && v.value_minor === d.value_minor);
    const diff = d.value_minor - acct.current_balance_minor;
    host.innerHTML = `<div class="rule-draft valuation-draft">
      <p class="rule-draft-head">${recorded ? "<strong>Recorded:</strong> " : ""}<strong>${esc(acct.name)}</strong>
        ${acct.type === "savings" ? (recorded ? "was worth" : "is worth") : (recorded ? "had a balance of" : "has a balance of")} <strong>${esc(money(d.value_minor))}</strong> on ${esc(dayMonthYearJS(d.date))}</p>
      ${recorded ? "" : `<div class="valuation-compare">
        <span><span class="cell-sub">Now</span> ${esc(money(acct.current_balance_minor))}</span>
        <svg class="icon"><use href="#i-arrow-right"/></svg>
        <span><strong>${esc(money(d.value_minor))}</strong></span>
        ${diff ? `<span class="cell-sub">(${esc(fmtMoneySigned(diff, cc))})</span>` : ""}
      </div>`}
      <div class="rule-draft-foot">
        <span class="cell-sub">${recorded ? `Recorded · <a href="/history#audit">undo in History</a>` : "Nothing changes until you record it."}</span>
        ${recorded ? "" : `<button class="btn btn-primary btn-xs" data-record-valuation>Record</button>`}
      </div></div>`;
  }).catch((e) => { host.innerHTML = `<p class="rules-empty">${esc(e.message || "Could not load the account.")}</p>`; });
}
// Transfer linking from Queries: the pairs detection would link now, read
// live, linked with one press. done is how many the press linked.
function loadPendingTransfers(host, done) {
  fetch("/api/v1/transfers/pending").then((r) => jsonOk(r)).then((body) => {
    const pairs = body.items || [];
    const leg = (tx) => `${esc(tx.description)}<div class="cell-sub">${esc(tx.account ? tx.account.name : "")} · ${dateHTML(tx.date)}</div>`;
    const head = done
      ? `<strong>Linked ${done} transfer${done === 1 ? "" : "s"}.</strong>${pairs.length ? ` ${pairs.length} more found:` : ""}`
      : pairs.length
        ? `<strong>${pairs.length} transfer${pairs.length === 1 ? "" : "s"}</strong> to link, money out of one account matched with the same money into another:`
        : "Every transfer with both sides imported is already linked.";
    host.innerHTML = `<div class="rule-draft">
      <p class="rule-draft-head">${head}</p>
      ${pairs.length ? `<div class="table-widget change-rows"><table class="rules-table">
        <thead><tr><th>Out</th><th>In</th><th class="num">Amount</th></tr></thead>
        <tbody>${pairs.map((p) => `<tr><td>${leg(p.source)}</td><td>${leg(p.destination)}</td>
          <td class="num">${esc(fmtMoneyMinor(-p.source.amount_minor, p.source.currency || "GBP"))}</td></tr>`).join("")}</tbody>
      </table></div>` : ""}
      <div class="rule-draft-foot">
        <span class="cell-sub">${done ? `Linked · <a href="/history#audit">undo in History</a>` : pairs.length ? "Nothing changes until you link them." : "Linking also runs after every import."}</span>
        ${pairs.length ? `<button class="btn btn-primary btn-xs" data-link-transfers>Link ${pairs.length === 1 ? "it" : `all ${pairs.length}`}</button>` : ""}
      </div></div>`;
  }).catch((e) => { host.innerHTML = `<p class="rules-empty">${esc(e.message || "Could not look for transfers.")}</p>`; });
}
function linkTransfers(btn) {
  const host = btn.closest(".rules-host");
  btn.disabled = true;
  fetch("/api/v1/transfers/detect", { method: "POST", headers: { "X-CSRF-Token": csrfToken() } })
    .then(async (r) => {
      if (!r.ok) { toast(await apiErrorText(r, "Could not link the transfers")); btn.disabled = false; return; }
      const n = (await r.json().catch(() => ({}))).linked || 0;
      toast(`Linked ${n} transfer${n === 1 ? "" : "s"}`);
      loadPendingTransfers(host, n);
    }).catch(() => { toast("Could not link the transfers"); btn.disabled = false; });
}
function recordValuation(btn) {
  const host = btn.closest(".rules-host");
  let d;
  try { d = JSON.parse(host.dataset.draft || "{}"); } catch (e) { return; }
  btn.disabled = true;
  fetch(`/api/v1/accounts/${encodeURIComponent(d.account_id)}/valuations`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify({ value_minor: d.value_minor, date: d.date, note: "From Queries" }),
  }).then(async (r) => {
    if (!r.ok) { toast(await apiErrorText(r, "Could not record the value")); btn.disabled = false; return; }
    toast(valuationToast(await r.json().catch(() => null), host.dataset.accountType));
    loadValuationDraft(host);
  }).catch(() => { toast("Could not record the value"); btn.disabled = false; });
}
function applyChange(btn) {
  const host = btn.closest(".rules-host");
  btn.disabled = true;
  fetch("/api/v1/changes/apply", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: host.dataset.draft,
  }).then(async (r) => {
    if (!r.ok) { toast(await apiErrorText(r, "Could not apply the change")); btn.disabled = false; return; }
    toast("Change applied");
    loadChange(host);
  }).catch(() => { toast("Could not apply the change"); btn.disabled = false; });
}
document.addEventListener("click", (e) => {
  const apply = e.target.closest("[data-apply-change]");
  if (apply) {
    if (apply.dataset.changeAction === "delete") {
      const n = apply.dataset.count;
      showConfirm(`Delete ${n} transaction${n === "1" ? "" : "s"}?`, "You can undo this from History.", "Delete", () => applyChange(apply), "danger");
    } else applyChange(apply);
    return;
  }
  const record = e.target.closest("[data-record-valuation]");
  if (record) { recordValuation(record); return; }
  const linkAll = e.target.closest("[data-link-transfers]");
  if (linkAll) { linkTransfers(linkAll); return; }
  const create = e.target.closest("[data-create-rule]");
  if (create) {
    const host = create.closest(".rules-host");
    create.disabled = true;
    fetch("/api/v1/group-rules", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: host.dataset.draft,
    }).then(async (r) => {
      if (!r.ok) { toast(await apiErrorText(r, "Could not create the rule")); create.disabled = false; return; }
      toast("Rule created");
      hydrateRules();
    }).catch(() => { toast("Could not create the rule"); create.disabled = false; });
    return;
  }
  const del = e.target.closest("[data-delete-rule]");
  if (del) {
    showConfirm("Delete this rule?", `${del.dataset.ruleLabel}. Its transactions leave the group, and a group with no rules left is gone. You can undo this from History.`, "Delete", () => {
      fetch(`/api/v1/group-rules/${encodeURIComponent(del.dataset.deleteRule)}`, {
        method: "DELETE",
        headers: { "X-CSRF-Token": csrfToken() },
      }).then(async (r) => {
        if (!r.ok) { toast(await apiErrorText(r, "Could not delete the rule")); return; }
        toast("Rule deleted");
        hydrateRules();
      }).catch(() => toast("Could not delete the rule"));
    }, "danger");
  }
});
document.addEventListener("DOMContentLoaded", () => {
  const box = document.getElementById("queryMessages");
  if (!box) return;
  hydrateRules(box);
  // Answers and older pages of the conversation arrive later.
  new MutationObserver((muts) => {
    for (const m of muts) m.addedNodes.forEach((n) => {
      if (n.nodeType !== 1) return;
      if (n.matches(".rules-host")) hydrateRules(n.parentNode);
      else if (n.querySelector(".rules-host")) hydrateRules(n);
    });
  }).observe(box, { childList: true, subtree: true });
});

function recurringRowHTML(t) {
  const cat = t.category ? t.category.name : "Uncategorised";
  const amt = fmtMoneySigned(t.amount_minor, t.currency || "GBP");
  const cls = t.type === "transfer" ? "transfer" : t.amount_minor >= 0 ? "pos" : "neg";
  return `<div class="tx-row recurring-row">
    <div class="tx-icon${t.type === "transfer" ? " transfer" : ""}">${t.type === "transfer" ? "⇄" : esc(initialOf(t.description))}</div>
    <div class="tx-main"><div class="tx-merchant">${esc(t.description)}</div><div class="tx-sub">${esc(cat)} · ${esc(t.account ? t.account.name : "")}</div></div>
    <div class="tx-date">${esc(shortDate(t.date))}</div>
    <div class="tx-amount ${cls}">${esc(amt)}</div>
  </div>`;
}
function fmtMoneyMinor(minor, cc) {
  const sym = ({ GBP: "£", USD: "$", EUR: "€" })[cc] || cc + " ";
  const n = Math.abs(Number(minor)) / 100;
  return sym + n.toLocaleString("en-GB", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}
// Any currency in its own minor units (yen has none), e.g. "−€10.00".
function fmtForeign(minor, cc) {
  try {
    const f = new Intl.NumberFormat("en-GB", { style: "currency", currency: cc });
    const digits = f.resolvedOptions().maximumFractionDigits;
    return (minor < 0 ? "\u2212" : "") + f.format(Math.abs(minor) / 10 ** digits);
  } catch (e) {
    return `${cc} ${(minor / 100).toFixed(2)}`;
  }
}
function fmtMoneySigned(minor, cc) {
  const sign = minor < 0 ? "\u2212" : minor > 0 ? "+" : "";
  return sign + fmtMoneyMinor(minor, cc);
}
function initialOf(s) {
  const words = String(s || "").trim().split(/\\s+/);
  return (words[0] && words[0][0] ? words[0][0] : "?").toUpperCase();
}
function monthEndOf(ym) {
  const [y, m] = String(ym || "").split("-").map(Number);
  if (!y || !m) return ym;
  return ym + "-" + String(new Date(y, m, 0).getDate()).padStart(2, "0");
}

function shortDate(iso) {
  if (!iso) return "";
  const d = new Date(iso + "T00:00:00");
  if (isNaN(d)) return iso;
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" });
}

document.addEventListener("glowworm-toast", (e) => {
  if (e.detail && e.detail.value) toast(e.detail.value);
});

const ACCOUNT_BANK_FIELDS = { account_number: "accNumber", sort_code: "accSortCode", iban: "accIban", bic: "accBic" };
// The bank details of the account being edited, as the form loaded them.
// Only the ones changed since are sent, and the server keeps the rest, so a
// form that didn't load can't blank them.
let accountLoaded = null;

function accountPayload() {
  const opening = parseFloat(document.getElementById("accOpening").value);
  const body = {
    name: document.getElementById("accName").value.trim(),
    type: document.getElementById("accType").value,
    opening_balance_minor: isNaN(opening) ? 0 : Math.round(opening * 100),
    opening_balance_date: document.getElementById("accOpeningDate").value || "",
  };
  const editing = !!document.getElementById("accountForm").dataset.accountId;
  Object.entries(ACCOUNT_BANK_FIELDS).forEach(([key, el]) => {
    const value = document.getElementById(el).value.trim();
    if (!editing || (accountLoaded && value !== accountLoaded[key])) body[key] = value;
  });
  return body;
}

// A date box wrapped in .date-field shows "Select date" while it's empty.
// Setting .value from code fires no event, so callers sync it themselves.
function syncDateField(input) {
  const wrap = input && input.closest("[data-date-field]");
  if (wrap) wrap.classList.toggle("is-empty", !input.value);
}
document.addEventListener("input", (e) => { if (e.target.type === "date") syncDateField(e.target); });
document.addEventListener("change", (e) => { if (e.target.type === "date") syncDateField(e.target); });

function openAccountForm(id) {
  const modal = document.getElementById("accountModal");
  const form = document.getElementById("accountForm");
  form.dataset.accountId = id || "";
  delete modal.dataset.changed;
  document.getElementById("accSaveBtn").textContent = id ? "Save changes" : "Add account";
  document.getElementById("accountModalTitle").textContent = id ? "Account settings" : "Add account";
  document.getElementById("accountModalSub").textContent = id ? "Edit this account's details." : "Add a new account to Glowworm.";
  document.getElementById("accDangerZone").hidden = !id;
  accountLoaded = null;
  ["accName", "accOpening", "accOpeningDate", ...Object.values(ACCOUNT_BANK_FIELDS)].forEach((x) => (document.getElementById(x).value = ""));
  syncDateField(document.getElementById("accOpeningDate"));
  if (id) {
    fetch(`/api/v1/accounts/${id}`).then((r) => jsonOk(r)).then((a) => {
      if (form.dataset.accountId !== id) return;
      document.getElementById("accName").value = a.name || "";
      document.getElementById("accType").value = a.type || "debit";
      document.getElementById("accOpening").value = a.opening_balance_minor != null ? (a.opening_balance_minor / 100).toFixed(2) : "";
      document.getElementById("accOpeningDate").value = a.opening_balance_date || "";
      syncDateField(document.getElementById("accOpeningDate"));
      accountLoaded = {};
      Object.entries(ACCOUNT_BANK_FIELDS).forEach(([key, el]) => {
        accountLoaded[key] = a[key] || "";
        document.getElementById(el).value = accountLoaded[key];
      });
    }).catch((e) => toast(e.message || "Could not load account"));
  } else {
    document.getElementById("accType").value = "debit";
  }
  modal.hidden = false;
  modal.scrollTop = 0;
  // Only a new account starts in the Name box, and not on a touch screen,
  // where focus throws up the keyboard before anything is tapped.
  if (!id && !window.matchMedia("(pointer: coarse)").matches) document.getElementById("accName").focus();
}

function localISODate(d) {
  const t = d || new Date();
  return `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, "0")}-${String(t.getDate()).padStart(2, "0")}`;
}
function dayMonthYearJS(iso) {
  const d = new Date(iso + "T00:00:00");
  return isNaN(d) ? iso : d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" });
}
// What recording a balance found: a statement that disagrees with the
// transactions, or a savings account's returns since its last value.
function valuationToast(res, type, hadEarlier) {
  const drift = res && res.drift_minor;
  const cc = (res && res.valuation && res.valuation.currency) || "GBP";
  if (type === "savings") return drift && hadEarlier ? `Balance updated · ${fmtMoneySigned(drift, cc)} since its last value` : "Balance updated";
  if (!drift) return "Balance recorded · matches your transactions";
  return `Balance recorded · ${fmtMoneyMinor(Math.abs(drift), cc)} out from your transactions`;
}
// "50,000", "£50000.00" and "50k" all read as 50,000.
function parseMoneyInput(raw) {
  let t = String(raw || "").replace(/[\s,£$€]/g, "").toLowerCase();
  let mult = 1;
  if (t.endsWith("k")) { mult = 1000; t = t.slice(0, -1); }
  if (!/^-?(\d+\.?\d*|\.\d+)$/.test(t)) return null;
  return Math.round(parseFloat(t) * mult * 100);
}
const signedMoney = (minor, cc) => (minor < 0 ? "−" : "") + fmtMoneyMinor(minor, cc);

// A balance panel (frag_balance_panel.html) records an account's balance on
// a day: a savings account's value on the Savings tab, or the balance a bank
// statement shows for a current or credit account on History, which is
// checked against the transactions. Each recorded balance is logged, so
// deleting one can be undone from History. onChange runs after either.
function initBalancePanel(root, onChange) {
  const id = root.dataset.accountId;
  const type = root.dataset.accountType;
  const cc = root.dataset.currency || "GBP";
  const q = (k) => root.querySelector(`[data-bp-${k}]`);
  const input = q("value"), date = q("date"), btn = q("record"), list = q("list");
  date.value = localISODate();
  date.max = localISODate();
  const load = () => {
    fetch(`/api/v1/accounts/${id}`).then((r) => jsonOk(r)).then((a) => {
      q("now").textContent = signedMoney(a.current_balance_minor, cc);
    }).catch(() => {});
    fetch(`/api/v1/accounts/${id}/valuations`).then((r) => jsonOk(r)).then((b) => {
      const items = (b.items || []).slice().sort((x, y) => (y.date + y.created_at).localeCompare(x.date + x.created_at));
      const shown = (v) => signedMoney(v, cc);
      q("asof").textContent = items.length ? `Last set ${dayMonthYearJS(items[0].date)}` : "";
      q("asof").hidden = !items.length;
      q("history").hidden = !items.length;
      q("count").textContent = items.length ? `(${items.length})` : "";
      list.innerHTML = items.map((v) => `<li data-valuation-id="${esc(v.id)}">
          <span class="valuation-date">${esc(dayMonthYearJS(v.date))}</span>
          <span class="valuation-value">${esc(shown(v.value_minor))}</span>
          <button type="button" class="widget-btn" data-valuation-delete title="Delete" aria-label="Delete"><svg class="icon"><use href="#i-close"/></svg></button>
        </li>`).join("");
    }).catch(() => {});
  };
  list.addEventListener("click", (e) => {
    const del = e.target.closest("[data-valuation-delete]");
    if (!del) return;
    const li = del.closest("li");
    showConfirm("Delete this balance?", `The ${li.querySelector(".valuation-value").textContent} recorded on ${li.querySelector(".valuation-date").textContent} is removed. You can undo this from History.`, "Delete", () => {
      fetch(`/api/v1/accounts/${id}/valuations/${li.dataset.valuationId}`, { method: "DELETE", headers: { "X-CSRF-Token": csrfToken() } })
        .then(async (r) => {
          if (!r.ok) { toast(await apiErrorText(r, "Could not delete the balance")); return; }
          toast("Balance deleted");
          load();
          if (onChange) onChange();
        }).catch(() => toast("Could not delete the balance"));
    }, "danger");
  });
  const record = () => {
    const raw = input.value.trim();
    if (raw === "") { input.focus(); return; }
    const minor = parseMoneyInput(raw);
    if (minor === null) { toast("Enter the balance as a number, e.g. 4520.00"); return; }
    // A credit card's balance is what's owed, stored as a negative.
    const body = { value_minor: type === "credit" ? -minor : minor, date: date.value || localISODate(), note: "Manual valuation" };
    const hadEarlier = list.children.length > 0;
    btn.disabled = true;
    fetch(`/api/v1/accounts/${id}/valuations`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify(body),
    }).then(async (r) => {
      if (!r.ok) { toast(await apiErrorText(r, "Could not record the balance")); return; }
      const res = await r.json().catch(() => null);
      if (res && res.valuation && !res.valuation.currency) res.valuation.currency = cc;
      toast(valuationToast(res, type, hadEarlier));
      input.value = "";
      input.blur();
      load();
      if (onChange) onChange();
    }).catch(() => toast("Could not record the balance")).finally(() => { btn.disabled = false; });
  };
  btn.addEventListener("click", record);
  input.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); record(); } });
  // The Savings page sends its balances with the page.
  if (!root.hasAttribute("data-bp-loaded")) load();
}

document.addEventListener("DOMContentLoaded", () => {
  // Savings tab: one panel per savings account.
  document.querySelectorAll("#view-savings [data-balance-panel]").forEach((p) => initBalancePanel(p));
  // History: the Reconciled card opens a statement balance for a current or
  // credit account; closing it after a change reloads the page's figures.
  const modal = document.getElementById("reconcileModal");
  if (!modal) return;
  const select = document.getElementById("reconcileAccount");
  const show = () => modal.querySelectorAll("[data-reconcile-account]").forEach((el) => { el.hidden = el.dataset.reconcileAccount !== select.value; });
  modal.querySelectorAll("[data-balance-panel]").forEach((p) => initBalancePanel(p, () => { modal.dataset.changed = "1"; }));
  select?.addEventListener("change", show);
  if (select) show();
  document.getElementById("reconcileOpen")?.addEventListener("click", () => {
    modal.hidden = false;
    modal.scrollTop = 0;
  });
  modal.querySelectorAll("[data-close-reconcile]").forEach((b) => b.addEventListener("click", () => closeModal("reconcileModal")));
  modal.addEventListener("click", (e) => { if (e.target === modal) closeModal("reconcileModal"); });
});

function saveAccount() {
  const form = document.getElementById("accountForm");
  const id = form.dataset.accountId;
  // An account whose details never arrived isn't saved over with an empty form.
  if (id && !accountLoaded) { toast("This account's details haven't loaded. Close and open it again.", "error"); return; }
  const body = accountPayload();
  const url = id ? `/api/v1/accounts/${id}` : "/api/v1/accounts";
  fetch(url, {
    method: id ? "PATCH" : "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: JSON.stringify(body),
  }).then(async (r) => {
    if (!r.ok) {
      const b = await r.json().catch(() => ({}));
      toast(apiErrorFrom(r, b, "Could not save the account"));
      return null;
    }
    return r.json();
  }).then((acct) => {
    if (!acct) return;
    closeModal("accountModal");
    toast("Account saved");
    location.reload();
  }).catch(() => toast("Could not save the account"));
}

document.addEventListener("DOMContentLoaded", () => {
  document.getElementById("settingsAddAccountBtn")?.addEventListener("click", () => openAccountForm(null));
  document.querySelectorAll("[data-account-form]").forEach((card) =>
    card.addEventListener("click", () => openAccountForm(card.dataset.accountForm)));
  document.getElementById("accSaveBtn")?.addEventListener("click", (e) => { e.preventDefault(); saveAccount(); });
  document.getElementById("accCancelBtn")?.addEventListener("click", () => closeModal("accountModal"));
  document.querySelectorAll("[data-action='close-account']").forEach((b) => b.addEventListener("click", () => closeModal("accountModal")));
  // The new-transaction modal is wired in the boot handler (guarded by
  // #newTxBtn). An unguarded copy here threw on pages without it and killed
  // every binding below.
  document.getElementById("accDeleteBtn")?.addEventListener("click", () => {
    const form = document.getElementById("accountForm");
    const id = form.dataset.accountId;
    if (!id) return;
    const name = document.getElementById("accName").value.trim() || "this account";
    showConfirm(`Delete “${name}”?`,
      "Its transactions, valuations and transfer links are removed everywhere. A copy is kept under Deleted accounts in Settings, so you can restore it later.",
      "Delete account", () => {
        fetch(`/api/v1/accounts/${id}`, { method: "DELETE", headers: { "X-CSRF-Token": csrfToken() } })
          .then(async (r) => {
            if (!r.ok) { toast(await apiErrorText(r, "Could not delete the account")); return; }
            closeModal("accountModal");
            reloadWithToast(`Deleted “${name}” — restore it from Deleted accounts`);
          });
      });
  });

  document.querySelectorAll("[data-close-modal]").forEach((b) =>
    b.addEventListener("click", () => closeModal(b.dataset.closeModal)));
  const instanceBackup = async (password) => {
    const r = await fetch("/api/v1/instance/backup", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ password }),
    });
    if (!r.ok) throw new Error(await apiErrorText(r, "Backup failed"));
    const name = (r.headers.get("Content-Disposition") || "").match(/filename="([^"]+)"/);
    const url = URL.createObjectURL(await r.blob());
    const a = document.createElement("a");
    a.href = url;
    a.download = name ? name[1] : "glowworm-backup.zip";
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  const instanceBackupModal = (afterwards) => {
    const pw = document.getElementById("instanceBackupPassword");
    if (pw) pw.value = "";
    document.getElementById("instanceBackupModal").dataset.after = afterwards || "";
    openModal("instanceBackupModal");
  };
  document.getElementById("instanceBackupBtn")?.addEventListener("click", () => instanceBackupModal());
  document.getElementById("instanceBackupGo")?.addEventListener("click", async (e) => {
    const btn = e.currentTarget;
    const pw = document.getElementById("instanceBackupPassword");
    btn.disabled = true;
    btn.textContent = "Preparing…";
    try {
      await instanceBackup(pw ? pw.value : "");
      closeModal("instanceBackupModal");
      toast("Backup downloaded");
      if (document.getElementById("instanceBackupModal").dataset.after === "delete") openModal("instanceDeleteModal");
    } catch (err) {
      toast(err.message);
    } finally {
      btn.disabled = false;
      btn.textContent = "Download backup";
    }
  });
  const delConfirm = document.getElementById("instanceDeleteConfirm");
  const delGo = document.getElementById("instanceDeleteGo");
  document.getElementById("instanceDeleteBtn")?.addEventListener("click", () => {
    delConfirm.value = "";
    delGo.disabled = true;
    const pw = document.getElementById("instanceDeletePassword");
    if (pw) pw.value = "";
    openModal("instanceDeleteModal");
  });
  document.getElementById("instanceDeleteBackupFirst")?.addEventListener("click", () => {
    closeModal("instanceDeleteModal");
    instanceBackupModal("delete");
  });
  delConfirm?.addEventListener("input", () => { delGo.disabled = delConfirm.value.trim() !== "DELETE"; });
  delGo?.addEventListener("click", () => {
    const pw = document.getElementById("instanceDeletePassword");
    delGo.disabled = true;
    delGo.textContent = "Deleting…";
    fetch("/api/v1/instance/delete", {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ password: pw ? pw.value : "", confirm: delConfirm.value.trim() }),
    }).then(async (r) => {
      if (!r.ok) {
        toast(await apiErrorText(r, "Delete failed"));
        delGo.disabled = false;
        delGo.textContent = "Delete everything";
        return;
      }
      // Straight to making a new account, the setup token filled in.
      const out = await r.json().catch(() => ({}));
      location.replace(typeof out.next === "string" && out.next.startsWith("/setup") ? out.next : "/setup");
    }).catch(() => {
      toast("Delete failed: network error");
      delGo.disabled = false;
      delGo.textContent = "Delete everything";
    });
  });

  document.getElementById("deletedAccounts")?.addEventListener("click", (e) => {
    const li = e.target.closest("[data-archive-id]");
    if (!li) return;
    const { archiveId, archiveName } = li.dataset;
    const restore = e.target.closest("[data-archive-restore]");
    if (restore) {
      restore.disabled = true;
      fetch(`/api/v1/accounts/archives/${archiveId}/restore`, { method: "POST", headers: { "X-CSRF-Token": csrfToken() } })
        .then(async (r) => {
          if (!r.ok) { restore.disabled = false; toast(await apiErrorText(r, "Restore failed")); return; }
          reloadWithToast(restoredMessage(await r.json()));
        });
    } else if (e.target.closest("[data-archive-purge]")) {
      showConfirm(`Delete “${archiveName}” forever?`,
        "Its transactions can't be restored after this. Use Download first if you might need them.",
        "Delete forever", () => {
          fetch(`/api/v1/accounts/archives/${archiveId}`, { method: "DELETE", headers: { "X-CSRF-Token": csrfToken() } })
            .then(async (r) => {
              if (!r.ok) { toast(await apiErrorText(r, "Could not delete")); return; }
              reloadWithToast(`“${archiveName}” deleted for good`);
            });
        });
    }
  });

  const pwOpen = document.getElementById("changePasswordBtn");
  if (pwOpen) {
    pwOpen.addEventListener("click", () => openModal("passwordModal"));
    document.getElementById("passwordCancel")?.addEventListener("click", () => closePasswordModal());
    document.querySelectorAll('[data-action="close-password"]').forEach((b) => b.addEventListener("click", closePasswordModal));
    document.getElementById("passwordModal")?.addEventListener("click", (e) => {
      if (e.target.id === "passwordModal") closePasswordModal();
    });
    const pwSave = document.getElementById("passwordSave");
    pwSave?.addEventListener("click", async () => {
      const cur = document.getElementById("pwCurrent").value;
      const nw = document.getElementById("pwNew").value;
      const cf = document.getElementById("pwConfirm").value;
      if (!cur || !nw) { toast("Fill in both password fields"); return; }
      if (nw !== cf) { toast("New passwords do not match"); return; }
      const r = await fetch("/api/v1/account/password", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ current_password: cur, new_password: nw }),
      });
      const b = await r.json().catch(() => ({}));
      if (!r.ok) { toast(apiErrorFrom(r, b, "Could not change password")); return; }
      closePasswordModal();
      toast("Password changed");
    });
  }

  const nameOpen = document.getElementById("changeNameBtn");
  if (nameOpen) {
    nameOpen.addEventListener("click", () => {
      const shown = document.getElementById("signedInName");
      const input = document.getElementById("nameInput");
      if (input && shown) input.value = shown.dataset.name || "";
      openModal("nameModal");
    });
    document.getElementById("nameCancel")?.addEventListener("click", closeNameModal);
    document.querySelectorAll('[data-action="close-name"]').forEach((b) => b.addEventListener("click", closeNameModal));
    document.getElementById("nameModal")?.addEventListener("click", (e) => {
      if (e.target.id === "nameModal") closeNameModal();
    });
    document.getElementById("nameSaveModal")?.addEventListener("click", () => {
      const name = document.getElementById("nameInput").value.trim();
      fetch("/api/v1/settings", {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
        body: JSON.stringify({ user_name: name }),
      }).then(async (r) => {
        if (!r.ok) {
          const b = await r.json().catch(() => ({}));
          toast(apiErrorFrom(r, b, "Could not update name"));
          return;
        }
        // Without a name, the username signed in with is shown.
        const shown = document.getElementById("signedInName");
        if (shown) {
          shown.dataset.name = name;
          shown.textContent = name || shown.dataset.username || "";
          document.getElementById("signedInLine").hidden = !shown.textContent;
        }
        closeNameModal();
        toast("Name updated");
      }).catch(() => toast("Could not update name"));
    });
  }

  document.getElementById("appearanceSave")?.addEventListener("click", () => {
    const theme = document.getElementById("themeSelect").value;
    const currency = document.getElementById("currencySelect").value;
    fetch("/api/v1/settings", {
      method: "PUT",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ theme, currency }),
    }).then((r) => {
      if (!r.ok) { toast("Could not save settings"); return; }
      try { localStorage.setItem("glowworm-theme", theme); } catch (e) {}
      applyTheme(theme);
      toast("Appearance updated");
    }).catch(() => toast("Could not save settings"));
  });

  const aiRoot = document.getElementById("aiSettings");
  if (aiRoot) setupAISettings(aiRoot);

  // Add and edit share one modal; cat is the category being edited, if any.
  // A category's type is fixed once it exists, so editing hides it.
  const catForm = document.getElementById("categoryForm");
  const catGrid = document.getElementById("categoriesGrid");
  const openCatModal = (cat) => {
    catForm.dataset.categoryId = cat ? cat.id : "";
    document.getElementById("catName").value = cat ? cat.name : "";
    document.getElementById("catType").value = "spend";
    document.getElementById("catDescription").value = cat ? cat.description : "";
    document.getElementById("catModalTitle").textContent = cat ? "Edit category" : "Add category";
    document.getElementById("catModalSub").textContent = cat ? "Rename it or change what the AI is told about it." : "Add a category to sort transactions into.";
    document.getElementById("catSaveBtn").textContent = cat ? "Save changes" : "Add category";
    document.getElementById("catDeleteBtn").hidden = !cat;
    document.getElementById("catTypeRow").hidden = !!cat;
    openModal("categoryModal");
  };
  document.getElementById("addCategoryBtn")?.addEventListener("click", () => openCatModal(null));
  catGrid?.addEventListener("click", (e) => {
    const b = e.target.closest("[data-cat-edit]");
    if (b) openCatModal({ id: b.dataset.catEdit, name: b.dataset.name, description: b.dataset.description || "" });
  });
  document.querySelectorAll("[data-action='close-category-modal']").forEach((b) =>
    b.addEventListener("click", () => closeModal("categoryModal")));
  document.getElementById("categoryModal")?.addEventListener("click", (e) => {
    if (e.target.id === "categoryModal") closeModal("categoryModal");
  });
  catForm?.addEventListener("submit", (e) => {
    e.preventDefault();
    const id = catForm.dataset.categoryId;
    const body = {
      name: document.getElementById("catName").value.trim(),
      type: document.getElementById("catType").value || "spend",
      description: document.getElementById("catDescription").value.trim(),
    };
    if (!body.name) { toast("Category name is required"); return; }
    fetch(id ? `/api/v1/categories/${id}` : "/api/v1/categories", {
      method: id ? "PATCH" : "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify(body),
    }).then(async (r) => {
      if (!r.ok) {
        const b = await r.json().catch(() => ({}));
        toast(apiErrorFrom(r, b, "Could not save the category"));
        return;
      }
      reloadWithToast(id ? "Category updated" : "Category added");
    }).catch(() => toast("Could not save the category"));
  });
  document.getElementById("catDeleteBtn")?.addEventListener("click", () => {
    const id = catForm.dataset.categoryId;
    if (!id) return;
    showConfirm("Delete category?", "Its transactions will be marked as Uncategorised.", "Delete category", () => {
      fetch(`/api/v1/categories/${id}`, { method: "DELETE", headers: { "X-CSRF-Token": csrfToken() } })
        .then((r) => { if (!r.ok) { toast("Could not delete"); return; } reloadWithToast("Category deleted"); });
    });
  });

  // History actions don't ask in a dialog. The rows an action touches first
  // take on how they'll look after it (struck through and hollow if they'll
  // be undone, solid if they come back, "Now" where the present will be),
  // and a bar at the bottom confirms or cancels.
  const confirmBar = document.getElementById("historyConfirm");
  let pending = null;
  const plural = (n, one, many) => `${n} ${n === 1 ? one : many || one + "s"}`;
  const entryIdsIn = (v) => new Set((v || "").split(",").filter(Boolean));
  function clearPreview() {
    const list = document.getElementById("historyList");
    list?.classList.remove("is-previewing");
    document.querySelectorAll(".pv-undo, .pv-redo, .pv-delete, .pv-head, .pv-source").forEach((el) =>
      el.classList.remove("pv-undo", "pv-redo", "pv-delete", "pv-head", "pv-source"));
    document.getElementById("view-history")?.classList.remove("is-confirming");
    if (confirmBar) confirmBar.hidden = true;
    pending = null;
  }
  // Rows inside a folded group mark the group too, so it shows while closed.
  function mark(rows, cls) {
    rows.forEach((row) => {
      row.classList.add(cls);
      const group = row.closest(".history-group");
      if (group && cls === "pv-undo") group.classList.add(cls);
    });
  }
  // The newest block that will be in effect once the action has run.
  function markNextHead(list) {
    if (!list.querySelector(".is-head")) return; // a filtered list has no "Now"
    for (const block of list.children) {
      let active;
      if (block.classList.contains("hg-undone")) active = block.classList.contains("pv-redo");
      else if (block.classList.contains("history-group")) active = [...block.querySelectorAll(".history-item")].some((r) => !r.classList.contains("pv-undo"));
      else active = block.matches(".hg-row:not(.inactive):not(.pv-undo)");
      if (active) { block.classList.add("pv-head"); return; }
    }
  }
  function preview(source, opts) {
    clearPreview();
    const list = document.getElementById("historyList");
    if (!list || !confirmBar) return;
    list.classList.add("is-previewing");
    source.classList.add("pv-source");
    mark(opts.undo || [], "pv-undo");
    if (opts.block) opts.block.classList.add(opts.redo ? "pv-redo" : "pv-delete");
    markNextHead(list);
    document.getElementById("historyConfirmTitle").textContent = opts.title;
    document.getElementById("historyConfirmDetail").textContent = opts.detail || "";
    confirmBar.querySelector("use").setAttribute("href", opts.icon);
    confirmBar.classList.toggle("danger", !!opts.danger);
    const ok = document.getElementById("historyConfirmOk");
    ok.textContent = opts.ok;
    ok.disabled = false;
    ok.classList.toggle("btn-primary", !opts.danger);
    ok.classList.toggle("btn-danger", !!opts.danger);
    document.getElementById("view-history")?.classList.add("is-confirming");
    confirmBar.hidden = false;
    pending = opts;
    ok.focus({ preventScroll: true });
  }
  function runPending() {
    if (!pending) return;
    const { url, body, done } = pending;
    const ok = document.getElementById("historyConfirmOk");
    ok.disabled = true;
    fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: body ? JSON.stringify(body) : undefined,
    }).then(async (r) => {
      if (!r.ok) {
        const b = await r.json().catch(() => ({}));
        ok.disabled = false;
        toast(apiErrorFrom(r, b, "That didn't work"));
        return;
      }
      // Parts changed again later are kept; say which.
      const out = await r.json().catch(() => ({}));
      reloadWithToast(out.notes && out.notes.length ? done + ". " + out.notes.join(". ") + "." : done);
    }).catch(() => { ok.disabled = false; toast("That didn't work"); });
  }
  document.getElementById("historyConfirmOk")?.addEventListener("click", runPending);
  document.getElementById("historyConfirmCancel")?.addEventListener("click", clearPreview);
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && pending) clearPreview(); });
  window.clearHistoryPreview = clearPreview;

  const idsOf = (v) => v.split(",").map(Number).filter(Boolean);
  const rowsIn = (root) => [...root.querySelectorAll(".history-item[data-entry-id]")];
  function wireAuditActions(root) {
    const wire = (sel, fn) => (root || document).querySelectorAll(sel).forEach((btn) => {
      if (btn.dataset.wired) return;
      btn.dataset.wired = "1";
      btn.addEventListener("click", (e) => {
        e.preventDefault(); // some sit in a <summary>; don't toggle the group
        e.stopPropagation();
        fn(btn);
      });
    });
    wire("[data-undo]", (btn) => {
      const row = btn.closest(".history-item");
      const effects = row.querySelectorAll(".history-effects li").length;
      preview(row, {
        undo: [row], icon: "#i-undo", ok: "Undo", done: "Change undone",
        title: "Undo this change",
        detail: effects ? `Along with ${plural(effects, "change")} it caused` : "",
        url: `/api/v1/audit/${btn.dataset.undo}/revert`,
      });
    });
    wire("[data-jump-back]", (btn) => {
      const row = btn.closest(".history-item");
      const id = Number(btn.dataset.jumpBack);
      const later = rowsIn(document.getElementById("historyList")).filter((r) =>
        Number(r.dataset.entryId) > id && !r.classList.contains("inactive") && !("fixed" in r.dataset));
      preview(row, {
        undo: later, icon: "#i-rewind", ok: "Rewind", done: "Rewound to this point",
        title: "Rewind to here",
        detail: `Undoes ${plural(later.length, "later change")}`,
        url: `/api/v1/audit/${id}/jump-back`,
      });
    });
    wire("[data-redo]", (btn) => {
      const block = btn.closest(".history-group");
      const aside = entryIdsIn(block.dataset.setsAside);
      const undo = rowsIn(document.getElementById("historyList")).filter((r) => aside.has(r.dataset.entryId));
      const n = idsOf(btn.dataset.redo).length;
      preview(btn.closest("summary"), {
        block, redo: true, undo, icon: "#i-redo", ok: "Redo", done: n === 1 ? "Change redone" : "Changes redone",
        title: `Redo ${plural(n, "change")}`,
        detail: aside.size ? `Undoes ${plural(aside.size, "newer change")}` : "",
        url: "/api/v1/audit/redo", body: { ids: idsOf(btn.dataset.redo) },
      });
    });
    wire("[data-discard]", (btn) => {
      const block = btn.closest(".history-group");
      const n = idsOf(btn.dataset.discard).length;
      preview(btn.closest("summary"), {
        block, icon: "#i-trash", ok: "Delete", danger: true, done: "Removed from the log",
        title: `Delete ${plural(n, "undone change")}`,
        detail: "They can't be redone after this",
        url: "/api/v1/audit/discard", body: { ids: idsOf(btn.dataset.discard) },
      });
    });
    wire("[data-edit-import]", openImportEdit);
  }

  // Edit import: only the account can be changed after an import.
  const importEdit = document.getElementById("importEditModal");
  function openImportEdit(btn) {
    if (!importEdit) return;
    importEdit.dataset.batchId = btn.dataset.editImport;
    document.getElementById("importEditTitle").textContent = btn.dataset.importTitle || "Edit import";
    document.getElementById("importEditSub").textContent = btn.dataset.importSub || "";
    const sel = document.getElementById("importEditAccount");
    sel.value = btn.dataset.importAccount || sel.value;
    sel.dataset.from = btn.dataset.importAccount || "";
    importEdit.hidden = false;
    sel.focus();
  }
  importEdit?.addEventListener("click", (e) => {
    if (e.target === importEdit || e.target.closest("[data-close-import-edit]")) importEdit.hidden = true;
  });
  document.getElementById("importEditSave")?.addEventListener("click", (e) => {
    const btn = e.currentTarget;
    const sel = document.getElementById("importEditAccount");
    if (sel.value === sel.dataset.from) { importEdit.hidden = true; return; }
    btn.disabled = true;
    fetch(`/api/v1/imports/${importEdit.dataset.batchId}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
      body: JSON.stringify({ account_id: sel.value }),
    }).then(async (r) => {
      btn.disabled = false;
      if (!r.ok) { toast(await apiErrorText(r, "Couldn't move the import")); return; }
      reloadWithToast(`Import moved to ${sel.options[sel.selectedIndex].text}`);
    }).catch(() => { btn.disabled = false; toast("Couldn't move the import"); });
  });
  window.wireAuditActions = wireAuditActions;
  wireAuditActions(document);
});


document.addEventListener("DOMContentLoaded", () => {
  const table = document.getElementById("transactionsTable");
  if (!table) return;
  let sentinel = document.getElementById("transactionsSentinel");
  if (!sentinel || table.dataset.txHasMore !== "1") return;
  const tbody = document.getElementById("transactionsBody");
  if (!tbody) return;

  let loading = false;
  const loadNext = () => {
    if (loading) return;
    if (table.dataset.txHasMore !== "1") return;
    loading = true;
    const params = new URLSearchParams();
    const put = (k, v) => { if (v) params.set(k, v); };
    put("q", table.dataset.txFilterQ);
    put("category", table.dataset.txFilterCategory);
    put("type", table.dataset.txFilterType);
    put("account", table.dataset.txFilterAccount);
    put("period", table.dataset.txFilterPeriod);
    put("from", table.dataset.txFilterFrom);
    put("to", table.dataset.txFilterTo);
    put("sort", table.dataset.txSort);
    put("dir", table.dataset.txDir);
    params.set("offset", table.dataset.txNextOffset || "0");
    fetch("/transactions/rows?" + params.toString(), { headers: { "X-Requested-With": "fetch" } })
      .then((r) => {
        if (!r.ok) throw new Error("rows " + r.status);
        const hasMore = r.headers.get("X-Has-More") === "1";
        const nextOffset = r.headers.get("X-Next-Offset");
        return r.text().then((html) => ({ html, hasMore, nextOffset }));
      })
      .then(({ html, hasMore, nextOffset }) => {
        if (html) {
          const tpl = document.createElement("template");
          tpl.innerHTML = html.trim();
          tbody.appendChild(tpl.content);
          dataVersion.hold();
        }
        if (nextOffset) table.dataset.txNextOffset = nextOffset;
        if (!hasMore) {
          table.dataset.txHasMore = "0";
          if (sentinel) { sentinel.remove(); sentinel = null; }
          if (io) io.disconnect();
        }
        loading = false;
      })
      .catch(() => { loading = false; });
  };

  let io = null;
  if (window.IntersectionObserver) {
    io = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (entry.isIntersecting) loadNext();
      }
    }, { root: document.getElementById("content"), rootMargin: "200px" });
    io.observe(sentinel);
  } else {
    window.addEventListener("scroll", () => {
      if (!sentinel) return;
      const rect = sentinel.getBoundingClientRect();
      if (rect.top < window.innerHeight + 200) loadNext();
    }, { passive: true, capture: true });
  }
});

document.addEventListener("DOMContentLoaded", () => {
  const body = document.getElementById("transactionsBody");
  if (!body) return;

  // Column headers sort the table (server-side, so infinite scroll keeps the
  // order). Text columns start A→Z; date and amount start largest first.
  document.getElementById("transactionsHead")?.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-sort]");
    if (!btn) return;
    const url = new URL(window.location);
    const key = btn.dataset.sort;
    const curKey = url.searchParams.get("sort") || "date";
    const curDir = url.searchParams.get("dir") || "desc";
    let dir = key === "date" || key === "amount" ? "desc" : "asc";
    if (key === curKey) dir = curDir === "asc" ? "desc" : "asc";
    if (key === "date" && dir === "desc") {
      url.searchParams.delete("sort");
      url.searchParams.delete("dir");
    } else {
      url.searchParams.set("sort", key);
      url.searchParams.set("dir", dir);
    }
    location.replace(url.toString());
  });

  let editorSave = null; // saves the open editor if it changed; set by openEditor
  const closeEditor = () => {
    document.getElementById("txEditorRow")?.remove();
    document.querySelectorAll("#transactionsBody tr.editing").forEach((r) => r.classList.remove("editing"));
    editorSave = null;
  };
  const toggleEditor = (tr, id) => {
    const open = document.getElementById("txEditorRow");
    if (open && open.dataset.txId === id) { closeEditor(); return; }
    if (open && editorSave && editorSave()) return; // a save reloads the page
    closeEditor();
    tr.classList.add("editing");
    openEditor(tr, id);
  };
  body.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-edit-tx]");
    if (!btn) return;
    toggleEditor(btn.closest("tr"), btn.dataset.editTx);
  });
  body.addEventListener("dblclick", (e) => {
    if (e.target.closest("button, a, input, select, .badge, #txEditorRow")) return;
    const tr = e.target.closest("tr[data-tx-id]");
    if (!tr) return;
    window.getSelection()?.removeAllRanges(); // a double click selects a word
    if (document.getElementById("txEditorRow")?.dataset.txId === tr.dataset.txId) return;
    toggleEditor(tr, tr.dataset.txId);
  });
  // Clicking anywhere outside the open editor saves it (or just closes it
  // when nothing changed). Escape closes without saving.
  document.addEventListener("mousedown", (e) => {
    const editor = document.getElementById("txEditorRow");
    if (!editor || !editorSave) return;
    if (editor.contains(e.target) || e.target.closest("tr.editing, .modal-backdrop, .toast, [data-edit-tx]")) return;
    if (!editorSave()) closeEditor();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && document.getElementById("txEditorRow")) closeEditor();
  });

  function openEditor(tr, id) {
    fetch(`/api/v1/transactions/${id}`).then(async (r) => {
      if (!r.ok) throw new Error(await apiErrorText(r, "Could not load the transaction"));
      return r.json();
    }).then((t) => {
      const editor = document.createElement("tr");
      editor.id = "txEditorRow";
      editor.dataset.txId = id;
      const linked = !!t.transfer_id;
      const cats = window.__CAT_OPTIONS__ || [];
      const accounts = window.__ACCOUNT_OPTIONS__ || [];
      const internalCat = cats.find((c) => c.internal);
      // The transfer link only applies to a transfer: one already linked, or
      // one in the Internal transfer category.
      const isTransfer = () => linked || (!!internalCat && document.getElementById("txeCategory").value === internalCat.id);
      editor.innerHTML = `<td colspan="6">
        <div class="tx-editor">
          <label class="tx-field">Date
            <input class="field" type="date" id="txeDate" value="${esc(t.date)}" />
          </label>
          <label class="tx-field">Account
            <select class="field" id="txeAccount" ${linked ? 'disabled title="Break the transfer link to move it to another account"' : ""}>
              ${accounts.map((a) => `<option value="${esc(a.id)}"${t.account_id === a.id ? " selected" : ""}>${esc(a.name)}</option>`).join("")}
            </select>
          </label>
          <label class="tx-field grow">Description
            <input class="field" type="text" id="txeDescription" value="${esc(t.description)}" placeholder="e.g. Tesco Express" />
          </label>
          <label class="tx-field">Category
            <select class="field" id="txeCategory" ${linked ? "disabled" : ""}>
              ${linked ? `<option selected>Internal transfer</option>` : `<option value="">Select category…</option>` + cats.map((c) => `<option value="${esc(c.id)}"${t.category && t.category.id === c.id ? " selected" : ""}>${esc(c.name)}</option>`).join("")}
            </select>
          </label>
          <div class="tx-field tx-link-field" id="txeTransferField" hidden>
            <span>Transfer link${linked ? "" : ` <span class="tx-link-hint">the other side of this transfer, in another account</span>`}</span>
            ${linked
              ? `<div class="tx-link-linked"><svg class="icon"><use href="#i-check"/></svg>Linked to <strong>${esc(t.counterpart_tx ? t.counterpart_tx.description : "its other leg")}</strong>${t.counterpart_tx && t.counterpart_tx.account_name ? ` in ${esc(t.counterpart_tx.account_name)}` : ""}</div>`
              : `<input type="hidden" id="txeCounterpart" value="" /><div class="tx-link-list" id="txeLinkList" role="radiogroup" aria-label="Transfer link"><div class="tx-link-empty">Looking for the other side…</div></div>`}
          </div>
          <label class="tx-field">Amount
            <input class="field" type="number" step="0.01" id="txeAmount" value="${(t.amount_minor / 100).toFixed(2)}" />
          </label>
          <div class="tx-editor-actions">
            ${linked ? `<button class="btn btn-ghost btn-xs" id="txeUnlink" type="button">Break link</button>` : ""}
            <button class="btn btn-primary btn-xs" id="txeSave" type="button">Save</button>
            <button class="btn btn-ghost btn-xs" id="txeCancel" type="button">Cancel</button>
          </div>
        </div>
      </td>`;
      tr.after(editor);
      document.getElementById("txeDescription").focus();

      const cpField = document.getElementById("txeCounterpartField") || document.getElementById("txeTransferField");
      const catSel = document.getElementById("txeCategory");
      // Likely other legs (same amount, within a week) are listed; the rest
      // of the other direction's transactions within a month are behind
      // "Show more". Picking one again clears it.
      const loadTransferSuggestions = async () => {
        const input = document.getElementById("txeCounterpart");
        const list = document.getElementById("txeLinkList");
        if (!input || !list) return;
        let items;
        try {
          const resp = await fetch(`/api/v1/transactions/${id}/transfer-suggestions`);
          items = (await resp.json()).items || [];
        } catch (e2) {
          list.innerHTML = `<div class="tx-link-empty">Couldn't load transactions to link to.</div>`;
          return;
        }
        const when = (s) => {
          if (!s.days) return "same day";
          const later = s.tx.date > t.date;
          return `${s.days} day${s.days === 1 ? "" : "s"} ${later ? "later" : "earlier"}`;
        };
        const card = (s) => {
          const tx = s.tx;
          const exact = tx.amount_minor === -t.amount_minor;
          return `<button type="button" class="tx-link-option" role="radio" aria-checked="false" data-link-id="${esc(tx.id)}">
            <span class="tx-link-main">
              <span class="tx-link-desc">${esc(tx.description)}</span>
              <span class="tx-link-meta">${tx.account ? `<span class="tx-link-acct"><span class="tx-link-dot" style="background:${esc(tx.account.color || "var(--muted)")}"></span>${esc(tx.account.name)}</span> · ` : ""}${dateHTML(tx.date)} · <span class="${s.days <= 3 ? "tx-link-good" : ""}">${when(s)}</span></span>
            </span>
            <span class="tx-link-amt tx-amount ${tx.amount_minor >= 0 ? "pos" : "neg"}">${fmtMoneySigned(tx.amount_minor, tx.currency || "GBP")}${exact ? `<span class="tx-link-good">exact amount</span>` : ""}</span>
          </button>`;
        };
        const close = items.filter((s) => s.close);
        const rest = items.filter((s) => !s.close);
        const render = (showAll) => {
          const shown = showAll ? close.concat(rest) : close;
          let html = shown.map(card).join("");
          if (!close.length && !showAll) {
            html = `<div class="tx-link-empty">Nothing in another account for ${esc(fmtMoneyMinor(Math.abs(t.amount_minor), t.currency || "GBP"))} within a week of this. ${rest.length ? "" : "Import the other account's statement, then try again."}</div>`;
          }
          if (!showAll && rest.length) {
            html += `<button type="button" class="btn btn-ghost btn-xs tx-link-more" data-link-more>Show ${rest.length} other transaction${rest.length === 1 ? "" : "s"} from the month around it</button>`;
          }
          list.innerHTML = html;
          const picked = input.value && list.querySelector(`[data-link-id="${CSS.escape(input.value)}"]`);
          if (picked) picked.setAttribute("aria-checked", "true");
        };
        list.addEventListener("click", (e) => {
          if (e.target.closest("[data-link-more]")) { render(true); return; }
          const opt = e.target.closest("[data-link-id]");
          if (!opt) return;
          const on = opt.getAttribute("aria-checked") !== "true";
          list.querySelectorAll("[data-link-id]").forEach((o) => o.setAttribute("aria-checked", "false"));
          opt.setAttribute("aria-checked", on ? "true" : "false");
          input.value = on ? opt.dataset.linkId : "";
        });
        render(false);
      };
      let suggestionsLoaded = false;
      const syncTransferField = () => {
        const show = isTransfer();
        cpField.hidden = !show;
        if (!show) {
          const cpSel = document.getElementById("txeCounterpart");
          if (cpSel && !linked) {
            cpSel.value = "";
            document.querySelectorAll("#txeLinkList [aria-checked=true]").forEach((o) => o.setAttribute("aria-checked", "false"));
          }
        } else if (!linked && !suggestionsLoaded) {
          suggestionsLoaded = true;
          loadTransferSuggestions();
        }
      };
      syncTransferField();
      catSel.addEventListener("change", syncTransferField);
      document.getElementById("txeCancel").addEventListener("click", closeEditor);
      const editorState = () => ["txeDate", "txeAccount", "txeDescription", "txeCategory", "txeCounterpart", "txeAmount"]
        .map((x) => document.getElementById(x)?.value ?? "").join("\u0000");
      const initialState = editorState();
      editorSave = () => {
        if (editorState() === initialState) return false;
        document.getElementById("txeSave").click();
        return true;
      };
      document.getElementById("txeSave").addEventListener("click", () => {
        const date = document.getElementById("txeDate").value;
        const description = document.getElementById("txeDescription").value.trim();
        const amount = parseFloat(document.getElementById("txeAmount").value);
        if (!date) { toast("Date is required"); return; }
        if (!description) { toast("Description is required"); return; }
        if (isNaN(amount)) { toast("Amount is required"); return; }
        const cpSel = document.getElementById("txeCounterpart");
        if (cpSel && !linked && isTransfer() && cpSel.value) {
          fetch(`/api/v1/transactions/${id}/link`, {
            method: "POST",
            headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
            body: JSON.stringify({ counterpart_id: cpSel.value }),
          }).then(async (r) => {
            if (!r.ok) { toast(await apiErrorText(r, "Could not link")); return; }
            toast("Transfer linked");
            location.reload();
          });
          return;
        }
        const body = {
          date, description,
          account_id: linked ? undefined : document.getElementById("txeAccount").value,
          amount_minor: Math.round(amount * 100),
          category_id: (!linked && document.getElementById("txeCategory").value) || null,
          // Refused if it was changed on another device since this opened.
          expected_updated_at: t.updated_at,
        };
        fetch(`/api/v1/transactions/${id}`, {
          method: "PATCH",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
          body: JSON.stringify(body),
        }).then(async (r) => {
          if (!r.ok) { toast(await apiErrorText(r, "Could not save")); return; }
          toast("Transaction updated");
          location.reload();
        }).catch(() => toast("Could not save"));
      });
      const unlink = document.getElementById("txeUnlink");
      if (unlink) {
        unlink.addEventListener("click", () => {
          if (!t.transfer_id) return;
          showConfirm("Break the link?", "Both transactions will stay but the transfer link will be removed.", "Break link", () => {
            fetch(`/api/v1/transactions/${id}/unlink`, {
              method: "POST",
              headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
              body: JSON.stringify({}),
            }).then((r) => {
              if (!r.ok) { toast("Could not unlink"); return; }
              toast("Transfer unlinked");
              location.reload();
            });
          });
        });
      }
    }).catch((e) => toast(e.message || "Could not load the transaction"));
  }
});


document.addEventListener("DOMContentLoaded", () => {
  const dropzone = document.getElementById("dropzone");
  const fileInput = document.getElementById("fileInput");
  const flowBox = document.getElementById("importFlow");
  const view = document.getElementById("view-import");
  if (!dropzone || !flowBox) return;

  const openFlow = (html) => {
    view?.classList.add("import-active");
    view?.classList.remove("import-reviewing");
    flowBox.hidden = false;
    flowBox.innerHTML = html;
  };
  const closeFlow = () => {
    view?.classList.remove("import-active", "import-reviewing");
    flowBox.hidden = true;
    flowBox.innerHTML = "";
  };
  const flowMessage = (html, withRetry) => {
    openFlow(`<div class="thinking-card panel"><div class="head">${html}</div>
      ${withRetry ? '<button class="btn btn-ghost btn-xs" type="button" data-close-flow>Try another file</button>' : ""}</div>`);
  };
  flowBox.addEventListener("click", (e) => {
    if (e.target.closest("[data-close-flow]")) closeFlow();
  });
  const jsonHeaders = () => ({ "Content-Type": "application/json", "X-CSRF-Token": csrfToken() });
  const errMessage = (b, status) => apiErrorFrom({ status }, b, `The server couldn't do that (error ${status}). Try again.`);

  // The account an upload starts in: the last one imported into, else the
  // first. A saved format's own account wins on the server.
  const lastImportAccount = () => {
    const accounts = window.__IMPORT_ACCOUNTS__ || [];
    let id = "";
    try { id = localStorage.getItem("glowworm-import-account") || ""; } catch (e) {}
    return accounts.some((a) => a.id === id) ? id : (accounts[0] ? accounts[0].id : "");
  };
  const upload = (file) => {
    const fd = new FormData();
    fd.append("file", file);
    const acc = lastImportAccount();
    if (acc) fd.append("account_id", acc);
    flowMessage(`<span class="spinner"></span> Reading ${esc(file.name)}…`);
    dataVersion.hold(); // the import's review lives only on this page
    fetch("/api/v1/imports", {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken() },
      body: fd,
    }).then(async (r) => {
      const b = await r.json().catch(() => ({}));
      if (!r.ok) {
        flowMessage(`Upload failed: ${esc(errMessage(b, r.status))}`, true);
        return;
      }
      renderBatch(b);
    }).catch((e) => {
      flowMessage(`Upload failed: ${esc(e && e.message ? e.message : "network error — is the server running?")}`, true);
    });
  };

  dropzone.addEventListener("dragover", (e) => { e.preventDefault(); dropzone.classList.add("drag"); });
  dropzone.addEventListener("dragleave", () => dropzone.classList.remove("drag"));
  dropzone.addEventListener("drop", (e) => {
    e.preventDefault(); dropzone.classList.remove("drag");
    if (e.dataTransfer.files.length) upload(e.dataTransfer.files[0]);
  });
  // The whole box opens the picker; the inner "browse files" button just bubbles here.
  dropzone.addEventListener("click", (e) => { if (e.target !== fileInput) fileInput.click(); });
  fileInput.addEventListener("change", () => {
    if (fileInput.files.length) upload(fileInput.files[0]);
    fileInput.value = "";
  });

  // The server parses Go reference layouts ("02/01/2006" = DD/MM/YYYY); users
  // only see the familiar codes.
  const GO_TO_HUMAN = [["January", "MMMM"], ["Jan", "MMM"], ["Monday", "dddd"], ["Mon", "ddd"],
    ["2006", "YYYY"], ["Z07:00", "Z"], ["15", "HH"], ["06", "YY"], ["01", "MM"], ["02", "DD"],
    ["03", "hh"], ["04", "mm"], ["05", "ss"], ["_2", "D"], ["PM", "A"], ["pm", "a"],
    ["1", "M"], ["2", "D"], ["3", "h"], ["4", "m"], ["5", "s"]];
  const HUMAN_TO_GO = [["YYYY", "2006"], ["YY", "06"], ["MMMM", "January"], ["MMM", "Jan"], ["MM", "01"], ["M", "1"],
    ["dddd", "Monday"], ["ddd", "Mon"], ["DD", "02"], ["D", "2"], ["HH", "15"], ["hh", "03"], ["h", "3"],
    ["mm", "04"], ["m", "4"], ["ss", "05"], ["s", "5"], ["A", "PM"], ["a", "pm"], ["Z", "Z07:00"]];
  const convertLayout = (str, table) => {
    let out = "";
    for (let i = 0; i < str.length;) {
      const hit = table.find(([from]) => str.startsWith(from, i));
      out += hit ? hit[1] : str[i];
      i += hit ? hit[0].length : 1;
    }
    return out;
  };
  const goLayoutToHuman = (layout) => convertLayout(layout || "", GO_TO_HUMAN);
  const humanToGoLayout = (fmt) => (!fmt || fmt.includes("2006") ? fmt : convertLayout(fmt, HUMAN_TO_GO));

  // ---- Column mapping: Glowworm's fields are the rows; each picks a column
  // from the file. ----
  const DEFAULT_OUT = ["DR", "D", "DEBIT", "OUT", "PAYMENT", "WITHDRAWAL", "PURCHASE"];
  const AMOUNT_MODES = [
    ["signed", "Money out is negative (−12.50)"],
    ["inverted", "Money out is positive (12.50)"],
    ["split", "Separate money in and money out columns"],
    ["direction", "Amount plus a debit/credit column"],
  ];

  function selectionsFrom(det) {
    const by = (role) => (det.columns || []).find((c) => c.role === role)?.source || "";
    const amount = det.amount || {};
    const sel = {
      date: by("date"), description: by("description"), memo: by("memo"), payee: by("destination"),
      amount: by("amount"), amountIn: by("amount_in"), amountOut: by("amount_out"),
      direction: amount.direction_column || "", outValues: amount.out_values || [],
      currency: by("currency"), origAmount: by("original_amount"), origCurrency: by("original_currency"),
      fxRates: Object.assign({}, det.fx_rates || {}),
    };
    sel.amountMode = sel.direction ? "direction"
      : (sel.amountIn || sel.amountOut) && !sel.amount ? "split"
      : amount.sign === "inverted" ? "inverted" : "signed";
    return sel;
  }

  function columnSelectHTML(id, headers, value, { optional = true, none = "None" } = {}) {
    return `<select class="field select" id="${id}">
      ${optional ? `<option value="">${esc(none)}</option>` : `<option value="" disabled${value ? "" : " selected"}>Choose a column…</option>`}
      ${headers.map((h) => `<option value="${esc(h)}"${h === value ? " selected" : ""}>${esc(h)}</option>`).join("")}
    </select>`;
  }

  // Values seen in a column of the sample, for the debit/credit picker.
  function distinctValues(sampleHeader, sampleBody, column) {
    const i = sampleHeader.indexOf(column);
    if (i < 0) return [];
    return [...new Set(sampleBody.map((r) => String(r[i] || "").trim()).filter(Boolean))];
  }

  function mappingFormHTML(sel, headers, changed, target) {
    const row = (key, label, hint, body) => `<div class="map-field${changed.has(key) ? " mapping-changed" : ""}" data-field="${key}">
        <div class="map-field-label"><strong>${label}</strong>${hint ? `<span class="muted">${hint}</span>` : ""}
          ${changed.has(key) ? '<span class="badge badge-transfer">AI changed</span>' : ""}</div>
        <div class="map-field-body">${body}</div>
      </div>`;
    const modes = AMOUNT_MODES.map(([v, label]) => `<label class="map-radio">
        <input type="radio" name="mapAmountMode" value="${v}"${sel.amountMode === v ? " checked" : ""} />
        <span><span>${label}</span></span></label>`).join("");
    return `<div class="map-fields" id="mapFields">
      ${row("date", "Date", "", `${columnSelectHTML("mapDate", headers, sel.date, { optional: false })}
        <div class="date-read" id="dateRead">
          <div class="date-preview" id="datePreview"></div>
          <div class="map-sub">
            <label class="map-sub-label" for="mapDateFormat">Format
              <button type="button" class="info-btn" id="dateInfoBtn" data-info-toggle="dateInfoPopover" aria-expanded="false" aria-controls="dateInfoPopover" aria-label="Date format help">i</button></label>
            <input class="field" id="mapDateFormat" placeholder="Auto-detect" />
          </div>
          <div id="dateInfoPopover" class="info-popover wide" role="dialog" aria-label="Date formats" hidden>
            <strong>How date formats work</strong>
            <p class="muted">Leave the field blank to let Glowworm auto-detect from ~15 common layouts.</p>
            <p class="muted">To pin a format, write it with these codes — anything else (<code>/</code>, <code>-</code>, spaces) must appear exactly as written:</p>
            <p class="muted"><code>DD</code> day 02 · <code>D</code> day 2 · <code>MM</code> month 06 · <code>MMM</code> Jun · <code>MMMM</code> June · <code>YYYY</code> 2026 · <code>YY</code> 26 · <code>HH</code> hour 0–23 · <code>hh</code> hour 1–12 · <code>mm</code> minutes · <code>ss</code> seconds · <code>A</code> AM/PM</p>
            <p class="muted">Click a format to use it:</p>
            <ul class="date-format-list">
              <li><button type="button" data-fmt="DD/MM/YYYY">DD/MM/YYYY</button> — UK, e.g. 02/06/2026</li>
              <li><button type="button" data-fmt="MM/DD/YYYY">MM/DD/YYYY</button> — US, e.g. 06/02/2026</li>
              <li><button type="button" data-fmt="YYYY-MM-DD">YYYY-MM-DD</button> — ISO, e.g. 2026-06-02</li>
              <li><button type="button" data-fmt="D MMM YYYY">D MMM YYYY</button> — e.g. 2 Jun 2026</li>
              <li><button type="button" data-fmt="D MMMM YYYY">D MMMM YYYY</button> — e.g. 2 June 2026</li>
              <li><button type="button" data-fmt="YYYY-MM-DD HH:mm:ss">YYYY-MM-DD HH:mm:ss</button> — ISO with time</li>
              <li><button type="button" data-fmt="DD/MM/YYYY HH:mm">DD/MM/YYYY HH:mm</button> — UK with time</li>
            </ul>
          </div>
        </div>`)}
      ${row("description", "Description", "", `${columnSelectHTML("mapDescription", headers, sel.description, { optional: false })}
        <div class="map-sub"><label class="map-sub-label" for="mapMemo">Add details from</label>${columnSelectHTML("mapMemo", headers, sel.memo)}</div>`)}
      ${row("amount", "Amount", "", `<div class="map-amount-col" data-mode-show="signed inverted direction">${columnSelectHTML("mapAmount", headers, sel.amount, { optional: false })}</div>
        <div class="map-sub" data-mode-show="split"><label class="map-sub-label" for="mapAmountIn">Money in</label>${columnSelectHTML("mapAmountIn", headers, sel.amountIn)}</div>
        <div class="map-sub" data-mode-show="split"><label class="map-sub-label" for="mapAmountOut">Money out</label>${columnSelectHTML("mapAmountOut", headers, sel.amountOut)}</div>
        <div class="map-radios">${modes}</div>
        <div class="map-sub" data-mode-show="direction"><label class="map-sub-label" for="mapDirection">Debit/credit column</label>${columnSelectHTML("mapDirection", headers, sel.direction, { optional: false })}</div>
        <div class="map-sub" data-mode-show="direction" id="mapOutValuesRow"><span class="map-sub-label">Values meaning money out</span><div class="map-checks" id="mapOutValues"></div></div>
        <div class="map-amount-preview" id="mapAmountPreview"></div>`)}
      ${row("currency", "Currency", "", `
        ${columnSelectHTML("mapCurrency", headers, sel.currency, { none: `Everything is in ${target}` })}
        <div class="map-fx" id="mapFx"></div>
        <details class="map-orig"${sel.origAmount ? " open" : ""}><summary>Paid in another currency</summary>
          <div class="map-sub"><label class="map-sub-label" for="mapOrigAmount">Amount</label>${columnSelectHTML("mapOrigAmount", headers, sel.origAmount)}</div>
          <div class="map-sub"><label class="map-sub-label" for="mapOrigCurrency">Currency</label>${columnSelectHTML("mapOrigCurrency", headers, sel.origCurrency)}</div>
        </details>`)}
      ${row("payee", "Payee", "", columnSelectHTML("mapPayee", headers, sel.payee))}
    </div>`;
  }

  function readSelections(sel) {
    const v = (id) => document.getElementById(id)?.value || "";
    const out = Object.assign({}, sel, {
      date: v("mapDate"), description: v("mapDescription"), memo: v("mapMemo"), payee: v("mapPayee"),
      amountMode: document.querySelector('input[name="mapAmountMode"]:checked')?.value || "signed",
      amount: v("mapAmount"), amountIn: v("mapAmountIn"), amountOut: v("mapAmountOut"), direction: v("mapDirection"),
      currency: v("mapCurrency"), origAmount: v("mapOrigAmount"), origCurrency: v("mapOrigCurrency"),
    });
    const checks = document.querySelectorAll("#mapOutValues input[type=checkbox]");
    if (checks.length) out.outValues = [...checks].filter((c) => c.checked).map((c) => c.value);
    document.querySelectorAll("#mapFx input[data-fx]").forEach((inp) => {
      const n = parseFloat(inp.value);
      if (n > 0) out.fxRates[inp.dataset.fx] = n; else delete out.fxRates[inp.dataset.fx];
    });
    return out;
  }

  function selectionErrors(sel) {
    if (!sel.date) return "Choose the date column";
    if (!sel.description) return "Choose the description column";
    if (sel.amountMode === "split" && !sel.amountIn && !sel.amountOut) return "Choose the money in and/or money out columns";
    if (sel.amountMode !== "split" && !sel.amount) return "Choose the amount column";
    if (sel.amountMode === "direction" && !sel.direction) return "Choose the debit/credit column";
    return "";
  }

  function mappingFrom(sel, d, dateFormat) {
    const cols = [];
    const add = (source, role) => { if (source) cols.push({ source, role, date_format: role === "date" ? dateFormat : undefined }); };
    add(sel.date, "date");
    add(sel.description, "description");
    add(sel.memo, "memo");
    add(sel.payee, "destination");
    if (sel.amountMode === "split") { add(sel.amountIn, "amount_in"); add(sel.amountOut, "amount_out"); }
    else add(sel.amount, "amount");
    add(sel.currency, "currency");
    add(sel.origAmount, "original_amount");
    add(sel.origCurrency, "original_currency");
    const amount = {};
    if (sel.amountMode === "inverted") amount.sign = "inverted";
    if (sel.amountMode === "direction") { amount.direction_column = sel.direction; amount.out_values = sel.outValues; }
    return {
      kind: d.kind || "csv", has_header: d.has_header !== false, delimiter: d.delimiter || ",",
      columns: cols, date_format: dateFormat, amount,
      fx_rates: Object.keys(sel.fxRates).length ? sel.fxRates : undefined,
      sheet: document.getElementById("mapSheet") ? document.getElementById("mapSheet").value : undefined,
      confidence: d.confidence || 0.5,
    };
  }

  // Mirrors the server's reading of an amount, for the live preview only.
  function previewAmount(raw) {
    let v = String(raw || "").trim().replace(/[£$€¥,\s]/g, "");
    if (!v) return null;
    let neg = false;
    if (/^\(.*\)$/.test(v)) { neg = true; v = v.slice(1, -1); }
    if (/dr$/i.test(v)) { neg = !neg; v = v.slice(0, -2); } else if (/cr$/i.test(v)) v = v.slice(0, -2);
    if (v.endsWith("-")) { neg = !neg; v = v.slice(0, -1); }
    if (v.startsWith("-")) { neg = !neg; v = v.slice(1); }
    const n = parseFloat(v.replace(/^\+/, ""));
    if (isNaN(n)) return null;
    return Math.round(n * 100) * (neg ? -1 : 1);
  }

  function wireMappingForm(sel, d, sampleHeader, sampleBody, target) {
    const col = (row, name) => { const i = sampleHeader.indexOf(name); return i < 0 ? "" : row[i]; };
    const sync = () => {
      const cur = readSelections(sel);
      document.querySelectorAll("#mapFields [data-mode-show]").forEach((el) => {
        el.hidden = !el.dataset.modeShow.split(" ").includes(cur.amountMode);
      });
      // Debit/credit values: tick the ones that mean money out.
      const outBox = document.getElementById("mapOutValues");
      if (cur.amountMode === "direction" && outBox && outBox.dataset.col !== cur.direction) {
        outBox.dataset.col = cur.direction;
        const vals = distinctValues(sampleHeader, sampleBody, cur.direction);
        const ticked = new Set((sel.outValues.length ? sel.outValues : DEFAULT_OUT).map((x) => x.toUpperCase()));
        outBox.innerHTML = vals.length
          ? vals.map((x) => `<label class="map-check"><input type="checkbox" value="${esc(x)}"${ticked.has(x.toUpperCase()) ? " checked" : ""} /> ${esc(x)}</label>`).join("")
          : '<span class="muted">No values in the sample rows.</span>';
      }
      // Exchange rates for foreign currencies seen in the file.
      const fx = document.getElementById("mapFx");
      const fromFile = cur.currency ? distinctValues(sampleHeader, sampleBody, cur.currency).map((x) => x.toUpperCase()) : [];
      const foreign = [...new Set([...(cur.currency ? d.foreign_currencies || [] : []), ...fromFile])].filter((c) => c && c !== target).sort();
      const key = foreign.join(",");
      if (fx.dataset.key !== key) {
        fx.dataset.key = key;
        fx.innerHTML = foreign.length ? `${foreign.map((c) => `<label class="map-fx-row">1 ${esc(c)} = <input class="field" type="number" step="any" min="0" data-fx="${esc(c)}" value="${sel.fxRates[c] || ""}" placeholder="ECB daily rate" /> ${esc(target)}</label>`).join("")}` : "";
      }
      // Preview: how the first rows will be read.
      const prevEl = document.getElementById("mapAmountPreview");
      const lines = sampleBody.slice(0, 3).map((r) => {
        let minor = null;
        if (cur.amountMode === "split") {
          const inn = previewAmount(col(r, cur.amountIn)), out = previewAmount(col(r, cur.amountOut));
          if (inn !== null || out !== null) minor = Math.abs(inn || 0) - Math.abs(out || 0);
        } else {
          minor = previewAmount(col(r, cur.amount));
          if (minor !== null && cur.amountMode === "inverted") minor = -minor;
          if (minor !== null && cur.amountMode === "direction") {
            const marker = String(col(r, cur.direction) || "").trim().toUpperCase();
            minor = Math.abs(minor) * (cur.outValues.map((x) => x.toUpperCase()).includes(marker) ? -1 : 1);
          }
        }
        if (minor === null) return "";
        const rowCur = (cur.currency && String(col(r, cur.currency) || "").trim().toUpperCase()) || target;
        // The file's own text, then how it's read: same as the date check.
        const raw = cur.amountMode === "split"
          ? (String(col(r, cur.amountIn) || "").trim() || String(col(r, cur.amountOut) || "").trim())
          : [col(r, cur.amount), cur.amountMode === "direction" ? col(r, cur.direction) : ""].map((x) => String(x || "").trim()).filter(Boolean).join(" ");
        return `<li><span class="date-raw">${esc(raw)}</span><span class="date-arrow" aria-label="reads as">→</span><span class="tx-amount ${minor < 0 ? "neg" : "pos"}">${minor < 0 ? "Money out" : "Money in"} · ${esc(fmtMoneyMinor(minor, rowCur))}</span></li>`;
      }).filter(Boolean);
      prevEl.innerHTML = lines.length ? `<ul>${lines.join("")}</ul>` : "";
    };
    document.getElementById("mapFields").addEventListener("change", sync);
    document.getElementById("mapFields").addEventListener("input", (e) => { if (e.target.matches("[data-fx]")) sync(); });
    sync();
  }

  // back, when set, is where Cancel on the Columns step returns: the review
  // it was opened from.
  const renderBatch = (b, prev, back) => {
    if (b.status === "failed") {
      const why = b.parse_error || "The file could not be read.";
      openFlow(`
        <div class="panel category-form">
          <h3 class="category-form-title">Import failed</h3>
          <p class="login-error">${esc(why)}</p>
          <p class="login-error" style="color:var(--muted)">Nothing was imported, and this attempt does not appear in your history.</p>
          <button class="btn btn-primary" type="button" data-close-flow>Try another file</button>
        </div>`);
      return;
    }
    if (b.status === "awaiting_review" && !back) {
      openReview(b.id, true);
      return;
    }
    const d = b.detection || {};
    const aiProposal = !!d.ai_proposed;
    const sheets = (d.sheets || []).map((s) => `<option value="${esc(s)}"${s === d.sheet ? " selected" : ""}>${esc(s)}</option>`).join("");
    const sel = selectionsFrom(d);
    // An AI proposal highlights the fields it picked differently.
    const changed = new Set();
    if (aiProposal && prev) {
      const before = selectionsFrom(prev);
      const same = (keys) => keys.every((k) => String(before[k] || "") === String(sel[k] || ""));
      if (!same(["date"])) changed.add("date");
      if (!same(["description", "memo"])) changed.add("description");
      if (!same(["amountMode", "amount", "amountIn", "amountOut", "direction"])) changed.add("amount");
      if (!same(["currency", "origAmount", "origCurrency"])) changed.add("currency");
      if (!same(["payee"])) changed.add("payee");
    }
    const nChanged = changed.size;
    const target = d.target_currency || (window.__IMPORT_CURRENCY__ || "GBP");
    const sampleHeader = (d.sample && d.sample[0]) || [];
    const sampleBody = (d.sample || []).slice(1, 5);
    const headerHTML = sampleHeader.map((c) => `<th>${esc(c)}</th>`).join("");
    const sample = sampleBody.map((r) => `<tr>${r.map((c) => `<td>${esc(c)}</td>`).join("")}</tr>`).join("");
    const title = d.matched_schema ? `Columns · saved format “${esc(d.matched_schema)}”`
      : aiProposal ? "Columns suggested by the AI"
      : `Columns in ${esc(b.filename || "the file")}`;
    const dateSampleFor = (column) => {
      const i = sampleHeader.indexOf(column);
      return i < 0 ? "" : (sampleBody.find((r) => r[i]) || [])[i] || "";
    };
    const intro = aiProposal && prev
      ? `<div class="ai-review-banner">${nChanged ? `The AI changed ${nChanged} field${nChanged === 1 ? "" : "s"} (highlighted).` : "The AI agreed with the existing mapping."}</div>`
      : "";
    openFlow(`
      <div class="panel category-form">
        <h3 class="category-form-title">${title}</h3>
        ${b.parse_error ? `<p class="login-error">${esc(b.parse_error)}</p>` : ""}
        ${intro}
        ${mappingFormHTML(sel, sampleHeader, changed, target)}
        ${sheets ? `<div class="field-row"><label>Sheet</label><select class="field select" id="mapSheet">${sheets}</select></div>` : ""}
        <div class="section-head"><h2>Sample Import</h2></div>
        <div style="overflow-x:auto"><table class="tx-table"><thead><tr>${headerHTML}</tr></thead><tbody>${sample}</tbody></table></div>
        <div class="category-form-actions">
          ${back ? "" : aiProposal
            ? (prev ? '<button class="btn btn-ghost" id="mapDiscardAI" type="button">Discard AI proposal</button>' : "")
            : '<button class="btn btn-ghost" id="mapAI" type="button">Ask AI to detect</button>'}
          <span class="spacer"></span>
          <button class="btn btn-ghost" id="mapCancel" type="button">Cancel</button>
          <button class="btn btn-primary" id="mapApply" type="button">Continue</button>
        </div>
      </div>`);
    document.getElementById("mapCancel").addEventListener("click", () => (back ? back() : closeFlow()));
    document.getElementById("mapDateFormat").value = goLayoutToHuman(d.date_format);
    const setDateSample = () => {
      const pv = document.getElementById("datePreview");
      pv.dataset.sample = dateSampleFor(document.getElementById("mapDate").value);
      if (!pv.dataset.sample) pv.textContent = "Choose the date column to check the format.";
    };
    setDateSample();
    wireMappingForm(sel, d, sampleHeader, sampleBody, target);
    document.getElementById("mapDate").addEventListener("change", () => { setDateSample(); refreshDatePreview(); });
    document.getElementById("mapDiscardAI")?.addEventListener("click", () => {
      renderBatch(Object.assign({}, b, { detection: prev, parse_error: null }), undefined, back);
      toast("AI proposal discarded");
    });

    const infoPop = document.getElementById("dateInfoPopover");
    infoPop?.addEventListener("click", (e) => {
      const btn = e.target.closest("button[data-fmt]");
      if (!btn) return;
      document.getElementById("mapDateFormat").value = btn.dataset.fmt;
      refreshDatePreview();
      closeInfoPopover();
    });

    const previewEl = document.getElementById("datePreview");
    const dateInput = document.getElementById("mapDateFormat");
    let previewToken = 0;
    function refreshDatePreview() {
      const sample = previewEl?.dataset.sample || "";
      if (!sample) return;
      const fmt = dateInput.value.trim();
      const myToken = ++previewToken;
      fetch("/api/v1/imports/preview-date", {
        method: "POST",
        headers: jsonHeaders(),
        body: JSON.stringify({ sample, format: humanToGoLayout(fmt) }),
      }).then((r) => jsonOk(r)).then((body) => {
        if (myToken !== previewToken) return;
        dateInput.placeholder = body.iso && !fmt ? `Auto-detect (${goLayoutToHuman(body.layout)})` : "Auto-detect";
        if (!body.iso) {
          previewEl.innerHTML = `<span class="date-preview-fail">Couldn't read “${esc(sample)}”${fmt ? ` as ${esc(fmt)}` : ""}. Pick a format from the “i” menu${fmt ? " or clear the field to auto-detect" : ""}.</span>`;
          return;
        }
        const detected = esc(goLayoutToHuman(body.layout));
        if (fmt && body.layout !== humanToGoLayout(fmt)) {
          // The parser falls back to auto-detection when a pinned format
          // doesn't match, so don't imply the pin worked.
          previewEl.innerHTML = `<span class="date-preview-fail">${esc(fmt)} doesn't match “${esc(sample)}”.</span>
            <span class="muted">Auto-detection would read it as ${esc(humanDate(body.iso))} (${detected}) — fix the format or clear it.</span>`;
          return;
        }
        previewEl.innerHTML = `<span class="date-raw">${esc(sample)}</span><span class="date-arrow" aria-label="reads as">→</span><span class="date-preview-ok">${esc(humanDate(body.iso))}</span>`;
      }).catch(() => {
        if (myToken !== previewToken) return;
        previewEl.textContent = "Preview unavailable";
      });
    }
    dateInput?.addEventListener("input", () => {
      clearTimeout(dateInput._t);
      dateInput._t = setTimeout(refreshDatePreview, 250);
    });
    refreshDatePreview();

    document.getElementById("mapAI")?.addEventListener("click", (e) => {
      e.currentTarget.disabled = true;
      const ncols = (d.columns || []).length;
      const nrows = Math.max(0, (d.sample || []).length - 1);
      const panel = document.createElement("div");
      panel.className = "ai-progress panel";
      panel.innerHTML = `
        <div class="ai-progress-head"><span class="spinner"></span> Asking the AI to detect your schema</div>
        <ul class="ai-progress-steps">
          <li id="aiStep0" class="active">Preparing ${ncols} column${ncols === 1 ? "" : "s"} and ${nrows} sample row${nrows === 1 ? "" : "s"}…</li>
          <li id="aiStep1">Waiting for the model to respond…</li>
          <li id="aiStep2">Validating the proposed mapping…</li>
        </ul>
        <div class="muted" id="aiElapsed">Elapsed: 0.0s</div>`;
      flowBox.querySelector(".category-form").appendChild(panel);
      const t0 = Date.now();
      const tick = setInterval(() => {
        const dt = (Date.now() - t0) / 1000;
        panel.querySelector("#aiElapsed").textContent = `Elapsed: ${dt.toFixed(1)}s`;
        if (dt > 1.5) panel.querySelector("#aiStep0").classList.remove("active");
        if (dt > 1.5) panel.querySelector("#aiStep1").classList.add("active");
      }, 100);
      const fail = (msg) => {
        clearInterval(tick);
        panel.querySelector(".ai-progress-head").innerHTML = `<strong>AI detection failed</strong>`;
        toast(msg || "AI detection failed");
        setTimeout(() => panel.remove(), 1200);
        const btn = document.getElementById("mapAI");
        if (btn) btn.disabled = false;
      };
      fetch(`/api/v1/imports/${b.id}/schema/ai-detect`, {
        method: "POST",
        headers: { "X-CSRF-Token": csrfToken() },
      }).then(async (r) => {
        panel.querySelector("#aiStep1").classList.remove("active");
        panel.querySelector("#aiStep2").classList.add("active");
        const nb = await r.json().catch(() => ({}));
        if (!r.ok) { fail(errMessage(nb, r.status)); return; }
        clearInterval(tick);
        panel.querySelector(".ai-progress-head").innerHTML = `<strong>Got a proposal · loading it for your review…</strong>`;
        setTimeout(() => renderBatch(nb, d, back), 350);
      }).catch(() => fail());
    });
    document.getElementById("mapApply").addEventListener("click", (e) => {
      const btn = e.currentTarget;
      const chosen = readSelections(sel);
      const problem = selectionErrors(chosen);
      if (problem) { toast(problem); return; }
      const m = mappingFrom(chosen, d, humanToGoLayout(document.getElementById("mapDateFormat").value.trim()));
      btn.disabled = true;
      fetch(`/api/v1/imports/${b.id}/schema`, {
        method: "POST",
        headers: jsonHeaders(),
        body: JSON.stringify({ mapping: m }),
      }).then(async (r) => {
        const nb = await r.json().catch(() => ({}));
        btn.disabled = false;
        if (!r.ok) { toast(errMessage(nb, r.status)); return; }
        renderBatch(nb);
      }).catch(() => { btn.disabled = false; toast("Re-parse failed"); });
    });
  };


  // Money in can be a refund, which belongs in the original spend category.
  const catsFor = (type) => (window.__IMPORT_CATS__ || []).filter((c) =>
    type === "income" || c.type === "internal" || c.type === "spend");
  const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;

  function sourceBadge(g) {
    if (g.source === "user") return '<span class="badge badge-income">You</span>';
    if (g.source === "ai") return `<span class="badge badge-transfer">AI</span>`;
    if (g.source === "local") return '<span class="badge badge-soft">Classifier</span>';
    return "";
  }

  function buildReview(body) {
    const R = {
      batchID: body.batch.id, batch: body.batch, aiAvailable: !!body.ai_available,
      groups: [], byKey: new Map(), dups: [], running: false, finished: false, aiFailed: false,
    };
    (body.rows || []).forEach((row) => {
      if (row.duplicate_of) { R.dups.push(row); return; }
      let g = R.byKey.get(row.group_key);
      if (!g) {
        g = {
          id: R.groups.length, key: row.group_key, type: row.type, description: row.description,
          destination: row.destination || "", currency: row.currency || "GBP", rows: [], total: 0, firstLine: row.line_no,
          categoryId: null, categoryName: "", source: "none", confidence: 0, suggestion: null, note: "",
        };
        R.byKey.set(row.group_key, g);
        R.groups.push(g);
      }
      g.rows.push(row);
      g.total += row.amount_minor;
      if (g.rows.length === 1 || row.category_source === "user") {
        g.categoryId = !row.needs_review && row.category_id ? row.category_id : null;
        g.categoryName = row.category_name || "";
        g.source = row.category_source || "none";
        g.confidence = row.category_confidence || 0;
        g.suggestion = row.suggested_category_id
          ? { id: row.suggested_category_id, name: row.suggested_category_name, conf: row.suggested_confidence || 0 } : null;
      }
    });
    R.groups.sort((a, b) => (b.rows.length - a.rows.length) || (a.firstLine - b.firstLine));
    R.groups.forEach((g, i) => { g.order = i; g.state = g.categoryId ? "done" : "review"; });
    R.byId = new Map(R.groups.map((g) => [String(g.id), g]));
    return R;
  }

  // What was paid abroad, when every row in the group was in one foreign currency.
  function originalOf(g) {
    const cur = g.rows[0].original_currency;
    if (!cur || !g.rows.every((r) => r.original_currency === cur)) return "";
    const total = g.rows.reduce((s, r) => s + (r.original_amount_minor || 0), 0);
    return `<span class="tx-orig" title="Amount paid">${esc(fmtForeign(total, cur))}</span>`;
  }

  // Left for the user to categorise: a web search on the description helps
  // place a merchant they don't recognise.
  function searchLink(desc) {
    return `<a class="review-search" href="https://www.google.com/search?q=${encodeURIComponent(desc)}" target="_blank" rel="noopener noreferrer"
      title="Search the web for “${esc(desc)}”" aria-label="Search the web for ${esc(desc)}"><svg class="icon"><use href="#i-search"/></svg></a>`;
  }

  function groupHTML(g, R) {
    const n = g.rows.length;
    const dates = g.rows.map((r) => r.date).sort();
    const when = n > 1 && dates[0] !== dates[n - 1] ? `${dateHTML(dates[0])} – ${dateHTML(dates[n - 1])}` : dateHTML(dates[0]);
    const opts = catsFor(g.type);
    if (g.categoryId && !opts.some((c) => c.id === g.categoryId)) {
      opts.push({ id: g.categoryId, name: g.categoryName || "Current category" });
    }
    const select = `<select class="field select" data-cat aria-label="Category for ${esc(g.description)}">
        <option value="">${g.state === "done" ? "Move back to review" : "Choose category…"}</option>
        ${opts.map((c) => `<option value="${esc(c.id)}"${c.id === g.categoryId ? " selected" : ""}>${esc(c.name)}</option>`).join("")}
      </select>`;
    let status = "";
    if (g.state === "pending") {
      status = `<span class="review-checking"><span class="spinner"></span>${R.phase === "ai" ? "Asking the AI…" : "Checking…"}</span>`;
    } else if (g.state === "review") {
      if (g.suggestion) {
        status = `<button type="button" class="review-suggest" data-accept title="Accept the AI's suggestion">
          AI suggests <strong>${esc(g.suggestion.name)}</strong> <svg class="icon"><use href="#i-check"/></svg></button>`;
      } else if (g.note) {
        status = `<span class="review-note">${esc(g.note)}</span>`;
      }
    } else {
      status = sourceBadge(g);
    }
    const rowsTable = n > 1 ? `<details class="review-group-rows"><summary>Show ${n} transactions</summary><table>
        ${g.rows.map((r) => `<tr><td class="cell-date">${dateHTML(r.date)}</td><td class="num tx-amount ${r.amount_minor >= 0 ? "pos" : "neg"}">${fmtMoneySigned(r.amount_minor, r.currency || g.currency)}</td></tr>`).join("")}
      </table></details>` : "";
    return `<div class="review-group" data-group="${g.id}" data-order="${g.order}" data-state="${g.state}">
        <div class="review-group-main">
          <div class="review-group-desc"><span class="review-group-name" title="${esc(g.description)}">${esc(g.description)}</span>${n > 1 ? `<span class="badge badge-soft">×${n}</span>` : ""}${g.state === "review" ? searchLink(g.description) : ""}</div>
          <div class="review-group-sub">${g.destination ? `→ ${esc(g.destination)} · ` : ""}${when}</div>
        </div>
        <div class="tx-amount ${g.total >= 0 ? "pos" : "neg"}" title="${n > 1 ? "Total of the group" : ""}">${fmtMoneySigned(g.total, g.currency)}${originalOf(g)}</div>
        <div class="review-group-cat">${select}${status}</div>
        ${rowsTable}
      </div>`;
  }

  const openReview = (batchID, run) => {
    openFlow(`<div class="thinking-card panel"><div class="head"><span class="spinner"></span> Loading transactions…</div></div>`);
    fetch(`/api/v1/imports/${batchID}/review`).then(async (r) => {
      const body = await r.json().catch(() => ({}));
      if (!r.ok) { flowMessage(`Couldn't load the review: ${esc(errMessage(body, r.status))}`, true); return; }
      const R = buildReview(body);
      if (run) R.groups.forEach((g) => { if (g.source !== "user") { g.state = "pending"; g.suggestion = null; } });
      renderReview(R);
      if (run && R.groups.length) {
        startPipeline(R, `/api/v1/imports/${batchID}/categorise/stream`, "full");
        return;
      }
      setStep("local", "done", "Done");
      setStep("ai", R.aiAvailable ? "done" : "skipped", R.aiAvailable ? "Done" : "Not configured. Add a provider in Settings → AI");
      finishPipeline(R);
    }).catch(() => flowMessage("Couldn't load the review — is the server running?", true));
  };

  function renderReview(R) {
    const b = R.batch;
    const d = b.detection || null;
    const nRows = R.groups.reduce((s, g) => s + g.rows.length, 0);
    const accounts = window.__IMPORT_ACCOUNTS__ || [];
    const defaultAcc = b.account_id || lastImportAccount();
    const dupRows = R.dups.map((row) => `<tr data-row-id="${esc(row.id)}">
        <td class="cell-sub cell-date">${dateHTML(row.date)}</td><td>${esc(row.description)}</td>
        <td class="num tx-amount ${row.amount_minor >= 0 ? "pos" : "neg"}">${fmtMoneySigned(row.amount_minor, row.currency || "GBP")}</td>
        <td><button class="btn btn-ghost btn-xs" type="button" data-force>Import anyway</button></td></tr>`).join("");
    openFlow(`
      <div class="panel import-review" id="importReview">
        <div class="import-review-head">
          <div class="review-into">
            <h3 class="category-form-title">Import ${plural(nRows, "transaction")} into</h3>
            <input type="hidden" id="reviewAccount" value="${esc(defaultAcc)}" />
            <div class="account-pick" role="radiogroup" aria-label="Account to import into">
              ${accounts.map((a) => `<button type="button" class="account-pick-option" role="radio" data-account-id="${esc(a.id)}" aria-checked="${a.id === defaultAcc}">
                <span class="account-pick-dot" style="background:${esc(a.color || "var(--muted)")}"></span>${esc(a.name)}</button>`).join("")}
            </div>
          </div>
          <p class="muted review-file">${esc(b.filename || "")}${b.schema_name ? ` · read as ${esc(b.schema_name)}` : ""}
            ${d ? ' · <button class="link" type="button" id="reviewColumns">Change columns</button>' : ""}</p>
        </div>
        <ol class="cat-pipeline">
          <li data-step="local"><span class="step-num">1</span><div><strong>Quick match</strong><span class="step-note"></span></div></li>
          <li data-step="ai"><span class="step-num">2</span><div><strong>AI</strong><span class="step-note">${R.aiAvailable ? "" : `Not set up · <a href="/settings">Settings</a>`}</span></div></li>
          <li data-step="you"><span class="step-num">3</span><div><strong>Your review</strong><span class="step-note"></span></div></li>
        </ol>
        <div class="progress-bar" id="catProgress"><div class="progress-fill" id="catBar" style="width:0%"></div></div>
        ${R.dups.length ? `<details class="review-dups">
          <summary><svg class="icon"><use href="#i-alert"/></svg>
            <span><strong>${plural(R.dups.length, "duplicate")} already in this account, not imported</strong></span>
            <svg class="icon review-dups-chev"><use href="#i-chevron-down"/></svg></summary>
          <div style="overflow-x:auto"><table class="tx-table"><tbody>${dupRows}</tbody></table></div></details>` : ""}
        <div class="review-cols">
          <section class="review-col review-col-todo" aria-label="Needs your review">
            <header class="review-col-head"><h4>Needs your review</h4><span class="count" id="todoCount"></span>
              <button class="btn btn-ghost btn-xs" type="button" id="acceptAllSuggestions" hidden>Accept all AI suggestions</button></header>
            <div class="review-list" id="todoList"></div>
            <div class="review-empty" id="todoEmpty" hidden>Nothing left to review.</div>
          </section>
          <section class="review-col review-col-done" aria-label="Categorised">
            <header class="review-col-head"><h4>Categorised</h4><span class="count" id="doneCount"></span></header>
            <div class="review-list" id="doneList"></div>
            <div class="review-empty" id="doneEmpty" hidden></div>
          </section>
        </div>
        <div class="review-footer">
          <span class="spacer"></span>
          <button class="btn btn-ghost" id="reviewCancel" type="button">Cancel</button>
          <button class="btn btn-primary" id="reviewConfirm" type="button">Import</button>
        </div>
      </div>`);

    view?.classList.add("import-reviewing");
    const todoList = document.getElementById("todoList");
    const doneList = document.getElementById("doneList");
    R.el = { todoList, doneList, root: document.getElementById("importReview") };
    const frag = { todo: document.createDocumentFragment(), done: document.createDocumentFragment() };
    R.groups.forEach((g) => {
      const tpl = document.createElement("template");
      tpl.innerHTML = groupHTML(g, R).trim();
      g.el = tpl.content.firstElementChild;
      (g.state === "done" ? frag.done : frag.todo).appendChild(g.el);
    });
    todoList.appendChild(frag.todo);
    doneList.appendChild(frag.done);
    updateCounts(R);

    const accSel = document.getElementById("reviewAccount");
    // Back to the Columns step. Continuing there reads the file again, so
    // categories picked in this review start over; Cancel returns here.
    document.getElementById("reviewColumns")?.addEventListener("click", () => {
      if (R.running) return;
      renderBatch(b, undefined, () => openReview(b.id, false));
    });
    // Picking the account checks the rows for duplicates in it; the review
    // is redrawn only when that changes which rows are duplicates.
    R.el.root.querySelector(".account-pick").addEventListener("click", (e) => {
      const opt = e.target.closest("[data-account-id]");
      if (!opt || R.running || opt.getAttribute("aria-checked") === "true") return;
      const prev = accSel.value;
      const pick = (id) => {
        accSel.value = id;
        R.el.root.querySelectorAll("[data-account-id]").forEach((o) => o.setAttribute("aria-checked", String(o.dataset.accountId === id)));
        accSel.dispatchEvent(new Event("change"));
        updateCounts(R);
      };
      pick(opt.dataset.accountId);
      fetch(`/api/v1/imports/${R.batchID}/account`, {
        method: "PUT", headers: jsonHeaders(), body: JSON.stringify({ account_id: opt.dataset.accountId }),
      }).then((r) => jsonOk(r)).then((body) => {
        const dups = (body.rows || []).filter((row) => row.duplicate_of).map((row) => row.id).sort().join();
        if (dups !== R.dups.map((row) => row.id).sort().join()) openReview(R.batchID, false);
      }).catch((err) => { pick(prev); toast(err.message); });
    });

    R.el.root.addEventListener("change", (e) => {
      const sel = e.target.closest("[data-cat]");
      if (!sel) return;
      const g = R.byId.get(sel.closest(".review-group").dataset.group);
      if (g) setGroupCategory(R, g, sel.value || null);
    });
    R.el.root.addEventListener("click", (e) => {
      const accept = e.target.closest("[data-accept]");
      if (accept) {
        const g = R.byId.get(accept.closest(".review-group").dataset.group);
        if (g && g.suggestion) setGroupCategory(R, g, g.suggestion.id, g.suggestion.name);
        return;
      }
      const force = e.target.closest("[data-force]");
      if (force) {
        force.disabled = true;
        patchReview(R, { force_import: [force.closest("tr").dataset.rowId] })
          .then(() => openReview(R.batchID, false))
          .catch((err) => { force.disabled = false; toast(err.message); });
        return;
      }
      if (e.target.closest("[data-retry-ai]")) {
        R.groups.forEach((g) => { if (g.state === "review" && g.source !== "user") { g.state = "pending"; g.note = ""; placeGroup(R, g); } });
        startPipeline(R, `/api/v1/imports/${R.batchID}/categorise/ai/stream`, "ai");
      }
    });
    document.getElementById("acceptAllSuggestions").addEventListener("click", () => {
      const targets = R.groups.filter((g) => g.state === "review" && g.suggestion);
      patchReview(R, { accept_suggestions: true }).then(() => {
        targets.forEach((g) => {
          Object.assign(g, { state: "done", categoryId: g.suggestion.id, categoryName: g.suggestion.name, source: "ai", confidence: g.suggestion.conf, suggestion: null });
          placeGroup(R, g);
        });
        updateCounts(R);
        toast(`Accepted ${plural(targets.length, "suggestion")}`);
      }).catch((err) => toast(err.message));
    });
    document.getElementById("reviewCancel").addEventListener("click", () => {
      if (R.es) R.es.close();
      fetch(`/api/v1/imports/${R.batchID}/cancel`, { method: "POST", headers: { "X-CSRF-Token": csrfToken() } });
      closeFlow();
    });
    document.getElementById("reviewConfirm").addEventListener("click", () => {
      const left = R.groups.filter((g) => g.state !== "done").reduce((s, g) => s + g.rows.length, 0);
      if (left > 0) {
        showConfirm(`${plural(left, "transaction")} still ${left === 1 ? "needs" : "need"} a category`,
          "They'll be imported as Uncategorised — you can categorise them later from Transactions.",
          "Import anyway", () => confirmImport(R), "primary");
      } else {
        confirmImport(R);
      }
    });
  }

  function patchReview(R, patch) {
    return fetch(`/api/v1/imports/${R.batchID}/review`, {
      method: "PATCH", headers: jsonHeaders(), body: JSON.stringify(patch),
    }).then(async (r) => {
      if (!r.ok) {
        const b = await r.json().catch(() => ({}));
        throw new Error(errMessage(b, r.status));
      }
    });
  }

  function setGroupCategory(R, g, catID, catName) {
    const before = { state: g.state, categoryId: g.categoryId, categoryName: g.categoryName, source: g.source, confidence: g.confidence };
    if (catID) {
      const c = (window.__IMPORT_CATS__ || []).find((x) => x.id === catID);
      Object.assign(g, { state: "done", categoryId: catID, categoryName: catName || (c ? c.name : ""), source: "user", confidence: 0 });
    } else {
      Object.assign(g, { state: "review", categoryId: null, categoryName: "", source: "none", confidence: 0 });
    }
    placeGroup(R, g);
    updateCounts(R);
    patchReview(R, { set_category: { row_ids: g.rows.map((r) => r.id), category_id: catID || "" } }).catch((err) => {
      Object.assign(g, before);
      placeGroup(R, g);
      updateCounts(R);
      toast(`Couldn't save: ${err.message}`);
    });
  }

  function placeGroup(R, g) {
    if (!R.el) return;
    const wasOpen = g.el && g.el.querySelector("details[open]");
    const tpl = document.createElement("template");
    tpl.innerHTML = groupHTML(g, R).trim();
    const el = tpl.content.firstElementChild;
    if (wasOpen) el.querySelector("details")?.setAttribute("open", "");
    const list = g.state === "done" ? R.el.doneList : R.el.todoList;
    let before = null;
    if (g.state === "done") {
      // Categorised reads in the order groups landed there, newest at the bottom.
      if (g.el && g.el.parentNode === list) before = g.el.nextSibling;
    } else {
      for (const child of list.children) {
        if (child !== g.el && Number(child.dataset.order) > g.order) { before = child; break; }
      }
    }
    if (g.el) g.el.remove();
    list.insertBefore(el, before);
    g.el = el;
  }

  function updateCounts(R) {
    const sum = (gs) => gs.reduce((s, g) => s + g.rows.length, 0);
    const todo = R.groups.filter((g) => g.state !== "done");
    const done = R.groups.filter((g) => g.state === "done");
    const label = (gs) => {
      const n = sum(gs);
      return n === gs.length ? plural(n, "transaction") : `${plural(n, "transaction")} · ${plural(gs.length, "group")}`;
    };
    document.getElementById("todoCount").textContent = todo.length ? label(todo) : "";
    document.getElementById("doneCount").textContent = done.length ? label(done) : "";
    document.getElementById("todoEmpty").hidden = todo.length > 0;
    document.getElementById("doneEmpty").hidden = done.length > 0;
    document.getElementById("acceptAllSuggestions").hidden = R.running || !todo.some((g) => g.state === "review" && g.suggestion);
    const btn = document.getElementById("reviewConfirm");
    const total = sum(R.groups);
    btn.disabled = R.running || total === 0;
    const acc = (window.__IMPORT_ACCOUNTS__ || []).find((a) => a.id === document.getElementById("reviewAccount").value);
    btn.textContent = R.running ? "Categorising…" : `Import ${plural(total, "transaction")}${acc ? ` into ${acc.name}` : ""}`;
    R.el?.root.querySelector(".account-pick")?.classList.toggle("busy", R.running);
  }

  function setStep(name, state, note) {
    const li = document.querySelector(`.cat-pipeline [data-step="${name}"]`);
    if (!li) return;
    li.dataset.state = state;
    if (note !== undefined) li.querySelector(".step-note").innerHTML = note;
  }
  function setBar(fraction) {
    const bar = document.getElementById("catBar");
    if (bar) bar.style.width = `${Math.max(0, Math.min(100, Math.round(fraction * 100)))}%`;
  }

  function startPipeline(R, url, mode) {
    R.running = true;
    R.finished = false;
    R.aiFailed = false;
    R.phase = mode === "ai" ? "ai" : "local";
    const localShare = R.aiAvailable ? 0.25 : 1;
    document.getElementById("catProgress").hidden = false;
    setBar(mode === "ai" ? localShare : 0);
    if (mode === "full") setStep("local", "active", "Matching your rules and classifier…");
    setStep("you", "", "Anything the AI wasn't sure about");
    updateCounts(R);
    const es = new EventSource(url);
    R.es = es;
    es.onmessage = (ev) => {
      let p;
      try { p = JSON.parse(ev.data); } catch { return; }
      switch (p.phase) {
        case "group": {
          const g = R.byKey.get(p.group_key);
          if (!g || g.source === "user") break;
          if (p.needs_review) {
            g.state = R.phase === "local" && R.aiAvailable ? "pending" : "review";
            g.categoryId = null; g.categoryName = ""; g.source = "none"; g.confidence = 0;
            g.suggestion = p.suggested_category_id ? { id: p.suggested_category_id, name: p.suggested_category, conf: p.suggested_confidence || 0 } : null;
            g.note = p.message || "";
          } else {
            Object.assign(g, { state: "done", categoryId: p.category_id, categoryName: p.category, source: p.source, confidence: p.confidence || 0, suggestion: null, note: "" });
          }
          placeGroup(R, g);
          if (R.phase === "local" && p.total) setBar(localShare * (p.index / p.total));
          break;
        }
        case "local_done":
          setStep("local", "done", p.categorised ? `Placed ${plural(p.categorised, "group")}` : "No matches yet - rules grow as you import");
          setBar(localShare);
          break;
        case "ai_start":
          R.phase = "ai";
          setStep("ai", "active", `Asking about ${plural(p.total || 0, "group")}${p.batches > 1 ? ` in ${p.batches} batches` : ""}…`);
          R.groups.forEach((g) => { if (g.state === "pending") placeGroup(R, g); });
          break;
        case "ai_progress":
          setBar(localShare + (1 - localShare) * (p.index / p.total));
          if (p.total > 1) setStep("ai", "active", `Batch ${p.index} of ${p.total} answered…`);
          break;
        case "ai_done":
          setStep("ai", R.aiFailed ? "failed" : "done", R.aiFailed
            ? `Stopped early — ${plural(p.categorised || 0, "group")} placed. <button class="link" type="button" data-retry-ai>Retry</button>`
            : `Placed ${plural(p.categorised || 0, "group")} confidently · ${p.pending || 0} unsure`);
          if (mode === "ai") finishPipeline(R);
          break;
        case "ai_skipped":
          setStep("ai", "skipped", p.message === "AI is not configured"
            ? "Not configured. Add a provider in Settings → AI" : esc(p.message || "Skipped"));
          if (mode === "ai") finishPipeline(R);
          break;
        case "warning":
          R.aiFailed = true;
          toast(p.message || "AI problem", "error");
          break;
        case "done":
          finishPipeline(R);
          break;
        case "error":
          toast(p.message || "Categorisation failed", "error");
          setStep(R.phase === "ai" ? "ai" : "local", "failed", esc(p.message || "Failed"));
          finishPipeline(R);
          break;
      }
      updateCounts(R);
    };
    es.onerror = () => {
      es.close(); // never auto-reconnect: that re-runs the pipeline and spends AI tokens again
      if (!R.finished) {
        toast("Lost the connection while categorising");
        finishPipeline(R);
      }
    };
  }

  function finishPipeline(R) {
    if (R.es) { R.es.close(); R.es = null; }
    R.running = false;
    R.finished = true;
    R.phase = "done";
    R.groups.forEach((g) => { if (g.state === "pending") { g.state = "review"; placeGroup(R, g); } });
    const todo = R.groups.filter((g) => g.state !== "done");
    setStep("you", todo.length ? "active" : "done", todo.length
      ? `${plural(todo.length, "group")} for you to check`
      : "Nothing to check - ready to import");
    setBar(1);
    const prog = document.getElementById("catProgress");
    if (prog) setTimeout(() => { if (!R.running) prog.hidden = true; }, 600);
    updateCounts(R);
  }

  function confirmImport(R) {
    const btn = document.getElementById("reviewConfirm");
    const accountID = document.getElementById("reviewAccount").value;
    if (!accountID) { toast("Choose the account to import into"); return; }
    try { localStorage.setItem("glowworm-import-account", accountID); } catch (e) {}
    btn.disabled = true;
    btn.textContent = "Importing…";
    fetch(`/api/v1/imports/${R.batchID}/confirm`, {
      method: "POST",
      headers: jsonHeaders(),
      body: JSON.stringify({ account_id: accountID }),
    }).then(async (r) => {
      const b = await r.json().catch(() => ({}));
      if (!r.ok) {
        toast(`Import failed: ${errMessage(b, r.status)}`);
        updateCounts(R);
        return;
      }
      // Back to the import page, ready for the next file.
      const acc = (window.__IMPORT_ACCOUNTS__ || []).find((a) => a.id === accountID);
      const imported = R.groups.reduce((s, g) => s + g.rows.length, 0);
      reloadWithToast(`Imported ${plural(imported, "transaction")}${acc ? ` into ${acc.name}` : ""}`);
    }).catch(() => { toast("Import failed: network error"); updateCounts(R); });
  }
});


// The current timeline's tick layout, redone when the window resizes. The
// timeline may be rebuilt, so it's kept here and the listener is added once.
let layoutTimelineTicks = null;
let tickResizeTimer;
window.addEventListener("resize", () => {
  clearTimeout(tickResizeTimer);
  tickResizeTimer = setTimeout(() => { if (layoutTimelineTicks) layoutTimelineTicks(); }, 100);
});

function initHistoryTimeline() {
  const slider = document.getElementById("timelineSlider");
  if (!slider) return;
  let months = [];
  try { months = JSON.parse(slider.dataset.months || "[]"); } catch (e) { months = []; }
  if (!months.length) months = (window.__TIMELINE_MONTHS__ || []);
  const chartHost = document.getElementById("timelineChart");
  let fullSeries = null;
  if (chartHost && chartHost.dataset.chartData) {
    try { fullSeries = JSON.parse(chartHost.dataset.chartData); } catch (e) { fullSeries = null; }
  }
  let timer = null;
  const renderChart = (endDate) => {
    if (!chartHost || !fullSeries || !Array.isArray(fullSeries.labels)) return;
    let count = fullSeries.labels.length;
    for (let i = 0; i < fullSeries.labels.length; i++) {
      if (String(fullSeries.labels[i]) > endDate) { count = i; break; }
    }
    const series = (fullSeries.series || []).map((s) => Object.assign({}, s, { values: (s.values || []).slice(0, count) }));
    chartHost.dataset.chartData = JSON.stringify({ labels: fullSeries.labels.slice(0, count), series });
    if (window.redrawChartHost) window.redrawChartHost(chartHost);
  };
  const render = (idx) => {
    const end = months[idx];
    if (!end) return;
    document.getElementById("timelineDate").textContent = humanDate(end);
    const chip = document.getElementById("timelineChip");
    const stepsBack = months.length - 1 - idx;
    chip.textContent = stepsBack === 0 ? "Latest" : stepsBack === 1 ? "1 step back" : stepsBack + " steps back";
    slider.style.setProperty("--fill", (months.length > 1 ? (idx / (months.length - 1)) * 100 : 0) + "%");
    renderChart(end);
    fetch(`/api/v1/snapshot?date=${end}`).then((r) => jsonOk(r)).then((b) => {
      const bal = document.getElementById("timelineBalance");
      if (bal) bal.textContent = fmtMoneyMinor(b.total_minor, b.currency);
      const count = document.getElementById("timelineTxCount");
      if (count) count.textContent = b.tx_count;
      const recon = b.reconciled || {};
      const reconLabel = document.getElementById("timelineReconLabel");
      const reconHint = document.getElementById("timelineReconHint");
      if (reconLabel && recon.label) {
        reconLabel.textContent = recon.label;
        reconLabel.classList.toggle("ok", recon.status === "verified");
        reconHint.textContent = recon.hint || "";
        reconHint.classList.toggle("up", recon.status === "verified");
      }
      const wrap = document.getElementById("timelineAccounts");
      if (wrap) {
        wrap.innerHTML = (b.accounts || []).map((a) => `
          <div class="timeline-account">
            <span class="account-dot" style="background:${esc(a.color)}"></span>
            <span class="account-name">${esc(a.name)}</span>
            <span class="account-balance ${a.balance_minor < 0 ? "neg" : ""}">${fmtMoneyMinor(a.balance_minor, b.currency)}</span>
          </div>`).join("");
      }
    }).catch(() => {});
  };
  slider.addEventListener("input", (e) => {
    const idx = Number(e.target.value);
    clearTimeout(timer);
    timer = setTimeout(() => render(idx), 80);
  });
  const ticks = document.getElementById("timelineTicks");
  // As many date labels as fit the slider's width (up to 7, and at least the
  // two ends); a middle one that would still run into its neighbour or the
  // last is dropped. A single date gets a single label, under the thumb.
  const layoutTicks = () => {
    if (!ticks || !months.length) return;
    const count = Math.min(months.length, Math.max(2, Math.min(7, Math.floor(ticks.clientWidth / 64))));
    const denom = count > 1 ? count - 1 : 1;
    let html = "";
    for (let i = 0; i < count; i++) {
      const tIdx = Math.round((i * (months.length - 1)) / denom);
      const pos = months.length > 1 ? (tIdx / (months.length - 1)) * 100 : 0;
      const cls = i === 0 ? " first" : i === count - 1 ? " last" : "";
      html += `<div class="timeline-tick${cls}" style="left:${pos}%"><span>${esc(tickDateLabel(months[tIdx]))}</span></div>`;
    }
    ticks.innerHTML = html;
    const spans = [...ticks.querySelectorAll(".timeline-tick span")];
    if (spans.length < 3) return;
    const gap = 8;
    const last = spans[spans.length - 1].getBoundingClientRect();
    let prevRight = spans[0].getBoundingClientRect().right;
    spans.slice(1, -1).forEach((span) => {
      const r = span.getBoundingClientRect();
      if (r.left < prevRight + gap || r.right > last.left - gap) span.parentElement.hidden = true;
      else prevRight = r.right;
    });
  };
  layoutTicks();
  layoutTimelineTicks = layoutTicks;

  render(Number(slider.value));
}
document.addEventListener("DOMContentLoaded", initHistoryTimeline);

function tickDateLabel(iso) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(iso || "")) return shortMonthLabel(iso);
  const d = new Date(iso + "T00:00:00");
  if (isNaN(d)) return iso;
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "2-digit" });
}
function humanDate(iso) {
  const d = new Date(iso + "T00:00:00");
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "long", year: "numeric" });
}

let historyPaneToken = 0;

function historySkeletonHTML() {
  return `<div class="pane-skeleton">${Array.from({ length: 6 }, () => `<div class="sk-block sk-row"></div>`).join("")}</div>`;
}

// The audit log's filter is remembered on this device, so it's still chosen
// after a visit to another page.
const auditFilter = {
  KEY: "glowworm-audit-filter",
  get() {
    try {
      const f = localStorage.getItem(this.KEY);
      return f === "you" || f === "system" ? f : "all";
    } catch (e) { return "all"; }
  },
  set(f) {
    try {
      if (f === "you" || f === "system") localStorage.setItem(this.KEY, f);
      else localStorage.removeItem(this.KEY);
    } catch (e) {}
  },
  // The History tab asks for the page with the filter already applied.
  pointTab() {
    const f = this.get();
    document.querySelectorAll('.tabnav a[href^="/history"]').forEach((a) => {
      a.setAttribute("href", f === "all" ? "/history" : "/history?filter=" + f);
    });
  },
};
document.addEventListener("DOMContentLoaded", () => auditFilter.pointTab());

// The audit log's filter chips reload just the log, not the timeline above it.
function loadHistoryPane(url, push) {
  const host = document.getElementById("historyPaneHost");
  if (!host) return;
  const u = new URL(url, location.origin);
  const filter = u.searchParams.get("filter") || "all";
  auditFilter.set(filter);
  auditFilter.pointTab();
  // "Show older changes" asks for a longer list; the one on screen stays
  // until it arrives, so the page doesn't jump.
  const show = u.searchParams.get("show") || "";

  document.querySelectorAll(".history-filter .chip").forEach((a) => {
    a.classList.toggle("active", new URL(a.href).searchParams.get("filter") === filter);
  });
  if (!show) host.innerHTML = historySkeletonHTML();

  const token = ++historyPaneToken;
  const more = show ? `&show=${encodeURIComponent(show)}` : "";
  fetch(`/history/pane?filter=${encodeURIComponent(filter)}${more}`, { headers: { "X-Requested-With": "fetch" } })
    .then((r) => {
      // A redirect (e.g. to /setup) would otherwise be injected as pane HTML.
      if (!r.ok || r.redirected) throw new Error("pane " + r.status);
      return r.text();
    })
    .then((html) => {
      if (token !== historyPaneToken) return;
      if (window.clearHistoryPreview) window.clearHistoryPreview();
      host.innerHTML = html;
      if (window.wireAuditActions) window.wireAuditActions(host);
    })
    .catch(() => { location.href = u.href; });
  if (push && !homeScreenApp) history.pushState({}, "", u.href);
  else if (push) history.replaceState(history.state, "", u.href);
}

document.addEventListener("DOMContentLoaded", () => {
  const view = document.getElementById("view-history");
  if (!view) return;
  // Reached by a link that names no filter (one in a page, a shortcut):
  // the remembered one is applied here.
  const asked = new URL(location.href).searchParams.get("filter");
  if (asked) {
    auditFilter.set(asked);
    auditFilter.pointTab();
  } else if (auditFilter.get() !== "all") {
    const u = new URL(location.href);
    u.searchParams.set("filter", auditFilter.get());
    history.replaceState(history.state, "", u.href);
    loadHistoryPane(u.href, false);
  }
  view.addEventListener("click", (e) => {
    const a = e.target.closest && e.target.closest('a[href^="/history?"]');
    if (!a) return;
    e.preventDefault();
    loadHistoryPane(a.getAttribute("href"), true);
  });
  window.addEventListener("popstate", () => {
    if (document.getElementById("historyPaneHost")) loadHistoryPane(location.href, false);
  });
});

// ---- Signing in with a passkey, and the Passkeys settings.
document.addEventListener("DOMContentLoaded", () => {
  const loginBox = document.getElementById("loginPasskey");
  if (loginBox && passkeys.supported()) {
    loginBox.hidden = false;
    const btn = document.getElementById("loginPasskeyBtn");
    const showError = (msg) => {
      let p = document.querySelector(".login-error");
      if (!p) {
        p = document.createElement("p");
        p.className = "login-error";
        document.querySelector(".login-card form").before(p);
      }
      p.textContent = msg;
    };
    // The username field offers passkeys in its autofill where the browser
    // can; the button asks outright, cancelling that first.
    let autofill = null;
    const offerInAutofill = async () => {
      if (!(await passkeys.conditionalSupported())) return;
      autofill = new AbortController();
      const { signal } = autofill;
      try {
        await passkeys.signIn({ conditional: true, signal });
        location.replace("/");
      } catch (err) {
        if (!signal.aborted && !passkeys.cancelled(err)) showError(err.message);
      }
    };
    btn.addEventListener("click", async () => {
      autofill?.abort();
      btn.disabled = true;
      try {
        await passkeys.signIn();
        location.replace("/");
        return;
      } catch (err) {
        if (!passkeys.cancelled(err)) showError(err.message || "Couldn't sign in with a passkey");
      }
      btn.disabled = false;
      offerInAutofill();
    });
    offerInAutofill();
  }

  const box = document.getElementById("passkeySettings");
  if (!box) return;
  // Greyed out by the server where passkeys can't work (no https); in a
  // browser that has none at all the section isn't shown.
  if (!box.classList.contains("is-unavailable") && !passkeys.supported()) {
    box.hidden = true;
    return;
  }
  const add = document.getElementById("passkeyAddBtn");
  add?.addEventListener("click", async () => {
    add.disabled = true;
    try {
      await passkeys.create();
      reloadWithToast("Passkey added. Use it next time you sign in.");
      return;
    } catch (err) {
      if (err && err.name === "InvalidStateError") toast("That passkey is already set up for Glowworm");
      else if (!passkeys.cancelled(err)) toast(err.message || "Couldn't add the passkey");
    }
    add.disabled = false;
  });
  box.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-passkey-remove]");
    if (!btn) return;
    showConfirm("Remove this passkey?", `It won't sign in to Glowworm any more. You can still sign in with your password. To tidy up, delete it from where it's kept (${btn.dataset.passkeyName}) too.`, "Remove", async () => {
      try {
        await fetch("/api/v1/passkeys/" + encodeURIComponent(btn.dataset.passkeyRemove), {
          method: "DELETE", headers: { "X-CSRF-Token": csrfToken() },
        }).then((r) => jsonOk(r));
        reloadWithToast("Passkey removed");
      } catch (err) { toast(err.message); }
    });
  });
});

// ---- Push notifications (Settings → Reminders). A device turns them on by
// subscribing with the server's VAPID key and sending the subscription to
// the server, which sends balance reminders to every subscribed device.
const push = (() => {
  const supported = () => "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
  const isIOS = () => /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
  const standalone = () => window.matchMedia("(display-mode: standalone)").matches || navigator.standalone === true;
  const send = (method, url, body) => fetch(url, {
    method, headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
    body: body ? JSON.stringify(body) : undefined,
  }).then((r) => jsonOk(r));
  // The service worker's registration, or null when there isn't one soon.
  const registration = () => Promise.race([
    navigator.serviceWorker.ready,
    new Promise((ok) => setTimeout(() => ok(null), 4000)),
  ]);
  const subscription = async () => {
    if (!supported() || !window.isSecureContext) return null;
    const reg = await registration();
    return reg ? reg.pushManager.getSubscription() : null;
  };
  const keyBytes = (b64) => {
    const s = (b64 + "===".slice((b64.length + 3) % 4)).replace(/-/g, "+").replace(/_/g, "/");
    return Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
  };
  const sameKey = (sub, key) => {
    const k = sub.options && sub.options.applicationServerKey;
    if (!k) return true;
    const a = new Uint8Array(k), b = keyBytes(key);
    return a.length === b.length && a.every((x, i) => x === b[i]);
  };
  const STORE = "glowworm-push-endpoint";
  const remember = (endpoint) => { try { endpoint ? localStorage.setItem(STORE, endpoint) : localStorage.removeItem(STORE); } catch (e) {} };
  return {
    supported, isIOS, standalone, send, subscription, remember,
    // on asks for permission and subscribes this device.
    async on(publicKey) {
      const perm = await Notification.requestPermission();
      if (perm !== "granted") throw new Error(perm === "denied"
        ? "Notifications are blocked for Glowworm. Allow them in this browser's settings, then try again."
        : "Notifications weren't allowed.");
      const reg = await registration();
      if (!reg) throw new Error("Couldn't start notifications on this device. Reload the page and try again.");
      let sub = await reg.pushManager.getSubscription();
      if (sub && !sameKey(sub, publicKey)) { await sub.unsubscribe(); sub = null; }
      if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(publicKey) });
      await send("POST", "/api/v1/notifications/subscribe", sub.toJSON());
      remember(sub.endpoint);
      return sub;
    },
    async off(sub) {
      if (!sub) return;
      await send("POST", "/api/v1/notifications/unsubscribe", { endpoint: sub.endpoint }).catch(() => {});
      await sub.unsubscribe().catch(() => {});
      remember("");
    },
    // sync tells the server about a subscription the browser renewed by
    // itself since this device last said, so reminders keep reaching it.
    async sync() {
      if (!supported() || !("Notification" in window) || Notification.permission !== "granted") return;
      const sub = await subscription();
      let known = "";
      try { known = localStorage.getItem(STORE) || ""; } catch (e) {}
      if (!sub || !known || sub.endpoint === known) return;
      await send("POST", "/api/v1/notifications/subscribe", sub.toJSON());
      await send("POST", "/api/v1/notifications/unsubscribe", { endpoint: known }).catch(() => {});
      remember(sub.endpoint);
    },
  };
})();

document.addEventListener("DOMContentLoaded", () => {
  setTimeout(() => push.sync().catch(() => {}), 3000);
});

document.addEventListener("DOMContentLoaded", () => {
  const root = document.getElementById("notifySettings");
  if (!root) return;
  const $ = (id) => document.getElementById(id);
  const status = $("notifyStatus"), toggle = $("notifyToggle"), test = $("notifyTest");
  let info = null, sub = null, busy = false;

  const hourSel = $("notifyHour");
  for (let h = 0; h < 24; h++) hourSel.insertAdjacentHTML("beforeend", `<option value="${h}">${String(h).padStart(2, "0")}:00</option>`);
  // A value the list doesn't offer (set some other way) still shows.
  const setSelect = (sel, v, label) => {
    v = String(v);
    if (![...sel.options].some((o) => o.value === v)) sel.insertAdjacentHTML("beforeend", `<option value="${esc(v)}">${esc(label(v))}</option>`);
    sel.value = v;
  };
  const day = (s) => (s ? new Date(s).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" }) : "");

  // What this device can do, as a sentence; null when it can have them.
  const blocker = () => {
    if (!window.isSecureContext) return "Notifications need Glowworm to be opened over HTTPS.";
    if (push.isIOS() && !push.standalone()) return "On iPhone and iPad, add Glowworm to your Home Screen (Share → Add to Home Screen), open it from there, and turn notifications on here.";
    if (!push.supported()) return "This browser can't show notifications from websites.";
    if (Notification.permission === "denied") return "Notifications are blocked for Glowworm in this browser's settings. Allow them there, then reload this page.";
    return null;
  };

  const renderDevices = () => {
    const list = $("notifyDevices");
    const devices = (info && info.devices) || [];
    list.hidden = !devices.length;
    list.innerHTML = devices.map((d) => {
      const mine = sub && d.this_device;
      const bits = [mine ? "This device" : "", d.last_sent_at ? `last sent ${esc(day(d.last_sent_at))}` : `added ${esc(day(d.created_at))}`].filter(Boolean);
      bits[0] = bits[0][0].toUpperCase() + bits[0].slice(1);
      return `<li class="passkey-item">
        <div>
          <strong>${esc(d.label || "A device")}</strong>
          <span class="muted">${bits.join(" · ")}</span>
          ${d.last_error ? `<span class="notify-error">${esc(d.last_error)}</span>` : ""}
        </div>
        <button class="btn btn-ghost btn-xs" type="button" data-notify-remove="${esc(d.id)}" data-mine="${mine ? 1 : ""}">Remove</button>
      </li>`;
    }).join("");
  };

  const render = () => {
    const why = blocker();
    const devices = (info && info.devices) || [];
    // Only a reason it can't be done is spelled out; the button says the rest.
    status.textContent = why || "";
    status.hidden = !why;
    if (why) {
      toggle.hidden = true;
    } else {
      toggle.hidden = false;
      toggle.textContent = sub ? "Turn off for this device" : "Turn on for this device";
      toggle.className = sub ? "btn btn-ghost" : "btn btn-primary";
    }
    test.hidden = !(sub || devices.length);
    test.textContent = sub ? "Send a test notification" : "Send a test to my devices";
    renderDevices();
    if (info) {
      const s = info.settings;
      $("notifyEnabled").checked = !!s.enabled;
      setSelect($("notifyDays"), s.default_days, (v) => (v === "0" ? "Never" : `${v} days`));
      setSelect(hourSel, s.hour, (v) => `${v}:00`);
      setSelect($("notifyRepeat"), s.repeat_days, (v) => `Every ${v} days`);
      hourSel.title = info.timezone ? `Time in ${info.timezone}` : "";
      $("notifyForm").hidden = false;
    }
  };

  const load = async () => {
    sub = await push.subscription().catch(() => null);
    const q = sub ? `?endpoint=${encodeURIComponent(sub.endpoint)}` : "";
    info = await fetch(`/api/v1/notifications${q}`).then((r) => jsonOk(r));
    // Subscribed here but unknown to the server (removed on another
    // device, or the server's data was replaced): say so again.
    if (sub && !info.devices.some((d) => d.this_device) && Notification.permission === "granted") {
      try {
        await push.on(info.public_key);
        sub = await push.subscription();
        info = await fetch(`/api/v1/notifications?endpoint=${encodeURIComponent(sub.endpoint)}`).then((r) => jsonOk(r));
      } catch (e) { sub = null; }
    }
    render();
  };

  toggle.addEventListener("click", async () => {
    if (busy || !info) return;
    busy = true;
    toggle.disabled = true;
    try {
      if (sub) {
        await push.off(sub);
        toast("Notifications turned off for this device");
      } else {
        await push.on(info.public_key);
        toast("Notifications turned on for this device", "ok");
      }
      await load();
    } catch (e) {
      toast(e.message || "Could not change notifications", "error");
      await load().catch(() => {});
    } finally {
      busy = false;
      toggle.disabled = false;
    }
  });

  const runTest = async (btn, body, fallback) => {
    btn.disabled = true;
    try {
      const out = await push.send("POST", "/api/v1/notifications/test", body);
      const failed = (out.results || []).filter((r) => !r.ok);
      if (!out.sent) toast(failed[0] ? `Not delivered: ${failed[0].error}` : fallback, "error");
      else if (failed.length) toast(`Sent to ${out.sent}; ${failed.length} device${failed.length > 1 ? "s" : ""} didn't take it`, "error");
      else toast(out.sent > 1 ? `Sent to ${out.sent} devices` : "Sent. It should appear in a moment.", "ok");
      await load();
    } catch (e) {
      toast(e.message || fallback, "error");
    } finally {
      btn.disabled = false;
    }
  };
  test.addEventListener("click", () => runTest(test, sub ? { endpoint: sub.endpoint } : {}, "Could not send the test"));

  $("notifyDevices").addEventListener("click", (e) => {
    const b = e.target.closest("[data-notify-remove]");
    if (!b) return;
    showConfirm("Remove device?", "It won't get reminders until notifications are turned on there again.", "Remove", async () => {
      try {
        if (b.dataset.mine) await push.off(sub);
        else await push.send("DELETE", `/api/v1/notifications/devices/${b.dataset.notifyRemove}`);
        await load();
      } catch (err) {
        toast(err.message || "Could not remove the device", "error");
      }
    });
  });

  $("notifyForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const btn = $("notifySave");
    btn.disabled = true;
    try {
      const s = await push.send("PUT", "/api/v1/notifications/settings", {
        enabled: $("notifyEnabled").checked,
        default_days: Number($("notifyDays").value),
        hour: Number(hourSel.value),
        repeat_days: Number($("notifyRepeat").value),
      });
      info.settings = s;
      render();
      toast("Reminders saved", "ok");
    } catch (err) {
      toast(err.message || "Could not save the reminders", "error");
    } finally {
      btn.disabled = false;
    }
  });

  load().catch((e) => { status.textContent = e.message || "Couldn't load the notification settings."; });
});

// ---- The Savings tab's count of out-of-date balances (see savings_dot.go).
// An installed HTTPS web app shows the same number on its icon where the
// platform badges web apps; it goes as balances are updated.
document.addEventListener("DOMContentLoaded", () => {
  if (!("setAppBadge" in navigator) || !window.isSecureContext) return;
  const n = Number(document.querySelector("[data-balances-due]")?.dataset.balancesDue || 0);
  (n > 0 ? navigator.setAppBadge(n) : navigator.clearAppBadge()).catch(() => {});
});
