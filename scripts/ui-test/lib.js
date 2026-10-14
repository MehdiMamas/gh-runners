// Shared helpers for the dashboard UI tests (headless Chrome via
// puppeteer-core). Set CHROME to the browser binary if it is not in the
// usual place.
const puppeteer = require('puppeteer-core');
const fs = require('fs');
const path = require('path');

function chromePath() {
  if (process.env.CHROME) return process.env.CHROME;
  const candidates = {
    win32: ['C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe', 'C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe'],
    darwin: ['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'],
    linux: ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser'],
  }[process.platform] || [];
  const found = candidates.find((p) => fs.existsSync(p));
  if (!found) throw new Error('Chrome not found: set CHROME to its path');
  return found;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const log = (...a) => console.log(new Date().toISOString().slice(11, 19), ...a);

// open launches Chrome, opens the unlock link and returns helpers bound to
// the page. prefix names the screenshots.
async function open(url, out, prefix, width) {
  const browser = await puppeteer.launch({ executablePath: chromePath(), headless: true, args: ['--no-first-run'] });
  const page = await browser.newPage();
  await page.setViewport({ width: width || 1280, height: 900 });
  const errors = [];
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('pageerror', (e) => errors.push('pageerror: ' + e.message));
  await page.goto(url, { waitUntil: 'networkidle0' });
  log('landed on', page.url().replace(/k=[^&]+/, 'k=***'));

  const t = {
    browser, page, errors,
    shot: async (n) => { const f = path.join(out, prefix + '-' + n + '.png'); await page.screenshot({ path: f }); return f; },
    get: (p) => page.evaluate(async (p) => (await fetch(p, { headers: { 'X-GHR': '1' } })).json(), p),
    post: (p, body) => page.evaluate(async (p, body) => (await fetch(p, { method: 'POST', headers: { 'X-GHR': '1', 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) })).json(), p, body),
    async waitFor(what, fn, secs) {
      for (let i = 0; i < secs * 2; i++) {
        const v = await fn();
        if (v) { log('ok:', what); return v; }
        await sleep(500);
      }
      throw new Error('timed out waiting for ' + what);
    },
    async tab(name) { await page.click(`#tabs button[data-tab="${name}"]`); await sleep(800); },
    // button returns the button with exactly this text inside the first
    // element matching scope (a selector or an element handle), or null.
    async button(scope, text) {
      const root = typeof scope === 'string' ? page : scope;
      const handles = await root.$$((typeof scope === 'string' ? scope + ' ' : '') + 'button');
      for (const b of handles) if ((await b.evaluate((e) => e.textContent.trim())) === text) return b;
      return null;
    },
    async click(scope, text) {
      const b = await t.button(scope, text);
      if (!b) throw new Error(`no button "${text}" in ${typeof scope === 'string' ? scope : 'element'}`);
      await b.click();
    },
    // showRepo filters the Repositories tab down to one repository.
    async showRepo(full) {
      await t.tab('repos');
      await t.waitFor('repo list', () => page.$('#repo-q'), 60);
      await page.$eval('#tab-repos select:nth-of-type(2)', (e) => { e.value = 'all'; e.dispatchEvent(new Event('change')); });
      await page.$eval('#repo-q', (e) => { e.value = ''; });
      await page.type('#repo-q', full.split('/')[1]);
      await sleep(400);
    },
    // repoRow is the row of a repository on the Repositories tab.
    repoRow: (full) => page.evaluateHandle((full) => document.querySelector(`#tab-repos .item[data-repo="${CSS.escape(full)}"]`), full).then((h) => h.asElement()),
    // runnerRow is the row of one local runner under its repository.
    runnerRow: (id) => page.$(`#tab-repos .item.sub[data-runner="${id}"]`),
    // addRunner clicks Add runner on a repository, sets labels (and a name
    // if given) in the dialog and confirms.
    async addRunner(full, labels, name) {
      const row = await t.waitFor('row of ' + full, () => t.repoRow(full), 30);
      await t.click(row, 'Add runner');
      await t.waitFor('labels dialog', () => page.evaluate(() => /Labels of the new runner/.test(document.querySelector('#dialog')?.innerText || '')), 30);
      // Start from no labels: the dialog preselects what workflows ask for.
      for (;;) {
        const x = await page.$('#dialog .stack .chip.custom button.x');
        if (!x) break;
        await x.click();
      }
      if (labels.length) {
        await page.type('#dialog input[aria-label="New label"]', labels.join(', '));
        await page.keyboard.press('Enter');
      }
      if (name) await page.type('#dialog input[aria-label="Runner name"]', name);
      await sleep(200);
      await t.click('#dialog .dlg-foot', 'Add runner');
    },
    async done() {
      log('console errors:', errors.length ? '\n' + errors.join('\n') : 'none');
      await browser.close();
      if (errors.length) throw new Error(errors.length + ' console errors');
    },
  };
  return t;
}

module.exports = { open, sleep, log, chromePath };
