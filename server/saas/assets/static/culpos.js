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
