'use strict';

// gh-runners dashboard. Plain DOM, no build step. Every string from the
// server goes in through textContent (h() below), never innerHTML.

const S = {
  state: null,
  repos: null, reposErr: '', reposLoading: false,
  remote: {},
  tab: 'repos',
  filter: { q: '', owner: '', show: 'private' },
  picked: new Set(),
  tasks: [], seenDone: new Set(), firstTasks: true,
  offline: false,
};

// ---------- helpers ----------
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'value') el.value = v;
    else if (k === 'checked') el.checked = !!v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat(Infinity)) {
    if (kid === undefined || kid === null || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}
const $ = (id) => document.getElementById(id);
function mount(el, ...kids) { el.replaceChildren(...kids.flat(Infinity).filter(Boolean)); }

async function api(method, path, body) {
  let res;
  try {
    res = await fetch(path, {
      method,
      headers: Object.assign({ 'X-GHR': '1' }, body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      body: body !== undefined ? JSON.stringify(body) : undefined,
      credentials: 'same-origin',
    });
  } catch (e) {
    setOffline(true);
    throw new Error('gh-runners is not answering');
  }
  setOffline(false);
  if (res.status === 401 && path === '/api/state') { location.reload(); }
  let data = null;
  const text = await res.text();
  try { data = text ? JSON.parse(text) : null; } catch { data = { error: text }; }
  if (!res.ok) throw new Error((data && data.error) || res.statusText);
  return data;
}

function toast(msg, bad) {
  const t = h('div', { class: 'toast' + (bad ? ' bad' : '') }, msg);
  $('toasts').append(t);
  setTimeout(() => t.remove(), bad ? 9000 : 4500);
}
async function guard(fn) {
  try { return await fn(); } catch (e) { toast(e.message, true); }
}

function ago(iso) {
  if (!iso) return '';
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  if (s < 0) return 'just now';
  const units = [[60, 'second'], [60, 'minute'], [24, 'hour'], [30, 'day'], [12, 'month'], [Infinity, 'year']];
  let v = s;
  for (const [n, name] of units) {
    if (v < n) { v = Math.floor(v); return v <= 1 && name === 'second' ? 'just now' : `${v} ${name}${v === 1 ? '' : 's'} ago`; }
    v /= n;
  }
  return '';
}
function dur(d) { // Go duration string -> short text
  if (!d) return '';
  return d.replace(/0s$/, '').replace(/0m$/, '').replace(/^(\d+h)0m$/, '$1') || d;
}
function osName(os) { return { darwin: 'macOS', linux: 'Linux', windows: 'Windows' }[os] || os; }
function list(v) { return (v || []).join(', '); }
function split(v) { return v.split(',').map((x) => x.trim()).filter(Boolean); }

async function copy(text) {
  try { await navigator.clipboard.writeText(text); toast('Copied'); } catch { toast('Copy failed: select the text and copy it', true); }
}

function stateClass(s) {
  if (!s) return '';
  if (s === 'running' || s === 'online' || s === 'jit-busy') return 'ok';
  if (s === 'busy') return 'info';
  if (s.startsWith('restarting') || s === 'starting' || s === 'stopping' || s === 'jit-idle') return 'warn';
  if (s === 'not registered' || s === 'error' || s === 'missing' || s === 'no daemon') return 'bad';
  return '';
}
function stateText(s) {
  return { running: 'Running', stopped: 'Stopped', starting: 'Starting', stopping: 'Stopping', online: 'Online on GitHub', offline: 'Offline on GitHub', busy: 'Running a job', missing: 'Not on GitHub', error: 'GitHub unreachable', 'not registered': 'Not registered', 'jit-idle': 'Waiting for its job', 'jit-busy': 'Running a job' }[s] || (s.charAt(0).toUpperCase() + s.slice(1));
}
function pill(s, extra) { return h('span', { class: 'pill ' + stateClass(s) + (extra ? ' ' + extra : '') }, stateText(s)); }

function sw(checked, onchange, label) {
  return h('label', { class: 'switch' }, h('input', { type: 'checkbox', checked, onchange: (e) => onchange(e.target.checked) }), h('span', { class: 'track' }), label);
}

// ---------- dialog ----------
const dlg = $('dialog');
function openDialog({ title, body, foot, wide, onclose }) {
  dlg.className = wide ? 'wide' : '';
  mount(dlg,
    h('div', { class: 'dlg-head' }, h('h2', {}, title), h('button', { class: 'btn ghost small', onclick: () => dlg.close(), 'aria-label': 'Close' }, '✕')),
    h('div', { class: 'dlg-body' }, body),
    foot ? h('div', { class: 'dlg-foot' }, foot) : null);
  dlg.onclose = () => { if (onclose) onclose(); };
  if (!dlg.open) dlg.showModal();
}
function closeDialog() { if (dlg.open) dlg.close(); }

function confirmDialog(title, text, okLabel, danger) {
  return new Promise((resolve) => {
    let answered = false;
    const done = (v) => { answered = true; closeDialog(); resolve(v); };
    openDialog({
      title,
      body: [].concat(text).map((t) => (t instanceof Node ? t : h('p', {}, t))),
      foot: [h('button', { class: 'btn', onclick: () => done(false) }, 'Cancel'),
        h('button', { class: 'btn ' + (danger ? 'danger solid' : 'primary'), onclick: () => done(true) }, okLabel)],
      onclose: () => { if (!answered) resolve(false); },
    });
  });
}

// ---------- state polling ----------
function setOffline(v) {
  if (v === S.offline) return;
  S.offline = v;
  $('offline').classList.toggle('hidden', !v);
}

async function refreshState() {
  try {
    S.state = await api('GET', '/api/state');
  } catch { return; }
  render();
}

async function loadRepos(force) {
  if (!S.state || !S.state.auth.signed_in) return;
  S.reposLoading = true;
  if (S.tab === 'repos') renderRepos();
  try {
    const r = await api('GET', '/api/repos' + (force ? '?refresh=1' : ''));
    S.repos = r; S.reposErr = '';
  } catch (e) { S.reposErr = e.message; }
  S.reposLoading = false;
  renderRepos();
}

async function loadRemote() {
  if (!S.state || !S.state.auth.signed_in || !S.state.runners.length) return;
  try { S.remote = await api('GET', '/api/github-runners'); } catch { /* shown as unknown */ }
  if (S.tab === 'machine') renderMachine();
}

async function loadTasks() {
  try {
    const list = await api('GET', '/api/tasks');
    for (const t of list) {
      const finished = t.state === 'done' || t.state === 'failed';
      if (finished && !S.seenDone.has(t.id)) {
        S.seenDone.add(t.id);
        if (!S.firstTasks) {
          if (t.state === 'done') toast(t.title + ': done');
          else toast(t.title + ': ' + (t.error || 'failed'), true);
          refreshState();
          if (S.repos) loadRepos(false);
        }
      }
    }
    S.firstTasks = false;
    S.tasks = list;
  } catch { return; }
  renderDock();
  if (S.tab === 'activity') renderActivity(false);
}

function render() {
  if (!S.state) return;
  renderHeader();
  renderBanner();
  const n = S.state.runners.length;
  $('count-runners').textContent = n ? String(n) : '';
  if (S.tab === 'repos') renderRepos();
  if (S.tab === 'machine') renderMachine();
  if (S.tab === 'automation' && !document.querySelector('#tab-automation [data-dirty="1"]')) renderAutomation();
  if (S.tab === 'settings' && !document.querySelector('#tab-settings [data-dirty="1"]')) renderSettings();
}

function renderHeader() {
  const st = S.state;
  mount($('machine'),
    h('span', { class: 'host' }, st.hostname),
    h('span', { class: 'pill plain' }, osName(st.os) + ' · ' + st.arch),
    st.service.installed ? h('span', { class: 'pill ok', title: st.service.where }, 'Starts at login') : h('span', { class: 'pill warn' }, 'Not started at login'));
  const a = st.auth;
  if (a.signed_in) {
    mount($('account'),
      a.avatar ? h('img', { class: 'avatar', src: a.avatar + (a.avatar.includes('?') ? '&' : '?') + 's=52', alt: '' }) : null,
      h('span', { class: 'login' }, a.login));
  } else {
    mount($('account'), h('button', { class: 'btn primary', onclick: signIn }, 'Sign in to GitHub'));
  }
}

function renderBanner() {
  const st = S.state;
  const items = [];
  if (st.auth.error && st.auth.saved) {
    items.push(h('div', { class: 'notice bad' }, h('div', { class: 'grow' }, st.auth.error), h('button', { class: 'btn', onclick: signIn }, 'Sign in again')));
  }
  if (!st.service.installed && st.runners.length) {
    items.push(h('div', { class: 'notice' },
      h('div', { class: 'grow' }, h('b', {}, 'Runners stop when you restart this computer. '), 'Turn on "Start at login" to keep them running.'),
      h('button', { class: 'btn primary', onclick: () => serviceAction('install') }, 'Start at login')));
  }
  mount($('banner'), items);
}

// ---------- sign in ----------
function signIn() {
  const can = S.state && S.state.auth.can_device;
  let mode = can ? 'device' : 'token';
  const body = h('div', {});
  const draw = () => {
    const seg = h('div', { class: 'seg' },
      can ? h('button', { class: mode === 'device' ? 'active' : '', onclick: () => { mode = 'device'; draw(); } }, 'Sign in with GitHub') : null,
      h('button', { class: mode === 'token' ? 'active' : '', onclick: () => { mode = 'token'; draw(); } }, 'Use a token'));
    if (mode === 'device') {
      mount(body, seg, h('p', {}, 'GitHub shows a page where you type a short code. gh-runners asks for the "repo" permission: enough to add runners to your repositories and to see queued jobs.'),
        h('div', { class: 'row' }, h('button', { class: 'btn primary big', onclick: startDevice }, 'Get a sign-in code')),
        h('div', { id: 'device-area' }),
        h('p', { class: 'muted small' }, 'You can also sign in with the GitHub CLI (gh auth login): gh-runners uses it when you have not signed in here.'));
    } else {
      const input = h('input', { type: 'password', placeholder: 'ghp_... or github_pat_...', autocomplete: 'off' });
      mount(body, seg,
        h('ol', { class: 'steps' },
          h('li', {}, h('a', { href: 'https://github.com/settings/tokens/new?scopes=repo&description=gh-runners', target: '_blank', rel: 'noopener' }, 'Create a token on GitHub'), ' (classic, "repo" scope is already ticked; pick an expiry).'),
          h('li', {}, 'Paste it here:')),
        h('div', { class: 'row' }, input, h('button', { class: 'btn primary', onclick: () => guard(async () => {
          const r = await api('POST', '/api/login/token', { token: input.value });
          closeDialog(); toast('Signed in as ' + r.login); await refreshState(); loadRepos(true);
        }) }, 'Save')),
        h('p', { class: 'muted small' }, 'Fine-grained tokens work too: give them Administration (read and write) and Actions (read) on your repositories. The token is stored in ' + (S.state ? S.state.home : '~/.gh-runners') + ' and only readable by you.'));
      setTimeout(() => input.focus(), 50);
    }
  };
  let polling = null;
  const stop = () => { if (polling) clearInterval(polling); polling = null; };
  async function startDevice() {
    await guard(async () => {
      const d = await api('POST', '/api/login/device');
      const area = $('device-area');
      mount(area,
        h('p', {}, '1. Copy this code:'),
        h('div', { class: 'code-box' }, d.user_code),
        h('div', { class: 'row' },
          h('button', { class: 'btn primary', onclick: () => { copy(d.user_code); window.open(d.verification_uri, '_blank', 'noopener'); } }, 'Copy code and open GitHub'),
          h('a', { class: 'btn', href: d.verification_uri, target: '_blank', rel: 'noopener' }, d.verification_uri)),
        h('p', {}, '2. Paste it on GitHub and press Authorize.'),
        h('div', { class: 'row muted' }, h('div', { class: 'spinner' }), 'Waiting for GitHub ...'));
      stop();
      polling = setInterval(async () => {
        let v;
        try { v = await api('GET', '/api/login/device'); } catch { return; }
        if (v.state === 'done') {
          stop(); closeDialog(); toast('Signed in as ' + v.login); await refreshState(); loadRepos(true);
        } else if (v.state === 'failed') {
          stop(); mount($('device-area'), h('p', { class: 'err' }, v.error), h('button', { class: 'btn', onclick: startDevice }, 'Try again'));
        }
      }, 2000);
    });
  }
  draw();
  openDialog({ title: 'Sign in to GitHub', body, onclose: stop });
}

// ---------- repositories ----------
function localRunner(id) { return S.state.runners.find((r) => r.id === id); }

function renderRepos() {
  const el = $('tab-repos');
  const st = S.state;
  if (!st) return;
  if (!st.auth.signed_in) {
    mount(el, h('div', { class: 'card hero' },
      h('h2', {}, 'Sign in to see your repositories'),
      h('p', { class: 'muted' }, 'Then pick a repository and click "Add runner". gh-runners downloads the GitHub Actions runner, registers it and keeps it running on this computer.'),
      st.auth.error && !st.auth.error.startsWith('not signed in') ? h('p', { class: 'err' }, st.auth.error) : null,
      h('div', { class: 'row' }, h('button', { class: 'btn primary big', onclick: signIn }, 'Sign in to GitHub'))));
    return;
  }
  if (!S.repos) {
    mount(el, S.reposErr
      ? h('div', { class: 'card' }, h('p', { class: 'err' }, S.reposErr), h('button', { class: 'btn', onclick: () => loadRepos(true) }, 'Try again'))
      : h('div', { class: 'card row' }, h('div', { class: 'spinner' }), 'Loading your repositories ...'));
    if (!S.reposLoading && !S.reposErr) loadRepos(false);
    return;
  }
  const f = S.filter;
  const repos = S.repos.repos;
  const owners = [...new Set(repos.map((r) => r.owner))].sort((a, b) => a.localeCompare(b));
  const q = f.q.toLowerCase();
  const shown = repos.filter((r) => {
    if (q && !r.full_name.toLowerCase().includes(q) && !(r.description || '').toLowerCase().includes(q)) return false;
    if (f.owner && r.owner !== f.owner) return false;
    if (f.show === 'private' && (!r.private || r.archived)) return false;
    if (f.show === 'here' && !r.runner_id) return false;
    if (f.show === 'none' && (r.runner_id || r.archived)) return false;
    return true;
  });
  const addable = (r) => r.admin && !r.archived && !r.runner_id;
  for (const p of [...S.picked]) if (!repos.some((r) => r.full_name === p && addable(r))) S.picked.delete(p);

  // Keep focus and caret in the search box across re-renders.
  const prev = document.activeElement && document.activeElement.id === 'repo-q' ? document.activeElement.selectionStart : null;
  const search = h('input', { type: 'search', id: 'repo-q', placeholder: 'Filter repositories', value: f.q, oninput: (e) => { f.q = e.target.value; renderRepos(); } });
  const ownerSel = h('select', { onchange: (e) => { f.owner = e.target.value; renderRepos(); } },
    h('option', { value: '' }, 'All owners'), owners.map((o) => h('option', { value: o, selected: f.owner === o }, o)));
  const showSel = h('select', { onchange: (e) => { f.show = e.target.value; renderRepos(); } },
    [['private', 'Private repos'], ['all', 'All repos'], ['here', 'With a runner here'], ['none', 'Without a runner here']].map(([v, t]) => h('option', { value: v, selected: f.show === v }, t)));

  const orgHint = f.owner && repos.some((r) => r.owner === f.owner && r.owner_type === 'Organization')
    ? h('div', { class: 'notice info' }, h('div', { class: 'grow' }, 'An organization runner serves every repository of ', h('b', {}, f.owner), ' at once (needs the admin:org permission).'),
      h('button', { class: 'btn', onclick: () => addOrg(f.owner) }, 'Add one runner for all of ' + f.owner))
    : null;

  const bulk = S.picked.size ? h('div', { class: 'bulk' },
    h('b', {}, S.picked.size + ' selected'),
    h('button', { class: 'btn primary', onclick: () => addRepos([...S.picked]) }, 'Add runners to ' + S.picked.size + ' repos'),
    h('button', { class: 'btn ghost', onclick: () => { S.picked.clear(); renderRepos(); } }, 'Clear')) : null;

  const allBox = h('input', { type: 'checkbox', title: 'Select all shown', checked: shown.some(addable) && shown.filter(addable).every((r) => S.picked.has(r.full_name)),
    onchange: (e) => { shown.filter(addable).forEach((r) => (e.target.checked ? S.picked.add(r.full_name) : S.picked.delete(r.full_name))); renderRepos(); } });

  const rows = shown.map((r) => repoRow(r, addable(r)));
  mount(el,
    h('div', { class: 'toolbar' }, search, ownerSel, showSel,
      h('button', { class: 'btn', onclick: () => loadRepos(true), disabled: S.reposLoading }, S.reposLoading ? 'Loading ...' : 'Refresh')),
    orgHint, bulk,
    h('div', { class: 'row muted small' }, allBox, `${shown.length} of ${repos.length} repositories`, S.repos.fetched ? ' · updated ' + ago(S.repos.fetched) : ''),
    h('div', { class: 'list' }, rows.length ? rows : h('div', { class: 'empty' }, f.show === 'private' && !q ? 'No private repositories. Choose "All repos" to see public ones.' : 'Nothing matches.')));
  if (prev !== null) { const s = $('repo-q'); s.focus(); s.setSelectionRange(prev, prev); }
}

function repoRow(r, addable) {
  const lr = r.runner_id ? localRunner(r.runner_id) : null;
  let status = h('span', { class: 'muted small' }, r.archived ? 'Archived' : 'No runner here');
  let actions = [];
  if (lr) {
    status = pill(lr.state);
    actions = [lr.disabled
      ? h('button', { class: 'btn small', onclick: () => startStop(lr, false) }, 'Start')
      : h('button', { class: 'btn small', onclick: () => startStop(lr, true) }, 'Stop'),
    h('button', { class: 'btn small', onclick: () => showLog(lr) }, 'Logs'),
    h('button', { class: 'btn small danger', onclick: () => removeRunner(lr) }, 'Remove')];
  } else if (!r.admin) {
    actions = [h('span', { class: 'muted small', title: 'Only repository admins can add runners' }, 'Needs admin rights')];
  } else if (!r.archived) {
    actions = [h('button', { class: 'btn primary small', onclick: () => addRepos([r.full_name]) }, 'Add runner')];
  }
  return h('div', { class: 'item' + (r.archived ? ' dim' : '') },
    h('input', { type: 'checkbox', disabled: !addable, checked: S.picked.has(r.full_name), 'aria-label': 'Select ' + r.full_name,
      onchange: (e) => { e.target.checked ? S.picked.add(r.full_name) : S.picked.delete(r.full_name); renderRepos(); } }),
    h('div', {},
      h('div', { class: 'name' }, h('a', { href: r.url, target: '_blank', rel: 'noopener' }, r.full_name)),
      r.description ? h('div', { class: 'desc' }, r.description) : null,
      h('div', { class: 'meta' },
        h('span', { class: 'pill plain ' + (r.private ? '' : 'warn') }, r.private ? 'Private' : 'Public'),
        r.fork ? h('span', { class: 'pill plain' }, 'Fork') : null,
        r.pushed_at ? h('span', { class: 'muted small' }, 'pushed ' + ago(r.pushed_at)) : null)),
    h('div', { class: 'status' }, status),
    h('div', { class: 'actions' }, actions));
}

async function addRepos(names) {
  const repos = (S.repos ? S.repos.repos : []).filter((r) => names.includes(r.full_name));
  const pub = repos.filter((r) => !r.private).map((r) => r.full_name);
  if (pub.length) {
    const ok = await confirmDialog('Add a runner to a public repository?', [
      h('p', {}, h('b', {}, pub.join(', ')), pub.length === 1 ? ' is public.' : ' are public.'),
      h('p', {}, 'Anyone on GitHub can open a pull request whose workflow then runs on this computer, with your files in reach. GitHub recommends self-hosted runners only for private repositories.')],
    'Add anyway', true);
    if (!ok) return;
  }
  await guard(async () => {
    await api('POST', '/api/add', { repos: names, allow_public: pub.length > 0 });
    S.picked.clear();
    toast(names.length === 1 ? 'Adding a runner to ' + names[0] + ' ...' : 'Adding ' + names.length + ' runners ...');
    loadTasks();
  });
}

async function addOrg(org) {
  if (!await confirmDialog('Organization runner for ' + org, 'One runner on this computer will take jobs from every repository of ' + org + '. Your token needs the admin:org permission.', 'Add')) return;
  await guard(async () => { await api('POST', '/api/add', { org }); loadTasks(); });
}

async function startStop(r, stop) {
  await guard(async () => {
    await api('POST', `/api/runners/${encodeURIComponent(r.id)}/${stop ? 'stop' : 'start'}`);
    toast(r.scope + (stop ? ' is stopping' : ' is starting'));
    await refreshState();
  });
}

async function removeRunner(r) {
  const body = [h('p', {}, 'This stops the runner, removes it from ', h('b', {}, r.scope), ' on GitHub and deletes its folder:'), h('p', {}, h('code', {}, r.dir))];
  if (!await confirmDialog('Remove the runner?', body, 'Remove', true)) return;
  await guard(async () => { await api('POST', `/api/runners/${encodeURIComponent(r.id)}/remove`); loadTasks(); });
}

function showLog(r) {
  const pre = h('pre', { class: 'log' }, 'Loading ...');
  let timer = null;
  let stick = true;
  pre.addEventListener('scroll', () => { stick = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 20; });
  const load = async () => {
    try {
      const d = await api('GET', r ? `/api/runners/${encodeURIComponent(r.id)}/log?lines=500` : '/api/daemon-log?lines=500');
      if (pre.textContent !== d.text) { pre.textContent = d.text; if (stick) pre.scrollTop = pre.scrollHeight; }
    } catch (e) { pre.textContent = e.message; }
  };
  openDialog({ title: r ? 'Output of ' + r.scope : 'gh-runners background service log', body: pre, wide: true, onclose: () => clearInterval(timer) });
  load();
  timer = setInterval(load, 2000);
}

// ---------- this machine ----------
function renderMachine() {
  const el = $('tab-machine');
  const st = S.state;
  if (!st) return;
  const cards = st.runners.map((r) => {
    const remote = S.remote[r.id];
    const runsOn = '[self-hosted, ' + (r.labels.find((l) => l !== 'gh-runners') || osName(st.os)) + ']';
    return h('div', { class: 'card runner' },
      h('div', { class: 'card-head' },
        h('div', {}, h('div', { class: 'title' }, r.kind === 'org' ? r.scope + ' (organization)' : r.scope), h('div', { class: 'muted small' }, 'runner name: ' + r.name)),
        pill(r.state)),
      h('dl', { class: 'kv' },
        h('dt', {}, 'GitHub'), h('dd', {}, remote ? pill(remote) : h('span', { class: 'muted' }, st.auth.signed_in ? 'checking ...' : 'sign in to see')),
        h('dt', {}, 'Labels'), h('dd', {}, h('div', { class: 'chips' }, ['self-hosted', osName(st.os)].concat(r.labels).map((l) => h('span', { class: 'chip' }, l)))),
        r.restarts ? [h('dt', {}, 'Restarts'), h('dd', {}, String(r.restarts))] : null,
        r.last_exit ? [h('dt', {}, 'Last exit'), h('dd', { class: 'err' }, r.last_exit)] : null,
        h('dt', {}, 'Folder'), h('dd', { class: 'mono small' }, r.dir),
        h('dt', {}, 'Runner'), h('dd', {}, (r.version || '?') + (r.source === 'sync' ? ' · added by sync' : ''))),
      h('div', { class: 'snippet' }, h('code', {}, 'runs-on: ' + runsOn), h('button', { class: 'btn small ghost', onclick: () => copy('runs-on: ' + runsOn) }, 'Copy')),
      h('div', { class: 'row', 'data-gap': '1' },
        r.disabled ? h('button', { class: 'btn', onclick: () => startStop(r, false) }, 'Start') : h('button', { class: 'btn', onclick: () => startStop(r, true) }, 'Stop'),
        h('button', { class: 'btn', onclick: () => showLog(r) }, 'Logs'),
        h('div', { class: 'grow' }),
        h('button', { class: 'btn danger', onclick: () => removeRunner(r) }, 'Remove')));
  });
  const od = st.on_demand_runners.length ? h('div', { class: 'card' },
    h('h3', {}, 'On-demand runners right now'),
    st.on_demand_runners.map((p) => h('div', { class: 'row' }, pill(p.state), h('span', { class: 'mono small' }, p.id), h('span', { class: 'muted small' }, 'since ' + ago(p.since))))) : null;
  mount(el,
    h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Runners on ' + st.hostname),
      h('span', { class: 'muted small' }, 'Folder: ', h('code', {}, st.runners_dir))),
    h('p', { class: 'muted' }, 'Target any runner of this computer from a workflow with ', h('code', {}, 'runs-on: [self-hosted, ' + st.machine_labels[3 + (st.config.labels || []).length] + ']'), ' or by its OS: ', h('code', {}, 'runs-on: [self-hosted, ' + osName(st.os) + ']'), '.'),
    od,
    cards.length ? h('div', { class: 'grid' }, cards) : h('div', { class: 'card empty' },
      h('h2', {}, 'No runners on this computer yet'),
      h('p', {}, 'Go to Repositories and click "Add runner" next to a repository, or turn on on-demand runners under Automation.'),
      h('button', { class: 'btn primary', onclick: () => selectTab('repos') }, 'Pick repositories')));
}

// ---------- automation ----------
function markDirty(card) { card.setAttribute('data-dirty', '1'); }

function renderAutomation() {
  const el = $('tab-automation');
  const st = S.state;
  if (!st) return;
  const cfg = JSON.parse(JSON.stringify(st.config));
  cfg.sync = cfg.sync || {}; cfg.on_demand = cfg.on_demand || {};

  const syncCard = h('div', { class: 'card' });
  const odCard = h('div', { class: 'card' });
  const input = (card, obj, key, attrs) => h('input', Object.assign({ type: 'text', value: Array.isArray(obj[key]) ? list(obj[key]) : (obj[key] || ''),
    oninput: (e) => { obj[key] = attrs && attrs.isList ? split(e.target.value) : e.target.value; markDirty(card); } }, attrs || {}));
  const select = (card, obj, key, options, def) => h('select', { onchange: (e) => { obj[key] = e.target.value; markDirty(card); } },
    options.map(([v, t]) => h('option', { value: v, selected: (dur(obj[key]) || def) === dur(v) }, t)));
  const check = (card, obj, key, label) => h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: !!obj[key], onchange: (e) => { obj[key] = e.target.checked; markDirty(card); } }), label);
  const save = async (card, msg) => guard(async () => {
    await api('PUT', '/api/config', cfg);
    card.removeAttribute('data-dirty');
    toast(msg);
    await refreshState();
  });

  const preview = h('div', {});
  mount(syncCard,
    h('div', { class: 'card-head' },
      h('div', {}, h('h2', {}, 'A runner for every repository'), h('p', { class: 'muted' }, 'Adds a runner on this computer to each of your repositories that match the rules below. With auto-sync on, new repositories get one within the interval.')),
      sw(cfg.sync.auto, (v) => { cfg.sync.auto = v; save(syncCard, 'Auto-sync ' + (v ? 'on' : 'off')); }, 'Auto-sync')),
    h('div', { class: 'fields' },
      h('label', { class: 'field' }, 'Check every', select(syncCard, cfg.sync, 'interval', [['15m0s', '15 minutes'], ['1h0m0s', '1 hour'], ['6h0m0s', '6 hours'], ['24h0m0s', '24 hours']], '1h')),
      h('label', { class: 'field' }, 'Owners', input(syncCard, cfg.sync, 'owners', { isList: true, placeholder: st.auth.login || 'you' }), h('span', { class: 'hint' }, 'Users or organizations, comma separated')),
      h('label', { class: 'field' }, 'Only these', input(syncCard, cfg.sync, 'include', { isList: true, placeholder: 'all, or api-*, web' }), h('span', { class: 'hint' }, 'Names or patterns')),
      h('label', { class: 'field' }, 'Never these', input(syncCard, cfg.sync, 'exclude', { isList: true, placeholder: '*-archive' }))),
    h('div', { class: 'stack' },
      check(syncCard, cfg.sync, 'include_forks', 'Include forks'),
      check(syncCard, cfg.sync, 'include_public', 'Include public repositories (unsafe: anyone can run code here through a pull request)'),
      check(syncCard, cfg.sync, 'prune', 'Remove runners that sync added when their repository no longer matches')),
    h('div', { class: 'row', 'data-gap': '1' },
      h('button', { class: 'btn primary', onclick: () => save(syncCard, 'Sync rules saved') }, 'Save rules'),
      h('button', { class: 'btn', onclick: () => guard(async () => {
        mount(preview, h('div', { class: 'row muted' }, h('div', { class: 'spinner' }), 'Checking your repositories ...'));
        const p = await api('GET', '/api/sync/plan');
        mount(preview, h('div', { class: 'card' },
          h('p', {}, p.have + ' already have a runner here.'),
          p.add.length ? [h('p', {}, h('b', {}, 'Would add ' + p.add.length + ':')), h('div', { class: 'chips' }, p.add.map((n) => h('span', { class: 'chip' }, n)))] : h('p', {}, 'Nothing to add.'),
          p.prune.length ? [h('p', {}, h('b', {}, 'Would remove ' + p.prune.length + ':')), h('div', { class: 'chips' }, p.prune.map((n) => h('span', { class: 'chip' }, n)))] : null));
      }) }, 'Preview'),
      h('button', { class: 'btn', onclick: () => guard(async () => { await api('POST', '/api/sync/apply'); loadTasks(); }) }, 'Sync now'),
      h('span', { class: 'muted small' }, st.last_sync ? 'Last auto-sync check ' + ago(st.last_sync) : ''),
      st.sync_error ? h('span', { class: 'err' }, 'Last sync failed: ' + st.sync_error) : null),
    preview);

  mount(odCard,
    h('div', { class: 'card-head' },
      h('div', {}, h('h2', {}, 'On-demand runners'), h('p', { class: 'muted' }, 'No setup per repository: gh-runners watches your repositories and starts a one-job runner here when a job asks for labels this computer has. Nothing runs while nothing is queued.')),
      sw(cfg.on_demand.enabled, (v) => { cfg.on_demand.enabled = v; save(odCard, 'On-demand runners ' + (v ? 'on' : 'off')); }, 'On')),
    h('p', {}, 'Status: ', h('b', {}, st.on_demand || 'off')),
    h('p', {}, 'Jobs it takes: ', h('code', {}, 'runs-on: [self-hosted, ' + osName(st.os) + ']'), ' or any of ', h('span', { class: 'chips' }, st.machine_labels.map((l) => h('span', { class: 'chip' }, l)))),
    h('div', { class: 'fields' },
      h('label', { class: 'field' }, 'Runners at once', h('input', { type: 'number', min: 1, max: 64, value: cfg.on_demand.max_concurrent || 2, oninput: (e) => { cfg.on_demand.max_concurrent = parseInt(e.target.value, 10) || 0; markDirty(odCard); } })),
      h('label', { class: 'field' }, 'Check for jobs every', select(odCard, cfg.on_demand, 'poll', [['15s', '15 seconds'], ['30s', '30 seconds'], ['1m0s', '1 minute'], ['2m0s', '2 minutes']], '1m')),
      h('label', { class: 'field' }, 'Watch repos pushed within', select(odCard, cfg.on_demand, 'active_within', [['24h0m0s', '1 day'], ['168h0m0s', '7 days'], ['336h0m0s', '14 days'], ['720h0m0s', '30 days'], ['2160h0m0s', '90 days']], '336h')),
      h('label', { class: 'field' }, 'Owners', input(odCard, cfg.on_demand, 'owners', { isList: true, placeholder: st.auth.login || 'you' })),
      h('label', { class: 'field' }, 'Only these', input(odCard, cfg.on_demand, 'include', { isList: true, placeholder: 'all' })),
      h('label', { class: 'field' }, 'Never these', input(odCard, cfg.on_demand, 'exclude', { isList: true }))),
    check(odCard, cfg.on_demand, 'include_public', 'Include public repositories (unsafe)'),
    h('div', { class: 'row' }, h('button', { class: 'btn primary', onclick: () => save(odCard, 'On-demand settings saved') }, 'Save')));

  mount(el, h('div', { class: 'two' }, syncCard, odCard));
}

