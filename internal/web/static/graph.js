// Progressive-enhancement layer for the graph (graph-ui v2 pinned timeline).
// The SVG + pinned overlays are fully server-rendered and every node is a real
// <a href>, so with JS off the timeline still scrolls natively and clicking a
// node navigates. This script adds: drag-to-pan, time-density zoom (buttons /
// ctrl-wheel / fit, center-preserving, re-rendered server-side via fetch so
// dots never distort), hover tooltip, click → side panel, search fade, and
// edge-style toggles. No framework. Interactions use event delegation on
// #tl-wrap so they survive the SVG being swapped on zoom.
(function () {
  "use strict";

  var wrap = document.getElementById("tl-wrap");
  var scroll = document.getElementById("tl-scroll");
  if (!wrap || !scroll) return; // empty-state graph: nothing to wire.

  var controls = document.getElementById("graph-controls");
  var hint = document.getElementById("graph-hint");
  var zoomBar = document.getElementById("graph-zoom");
  var tooltip = document.getElementById("graph-tooltip");
  var panel = document.getElementById("graph-panel");
  var search = document.getElementById("graph-search");
  if (controls) controls.hidden = false;
  if (hint) hint.hidden = false;
  if (zoomBar) zoomBar.hidden = false;

  var selectedId = null;
  var query = "";

  function svg() {
    return document.getElementById("tl-svg");
  }
  function nodeEls() {
    var list = [].slice.call(document.querySelectorAll("#tl-svg .node"));
    return list.concat([].slice.call(document.querySelectorAll(".tl-goal")));
  }
  function edgeEls() {
    return [].slice.call(document.querySelectorAll("#tl-svg .edge"));
  }
  function num(el, attr) {
    return parseFloat(el.getAttribute(attr)) || 0;
  }
  function data(el, key) {
    return el.getAttribute("data-" + key);
  }

  // ── tooltip ───────────────────────────────────────────────────────────
  function showTooltip(node) {
    if (!tooltip) return;
    var type = data(node, "type") || "";
    var status = data(node, "status");
    var occurred = data(node, "occurred");
    var body = data(node, "body") || "";
    var isGoal = data(node, "kind") === "goal";
    var mark = isGoal
      ? '<span class="gp-goalmark"></span>'
      : '<span class="tt-dot legend-dot ' + esc(type) + '"></span>';
    var html =
      '<div class="tt-head">' + mark +
      '<span class="tt-type">' + esc(type) + (status ? " · " + esc(status) : "") + "</span></div>" +
      '<div class="tt-title">' + esc(data(node, "title") || "") + "</div>";
    if (occurred && occurred !== "-") html += '<div class="tt-meta">' + esc(occurred) + "</div>";
    if (body) html += '<div class="tt-body">' + esc(body) + "</div>";
    tooltip.innerHTML = html;
    tooltip.hidden = false;
  }
  function positionTooltip(evt) {
    if (!tooltip || tooltip.hidden) return;
    var rect = wrap.getBoundingClientRect();
    var x = evt.clientX - rect.left + 14;
    var y = evt.clientY - rect.top + 14;
    if (x > rect.width - 280) x = Math.max(8, x - 300);
    tooltip.style.left = x + "px";
    tooltip.style.top = y + "px";
  }
  function hideTooltip() {
    if (tooltip) tooltip.hidden = true;
  }

  // ── side detail panel ───────────────────────────────────────────────────
  function openPanel(node) {
    if (!panel) return;
    var type = data(node, "type") || "";
    var status = data(node, "status");
    var occurred = data(node, "occurred");
    var ingested = data(node, "ingested");
    var version = data(node, "version");
    var body = data(node, "body") || "";
    var href = data(node, "href") || "#";
    var isGoal = data(node, "kind") === "goal";
    var mark = isGoal
      ? '<span class="gp-goalmark"></span>'
      : '<span class="legend-dot ' + esc(type) + '"></span>';
    var kv = "";
    if (occurred && occurred !== "-") kv += "<dt>occurred</dt><dd>" + esc(occurred) + "</dd>";
    if (ingested && ingested !== "-") kv += "<dt>ingested</dt><dd>" + esc(ingested) + "</dd>";
    if (version) kv += "<dt>version</dt><dd>v" + esc(version) + "</dd>";
    panel.innerHTML =
      '<div class="gp-head">' + mark +
      '<span class="gp-type">' + esc(type) + "</span>" +
      '<button type="button" class="gp-close" title="close">×</button></div>' +
      "<h3>" + esc(data(node, "title") || "") + "</h3>" +
      (status ? '<div class="gp-status">' + esc(status) + "</div>" : "") +
      (body ? "<pre>" + esc(body) + "</pre>" : "") +
      '<dl class="gp-kv">' + kv + "</dl>" +
      '<a class="gp-open" href="' + esc(href) + '">open full entry →</a>';
    panel.hidden = false;
    panel.querySelector(".gp-close").addEventListener("click", closePanel);
    selectedId = data(node, "id");
    applyHighlight();
    updateReset();
  }
  function closePanel() {
    if (panel) panel.hidden = true;
    selectedId = null;
    applyHighlight();
    updateReset();
  }

  // ── search fade + selection ─────────────────────────────────────────────
  function applyHighlight() {
    var q = query.trim().toLowerCase();
    var titleById = {};
    nodeEls().forEach(function (n) {
      titleById[data(n, "id")] = (data(n, "title") || "").toLowerCase();
      var id = data(n, "id");
      var hit = !q || (data(n, "title") || "").toLowerCase().indexOf(q) !== -1;
      toggle(n, "dimmed", q && !hit);
      toggle(n, "matched", !!q && hit);
      toggle(n, "selected", id === selectedId);
    });
    edgeEls().forEach(function (e) {
      var hit = !q ||
        (titleById[data(e, "from")] || "").indexOf(q) !== -1 ||
        (titleById[data(e, "to")] || "").indexOf(q) !== -1;
      toggle(e, "dimmed", q && !hit);
    });
  }

  // ── edge-style toggles (blocks always on) ────────────────────────────────
  function applyEdgeFilter() {
    var on = {};
    [].forEach.call(document.querySelectorAll(".edge-toggle"), function (cb) {
      on[cb.value] = cb.checked;
    });
    edgeEls().forEach(function (e) {
      var style = data(e, "style");
      e.style.display = style === "blocks" || on[style] ? "" : "none";
    });
  }

  // ── delegated hover / click (survive SVG swaps) ──────────────────────────
  wrap.addEventListener("mouseover", function (e) {
    var n = e.target.closest(".node, .tl-goal");
    if (n) showTooltip(n);
  });
  wrap.addEventListener("mousemove", positionTooltip);
  wrap.addEventListener("mouseout", function (e) {
    if (!e.relatedTarget || !e.relatedTarget.closest || !e.relatedTarget.closest(".node, .tl-goal")) {
      hideTooltip();
    }
  });
  wrap.addEventListener("click", function (e) {
    var n = e.target.closest(".node, .tl-goal");
    if (!n) return;
    if (dragMoved) { e.preventDefault(); return; }
    e.preventDefault(); // inspect in-place; panel's "open full entry →" navigates
    hideTooltip();
    openPanel(n);
  });

  // ── drag-to-pan + wheel-to-pan ───────────────────────────────────────────
  var dragStart = null;
  var dragMoved = false;
  scroll.addEventListener("pointerdown", function (e) {
    dragStart = { x: e.clientX, left: scroll.scrollLeft };
    dragMoved = false;
  });
  scroll.addEventListener("pointermove", function (e) {
    if (!dragStart) return;
    var dx = e.clientX - dragStart.x;
    if (Math.abs(dx) > 4) dragMoved = true;
    scroll.scrollLeft = dragStart.left - dx;
  });
  function endDrag() {
    dragStart = null;
    setTimeout(function () { dragMoved = false; }, 0);
  }
  scroll.addEventListener("pointerup", endDrag);
  scroll.addEventListener("pointerleave", function () { endDrag(); hideTooltip(); });

  // ── time-density zoom ────────────────────────────────────────────────────
  var DEFAULT_PPD = 160;
  var zoomTimer = null;
  var pendingPpd = null;

  function fitPpd() {
    var s = svg();
    var spanDays = s ? num(s, "data-span-days") : 0;
    if (spanDays <= 0) return null;
    var titleW = num(s, "data-title-w");
    var goalW = num(s, "data-goal-w");
    var usable = scroll.clientWidth - titleW - goalW - 56;
    if (usable < 120) usable = 120;
    return usable / spanDays;
  }

  function currentPpd() {
    var s = svg();
    return s ? num(s, "data-ppd") || DEFAULT_PPD : DEFAULT_PPD;
  }

  function updatePct() {
    var el = document.getElementById("graph-zoom-pct");
    if (!el) return;
    var base = fitPpd() || DEFAULT_PPD;
    el.textContent = Math.round((currentPpd() / base) * 100) + "%";
  }

  // Fraction of the time span currently under the viewport center.
  function centerFraction() {
    var s = svg();
    if (!s) return 0.5;
    var x1 = num(s, "data-axis-x1");
    var x2 = num(s, "data-axis-x2");
    var spanPx = x2 - x1;
    if (spanPx <= 0) return 0;
    var centerX = scroll.scrollLeft + scroll.clientWidth / 2;
    var f = (centerX - x1) / spanPx;
    return Math.max(0, Math.min(1, f));
  }

  // Re-render the SVG at a new px-per-day (server keeps geometry honest),
  // holding the timestamp under the viewport center fixed across the swap.
  function zoomTo(ppd) {
    var frac = centerFraction();
    var params = new URLSearchParams(location.search);
    params.set("ppd", String(Math.round(ppd)));
    fetch("/?" + params.toString(), { headers: { "X-Requested-With": "fetch" } })
      .then(function (r) { return r.text(); })
      .then(function (html) {
        var doc = new DOMParser().parseFromString(html, "text/html");
        var fresh = doc.getElementById("tl-svg");
        var old = svg();
        if (!fresh || !old) return;
        old.parentNode.replaceChild(fresh, old);
        var x1 = num(fresh, "data-axis-x1");
        var x2 = num(fresh, "data-axis-x2");
        scroll.scrollLeft = x1 + frac * (x2 - x1) - scroll.clientWidth / 2;
        applyEdgeFilter();
        applyHighlight();
        updatePct();
      })
      .catch(function () {});
  }

  function nudgeZoom(factor) {
    pendingPpd = (pendingPpd || currentPpd()) * factor;
    if (zoomTimer) clearTimeout(zoomTimer);
    zoomTimer = setTimeout(function () {
      var target = pendingPpd;
      pendingPpd = null;
      zoomTo(target);
    }, 130);
  }

  if (zoomBar) {
    zoomBar.addEventListener("click", function (e) {
      var act = e.target.getAttribute && e.target.getAttribute("data-zoom");
      if (act === "in") nudgeZoom(1.6);
      else if (act === "out") nudgeZoom(1 / 1.6);
      else if (act === "fit") { var f = fitPpd(); if (f) zoomTo(f); }
    });
  }

  scroll.addEventListener(
    "wheel",
    function (e) {
      if (e.ctrlKey || e.metaKey) {
        e.preventDefault();
        nudgeZoom(e.deltaY > 0 ? 1 / 1.12 : 1.12);
      } else if (e.deltaY && !e.shiftKey) {
        // plain vertical wheel pans the timeline horizontally (plurk-style)
        scroll.scrollLeft += e.deltaY;
      }
    },
    { passive: false }
  );

  // ── search input + reset ────────────────────────────────────────────────
  if (search) {
    search.addEventListener("input", function () {
      query = search.value;
      applyHighlight();
      updateReset();
    });
  }
  [].forEach.call(document.querySelectorAll(".edge-toggle"), function (cb) {
    cb.addEventListener("change", applyEdgeFilter);
  });
  var resetLink = document.getElementById("graph-reset");
  function updateReset() {
    if (resetLink) resetLink.hidden = !(query || selectedId);
  }
  if (resetLink) {
    resetLink.addEventListener("click", function (e) {
      e.preventDefault();
      query = "";
      if (search) search.value = "";
      closePanel();
    });
  }

  // ── toasts ────────────────────────────────────────────────────────────
  [].forEach.call(document.querySelectorAll("[data-toast]"), function (t) {
    var timer = setTimeout(function () { t.remove(); }, 4000);
    var close = t.querySelector(".toast-close");
    if (close) close.addEventListener("click", function () { clearTimeout(timer); t.remove(); });
  });

  function esc(s) {
    return String(s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;")
      .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }
  function toggle(el, cls, on) {
    if (on) el.classList.add(cls);
    else el.classList.remove(cls);
  }

  // initial state: apply edge defaults, then land fit-to-width.
  applyEdgeFilter();
  var fit = fitPpd();
  if (fit && Math.abs(fit - currentPpd()) / currentPpd() > 0.1) {
    zoomTo(fit);
  } else {
    updatePct();
  }
})();
