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
