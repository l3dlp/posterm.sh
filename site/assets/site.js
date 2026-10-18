// posterm.sh: the only script. Copies the install line; the page works without it.
document.querySelectorAll('[data-copy]').forEach(btn => {
  const src = btn.parentElement.querySelector('[data-copy-src]');
  const label = btn.textContent;
  btn.addEventListener('click', async () => {
    const text = src.textContent.trim();
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const r = document.createRange(); // no clipboard API (plain http): select it instead
      r.selectNodeContents(src);
      const s = getSelection(); s.removeAllRanges(); s.addRange(r);
      document.execCommand('copy');
    }
    btn.textContent = btn.dataset.done;
    btn.classList.add('done');
    setTimeout(() => { btn.textContent = label; btn.classList.remove('done'); }, 1600);
  });
});
