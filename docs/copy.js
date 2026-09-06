// Progressive enhancement: copy buttons read from the adjacent <pre>.
document.documentElement.classList.add('js');

document.querySelectorAll('[data-copy]').forEach((btn) => {
  btn.addEventListener('click', async () => {
    const block = btn.closest('.code-block');
    const pre = block && block.querySelector('pre');
    const text = pre ? pre.textContent : '';
    if (!text) return;
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(text);
      } else {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.setAttribute('readonly', '');
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
      const prev = btn.textContent;
      btn.textContent = 'copied';
      btn.classList.add('copied');
      if (block) {
        block.classList.add('just-copied');
        setTimeout(() => block.classList.remove('just-copied'), 600);
        const hint = block.nextElementSibling;
        if (hint && hint.classList.contains('copy-hint')) hint.hidden = false;
      }
      setTimeout(() => {
        btn.textContent = prev;
        btn.classList.remove('copied');
      }, 1600);
    } catch (_) { /* leave the <pre> selectable */ }
  });
});
