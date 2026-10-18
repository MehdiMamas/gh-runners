// Manages a runner on another machine from the main host's dashboard: add
// one there (repository picker, then labels), stop it, Run all, remove it.
// It registers a real runner on that machine: use a test repository.
// Usage: node remote.js <unlock-url of the main host> <outdir> <machine id> <owner/repo>
const { open, sleep, log } = require('./lib');

const [url, out, id, repo] = process.argv.slice(2);
const name = 'ghr-remote-test-' + Date.now().toString(36);
let before = null; // runner ids on that machine before the add
let added = null; // the runner the test added

(async () => {
  const t = await open(url, out, 'remote');
  const { page } = t;
  const card = `#tab-machines .machine-card[data-machine="${id}"]`;
  const dialogText = () => page.evaluate(() => (document.querySelector('#dialog[open]')?.innerText || ''));
  const runners = async () => (await t.get(`/api/machines/${id}/api/state`)).runners || [];
  // mine is the runner this test added: named so when the dialog asks for a
  // name (Add another), else the one new id.
  const mine = async () => (await runners()).find((r) => (added ? r.id === added : r.name === name || !before.has(r.id)));
  const row = async () => {
    const r = await mine();
    return r && page.$(`${card} .mrunner[data-runner="${CSS_escape(r.id)}"]`);
  };

  before = new Set((await runners()).map((r) => r.id));
  await t.tab('machines');
  await t.waitFor('card of ' + id, () => page.$(card), 30);
  await t.shot('0-machines');

  // 1. Add runner: pick the repository, then the labels dialog must open
  // and its Add runner must send the add.
  await t.click(card, 'Add runner');
  await t.waitFor('repository picker', async () => /Pick a repository/.test(await dialogText()), 30);
  await page.type('#dialog input[aria-label="Filter repositories"]', repo.split('/')[1]);
  await sleep(300);
  const item = await t.waitFor('row of ' + repo, () => page.$(`#dialog .item.pick[data-repo="${repo}"]`), 30);
  const pick = (await t.button(item, 'Add another')) || (await t.button(item, 'Add runner'));
  await pick.click();
  await sleep(300);
  if (/public repository/.test(await dialogText())) await t.click('#dialog .dlg-foot', 'Add anyway');
  await t.waitFor('labels dialog', async () => /Labels of the new runner/.test(await dialogText()), 30);
  await sleep(500); // the picker's close event has come by now: the labels dialog must still be open
  if (!/Labels of the new runner/.test(await dialogText())) throw new Error('the labels dialog closed on its own');
  for (;;) {
    const x = await page.$('#dialog .stack .chip.custom button.x');
    if (!x) break;
    await x.click();
  }
  if (await page.$('#dialog input[aria-label="Runner name"]')) await page.type('#dialog input[aria-label="Runner name"]', name);
  await t.shot('1-labels');
  await t.click('#dialog .dlg-foot', 'Add runner');
  log('clicked Add runner for', name);
  // The bug this guards: the click did nothing, no error, the dialog stayed.
  await t.waitFor('labels dialog closed by Add runner', async () => !/Labels of the new runner/.test(await dialogText()), 10);
  let r = await t.waitFor('runner running there', async () => { const x = await mine(); return x && x.state === 'running' ? x : null; }, 600);
  added = r.id;
  log('runner', r.id);
  await t.waitFor('its row on the card', row, 60);
  await t.shot('2-added');

  // 2. Stop it, then Run all on the card starts it again.
  await t.click(await row(), 'Stop');
  await t.waitFor('stopped', async () => (await mine()).state === 'stopped', 120);
  await t.waitFor('Run all shown', async () => !!(await t.button(card, 'Run all')), 60);
  await t.click(card, 'Run all');
  await t.waitFor('running again', async () => (await mine()).state === 'running', 120);
  await t.shot('3-run-all');

  // 3. Remove it.
  await t.click(await row(), 'Remove');
  await t.waitFor('remove dialog', async () => /Remove the runner/.test(await dialogText()), 30);
  await t.click('#dialog .dlg-foot', 'Remove');
  await t.waitFor('removed', async () => !(await mine()), 180);
  await t.shot('4-removed');
  await t.done();
  log('PASS');
})().catch((e) => { console.error('FAIL', e.message); process.exit(1); });

function CSS_escape(s) { return s.replace(/["\\]/g, '\\$&'); }