// ---------- activity ----------
function renderActivity(full) {
  const el = $('tab-activity');
  const open = new Set([...el.querySelectorAll('details[open]')].map((d) => d.dataset.id));
  const tasks = S.tasks.map((t) => h('details', { class: 'task', 'data-id': String(t.id), open: open.has(String(t.id)) || t.state === 'running' },
    h('summary', {}, t.state === 'running' || t.state === 'queued' ? h('div', { class: 'spinner' }) : h('span', { class: 'pill ' + (t.state === 'done' ? 'ok' : 'bad') }, t.state === 'done' ? 'Done' : 'Failed'),
      h('b', { class: 'grow' }, t.title), h('span', { class: 'muted small' }, ago(t.created))),
    t.error ? h('p', { class: 'err' }, t.error) : null,
    h('pre', { class: 'log' }, t.log || '...')));
  mount(el,
    h('div', { class: 'card' }, h('div', { class: 'card-head' }, h('div', {}, h('h2', {}, 'What gh-runners did from this dashboard')), h('button', { class: 'btn', onclick: () => showLog(null) }, 'Background service log')),
      tasks.length ? tasks : h('p', { class: 'muted' }, 'Nothing yet. Adding and removing runners shows up here.')));
  if (full) loadTasks();
}

function renderDock() {
  const active = S.tasks.filter((t) => t.state === 'running' || t.state === 'queued');
  $('count-tasks').textContent = active.length ? String(active.length) : '';
  mount($('tasks-dock'), active.slice(0, 3).map((t) => h('div', { class: 'dock-item', onclick: () => selectTab('activity') },
    h('div', { class: 'spinner' }),
    h('div', { class: 't' }, h('div', {}, h('b', {}, t.title)), h('div', { class: 'muted small mono' }, (t.log || 'waiting ...').trim().split('\n').pop())))));
}

