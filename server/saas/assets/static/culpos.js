// CulpOS — confirmation prompts for destructive actions (CSP-safe, no inline handlers)
document.addEventListener('submit', function (e) {
  var f = e.target
  if (f && f.dataset && f.dataset.confirm && !window.confirm(f.dataset.confirm)) {
    e.preventDefault()
  }
}, true)

// Command Deck: open the activity graph at the most recent weeks
document.addEventListener('DOMContentLoaded', function () {
  document.querySelectorAll('.heat-wrap').forEach(function (el) { el.scrollLeft = el.scrollWidth; });
});

// Scope filters apply as soon as a value changes
document.addEventListener('DOMContentLoaded', function () {
  document.querySelectorAll('form[data-autosubmit]').forEach(function (form) {
    var apply = form.querySelector('[data-apply]');
    if (apply) { apply.hidden = true; }
    form.querySelectorAll('select').forEach(function (s) {
      s.addEventListener('change', function () { form.submit(); });
    });
  });
  // keep the active tab in view on small screens
  document.querySelectorAll('.deck-tabs .active').forEach(function (a) {
    a.scrollIntoView({ block: 'nearest', inline: 'center' });
  });
});

// Command Deck: definitions and activity tooltips (hover, keyboard focus, tap)
(function () {
  var tip = null, owner = null;
  function show(el) {
    var text = el.getAttribute('data-tip');
    if (!text) { return; }
    if (!tip) { tip = document.createElement('div'); tip.className = 'tip'; tip.setAttribute('role', 'tooltip'); document.body.appendChild(tip); }
    tip.textContent = text;
    tip.hidden = false;
    owner = el;
    var r = el.getBoundingClientRect(), w = tip.offsetWidth, h = tip.offsetHeight;
    var x = Math.min(window.innerWidth - w - 8, Math.max(8, r.left + r.width / 2 - w / 2));
    var y = r.top - h - 8 < 8 ? r.bottom + 8 : r.top - h - 8;
    tip.style.left = x + 'px';
    tip.style.top = y + 'px';
  }
  function hide() { if (tip) { tip.hidden = true; } owner = null; }
  document.addEventListener('mouseover', function (e) { var el = e.target.closest && e.target.closest('[data-tip]'); if (el) { show(el); } });
  document.addEventListener('mouseout', function (e) { var el = e.target.closest && e.target.closest('[data-tip]'); if (el && !el.contains(e.relatedTarget)) { hide(); } });
  document.addEventListener('focusin', function (e) { if (e.target.hasAttribute && e.target.hasAttribute('data-tip')) { show(e.target); } });
  document.addEventListener('focusout', hide);
  window.addEventListener('scroll', hide, true);
  // touch: first tap on a non-link shows the tip; on a heat cell the first tap shows it, the second opens the day
  document.addEventListener('touchstart', function (e) {
    var el = e.target.closest && e.target.closest('[data-tip]');
    if (!el) { hide(); return; }
    if (owner !== el) {
      show(el);
      if (el.tagName === 'A') { e.preventDefault(); }
    }
  }, { passive: false });
})();

// Command Deck: record intelligence opens in a drawer (full page without JS)
(function () {
  var drawer = document.getElementById('drawer');
  if (!drawer || !window.fetch) { return; }
  var body = drawer.querySelector('.drawer-body'), full = drawer.querySelector('[data-full]'), last = null;
  function close() {
    drawer.hidden = true;
    document.body.classList.remove('drawer-open');
    if (last) { last.focus(); }
  }
  function open(href, from) {
    last = from || last;
    fetch(href + (href.indexOf('?') < 0 ? '?' : '&') + 'partial=1', { credentials: 'same-origin' })
      .then(function (r) { if (!r.ok) { throw new Error(r.status); } return r.text(); })
      .then(function (html) {
        body.innerHTML = html;
        full.href = href;
        drawer.hidden = false;
        document.body.classList.add('drawer-open');
        body.scrollTop = 0;
        drawer.querySelector('[data-close]').focus();
      })
      .catch(function () { window.location.href = href; });
  }
  document.addEventListener('click', function (e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey) { return; }
    var a = e.target.closest && e.target.closest('a[href^="/command/record/"]');
    if (!a) { return; }
    e.preventDefault();
    open(a.getAttribute('href'), a);
  });
  drawer.querySelector('[data-close]').addEventListener('click', close);
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !drawer.hidden) { close(); } });
})();

// Scope bar: custom dates only for a custom range
document.addEventListener('DOMContentLoaded', function () {
  var range = document.getElementById('s-range');
  if (!range) { return; }
  var dates = range.form.querySelector('.dates');
  range.addEventListener('change', function (e) {
    if (range.value === 'custom') {
      e.stopImmediatePropagation();
      dates.classList.remove('off');
    }
  }, true);
  dates.querySelectorAll('input').forEach(function (i) {
    i.addEventListener('change', function () { if (dates.querySelectorAll('input')[0].value && dates.querySelectorAll('input')[1].value) { range.form.submit(); } });
  });
});
