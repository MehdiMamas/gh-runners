// Pause and resume: this machine's own button, the main host's own card and
// a member's card on the Machines tab. Needs a main host with one machine
// joined; nothing runs a job, so no question is asked.
// Usage: node pause.js <host-unlock-url> <outdir> <member-unlock-url> [width]
const { open, sleep, log } = require('./lib');

(async () => {
  const [url, out, memberUrl, width] = process.argv.slice(2);
  const w = Number(width || 1280);
  const t = await open(url, out, 'pause-' + w, w);
  const { page } = t;
  const text = () => page.evaluate(() => document.body.innerText);
  // Start from a clean slate.
  await t.post("/api/resume");
  await t.post("/api/machines/" + (await t.get("/api/machines")).machines[0].id + "/api/resume");
  await sleep(2000);
  const state = async () => (await t.get('/api/state')).paused;

  // The host's own dashboard: the button at the top of the main view.
  await t.tab('machine');
  await t.click('body', 'Pause this machine');
  await t.waitFor("host paused", state, 30);
  await sleep(6000); // the toast covers the header for a few seconds
  if (!/Paused/.test(await text())) throw new Error('no Paused pill on the host');
  log('[host paused] ' + await t.shot('own-paused'));

  // Its Machines tab: its own card and the member's.
  await t.tab('machines');
  await sleep(1500);
  log('[machines] ' + await t.shot('machines-host-paused'));
  const cards = await page.$$('#tab-machines .card');
  let member = null;
  for (const c of cards) if (await t.button(c, 'Remove machine')) member = c;
  if (!member) throw new Error('no member card with Pause machine');
  await t.click(member, 'Pause machine');
  await t.waitFor("member shows Resume machine", async () => !!(await t.button(await page.$$("#tab-machines .card").then((l) => l[l.length - 1]), "Resume machine")), 30);
  await sleep(1500);
  log('[member paused] ' + await t.shot('machines-member-paused'));
  const m = await t.get('/api/machines');
  if (!m.machines[0].info.paused) throw new Error('the member did not report paused');

  // Resume everything.
  const lastCard = async () => (await page.$$("#tab-machines .card")).pop();
  await t.click(await lastCard(), "Resume machine");
  await t.waitFor("member resumed", async () => !(await t.get("/api/machines")).machines[0].info.paused, 30);
  await sleep(1500);
  await t.tab("machine");
  await sleep(6000);
  await t.click('body', 'Resume');
  await t.waitFor('host resumed', async () => !(await state()), 30);
  log('[resumed] ' + await t.shot('resumed'));

  // A member's own dashboard works on its own.
  const mt = await open(memberUrl, out, 'pause-member-' + w, w);
  await mt.tab('machine');
  await mt.click('body', 'Pause this machine');
  await mt.waitFor("member paused", async () => (await mt.get("/api/state")).paused, 30);
  await sleep(6000);
  log('[member own] ' + await mt.shot('own-paused'));
  await mt.click('body', 'Resume');
  await mt.waitFor('member resumed', async () => !(await mt.get('/api/state')).paused, 30);
  await mt.done();
  await t.done();
})().catch((e) => { console.error('FAILED:', e.message); process.exit(1); });
