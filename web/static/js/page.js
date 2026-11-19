// A page's lifetime. In the home screen app, moving between the tabs swaps
// the page's content in place instead of loading a whole new document (see
// softNav in app.js), so the setup each page does when it opens has to be
// able to run again, and whatever it left running has to stop when the
// page is swapped out.
//
// Page setup registers with onPage(fn) instead of DOMContentLoaded. While
// those functions run, every event listener they add (on the document, the
// window, a media query, an element) is tied to this page, and so are the
// intervals they start and the observers they make: leaving the page
// removes, clears and disconnects them all. Work a page starts later (in a
// click handler, after a fetch) is not tracked, so it must clean up after
// itself as it always has.
(() => {
  const inits = [];
  let life = null; // the current page's { ctrl, intervals, observers }
  let tracking = null; // set only while the page's setup runs

  const addEventListener = EventTarget.prototype.addEventListener;
  EventTarget.prototype.addEventListener = function (type, fn, opts) {
    if (tracking && !(opts && typeof opts === "object" && opts.signal)) {
      opts = opts && typeof opts === "object"
        ? Object.assign({}, opts, { signal: tracking.ctrl.signal })
        : { capture: !!opts, signal: tracking.ctrl.signal };
    }
    return addEventListener.call(this, type, fn, opts);
  };
  const setInterval = window.setInterval;
  window.setInterval = function () {
    const id = setInterval.apply(window, arguments);
    if (tracking) tracking.intervals.push(id);
    return id;
  };
  for (const name of ["MutationObserver", "ResizeObserver", "IntersectionObserver"]) {
    const Base = window[name];
    if (!Base) continue;
    window[name] = class extends Base {
      constructor(...args) {
        super(...args);
        if (tracking) tracking.observers.push(this);
      }
    };
  }

  window.onPage = (fn) => { inits.push(fn); };
  window.pageLife = {
    // Runs every page's setup. One that throws doesn't stop the rest.
    start() {
      life = { ctrl: new AbortController(), intervals: [], observers: [] };
      tracking = life;
      try {
        for (const fn of inits) {
          try { fn(); } catch (e) { console.error(e); }
        }
      } finally {
        tracking = null;
      }
    },
    stop() {
      if (!life) return;
      life.ctrl.abort();
      life.intervals.forEach(clearInterval);
      life.observers.forEach((o) => o.disconnect());
      life = null;
    },
  };
  document.addEventListener("DOMContentLoaded", () => window.pageLife.start());
})();
