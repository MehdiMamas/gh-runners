// Opens the dashboard headless, visits every tab, saves a screenshot of
// each and fails on console errors.
// Usage: node tour.js <unlock-url> <outdir> [width]
const { open, sleep, log } = require('./lib');

(async () => {
  const [url, out, width] = process.argv.slice(2);
  const t = await open(url, out, String(width || 1280), Number(width || 1280));
  const shown = (tab) => t.page.$eval(`#tabs button[data-tab="${tab}"]`, (b) => b.offsetParent !== null);
  for (const tab of ['repos', 'machine', 'machines', 'automation', 'activity', 'settings', 'health']) {
    if (tab === 'machines' && !await shown(tab)) { log('[machines] hidden: this is not a main host, skipped'); continue; }
    await t.tab(tab);
    await sleep(tab === 'repos' || tab === 'health' ? 3500 : 600);
    const file = await t.shot(tab);
    const text = await t.page.$eval('#tab-' + tab, (el) => el.innerText.slice(0, 200).replace(/\s+/g, ' '));
    log(`[${tab}] ${file}: ${text}`);
  }
  if (await shown('machines')) {
    await t.tab('machines');
    await t.click('#tab-machines', 'Add a machine');
    await t.waitFor('invite dialog', () => t.page.evaluate(() => /Paste one of these|Could not|not listening/i.test(document.querySelector('#dialog')?.innerText || '')), 20);
    log('[add-machine] ' + await t.shot('add-machine'));
    await t.click('#dialog .dlg-foot', 'Done');
  }
  await t.done();
})().catch((e) => { console.error('FAILED:', e.message); process.exit(1); });