// ---------- settings ----------
function renderSettings() {
  const el = $('tab-settings');
  const st = S.state;
  if (!st) return;
  const a = st.auth;
  const cfg = JSON.parse(JSON.stringify(st.config));
  const account = h('div', { class: 'card' },
    h('h2', {}, 'GitHub account'),
    a.signed_in
      ? [h('div', { class: 'row' }, a.avatar ? h('img', { class: 'avatar', src: a.avatar, alt: '' }) : null, h('b', {}, a.login), a.name ? h('span', { class: 'muted' }, a.name) : null),
        h('p', { class: 'muted small' }, 'Token from: ' + a.source + (a.scopes ? ' · permissions: ' + a.scopes : '')),
        h('div', { class: 'row' }, h('button', { class: 'btn', onclick: signIn }, 'Sign in with another account'),
          a.saved ? h('button', { class: 'btn danger', onclick: () => guard(async () => { await api('POST', '/api/logout'); S.repos = null; toast('Signed out'); await refreshState(); }) }, 'Sign out') : null),
        !a.saved ? h('p', { class: 'muted small' }, 'This token comes from ' + a.source + '. Signing in here takes priority over it.') : null]
      : [h('p', {}, 'Not signed in.'), a.error ? h('p', { class: 'muted small' }, a.error) : null, h('button', { class: 'btn primary', onclick: signIn }, 'Sign in to GitHub')]);

  const folder = h('div', { class: 'card' },
    h('h2', {}, 'Runners folder'),
    h('p', { class: 'muted' }, 'Each runner gets a subfolder here with the GitHub runner program and the checkouts of the jobs it runs, so pick a disk with room.'),
    h('div', { class: 'snippet' }, h('code', {}, st.runners_dir), h('button', { class: 'btn small', onclick: pickFolder }, 'Change ...')),
    st.runners_dir !== st.default_runners_dir ? h('p', {}, h('button', { class: 'btn small ghost', onclick: () => moveTo('') }, 'Back to the default folder (' + st.default_runners_dir + ')')) : null);

  const defaults = h('div', { class: 'card' });
  mount(defaults,
    h('h2', {}, 'New runners'),
    h('p', { class: 'muted' }, 'Used for runners you add from now on.'),
    h('div', { class: 'fields' },
      h('label', { class: 'field' }, 'Runner name', h('input', { type: 'text', value: cfg.runner_name || '', placeholder: st.hostname, oninput: (e) => { cfg.runner_name = e.target.value; markDirty(defaults); } }), h('span', { class: 'hint' }, 'Shown on GitHub. Default: this computer\'s name')),
      h('label', { class: 'field' }, 'Extra labels', h('input', { type: 'text', value: list(cfg.labels), placeholder: 'gh-runners, gpu', oninput: (e) => { cfg.labels = split(e.target.value); markDirty(defaults); } }), h('span', { class: 'hint' }, 'Comma separated. GitHub adds self-hosted, the OS and the CPU itself'))),
    h('button', { class: 'btn primary', onclick: () => guard(async () => { await api('PUT', '/api/config', cfg); defaults.removeAttribute('data-dirty'); toast('Saved'); await refreshState(); }) }, 'Save'));

  const svc = h('div', { class: 'card' },
    h('div', { class: 'card-head' }, h('div', {}, h('h2', {}, 'Background service'),
      h('p', { class: 'muted' }, 'gh-runners runs in the background and keeps your runners going. With "Start at login" on it comes back after every restart.' + (st.os === 'windows' ? ' On Windows that means while you are signed in.' : ''))),
    sw(st.service.installed, (v) => serviceAction(v ? 'install' : 'uninstall'), 'Start at login')),
    h('p', { class: 'muted small' }, st.service.installed ? 'Registered in ' + st.service.where : ''),
    h('div', { class: 'row' },
      h('button', { class: 'btn', onclick: () => serviceAction('restart') }, 'Restart'),
      h('button', { class: 'btn danger', onclick: async () => {
        if (await confirmDialog('Stop gh-runners?', 'Every runner on this computer stops, and so does this dashboard, until you open gh-runners again' + (st.service.installed ? ' or sign in again.' : '.'), 'Stop everything', true)) serviceAction('stop');
      } }, 'Stop everything')));

  const u = st.update || {};
  const autoUpdate = cfg.auto_update !== false;
  const newer = u.latest && u.latest !== st.version && u.note !== 'up to date';
  const updates = h('div', { class: 'card' },
    h('div', { class: 'card-head' }, h('div', {}, h('h2', {}, 'Updates'),
      h('p', { class: 'muted' }, 'gh-runners checks for a new version every few hours. With auto-update on it installs it and restarts by itself, but only while no runner here is running a job.')),
    sw(autoUpdate, (v) => guard(async () => { cfg.auto_update = v; await api('PUT', '/api/config', cfg); toast('Auto-update ' + (v ? 'on' : 'off')); await refreshState(); }), 'Auto-update')),
    h('dl', { class: 'kv' },
      h('dt', {}, 'This version'), h('dd', {}, st.version),
      h('dt', {}, 'Latest'), h('dd', {}, (u.latest || 'not checked yet') + (u.checked ? ' · checked ' + ago(u.checked) : ''))),
    u.note ? h('p', {}, u.note) : null,
    u.error ? h('p', { class: 'err' }, 'Last check failed: ' + u.error) : null,
    h('div', { class: 'row' },
      h('button', { class: 'btn' + (newer ? ' primary' : ''), onclick: () => guard(async () => {
        await api('POST', '/api/update');
        toast(newer ? 'Updating once no job is running ...' : 'Checking for updates ...');
        setTimeout(refreshState, 3000);
        setTimeout(refreshState, 10000);
        setTimeout(refreshState, 20000);
      }) }, newer ? 'Update to ' + u.latest + ' now' : 'Check now')));

  const about = h('div', { class: 'card' }, h('h2', {}, 'About'),
    h('dl', { class: 'kv' },
      h('dt', {}, 'Version'), h('dd', {}, st.version),
      h('dt', {}, 'Computer'), h('dd', {}, st.hostname + ' · ' + osName(st.os) + ' ' + st.arch),
      h('dt', {}, 'Settings folder'), h('dd', { class: 'mono small' }, st.home),
      h('dt', {}, 'Running since'), h('dd', {}, ago(st.started) + ' (pid ' + st.pid + ')')),
    h('p', {}, h('a', { href: 'https://github.com/MehdiMamas/gh-runners', target: '_blank', rel: 'noopener' }, 'github.com/MehdiMamas/gh-runners')));

  mount(el, h('div', { class: 'two' }, h('div', {}, account, folder, defaults), h('div', {}, svc, updates, about)));
}

