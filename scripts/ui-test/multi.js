// Several runners for one repository: add a heavy runner, then two more
// light ones right after each other (the second Add is clicked while the
// first still runs), stop/start/logs/labels on one of them, remove one and
// check the others never stopped. It registers real runners: use a test
// repository and a throwaway --home.
// Usage: node multi.js <unlock-url> <outdir> <owner/repo>
const { open, sleep, log } = require('./lib');

const [url, out, repo] = process.argv.slice(2);

(async () => {
  const t = await open(url, out, 'multi');
  const { page } = t;
  const runners = async () => (await t.get('/api/state')).runners.filter((r) => r.scope.toLowerCase() === repo.toLowerCase());
  const byId = async (id) => (await runners()).find((r) => r.id === id);
  const onGitHub = async () => {
    const d = await t.post('/api/repo-runners', { repos: [repo], refresh: true });
    return d.runners[repo] || [];
  };
  // watch records any moment a runner that should keep running is not.
  const hiccups = [];
  let watched = [];
  const watcher = setInterval(async () => {
    try {
      for (const r of await runners()) if (watched.includes(r.id) && r.state !== 'running') hiccups.push(r.name + ' was ' + r.state);
    } catch { /* page busy */ }
  }, 1000);

  // 1. The first runner, labelled heavy.
  await t.showRepo(repo);
  await t.addRunner(repo, ['heavy']);
  const first = await t.waitFor('first runner running', async () => { const x = (await runners())[0]; return x && x.state === 'running' ? x : null; }, 600);
  log('first:', first.id, first.name, first.labels.join(','));
  watched = [first.id];
  await sleep(1500);
  await t.shot('1-one-runner');

  // 2. Two more, light, without waiting in between: the second is queued
  // behind the first. One gets the next free name, one a picked name.
  await t.addRunner(repo, ['light']);
  await sleep(300);
  const rowNow = await (await t.repoRow(repo)).evaluate((e) => e.innerText.replace(/\s+/g, ' '));
  log('row right after the second Add:', rowNow);
  await t.addRunner(repo, ['light'], 'e2e-light-b');
  await sleep(500);
  await t.shot('2-adding-two');
  const all = await t.waitFor('three runners running', async () => {
    const rs = await runners();
    return rs.length === 3 && rs.every((r) => r.state === 'running') ? rs : null;
  }, 900);
  for (const r of all) log('runner', r.id, '|', r.name, '|', r.labels.join(','), '|', r.dir);
  const names = all.map((r) => r.name.toLowerCase());
  if (new Set(names).size !== 3) throw new Error('names not unique: ' + names);
  if (new Set(all.map((r) => r.dir)).size !== 3) throw new Error('folders not unique');
  const [auto, picked] = [all.find((r) => r.id !== first.id && r.name !== 'e2e-light-b'), all.find((r) => r.name === 'e2e-light-b')];
  if (!auto || !picked) throw new Error('missing the auto-named or the picked runner');
  if (auto.name.toLowerCase() !== first.name.toLowerCase() + '-2') throw new Error('auto name ' + auto.name + ', want ' + first.name + '-2');
  if ((await byId(first.id)).dir !== first.dir) throw new Error('first runner moved');
  watched = all.map((r) => r.id);
  const gh = await t.waitFor('all three online on GitHub', async () => {
    const g = await onGitHub();
    return names.every((n) => g.some((x) => x.name.toLowerCase() === n && x.status === 'online')) ? g : null;
  }, 120);
  for (const x of gh) log('GitHub:', x.name, x.status, x.labels.join(','));
  await t.showRepo(repo);
  await sleep(1500);
  await t.shot('3-three-rows');
  await t.tab('machine');
  await sleep(1000);
  await t.shot('4-machine');

  // 3. Stop and start the auto-named one from its own row.
  await t.showRepo(repo);
  watched = [first.id, picked.id];
  await t.click(await t.runnerRow(auto.id), 'Stop');
  await t.waitFor(auto.name + ' stopped', async () => (await byId(auto.id)).state === 'stopped', 60);
  await sleep(800);
  await t.shot('5-one-stopped');
  await t.click(await t.runnerRow(auto.id), 'Start');
  await t.waitFor(auto.name + ' running again', async () => (await byId(auto.id)).state === 'running', 120);
  watched = all.map((r) => r.id);

  // 4. Its logs.
  await t.click(await t.runnerRow(auto.id), 'Logs');
  await sleep(2500);
  const title = await page.$eval('#dialog .dlg-head', (e) => e.innerText);
  const text = await page.$eval('#dialog pre.log', (e) => e.textContent);
  log('log dialog:', title.trim().split('\n')[0], '| lines:', text.split('\n').length);
  if (!title.includes(auto.name)) throw new Error('log title does not name the runner: ' + title);
  await t.shot('6-logs');
  await page.click('#dialog .dlg-head button');
  await sleep(300);

  // 5. Edit the labels of the picked one: add medium.
  await t.click(await t.runnerRow(picked.id), 'Edit labels');
  await t.waitFor('labels dialog', () => page.$('#dialog input[aria-label="New label"]'), 30);
  await page.type('#dialog input[aria-label="New label"]', 'medium');
  await page.keyboard.press('Enter');
  await t.shot('7-edit-labels');
  await t.click('#dialog .dlg-foot', 'Save labels');
  await t.waitFor('GitHub has medium on ' + picked.name, async () => (await onGitHub()).some((x) => x.name === picked.name && x.labels.includes('medium')), 30);

  // 6. Remove the auto-named one: the others keep running.
  watched = [first.id, picked.id];
  await t.showRepo(repo);
  await t.click(await t.runnerRow(auto.id), 'Remove');
  await sleep(500);
  const confirm = await page.$eval('#dialog', (e) => e.innerText.replace(/\s+/g, ' '));
  log('confirm:', confirm);
  if (!confirm.includes('keep running')) throw new Error('confirm does not say the others keep running');
  await t.shot('8-confirm-remove');
  await t.click('#dialog .dlg-foot', 'Remove');
  await t.waitFor(auto.name + ' removed', async () => !(await byId(auto.id)), 180);
  await t.waitFor(auto.name + ' gone from GitHub', async () => !(await onGitHub()).some((x) => x.name === auto.name), 60);
  const left = await runners();
  log('left:', left.map((r) => r.name + ' ' + r.state).join(', '));
  if (left.length !== 2 || !left.every((r) => r.state === 'running')) throw new Error('the other runners are not both running');
  await t.showRepo(repo);
  await sleep(1500);
  await t.shot('9-after-remove');

  clearInterval(watcher);
  if (hiccups.length) throw new Error('runners that should keep running did not: ' + [...new Set(hiccups)].join('; '));
  log('the runners that should keep running never stopped');

  // Clean up: remove the rest.
  for (const r of left) await t.post(`/api/runners/${encodeURIComponent(r.id)}/remove`);
  await t.waitFor('all removed', async () => !(await runners()).length, 300);
  await t.waitFor('none left on GitHub', async () => !(await onGitHub()).length, 60);
  for (const k of await t.get('/api/tasks')) log('task', k.id, k.state, '-', k.title, k.error ? '(' + k.error + ')' : '');
  await t.done();
})().catch((e) => { console.error('FAILED:', e.message); process.exit(1); });
