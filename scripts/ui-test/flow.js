// Drives the dashboard like a person: add a runner to a repo, stop/start it,
// open its logs, move the runners folder, remove it. It registers a real
// runner: use a test repository and a throwaway --home.
// Usage: node flow.js <unlock-url> <outdir> <owner/repo>
const path = require('path');
const { open, sleep, log } = require('./lib');

const [url, out, repo] = process.argv.slice(2);

(async () => {
  const t = await open(url, out, 'flow');
  const { page } = t;
  const mine = async () => (await t.get('/api/state')).runners.find((r) => r.scope.toLowerCase() === repo.toLowerCase());

  // 1. Add from the Repositories tab.
  await t.showRepo(repo);
  await t.addRunner(repo, []);
  log('clicked Add runner');
  await sleep(1000);
  await t.shot('1-adding');
  let r = await t.waitFor('runner running', async () => { const x = await mine(); return x && x.state === 'running' ? x : null; }, 600);
  log('runner', r.id, 'dir', r.dir);

  // 2. This machine shows it, GitHub sees it online.
  await t.tab('machine');
  await t.waitFor('GitHub says online', () => page.evaluate(() => /online/i.test(document.querySelector('#tab-machine .card.runner')?.innerText || '')), 90);
  await t.shot('2-machine');

  // 3. Stop, then start.
  await t.click('#tab-machine .card.runner', 'Stop');
  await t.waitFor('stopped', async () => (await mine()).state === 'stopped', 60);
  await sleep(500);
  await t.shot('3-stopped');
  await t.click('#tab-machine .card.runner', 'Start');
  await t.waitFor('running again', async () => (await mine()).state === 'running', 120);

  // 4. Logs dialog.
  await t.click('#tab-machine .card.runner', 'Logs');
  await sleep(3000);
  const logText = await page.$eval('#dialog pre.log', (e) => e.textContent);
  log('log lines:', logText.split('\n').length, '| last:', logText.trim().split('\n').pop());
  await t.shot('4-logs');
  await page.click('#dialog .dlg-head button');
  await sleep(300);

  // 5. Move the runners folder with the picker.
  const before = (await t.get('/api/state')).runners_dir;
  await t.tab('settings');
  await t.click('#tab-settings', 'Change ...');
  await sleep(1000);
  const parent = path.dirname(before);
  await page.$eval('#dialog .picker-path input', (e, v) => { e.value = v; }, parent);
  await t.click('#dialog .picker-path', 'Go');
  await sleep(800);
  await t.click('#dialog', 'New folder');
  await page.type('#dialog .folders input', 'moved-runners');
  await t.click('#dialog .folders', 'Create');
  await sleep(1000);
  await t.shot('5-picker');
  await t.click('#dialog', 'Use this folder');
  const want = path.join(parent, 'moved-runners');
  await t.waitFor('folder moved', async () => (await t.get('/api/state')).runners_dir === want, 120);
  r = await t.waitFor('running in new folder', async () => { const x = await mine(); return x && x.state === 'running' && x.dir.startsWith(want) ? x : null; }, 180);
  log('runner now in', r.dir);
  await t.tab('activity');
  await t.shot('6-activity');

  // 6. Remove.
  await t.tab('machine');
  await t.click('#tab-machine .card.runner', 'Remove');
  await sleep(500);
  await t.shot('7-confirm');
  await t.click('#dialog .dlg-foot', 'Remove');
  await t.waitFor('runner removed', async () => !(await mine()), 180);
  await sleep(1000);
  await t.shot('8-removed');

  // Back to the default folder so the temp home is tidy.
  await t.post('/api/runners-dir', { path: '' });
  await t.waitFor('folder back to default', async () => (await t.get('/api/state')).runners_dir === before, 60);
  for (const k of await t.get('/api/tasks')) log('task', k.id, k.state, '-', k.title, k.error ? '(' + k.error + ')' : '');
  await t.done();
})().catch((e) => { console.error('FAILED:', e.message); process.exit(1); });