async function serviceAction(action) {
  await guard(async () => {
    await api('POST', '/api/service/' + action);
    const msg = { install: 'Turning on "Start at login" ...', uninstall: 'Turning off "Start at login" ...', restart: 'Restarting ...', stop: 'Stopping gh-runners ...' }[action];
    toast(msg);
    if (action === 'stop') { $('offline-text').textContent = 'gh-runners is stopped. Open gh-runners again to start it.'; }
    setTimeout(refreshState, 2500);
    setTimeout(refreshState, 8000);
  });
}

// ---------- folder picker ----------
function pickFolder() {
  let cur = S.state.runners_dir;
  const pathInput = h('input', { type: 'text', value: cur, onkeydown: (e) => { if (e.key === 'Enter') go(pathInput.value); } });
  const folders = h('div', { class: 'folders' });
  const roots = h('div', { class: 'row' });
  const target = h('code', {});
  const useBtn = h('button', { class: 'btn primary', onclick: () => { closeDialog(); moveTo(cur); } }, 'Use this folder');
  async function go(p) {
    try {
      const d = await api('GET', '/api/fs?path=' + encodeURIComponent(p));
      cur = d.path;
      pathInput.value = d.path;
      target.textContent = d.path;
      mount(roots, d.roots.map((r) => h('button', { class: 'btn small', onclick: () => go(r.path) }, r.name)));
      mount(folders,
        d.parent ? h('div', { class: 'folder', onclick: () => go(d.parent) }, h('span', { class: 'ico' }), '..') : null,
        d.error ? h('div', { class: 'folder err' }, d.error) : null,
        d.dirs.map((x) => h('div', { class: 'folder', onclick: () => go(x.path) }, h('span', { class: 'ico' }), x.name)),
        !d.dirs.length && !d.error ? h('div', { class: 'empty small' }, 'No folders inside') : null);
    } catch (e) { toast(e.message, true); }
  }
  const newFolder = async () => {
    const name = h('input', { type: 'text', placeholder: 'gh-runners' });
    const row = h('div', { class: 'row' }, name, h('button', { class: 'btn primary small', onclick: () => guard(async () => {
      const d = await api('POST', '/api/fs/mkdir', { parent: cur, name: name.value });
      await go(d.path);
    }) }, 'Create'));
    folders.prepend(row);
    name.focus();
  };
  openDialog({
    title: 'Choose the runners folder',
    body: [h('div', { class: 'picker-path' }, pathInput, h('button', { class: 'btn', onclick: () => go(pathInput.value) }, 'Go')),
      roots, h('div', { class: 'row' }, h('div', { class: 'grow' }), h('button', { class: 'btn small', onclick: newFolder }, 'New folder')), folders,
      h('p', { class: 'muted small' }, 'Runners go in ', target, '. Runners you already have are stopped, moved there and started again.')],
    foot: [h('button', { class: 'btn', onclick: closeDialog }, 'Cancel'), useBtn],
  });
  go(cur);
}

