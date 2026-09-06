// Hero ReAct typewriter. Static text stays in the DOM for no-JS / reduced motion.
(function () {
  const line = document.querySelector('.react-line');
  const out = line && line.querySelector('.rl-type');
  if (!out) return;
  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

  const phrase = 'think → act → answer';
  const pauses = { 5: 280, 11: 280 }; // after "think", after "act"
  const charMs = 70;
  const holdMs = 1600;
  const gapMs = 400;

  function wait(ms) {
    return new Promise((r) => setTimeout(r, ms));
  }

  async function type() {
    line.classList.add('is-typing');
    out.textContent = '';
    for (let i = 0; i < phrase.length; i++) {
      out.textContent = phrase.slice(0, i + 1);
      await wait(charMs);
      if (pauses[i + 1]) await wait(pauses[i + 1]);
    }
    line.classList.remove('is-typing');
    await wait(holdMs);
    out.textContent = '';
    await wait(gapMs);
    type();
  }

  type();
})();

// Current-step rail.
(function () {
  const steps = Array.from(document.querySelectorAll('.step[id]'));
  const toc = Array.from(document.querySelectorAll('.toc a[href^="#"]'));
  if (!steps.length) return;

  function setCurrent(id) {
    steps.forEach((s) => s.classList.toggle('is-current', s.id === id));
    toc.forEach((a) => {
      const on = a.getAttribute('href') === '#' + id;
      if (on) a.setAttribute('aria-current', 'location');
      else a.removeAttribute('aria-current');
    });
  }

  const fromHash = location.hash.replace('#', '');
  if (steps.some((s) => s.id === fromHash)) setCurrent(fromHash);

  window.addEventListener('hashchange', () => {
    const id = location.hash.replace('#', '');
    if (steps.some((s) => s.id === id)) setCurrent(id);
  });

  if (!('IntersectionObserver' in window)) return;

  const io = new IntersectionObserver((entries) => {
    const hit = entries
      .filter((e) => e.isIntersecting)
      .sort((a, b) => b.intersectionRatio - a.intersectionRatio)[0];
    if (hit) setCurrent(hit.target.id);
  }, { rootMargin: '-20% 0px -60% 0px', threshold: 0 });

  steps.forEach((s) => io.observe(s));
})();
