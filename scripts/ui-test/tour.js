// Opens the dashboard headless, visits every tab, saves a screenshot of
// each and fails on console errors.
// Usage: node tour.js <unlock-url> <outdir> [width]
const { open, sleep, log } = require('./lib');

(async () => {
  const [url, out, width] = process.argv.slice(2);
  const t = await open(url, out, String(width || 1280), Number(width || 1280));
  for (const tab of ['repos', 'machine', 'automation', 'activity', 'settings', 'health']) {
    await t.tab(tab);
    await sleep(tab === 'repos' || tab === 'health' ? 3500 : 600);
    const file = await t.shot(tab);
    const text = await t.page.$eval('#tab-' + tab, (el) => el.innerText.slice(0, 200).replace(/\s+/g, ' '));
    log(`[${tab}] ${file}: ${text}`);
  }
  await t.done();
})().catch((e) => { console.error('FAILED:', e.message); process.exit(1); });