async function moveTo(path) {
  await guard(async () => {
    await api('POST', '/api/runners-dir', { path });
    toast('Moving the runners folder ...');
    loadTasks();
  });
}

// ---------- health ----------
async function renderHealth() {
  const el = $('tab-health');
  mount(el, h('div', { class: 'card row' }, h('div', { class: 'spinner' }), 'Checking this computer ...'));
  try {
    const checks = await api('GET', '/api/doctor');
    const bad = checks.filter((c) => c.level === 'fix').length;
    mount(el, h('div', { class: 'card' },
      h('div', { class: 'card-head' }, h('div', {}, h('h2', {}, bad ? bad + ' thing' + (bad > 1 ? 's' : '') + ' to fix' : 'All good'), h('p', { class: 'muted' }, 'What gh-runners checked on this computer.')),
        h('button', { class: 'btn', onclick: renderHealth }, 'Check again')),
      checks.map((c) => h('div', { class: 'check' }, h('span', { class: 'mark ' + c.level }, { ok: '✓', fix: '!', note: 'i' }[c.level]), h('div', {}, c.text)))));
  } catch (e) { mount(el, h('div', { class: 'card err' }, e.message)); }
}

// ---------- tabs ----------
function selectTab(tab) {
  S.tab = tab;
  for (const b of document.querySelectorAll('#tabs button')) b.classList.toggle('active', b.dataset.tab === tab);
  for (const s of document.querySelectorAll('.tab')) s.classList.toggle('active', s.id === 'tab-' + tab);
  try { localStorage.setItem('ghr-tab', tab); } catch { /* private window */ }
  if (tab === 'repos') renderRepos();
  if (tab === 'machine') { renderMachine(); loadRemote(); }
  if (tab === 'automation') renderAutomation();
  if (tab === 'activity') renderActivity(true);
  if (tab === 'settings') renderSettings();
  if (tab === 'health') renderHealth();
}

document.addEventListener('DOMContentLoaded', async () => {
  for (const b of document.querySelectorAll('#tabs button')) b.addEventListener('click', () => selectTab(b.dataset.tab));
  let tab = 'repos';
  try { tab = localStorage.getItem('ghr-tab') || 'repos'; } catch { /* ignore */ }
  await refreshState();
  await loadTasks();
  selectTab(tab);
  setInterval(() => { if (!document.hidden) refreshState(); }, 3000);
  setInterval(() => { if (!document.hidden || S.tasks.some((t) => t.state === 'running')) loadTasks(); }, 1500);
  setInterval(() => { if (!document.hidden && S.tab === 'machine') loadRemote(); }, 15000);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) { refreshState(); loadTasks(); } });
});
