// Records the frames of the README demo: starts ./gh-runners with a throwaway
// home, opens the dashboard in headless Chrome and answers the calls that need
// GitHub with made-up data (account acme-dev, repositories of acme, machines
// build-mac and build-pc). Nothing leaves this computer.
// Usage: node demo.js <outdir>          (build the binary first: go build)
// Then, with ffmpeg: frames -> ../../docs/images/demo.gif (see GIF below).
const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');
const { open, sleep, log } = require('./lib');

const root = path.resolve(__dirname, '..', '..');
const out = path.resolve(process.argv[2] || path.join(os.tmpdir(), 'gh-runners-demo'));
fs.mkdirSync(out, { recursive: true });

const bin = path.join(root, process.platform === 'win32' ? 'gh-runners.exe' : 'gh-runners');
const home = fs.mkdtempSync(path.join(os.tmpdir(), 'ghr-demo-'));
const env = Object.assign({}, process.env, { HOME: home, USERPROFILE: home, GH_RUNNERS_HOME: path.join(home, '.gh-runners') });

const ago = (min) => new Date(Date.now() - min * 60000).toISOString();
const repos = [
  ['web-app', 'Storefront and checkout', true],
  ['api-server', 'REST API behind the storefront', true],
  ['mobile-app', 'iOS and Android client', true],
  ['docs-site', 'Documentation website', false],
  ['infra', 'Terraform for the cloud setup', true],
].map(([name, description, priv], i) => ({
  full_name: 'acme/' + name, owner: 'acme', owner_type: 'Organization', name, description,
  url: 'https://github.com/acme/' + name, private: priv, archived: false, fork: false, admin: true,
  pushed_at: ago(40 + i * 300), sync_match: priv, sync_reason: priv ? '' : 'public',
}));

let runners = [
  { id: 'r1', scope: 'acme/web-app', kind: 'repo', owner: 'acme', repo: 'web-app', name: 'build-mac', labels: ['gh-runners', 'build-mac'], disabled: false, configured: true, state: 'running', restarts: 0, since: '2 hours ago', version: '2.321.0', dir: '/Users/dev/gh-runners/acme-web-app', created: '2 hours ago' },
  { id: 'r2', scope: 'acme/api-server', kind: 'repo', owner: 'acme', repo: 'api-server', name: 'build-mac', labels: ['gh-runners', 'build-mac'], disabled: false, configured: true, state: 'running', restarts: 0, since: '2 hours ago', version: '2.321.0', dir: '/Users/dev/gh-runners/acme-api-server', created: '2 hours ago' },
];
const remote = { r1: 'online', r2: 'busy' };
const peerRunner = { id: 'p1', scope: 'acme/web-app', name: 'build-pc', state: 'running', labels: ['self-hosted', 'Windows', 'X64', 'gh-runners', 'build-pc'] };
const machines = {
  main_host: false, listening: false, port: 0, account: true, allow_control: true, login: 'acme-dev',
  machines: [{
    id: 'pc1', online: true, peer: true, added: ago(9000), last_seen: ago(0),
    info: { hostname: 'build-pc', os: 'windows', arch: 'amd64', version: '1.0.0', runners: [peerRunner], login: 'acme-dev' },
  }],
};

function patchState(s) {
  Object.assign(s, {
    hostname: 'build-mac', os: 'darwin', arch: 'arm64', home: '/Users/dev', runners_dir: '/Users/dev/gh-runners', default_runners_dir: '/Users/dev/gh-runners',
    service: { installed: true, where: 'launchd' }, machine_labels: ['self-hosted', 'macOS', 'ARM64', 'gh-runners', 'build-mac'], version: '1.0.0', runners, tasks_busy: 0,
    auth: { signed_in: true, login: 'acme-dev', name: 'Acme Dev', saved: true, can_device: true, source: 'gh-runners' },
  });
  return s;
}

async function main() {
  const r = spawnSync(bin, ['ui', '--no-browser'], { env, encoding: 'utf8' });
  const url = (r.stdout || '').split('\n').map((l) => l.trim()).find((l) => l.startsWith('http://'));
  if (!url) throw new Error('no dashboard link: ' + r.stdout + r.stderr);
  log('dashboard up on a throwaway home');
  let t;
  try {
    t = await open(url, out, 'demo');
    const { page } = t;
    await page.setRequestInterception(true);
    page.on('request', (req) => {
      const u = new URL(req.url());
      const p = u.pathname;
      const json = (o) => req.respond({ status: 200, contentType: 'application/json', body: JSON.stringify(o) });
      if (req.method() === 'GET' && p === '/api/repos') return json({ me: 'acme-dev', repos: repos.map((x) => Object.assign({ runner_ids: runners.filter((r) => r.scope === x.full_name).map((r) => r.id) }, x)), fetched: 'just now' });
      if (req.method() === 'GET' && p === '/api/github-runners') return json(remote);
      if (req.method() === 'GET' && p === '/api/machines') return json(machines);
      if (req.method() === 'GET' && /^\/api\/machines\/.+\/api\/github-runners$/.test(p)) return json({ p1: 'online' });
      if (req.method() === 'GET' && p === '/api/workflow-labels') return json({ labels: [], found: false });
      if (req.method() === 'POST' && p === '/api/add') {
        runners = runners.concat([{ id: 'r3', scope: 'acme/mobile-app', kind: 'repo', owner: 'acme', repo: 'mobile-app', name: 'build-mac', labels: ['gh-runners', 'build-mac'], disabled: false, configured: true, state: 'running', restarts: 0, since: 'just now', version: '2.321.0', dir: '/Users/dev/gh-runners/acme-mobile-app', created: 'just now' }]);
        remote.r3 = 'online';
        return json({ task: 't1' });
      }
      if (req.method() === 'GET' && p === '/api/state') {
        return fetch(req.url(), { headers: req.headers() }).then((x) => x.json()).then((s) => json(patchState(s))).catch(() => req.continue());
      }
      return req.continue();
    });
    await page.reload({ waitUntil: 'networkidle0' });
    await t.tab('repos');
    await t.waitFor('repo list', () => page.$('#repo-q'), 30);
    await sleep(1500);
    await t.shot('1-repos');

    const row = await t.waitFor('row of acme/mobile-app', () => t.repoRow('acme/mobile-app'), 30);
    await t.click(row, 'Add runner');
    await t.waitFor('labels dialog', () => page.evaluate(() => /Labels of the new runner/.test(document.querySelector('#dialog')?.innerText || '')), 30);
    await sleep(500);
    await t.shot('2-add-dialog');
    await t.click('#dialog .dlg-foot', 'Add runner');
    await sleep(3000);
    await t.click('#tab-repos', 'Refresh');
    await sleep(4000);
    await t.shot('3-added');

    await t.tab('machine');
    await sleep(2500);
    await t.shot('4-machine');

    await t.tab('machines');
    await sleep(2500);
    await t.shot('5-machines');

    await t.tab('automation');
    await sleep(1000);
    await t.shot('6-automation');
  } finally {
    if (t) await t.browser.close();
    spawnSync(bin, ['pause'], { env, encoding: 'utf8' });
    spawnSync(bin, ['daemon', '--stop'], { env, encoding: 'utf8' });
    log('frames in', out);
  }
}

main().catch((e) => { console.error('FAILED:', e.message); process.exit(1); });
