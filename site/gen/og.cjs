// Render site/gen/og.html to site/og.png: node site/gen/og.cjs (PLAYWRIGHT=path to the package, CHROME=system Chrome, both optional)
const { chromium } = require(process.env.PLAYWRIGHT || 'playwright');
const path = require('path');
(async () => {
  const b = await chromium.launch({ executablePath: process.env.CHROME || undefined, args: ['--no-sandbox'] });
  const p = await b.newPage({ viewport: { width: 1200, height: 630 }, deviceScaleFactor: 1 });
  await p.goto('file://' + path.join(__dirname, 'og.html'));
  await p.waitForTimeout(200);
  await p.screenshot({ path: path.join(__dirname, '..', 'og.png') });
  await b.close();
})();
