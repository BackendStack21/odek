// All screenshots remain available when JavaScript is disabled.
(() => {
  const controls = document.querySelector('.gallery-controls');
  if (!controls) return;
  const buttons = Array.from(controls.querySelectorAll('[data-view]'));
  const views = Array.from(document.querySelectorAll('[data-gallery-view]'));
  function select(name) {
    buttons.forEach(button => button.setAttribute('aria-pressed', String(button.dataset.view === name)));
    views.forEach(view => { view.hidden = view.dataset.galleryView !== name; });
  }
  buttons.forEach(button => button.addEventListener('click', () => select(button.dataset.view)));
  select('results');
  controls.hidden = false;
})();
