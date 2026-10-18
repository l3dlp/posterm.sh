// Render the X cards: node docs/x/render.cjs "$PWD/docs/x" (needs playwright; CHROME=/path/to/chrome to use a system Chrome)
const { chromium } = require(process.env.PLAYWRIGHT || 'playwright');
const dir = process.argv[2];
(async () => {
  const b = await chromium.launch({ executablePath: process.env.CHROME || undefined, args: ['--no-sandbox', '--allow-file-access-from-files'] });
  const p = await b.newPage({ viewport: { width: 1600, height: 900 }, deviceScaleFactor: 1.5 });
  for (const n of ['x-01-wall', 'x-02-states', 'x-03-lab', 'x-04-arch']) {
    await p.goto('file://' + dir + '/' + n + '.html');
    await p.waitForTimeout(300);
    await p.screenshot({ path: dir + '/' + n + '.png' });
  }
  await b.close();
})();
