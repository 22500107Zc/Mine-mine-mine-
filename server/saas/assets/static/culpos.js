// CulpOS — confirmation prompts for destructive actions (CSP-safe, no inline handlers)
document.addEventListener('submit', function (e) {
  var f = e.target
  if (f && f.dataset && f.dataset.confirm && !window.confirm(f.dataset.confirm)) {
    e.preventDefault()
  }
}, true)
