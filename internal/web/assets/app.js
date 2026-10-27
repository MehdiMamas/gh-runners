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
  ghRunners: {}, // owner/repo -> runners GitHub has for it
  found: [], foundHidden: false, // runners set up by hand on this computer
  pending: new Set(), // repos whose add request is on its way
  fleet: null, // GET /api/machines: the main host's view of its machines
  mgh: {}, // machine id -> runner id -> state GitHub reports, for that machine's runners
  mwatch: new Set(), // "machine:task" pairs being followed
  ci: null, // GET /api/ci: Local CI settings and recent runs
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
  if (!res.ok) throw Object.assign(new Error((data && data.error) || res.statusText), { status: res.status, data });
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

// copy puts text on the clipboard. When that is refused, it selects el (if
// given) so Ctrl+C works.
async function copy(text, el) {
  try { await navigator.clipboard.writeText(text); toast('Copied'); } catch {
    if (el) { const r = document.createRange(); r.selectNodeContents(el); const s = getSelection(); s.removeAllRanges(); s.addRange(r); }
    toast('Copy failed: select the text and copy it', true);
  }
}

function stateClass(s) {
  if (!s) return '';
  if (s === 'running' || s === 'online' || s === 'jit-busy') return 'ok';
  if (s === 'busy') return 'info';
  if (s.startsWith('restarting') || s === 'starting' || s === 'stopping' || s === 'jit-idle' || s === 'paused') return 'warn';
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
dlg.tabIndex = -1;
// dlgDone is the onclose of the dialog last opened, not run yet. The close
// event comes a task after dlg.close(): when a dialog closes and the next
// opens right away (repo picker, then labels), the event arrives while the
// next one is open and must not count as closing it.
let dlgDone = null;
function dialogClosed() { const f = dlgDone; dlgDone = null; if (f) f(); }
dlg.onclose = () => { if (!dlg.open) dialogClosed(); };
function openDialog({ title, body, foot, wide, onclose }) {
  if (!dlg.open) dialogClosed(); // the one closed just before, if its event is still on the way
  dlg.className = wide ? 'wide' : '';
  mount(dlg,
    h('div', { class: 'dlg-head' }, h('h2', {}, title), h('button', { class: 'btn ghost small', onclick: () => dlg.close(), 'aria-label': 'Close' }, '✕')),
    h('div', { class: 'dlg-body' }, body),
    foot ? h('div', { class: 'dlg-foot' }, foot) : null);
  dlgDone = onclose || null;
  if (!dlg.open) dlg.showModal();
  dlg.focus(); // not the close button, which would show a focus ring on open
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

// confirmBusy warns that runners are running a job and asks to go on
// anyway. gerund is "Stopping", "Restarting", "Removing", "Updating" or "Pausing".
const BUSY_VERBS = { Stopping: 'Stop', Restarting: 'Restart', Removing: 'Remove', Updating: 'Update', Pausing: 'Pause' };
function confirmBusy(names, gerund) {
  const one = names.length === 1;
  return confirmDialog(gerund + ' will cancel a job',
    [h('p', {}, h('b', {}, names.join(', ')), (one ? ' is' : ' are') + ' running a job now. ' + gerund + ' will cancel that job.')], BUSY_VERBS[gerund] + ' anyway', true);
}

// confirmedPost posts to a machine's dashboard. When that machine says a
// runner is running a job (409 with confirm), it asks and, on yes, sends
// the same request again with confirm_busy=1. It resolves to null when the
// answer is no.
async function confirmedPost(path, gerund) {
  try { return await api('POST', path); } catch (e) {
    if (!(e.data && e.data.confirm)) throw e;
    const busy = e.data.busy || [];
    const ok = busy.length ? await confirmBusy(busy, gerund)
      : await confirmDialog('Could not check for running jobs', [h('p', {}, e.message), h('p', {}, gerund + ' may cancel a job that is running.')], BUSY_VERBS[gerund] + ' anyway', true);
    if (!ok) return null;
    return api('POST', path + (path.includes('?') ? '&' : '?') + 'confirm_busy=1');
  }
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
  loadGhRunners(force);
  loadCI();
}

async function loadCI() {
  try { S.ci = await api('GET', '/api/ci'); } catch { return; }
  if (S.tab === 'repos') renderRepos();
}

async function loadRemote() {
  if (!S.state || !S.state.auth.signed_in || !S.state.runners.length) return;
  try { S.remote = await api('GET', '/api/github-runners'); } catch { /* shown as unknown */ }
  if (S.tab === 'machine') renderMachine();
  if (S.tab === 'machines') renderMachines();
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
          loadFound(false);
        }
      }
    }
    S.firstTasks = false;
    const before = busySig();
    S.tasks = list;
    if (busySig() !== before && S.tab === 'repos') renderRepos();
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
  if (S.tab === 'machines') renderMachines();
  if (S.tab === 'automation' && !document.querySelector('#tab-automation [data-dirty="1"]')) renderAutomation();
  if (S.tab === 'settings' && !document.querySelector('#tab-settings [data-dirty="1"]')) renderSettings();
}

function renderHeader() {
  const st = S.state;
  mount($('machine'),
    h('span', { class: 'host' }, st.hostname),
    h('span', { class: 'pill plain' }, osName(st.os) + ' · ' + st.arch),
    st.service.installed ? h('span', { class: 'pill ok', title: st.service.where }, 'Starts at login') : h('span', { class: 'pill warn' }, 'Not started at login'),
    st.paused ? h('span', { class: 'pill warn' }, 'Paused') : null,
    h('button', { class: 'btn small pause-btn' + (st.paused ? ' primary' : ''), title: st.paused ? 'Start the runners again' : 'Stop every runner and all background work on this computer until you resume', onclick: () => setPaused(null, !st.paused) }, st.paused ? 'Resume' : 'Pause this machine'));
  const a = st.auth;
  const fb = h('button', { class: 'btn small', title: 'Report a problem or an idea, with logs, as a GitHub issue', onclick: feedbackDialog }, 'Feedback');
  if (a.signed_in) {
    mount($('account'), fb,
      a.avatar ? h('img', { class: 'avatar', src: a.avatar + (a.avatar.includes('?') ? '&' : '?') + 's=52', alt: '' }) : null,
      h('span', { class: 'login' }, a.login));
  } else {
    mount($('account'), fb, h('button', { class: 'btn primary', onclick: signIn }, 'Sign in to GitHub'));
  }
}

// feedbackDialog: tell what happened, pick what to attach, read the exact
// scrubbed text, then send it as an issue under the user's own GitHub sign-in.
async function feedbackDialog() {
  if (!S.ci) await loadCI();
  const runs = ((S.ci && S.ci.runs) || []).slice(0, 15);
  const text = h('textarea', { rows: 4, placeholder: 'What happened? What did you expect?', 'aria-label': 'What happened' });
  const run = h('select', { 'aria-label': 'Local CI run to attach' }, h('option', { value: '' }, 'No Local CI run'),
    runs.map((r) => h('option', { value: r.id }, r.repo + (r.pr ? ' #' + r.pr : '') + (r.title ? ' · ' + r.title : '') + (r.passed ? '' : ' (failed)'))));
  const doctor = h('input', { type: 'checkbox', checked: true });
  const daemon = h('input', { type: 'checkbox' });
  const out = h('div', { class: 'fb-out' });
  const note = h('p', { class: 'hint' }, 'Nothing is sent until you press Send. Tokens, e-mail addresses and your home folder are removed first, and you read the exact text below.');
  let body = '';
  const send = h('button', { class: 'btn primary', disabled: true }, 'Send as GitHub issue');
  const payload = () => ({ text: text.value, run_id: run.value, doctor: doctor.checked, daemon_log: daemon.checked });
  const preview = h('button', { class: 'btn' }, 'Preview');
  preview.onclick = () => guard(async () => {
    if (!text.value.trim()) { toast('Write what happened first', true); return; }
    preview.disabled = true;
    try {
      const v = await api('POST', '/api/feedback/preview', payload());
      if (!v.signed_in) {
        body = '';
        send.disabled = true;
        mount(out, h('p', { class: 'fb-warn' }, v.message || 'Sign in to GitHub first.'),
          h('button', { class: 'btn', onclick: () => { closeDialog(); signIn(); } }, 'Sign in to GitHub'));
        return;
      }
      body = v.body;
      send.disabled = false;
      mount(out, h('p', { class: 'hint' }, 'Will be filed as ' + v.login + ' in MehdiMamas/gh-runners:'), h('pre', { class: 'log' }, v.body));
    } finally { preview.disabled = false; }
  });
  text.oninput = () => { send.disabled = true; body = ''; };
  send.onclick = () => guard(async () => {
    send.disabled = true;
    const v = await api('POST', '/api/feedback', Object.assign(payload(), { body }));
    mount(out, h('p', {}, 'Sent. ', h('a', { href: v.url, target: '_blank', rel: 'noopener' }, 'Issue #' + v.number)));
    toast('Feedback sent');
  });
  openDialog({
    title: 'Send feedback',
    wide: true,
    body: [text,
      h('label', { class: 'field' }, 'Attach a Local CI run (its logs)', run),
      h('label', { class: 'fb-check' }, doctor, ' Health check output'),
      h('label', { class: 'fb-check' }, daemon, ' End of the background log'),
      note, out],
    foot: [h('button', { class: 'btn', onclick: closeDialog }, 'Close'), preview, send],
  });
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
  if (st.paused) {
    items.unshift(h('div', { class: 'notice' },
      h('div', { class: 'grow' }, h('b', {}, 'This machine is paused. '), 'No runner, on-demand runner, sync or update runs' + (st.paused_since ? ' (since ' + ago(st.paused_since) + ')' : '') + '. It stays paused after a restart until you resume.'),
      h('button', { class: 'btn primary', onclick: () => setPaused(null, false) }, 'Resume')));
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
// localRunners lists the runners of a repository on this computer: a repo
// can have several (say a heavy one and a light one).
function localRunners(r) {
  const ids = r.runner_ids || (r.runner_id ? [r.runner_id] : []);
  return ids.map(localRunner).filter(Boolean);
}
// who names a runner in messages: the scope, plus the runner name when
// the scope has more than one runner here.
function who(r) {
  const n = S.state.runners.filter((x) => x.scope.toLowerCase() === r.scope.toLowerCase()).length;
  return n > 1 ? r.scope + ' (' + r.name + ')' : r.scope;
}

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
    if (f.show === 'here' && !localRunners(r).length) return false;
    if (f.show === 'none' && (localRunners(r).length || r.archived)) return false;
    return true;
  });
  const busy = busyScopes();
  const addable = (r) => r.admin && !r.archived && !localRunners(r).length && !busy.has(r.full_name.toLowerCase());
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
    foundBanner(), orgHint, bulk,
    h('div', { class: 'row muted small' }, allBox, `${shown.length} of ${repos.length} repositories`, S.repos.fetched ? ' · updated ' + ago(S.repos.fetched) : ''),
    h('div', { class: 'list' }, rows.length ? rows : h('div', { class: 'empty' }, f.show === 'private' && !q ? 'No private repositories. Choose "All repos" to see public ones.' : 'Nothing matches.')));
  if (prev !== null) { const s = $('repo-q'); s.focus(); s.setSelectionRange(prev, prev); }
}

function repoRow(r, addable) {
  const lrs = localRunners(r);
  const busy = repoBusy(r.full_name);
  const gh = S.ghRunners[r.full_name] || [];
  const mine = lower(lrs.map((x) => x.name));
  const others = gh.filter((x) => !mine.includes(x.name.toLowerCase()));
  const hand = (S.found || []).find((f) => f.scope && f.scope.toLowerCase() === r.full_name.toLowerCase() && !f.problem);
  const ghLink = (text, cls) => h('button', { class: 'linkbtn', title: gh.map((x) => x.name + ': ' + stateText(ghState(x))).join(', '), onclick: () => repoRunnersDialog(r.full_name) }, h('span', { class: 'pill ' + cls }, text));
  let status = h('span', { class: 'muted small' }, r.archived ? 'Archived' : 'No runner here');
  if (!r.archived && gh.length) {
    const online = gh.filter((x) => x.status === 'online').length;
    status = ghLink(gh.length === 1 ? '1 runner on GitHub' : gh.length + ' runners on GitHub', online ? 'ok' : '');
  }
  let actions = [];
  if (busy && !lrs.length) {
    status = h('span', { class: 'pill info' }, busyText(busy));
    actions = [h('button', { class: 'btn primary small', disabled: true }, busy.state === 'running' ? h('div', { class: 'spinner' }) : null, busyText(busy))];
  } else if (lrs.length) {
    // Each runner here gets its own row below; this one adds more. Adds
    // queue one after another, so a click while one runs is fine.
    status = busy ? h('span', { class: 'pill info' }, busyText(busy))
      : h('span', { class: 'pill plain ok' }, lrs.length === 1 ? '1 runner here' : lrs.length + ' runners here');
    actions = r.admin && !r.archived
      ? [h('button', { class: 'btn small', title: 'Add one more runner for ' + r.full_name + ', say a light one next to a heavy one', onclick: () => addRepos([r.full_name], true) }, 'Add runner')]
      : [];
  } else if (!r.admin) {
    // No runner can be added without admin rights; Local CI runs the
    // repo's ci.yml on this machine (and the fleet) instead.
    if (!r.archived && ciSettingsOf(r.full_name).watch) status = h('span', { class: 'pill plain ok' }, 'Local CI on');
    actions = [h('button', { class: 'btn small', title: 'Only repository admins can add runners. Local CI runs this repo\'s ci.yml on your machines instead.', disabled: r.archived, onclick: () => ciDialog(r.full_name) }, 'Local CI')];
  } else if (!r.archived && hand) {
    status = h('span', { class: 'pill warn', title: hand.dir }, 'Set up by hand here');
    actions = [h('button', { class: 'btn primary small', onclick: () => adopt(hand) }, 'Move into gh-runners')];
  } else if (!r.archived) {
    actions = [h('button', { class: 'btn primary small', onclick: () => addRepos([r.full_name]) }, 'Add runner')];
  }
  const row = h('div', { class: 'item' + (r.archived ? ' dim' : '') + (lrs.length ? ' has-runners' : ''), 'data-repo': r.full_name },
    h('input', { type: 'checkbox', disabled: !addable, checked: S.picked.has(r.full_name), 'aria-label': 'Select ' + r.full_name,
      onchange: (e) => { e.target.checked ? S.picked.add(r.full_name) : S.picked.delete(r.full_name); renderRepos(); } }),
    h('div', {},
      h('div', { class: 'name' }, h('a', { href: r.url, target: '_blank', rel: 'noopener' }, r.full_name)),
      r.description ? h('div', { class: 'desc' }, r.description) : null,
      h('div', { class: 'meta' },
        h('span', { class: 'pill plain ' + (r.private ? '' : 'warn') }, r.private ? 'Private' : 'Public'),
        r.fork ? h('span', { class: 'pill plain' }, 'Fork') : null,
        lrs.length && others.length ? h('button', { class: 'linkbtn small', onclick: () => repoRunnersDialog(r.full_name) }, '+' + others.length + ' more on GitHub') : null,
        r.pushed_at ? h('span', { class: 'muted small' }, 'pushed ' + ago(r.pushed_at)) : null)),
    h('div', { class: 'status' }, status),
    h('div', { class: 'actions' }, actions));
  return [row, lrs.map(runnerRow)];
}

// runnerRow is one runner of a repository on this computer, under the
// repository's row, with its own state, labels and buttons.
function runnerRow(lr) {
  const extra = lr.labels.filter((l) => !lower(S.state.machine_labels).includes(l.toLowerCase()));
  return h('div', { class: 'item sub', 'data-runner': lr.id },
    h('span', { class: 'branch', 'aria-hidden': 'true' }),
    h('div', {},
      h('div', { class: 'name' }, h('span', { class: 'muted' }, 'Runner '), lr.name),
      h('div', { class: 'meta' },
        extra.length ? extra.map((l) => h('span', { class: 'chip custom', title: 'Label of this runner' }, l))
          : h('span', { class: 'muted small' }, 'No custom labels: takes any job for self-hosted ' + osName(S.state.os)))),
    h('div', { class: 'status' }, pill(lr.state)),
    h('div', { class: 'actions' },
      lr.disabled
        ? h('button', { class: 'btn small', onclick: () => startStop(lr, false) }, 'Start')
        : h('button', { class: 'btn small', onclick: () => startStop(lr, true) }, 'Stop'),
      lr.disabled ? null : h('button', { class: 'btn small', title: 'Stop and start again, with the current PATH and other variables', onclick: () => restartRunner(lr) }, 'Restart'),
      h('button', { class: 'btn small', onclick: () => showLog(lr) }, 'Logs'),
      S.state.auth.signed_in ? h('button', { class: 'btn small', onclick: () => editLocalLabels(lr) }, 'Edit labels') : null,
      h('button', { class: 'btn small danger', onclick: () => removeRunner(lr) }, 'Remove')));
}

// addRepos adds a runner to each repository. another adds one more to
// repositories that already have a runner here.
async function addRepos(names, another) {
  if (!another) names = names.filter((n) => !repoBusy(n));
  if (!names.length) return;
  const repos = (S.repos ? S.repos.repos : []).filter((r) => names.includes(r.full_name));
  const pub = repos.filter((r) => !r.private).map((r) => r.full_name);
  // The warning was already accepted when the first runner was added.
  if (pub.length && !another) {
    const ok = await confirmDialog('Add a runner to a public repository?', [
      h('p', {}, h('b', {}, pub.join(', ')), pub.length === 1 ? ' is public.' : ' are public.'),
      h('p', {}, 'Anyone on GitHub can open a pull request whose workflow then runs on this computer, with your files in reach. GitHub recommends self-hosted runners only for private repositories.')],
    'Add anyway', true);
    if (!ok) return;
  }
  const picked = await pickLabels(names, another);
  if (picked === null) return;
  const { labels, name } = picked;
  // Another add may have started while the dialog was open.
  if (!another) names = names.filter((n) => !repoBusy(n));
  if (!names.length) return;
  const keys = names.map((n) => n.toLowerCase());
  keys.forEach((k) => S.pending.add(k));
  names.forEach((n) => S.picked.delete(n));
  renderRepos();
  await guard(async () => {
    await api('POST', '/api/add', { repos: names, labels, allow_public: pub.length > 0, another: !!another, name: name || undefined });
    const tag = labels.length ? ' (' + labels.join(', ') + ')' : '';
    toast((names.length === 1 ? (another ? 'Adding another runner to ' : 'Adding a runner to ') + names[0] : 'Adding ' + names.length + ' runners') + tag + ' ...');
  });
  await loadTasks();
  keys.forEach((k) => S.pending.delete(k));
  renderRepos();
}

// ---------- busy repositories ----------
// Repositories a queued or running task works on, and ones whose request
// is still on its way, show as busy so a second click cannot start the
// same work twice.
function busyScopes() {
  const m = new Map();
  for (const t of S.tasks) {
    if (t.state !== 'queued' && t.state !== 'running') continue;
    const verb = t.title.startsWith('Move') ? 'Moving in' : 'Adding';
    for (const sc of t.scopes || []) {
      const k = sc.toLowerCase();
      const prev = m.get(k);
      if (!prev || prev.state !== 'running') m.set(k, { state: t.state, verb });
    }
  }
  for (const k of S.pending) if (!m.has(k)) m.set(k, { state: 'queued', verb: 'Adding' });
  return m;
}
function repoBusy(full) { return busyScopes().get(full.toLowerCase()) || null; }
function busyText(b) { return b.state === 'running' ? b.verb + ' ...' : 'Queued'; }
function busySig() { return [...busyScopes()].map(([k, v]) => k + ':' + v.state).sort().join(','); }

// ---------- labels ----------
const OS_LABELS = ['linux', 'macos', 'windows'];
const ARCH_LABELS = ['x64', 'arm64', 'arm'];
const BUILTIN = ['self-hosted'].concat(OS_LABELS, ARCH_LABELS);
function lower(v) { return (v || []).map((l) => l.toLowerCase()); }
function customOf(labels) { return labels.filter((l) => !BUILTIN.includes(l.toLowerCase())); }
function setKey(set) { return lower(set.labels).sort().join(','); }
// setFits: a runner with these fixed labels (self-hosted, OS, CPU) can take
// the jobs once it has the set's custom labels.
function setFits(set, fixed) {
  const have = lower(fixed);
  return set.labels.every((l) => { const k = l.toLowerCase(); return !(OS_LABELS.includes(k) || ARCH_LABELS.includes(k)) || have.includes(k); });
}
function setTakes(set, labels) { const have = lower(labels); return lower(set.labels).every((l) => have.includes(l)); }
function setNeeds(set, fixed) {
  const have = lower(fixed);
  const miss = set.labels.filter((l) => { const k = l.toLowerCase(); return (OS_LABELS.includes(k) || ARCH_LABELS.includes(k)) && !have.includes(k); });
  return 'Needs a ' + miss.join(' ') + ' runner';
}

// setsList shows the runs-on label sets of the workflows and whether a
// runner with these labels takes them. pick, when given, adds a checkbox.
function setsList(sets, fixed, labels, pick) {
  return h('div', { class: 'sets' }, sets.map((set) => {
    const fits = setFits(set, fixed);
    const takes = setTakes(set, labels);
    const custom = customOf(set.labels);
    return h('label', { class: 'set' + (fits ? '' : ' dim') },
      pick ? h('input', { type: 'checkbox', checked: takes && custom.length > 0, disabled: !fits || !custom.length, onchange: (e) => pick(set, e.target.checked) }) : null,
      h('div', { class: 'grow' },
        h('div', { class: 'chips' }, h('span', { class: 'muted small' }, 'runs-on:'), set.labels.map((l) => h('span', { class: 'chip' + (custom.includes(l) ? ' custom' : '') }, l))),
        h('div', { class: 'muted small' }, set.jobs + (set.jobs === 1 ? ' job' : ' jobs') + ' in ' + set.files.join(', '))),
      h('span', { class: 'pill ' + (takes ? 'ok' : fits ? '' : 'bad') }, takes ? 'Takes these jobs' : fits ? 'Not these jobs' : setNeeds(set, fixed)));
  }));
}

async function workflowSets(names, base = '') {
  const results = await Promise.all(names.map((n) => api('GET', base + '/api/workflow-labels?repo=' + encodeURIComponent(n))
    .then((d) => ({ n, sets: d.sets || [] }), (e) => ({ n, err: e.message }))));
  const byKey = new Map();
  const errors = [];
  for (const r of results) {
    if (r.err) { errors.push(r.n + ': ' + r.err); continue; }
    for (const set of r.sets) {
      const k = setKey(set);
      const files = set.files.map((f) => (names.length > 1 ? r.n.split('/')[1] + '/' + f : f));
      const cur = byKey.get(k);
      if (cur) { cur.jobs += set.jobs; cur.files.push(...files); } else byKey.set(k, { labels: set.labels, jobs: set.jobs, files });
    }
  }
  return { sets: [...byKey.values()].sort((a, b) => b.jobs - a.jobs), errors };
}

// COMMON_LABELS are offered as tick boxes in every label editor.
const COMMON_LABELS = [
  { name: 'heavy', hint: 'long jobs: builds, tests, packaging' },
  { name: 'light', hint: 'short jobs: triage, checks, bots' },
];

// labelEditor is a tick box per common label, then a chip list with an
// input to add any other label. It edits labels in place.
function labelEditor(labels, fixed, onchange) {
  const chips = h('div', { class: 'chips' });
  const common = h('div', { class: 'common-labels' });
  const input = h('input', { type: 'text', placeholder: 'Other label, e.g. gpu', 'aria-label': 'New label', onkeydown: (e) => { if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); take(); } } });
  const toggle = (name, on) => {
    const i = lower(labels).indexOf(name);
    if (on && i < 0) labels.push(name);
    if (!on && i >= 0) labels.splice(i, 1);
    draw(); onchange();
  };
  const draw = () => {
    mount(common, COMMON_LABELS.map((c) => {
      const isFixed = lower(fixed).includes(c.name);
      return h('label', { class: 'check common-label' + (isFixed ? ' dim' : '') },
        h('input', { type: 'checkbox', 'aria-label': c.name, checked: isFixed || lower(labels).includes(c.name), disabled: isFixed, onchange: (e) => toggle(c.name, e.target.checked) }),
        h('span', {}, h('b', {}, c.name), h('span', { class: 'muted small' }, ' ' + c.hint)));
    }));
    mount(chips,
      fixed.map((l) => h('span', { class: 'chip fixed', title: 'Set by GitHub or gh-runners' }, l)),
      labels.map((l) => h('span', { class: 'chip custom' }, l, h('button', { class: 'x', title: 'Remove ' + l, 'aria-label': 'Remove ' + l, onclick: (e) => { e.preventDefault(); labels.splice(labels.indexOf(l), 1); draw(); onchange(); } }, '×'))));
  };
  const take = () => {
    const have = lower(fixed.concat(labels));
    for (const l of split(input.value)) if (!have.includes(l.toLowerCase())) { labels.push(l); have.push(l.toLowerCase()); }
    input.value = '';
    draw(); onchange();
  };
  draw();
  return { el: h('div', { class: 'stack' }, common, chips, h('div', { class: 'row' }, input, h('button', { class: 'btn small', onclick: take }, 'Add label'))), take, redraw: draw };
}

// pickLabels asks which labels new runners get, suggesting the ones the
// repositories' workflows ask for, and, for one more runner of a single
// repository, its name. It resolves to {labels, name}, or null when
// cancelled. st and api are the dashboard state and API prefix of the
// machine the runners go on: this one by default.
function pickLabels(names, another, st = S.state, apiBase = '') {
  const fixed = st.machine_labels.slice();
  const chosen = [];
  const base = (st.config && st.config.runner_name) || st.hostname;
  const nameInput = another && names.length === 1
    ? h('input', { type: 'text', 'aria-label': 'Runner name', placeholder: base + '-2', maxlength: '64' })
    : null;
  return new Promise((resolve) => {
    let answered = false;
    const done = (v) => { answered = true; closeDialog(); resolve(v); };
    let found = null;
    const setsBox = h('div', {}, h('div', { class: 'row muted' }, h('div', { class: 'spinner' }), 'Reading the workflows ...'));
    const drawSets = () => {
      if (!found) return;
      const pick = (set, on) => {
        for (const l of customOf(set.labels)) {
          const i = lower(chosen).indexOf(l.toLowerCase());
          if (on && i < 0 && !lower(fixed).includes(l.toLowerCase())) chosen.push(l);
          if (!on && i >= 0) chosen.splice(i, 1);
        }
        editor.redraw(); drawSets();
      };
      mount(setsBox,
        found.sets.length
          ? [h('p', {}, 'Your workflows ask for these runners. Tick the jobs this computer should take:'), setsList(found.sets, fixed, fixed.concat(chosen), pick)]
          : h('p', { class: 'muted' }, 'No workflow in ' + (names.length === 1 ? names[0] : 'these repositories') + ' asks for a self-hosted runner yet. Add the labels your jobs will use, or none.'),
        found.more ? h('p', { class: 'muted small' }, 'Looked at the workflows of the first ' + found.more + ' repositories.') : null,
        found.errors.length ? h('p', { class: 'err' }, 'Could not read: ' + found.errors.join('; ')) : null);
    };
    const editor = labelEditor(chosen, fixed, drawSets);
    const n = names.length;
    openDialog({
      title: n === 1 ? (another ? 'Add another runner to ' : 'Add a runner to ') + names[0] : (another ? 'Add another runner to ' : 'Add runners to ') + n + ' repositories',
      body: [
        another ? h('p', {}, 'A runner takes one job at a time. A second runner with other labels, say light next to heavy, lets short jobs run while a long one is busy. The runners already here keep running.') : null,
        h('p', { class: 'muted' }, 'A job runs on a runner that has every label in its runs-on. With runs-on: [self-hosted, heavy], only runners labelled heavy take it.'),
        setsBox,
        h('h3', {}, 'Labels of the new runner' + (n > 1 ? 's' : '')),
        editor.el,
        h('p', { class: 'muted small' }, 'GitHub adds self-hosted, the OS and the CPU; gh-runners adds ' + list(fixed.slice(3)) + '. You can change labels later.'),
        nameInput ? [h('h3', {}, 'Name on GitHub'), nameInput,
          h('p', { class: 'muted small' }, 'Leave it empty to get the next free name, like ' + base + '-2. It must not be taken by another runner of ' + names[0] + '.')] : null],
      foot: [h('button', { class: 'btn', onclick: () => done(null) }, 'Cancel'),
        h('button', { class: 'btn primary', onclick: () => { editor.take(); done({ labels: chosen.slice(), name: nameInput ? nameInput.value.trim() : '' }); } }, n === 1 ? 'Add runner' : 'Add ' + n + ' runners')],
      onclose: () => { if (!answered) resolve(null); },
    });
    const look = names.slice(0, 20);
    workflowSets(look, apiBase).then((r) => {
      found = Object.assign(r, { more: names.length > look.length ? look.length : 0 });
      // Preselect the labels of the most used set this computer can take.
      const extra = (s) => customOf(s.labels).filter((l) => !lower(fixed).includes(l.toLowerCase()));
      const best = found.sets.find((s) => setFits(s, fixed) && extra(s).length);
      if (best && !chosen.length) chosen.push(...extra(best));
      editor.redraw();
      drawSets();
    });
  });
}

// editLabels changes the custom labels of a runner on GitHub.
function editLabels(scope, x, after, base = '') {
  const fixed = x.labels.filter((l) => !x.custom.includes(l));
  const labels = x.custom.slice();
  const setsBox = h('div', {});
  let sets = null;
  const drawSets = () => {
    if (!sets || !sets.length) { mount(setsBox); return; }
    mount(setsBox, h('p', {}, 'What the workflows of ' + scope + ' ask for:'), setsList(sets, fixed, fixed.concat(labels), (set, on) => {
      for (const l of customOf(set.labels)) {
        const i = lower(labels).indexOf(l.toLowerCase());
        if (on && i < 0) labels.push(l);
        if (!on && i >= 0) labels.splice(i, 1);
      }
      editor.redraw(); drawSets();
    }));
  };
  const editor = labelEditor(labels, fixed, drawSets);
  const save = h('button', { class: 'btn primary', onclick: () => guard(async () => {
    editor.take();
    save.disabled = true;
    try {
      await api('PUT', base + '/api/runner-labels', { repo: scope, runner_id: x.id, labels });
    } finally { save.disabled = false; }
    closeDialog();
    toast('Labels of ' + x.name + ' saved');
    if (base) loadFleet(); else refreshState();
    if (!base && scope.includes('/')) loadGhRunners(false, [scope]);
    if (after) after();
  }) }, 'Save labels');
  openDialog({
    title: 'Labels of ' + x.name,
    body: [h('p', { class: 'muted' }, 'Runner ' + x.name + ' of ' + scope + '. A job runs on it when it has every label in the job\'s runs-on.'),
      editor.el, setsBox,
      h('p', { class: 'muted small' }, 'The grey labels come from GitHub and stay. Changes apply right away; the runner does not restart.')],
    foot: [h('button', { class: 'btn', onclick: closeDialog }, 'Cancel'), save],
  });
  if (scope.includes('/')) workflowSets([scope], base).then((r) => { sets = r.sets; drawSets(); });
}

// ---------- runners GitHub knows ----------
async function loadGhRunners(refresh, only) {
  if (!S.repos || !S.state || !S.state.auth.signed_in) return;
  const repos = only || S.repos.repos.filter((r) => r.admin && !r.archived)
    .sort((a, b) => (b.private - a.private)).map((r) => r.full_name).slice(0, 200);
  if (!repos.length) return;
  try {
    const d = await api('POST', '/api/repo-runners', { repos, refresh: !!refresh });
    Object.assign(S.ghRunners, d.runners || {});
  } catch { return; }
  if (S.tab === 'repos') renderRepos();
}

function ghState(x) { return x.status === 'online' ? (x.busy ? 'busy' : 'online') : 'offline'; }

async function repoRunnersDialog(full) {
  const body = h('div', {}, h('div', { class: 'row muted' }, h('div', { class: 'spinner' }), 'Asking GitHub ...'));
  openDialog({ title: 'Runners of ' + full, body });
  let runners;
  try {
    const d = await api('POST', '/api/repo-runners', { repos: [full], refresh: true });
    if (d.errors && d.errors[full]) throw new Error(d.errors[full]);
    runners = (d.runners && d.runners[full]) || [];
    S.ghRunners[full] = runners;
  } catch (e) { mount(body, h('p', { class: 'err' }, e.message)); return; }
  if (S.tab === 'repos') renderRepos();
  mount(body,
    runners.length ? runners.map((x) => h('div', { class: 'gh-runner' },
      h('div', { class: 'row' }, h('b', { class: 'grow' }, x.name),
        x.local ? h('span', { class: 'pill plain ok' }, 'This computer') : x.found ? h('span', { class: 'pill plain warn' }, 'This computer, set up by hand') : h('span', { class: 'pill plain' }, 'Another computer'),
        pill(ghState(x))),
      h('div', { class: 'chips' }, x.labels.map((l) => h('span', { class: 'chip' + (x.custom.includes(l) ? ' custom' : '') }, l))),
      h('div', { class: 'row' },
        h('button', { class: 'btn small', onclick: () => editLabels(full, x, () => repoRunnersDialog(full)) }, 'Edit labels'),
        x.found ? h('button', { class: 'btn small primary', onclick: () => adopt(S.found.find((f) => f.dir === x.found) || { dir: x.found, name: x.name, scope: full }) }, 'Move into gh-runners') : null)))
      : h('p', { class: 'muted' }, 'No runners are registered to ' + full + '.'),
    h('p', { class: 'muted small' }, 'GitHub sends a job to an online runner that has every label in its runs-on.'));
}

// ---------- runners set up by hand ----------
async function loadFound(refresh) {
  try { S.found = (await api('GET', '/api/found' + (refresh ? '?refresh=1' : ''))) || []; } catch { return; }
  if (S.tab === 'repos') renderRepos();
  if (S.tab === 'machine') renderMachine();
}

function foundHere() {
  const dirs = new Set(S.state.runners.map((r) => r.dir));
  return (S.found || []).filter((f) => !dirs.has(f.dir));
}

function foundBanner() {
  const items = foundHere();
  if (!items.length || S.foundHidden) return null;
  return h('div', { class: 'notice info' },
    h('div', { class: 'grow' },
      h('b', {}, items.length === 1 ? 'Found a runner set up without gh-runners' : 'Found ' + items.length + ' runners set up without gh-runners'),
      h('div', { class: 'muted small' }, 'Move them in and gh-runners keeps them running, restarts them after a crash and shows them here.'),
      h('div', { class: 'found-list' }, items.map((f) => {
        const busy = f.scope ? repoBusy(f.scope) : null;
        return h('div', { class: 'found' },
          h('div', { class: 'grow' }, h('b', {}, f.name || '?'), f.scope ? h('span', { class: 'muted' }, ' · ' + f.scope) : null,
            h('div', { class: 'mono small muted' }, f.dir)),
          f.running ? h('span', { class: 'pill ok' }, 'Running') : h('span', { class: 'pill' }, 'Not running'),
          f.problem ? h('span', { class: 'err' }, f.problem)
            : h('button', { class: 'btn small primary', disabled: !!busy, onclick: () => adopt(f) }, busy ? busyText(busy) : 'Move into gh-runners'));
      }))),
    h('button', { class: 'btn ghost small', onclick: () => { S.foundHidden = true; render(); } }, 'Hide'));
}

async function adopt(f) {
  const st = S.state;
  const starts = f.starter && f.starter.length;
  const body = [
    h('p', {}, 'gh-runners takes over ', h('b', {}, f.name), f.scope ? ' (' + f.scope + ')' : '', ':'),
    h('ol', { class: 'steps' },
      h('li', {}, starts ? 'stops it and removes what starts it now: ' + f.starter.join(', ') : 'stops it if it runs'),
      h('li', {}, 'moves its folder from ', h('code', {}, f.dir), ' into ', h('code', {}, st.runners_dir)),
      h('li', {}, 'starts it from there, like the runners it set up itself')),
    h('p', {}, 'Its name, labels and registration on GitHub stay the same. It only works while the runner is not running a job.'),
    st.os === 'windows' && starts ? h('p', { class: 'muted small' }, 'Removing a Windows service needs administrator rights: Windows may ask you to allow it.') : null,
    st.os === 'darwin' ? h('p', { class: 'muted small' }, 'If it was installed with sudo as a launch daemon, gh-runners shows the commands to run first.') : null,
    st.os === 'linux' ? h('p', { class: 'muted small' }, 'If it was installed as a system service, gh-runners shows the commands to run first.') : null,
  ].filter(Boolean);
  if (!await confirmDialog('Move this runner into gh-runners?', body, 'Move it in')) return;
  const key = (f.scope || '').toLowerCase();
  if (key) { S.pending.add(key); render(); }
  await guard(async () => {
    await api('POST', '/api/adopt', { dir: f.dir });
    toast('Moving ' + f.name + ' into gh-runners ...');
  });
  await loadTasks();
  if (key) { S.pending.delete(key); render(); }
}

async function editLocalLabels(r, base = '') {
  await guard(async () => {
    const d = await api('POST', base + '/api/repo-runners', { repos: [r.scope], refresh: true });
    if (d.errors && d.errors[r.scope]) throw new Error(d.errors[r.scope]);
    const x = ((d.runners && d.runners[r.scope]) || []).find((g) => g.name === r.name);
    if (!x) throw new Error(r.name + ' is not registered to ' + r.scope + ' on GitHub');
    editLabels(r.scope, x, null, base);
  });
}

async function addOrg(org) {
  if (!await confirmDialog('Organization runner for ' + org, 'One runner on this computer will take jobs from every repository of ' + org + '. Your token needs the admin:org permission.', 'Add')) return;
  await guard(async () => { await api('POST', '/api/add', { org }); loadTasks(); });
}

// setPaused pauses or resumes a machine: m is a machine of the main host, or
// null for this one. Pausing cancels a running job, so it asks first.
async function setPaused(m, on) {
  const base = m ? mbase(m) : '';
  const name = m ? machineName(m) : (S.state ? S.state.hostname : 'this machine');
  await guard(async () => {
    if (on) {
      const res = await confirmedPost(base + '/api/pause', 'Pausing');
      if (res === null) return;
      toast(name + ' is paused');
    } else {
      await api('POST', base + '/api/resume');
      toast(name + ' is resuming');
    }
    await refreshState();
    if (S.fleet) loadFleet();
  });
}

async function startSet(m, which) {
  const base = m ? mbase(m) : '';
  await guard(async () => {
    const res = await api('POST', base + '/api/runners/start-' + which);
    toast((m ? machineName(m) + ': ' : '') + (res && res.started ? res.started + ' runner(s) starting' : 'nothing to start'));
    await refreshState();
    if (m) setTimeout(loadFleet, 1500);
  });
}

function startSetButtons(m, last, anyStopped) {
  const lastTxt = 'Run what was on before it closed' + (last != null ? ' (' + last + ')' : '');
  return [
    last === 0 ? null : h('button', { class: 'btn small', 'data-act': 'start-last', onclick: () => startSet(m, 'last') }, lastTxt),
    anyStopped ? h('button', { class: 'btn small', 'data-act': 'start-all', onclick: () => startSet(m, 'all') }, 'Run all') : null];
}

async function startStop(r, stop) {
  await guard(async () => {
    const path = `/api/runners/${encodeURIComponent(r.id)}/${stop ? 'stop' : 'start'}`;
    // Stopping cancels a job the runner is running: ask first.
    const res = stop ? await confirmedPost(path, 'Stopping') : await api('POST', path);
    if (res === null) return;
    toast(who(r) + (stop ? ' is stopping' : ' is starting'));
    await refreshState();
  });
}

// restartRunner stops the runner and starts it again, so it picks up tools
// installed since it started. A job it is running is cancelled.
async function restartRunner(r) {
  if (S.remote[r.id] === 'busy' && !await confirmBusy([r.name], 'Restarting')) return;
  await guard(async () => { await api('POST', `/api/runners/${encodeURIComponent(r.id)}/restart`); loadTasks(); });
}

async function removeRunner(r) {
  const rest = S.state.runners.filter((x) => x.id !== r.id && x.scope.toLowerCase() === r.scope.toLowerCase());
  const body = [h('p', {}, 'This stops the runner ', h('b', {}, r.name), ', removes it from ', h('b', {}, r.scope), ' on GitHub and deletes its folder:'), h('p', {}, h('code', {}, r.dir)),
    rest.length ? h('p', { class: 'muted' }, 'The other runner' + (rest.length > 1 ? 's' : '') + ' of ' + r.scope + ' here (' + rest.map((x) => x.name).join(', ') + ') keep' + (rest.length > 1 ? '' : 's') + ' running.') : null];
  if (!await confirmDialog('Remove the runner ' + r.name + '?', body, 'Remove', true)) return;
  if (S.remote[r.id] === 'busy' && !await confirmBusy([r.name], 'Removing')) return;
  await guard(async () => { await api('POST', `/api/runners/${encodeURIComponent(r.id)}/remove`); loadTasks(); });
}

function showLog(r, base = '', title) {
  const pre = h('pre', { class: 'log' }, 'Loading ...');
  let timer = null;
  let stick = true;
  pre.addEventListener('scroll', () => { stick = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 20; });
  const load = async () => {
    try {
      const d = await api('GET', r ? `${base}/api/runners/${encodeURIComponent(r.id)}/log?lines=500` : '/api/daemon-log?lines=500');
      if (pre.textContent !== d.text) { pre.textContent = d.text; if (stick) pre.scrollTop = pre.scrollHeight; }
    } catch (e) { pre.textContent = e.message; }
  };
  openDialog({ title: r ? 'Output of ' + (title || who(r)) : 'gh-runners background service log', body: pre, wide: true, onclose: () => clearInterval(timer) });
  load();
  timer = setInterval(load, 2000);
}

// ---------- local CI ----------
function ciSettingsOf(full) {
  const c = S.ci && S.ci.repos.find((x) => x.repo === full);
  return c || { repo: full, watch: false, post: false, merge: false, allow: [] };
}

function ciDur(ns) { // time.Duration in nanoseconds -> short text
  const s = Math.round((ns || 0) / 1e9);
  if (s < 60) return s + 's';
  return Math.floor(s / 60) + 'm' + (s % 60 ? ' ' + (s % 60) + 's' : '');
}

function ciStatusPill(st) {
  const cls = { PASS: 'ok', FAIL: 'bad', UNSUPPORTED: 'warn', BLOCKED: 'warn' }[st] || '';
  const text = { PASS: 'Passed', FAIL: 'Failed', SKIPPED: 'Skipped', UNSUPPORTED: 'Not supported', BLOCKED: 'Blocked' }[st] || st;
  return h('span', { class: 'pill plain ' + cls }, text);
}

function ciJobLog(run, job) {
  const pre = h('pre', { class: 'log' }, 'Loading ...');
  openDialog({ title: `Log of ${job}, ${run.repo} #${run.pr}`, body: pre, wide: true, onclose: () => ciDialog(run.repo) });
  api('GET', `/api/ci/runs/${encodeURIComponent(run.id)}/logs/${encodeURIComponent(job)}?lines=500`)
    .then((d) => { pre.textContent = d.text; pre.scrollTop = pre.scrollHeight; })
    .catch((e) => { pre.textContent = e.message; });
}

function ciDialog(full) {
  const cur = ciSettingsOf(full);
  const set = { watch: !!cur.watch, post: !!cur.post, merge: !!cur.merge };
  let timer = null;
  const allow = h('input', { type: 'text', value: list(cur.allow), placeholder: (S.state && S.state.auth.login) || 'you', 'aria-label': 'Allowed authors', onchange: () => save() });
  const save = () => guard(async () => {
    await api('PUT', '/api/ci/' + full.split('/').map(encodeURIComponent).join('/'), { ...set, allow: split(allow.value) });
    toast('Local CI settings saved for ' + full);
    await loadCI();
  });
  const opt = (key, title, hint) => sw(set[key], (v) => { set[key] = v; save(); },
    h('span', { class: 'ci-sw-text' }, h('span', {}, title), h('span', { class: 'hint' }, hint)));
  const settings = h('div', { class: 'ci-settings' },
    opt('watch', 'Run pull requests automatically', 'Every new commit on an open pull request runs here, if its author is allowed below.'),
    opt('post', 'Post results on the pull request', 'Adds a check with the result of each job. Off: results stay on this machine.'),
    opt('merge', 'Merge when everything passes', 'Only pull requests that are approved and green on every job.'),
    h('label', { class: 'field' }, 'Authors whose pull requests run on their own', allow,
      h('span', { class: 'hint' }, 'Comma separated. Empty means only you. Pull requests from forks never run on their own.')));

  const prInput = h('input', { type: 'number', min: 1, placeholder: 'PR number', 'aria-label': 'Pull request number' });
  const dry = h('input', { type: 'checkbox', checked: true });
  const approve = h('input', { type: 'checkbox' });
  const pre = h('pre', { class: 'log', hidden: true });
  const follow = (id) => {
    pre.hidden = false; pre.textContent = 'Queued ...';
    clearInterval(timer);
    const load = async () => {
      try {
        const t = await api('GET', '/api/ci/tasks/' + id);
        const stick = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 20;
        const text = (t.log || 'Waiting to start ...') + (t.error ? '\n' + t.error : '');
        if (pre.textContent !== text) { pre.textContent = text; if (stick) pre.scrollTop = pre.scrollHeight; }
        if (t.state === 'done' || t.state === 'failed' || t.state === 'cancelled') {
          clearInterval(timer); timer = null;
          await loadCI();
          mount(recent, ciRecent(full));
        }
      } catch (e) { pre.textContent = e.message; clearInterval(timer); timer = null; }
    };
    load();
    timer = setInterval(load, 2000);
  };
  const run = () => guard(async () => {
    const pr = parseInt(prInput.value, 10);
    if (!(pr > 0)) { toast('Type a pull request number', true); return; }
    const r = await api('POST', '/api/ci/run', { repo: full, pr, dry_run: dry.checked, approve: approve.checked });
    if (r.existing) toast(`Already queued as task ${r.task}`);
    follow(r.task);
  });
  prInput.onkeydown = (e) => { if (e.key === 'Enter') run(); };
  const runForm = h('div', { class: 'ci-run' },
    h('div', { class: 'row' }, prInput, h('button', { class: 'btn primary small', onclick: run }, 'Run now')),
    h('label', { class: 'check' }, dry, 'Dry run: post and merge nothing'),
    h('label', { class: 'check' }, approve, 'Run even if the author is not trusted (read the changes first)'),
    pre);

  const recent = h('div', {}, ciRecent(full));
  // A run of this repo still going on: follow it.
  const live = S.ci && S.ci.tasks.find((t) => (t.scopes || []).includes(full) && (t.state === 'queued' || t.state === 'running'));
  openDialog({
    title: 'Local CI: ' + full, wide: true, onclose: () => clearInterval(timer),
    body: h('div', {},
      h('p', { class: 'muted small' }, 'Runs this repository\'s .github/workflows/ci.yml on this machine and your other machines, without adding a runner. Only your pull requests and those of the authors below run on their own.'),
      h('h3', { class: 'ci-h' }, 'Settings'), settings,
      h('h3', { class: 'ci-h' }, 'Run a pull request'), runForm,
      h('h3', { class: 'ci-h' }, 'Recent results'), recent),
  });
  if (live) follow(live.id);
}

function ciRecent(full) {
  const runs = ((S.ci && S.ci.runs) || []).filter((r) => r.repo === full);
  if (!runs.length) return h('p', { class: 'muted small' }, 'No runs yet.');
  const rows = [];
  for (const r of runs) {
    const when = h('span', { class: 'muted small', title: r.started }, ago(r.ended || r.started));
    const head = 'PR #' + r.pr + (r.sha ? ' · ' + r.sha.slice(0, 7) : '') + (r.dry_run ? ' · dry run' : '') + (r.auto ? ' · automatic' : '') + (r.merged ? ' · merged' : '');
    if (!r.results || !r.results.length) {
      rows.push(h('tr', {}, h('td', {}, head), h('td', {}, '-'), h('td', {}, '-'), h('td', { class: 'err' }, r.error || 'Nothing ran'), h('td', {}, when), h('td', {})));
      continue;
    }
    r.results.forEach((x, i) => rows.push(h('tr', {},
      h('td', {}, i === 0 ? head : ''),
      h('td', {}, x.Job),
      h('td', {}, x.Machine || '-'),
      h('td', { title: x.Reason || '' }, ciStatusPill(x.Status), x.Reason && x.Status !== 'PASS' ? h('span', { class: 'muted small' }, ' ' + x.Reason) : null, x.Duration ? h('span', { class: 'muted small' }, ' ' + ciDur(x.Duration)) : null),
      h('td', {}, i === 0 ? when : ''),
      h('td', {}, x.Log ? h('button', { class: 'linkbtn small', onclick: () => ciJobLog(r, x.Job) }, 'Log') : null))));
  }
  return h('table', { class: 'ci-table' },
    h('thead', {}, h('tr', {}, ['Pull request', 'Job', 'Machine', 'Result', 'When', ''].map((t) => h('th', {}, t)))),
    h('tbody', {}, rows));
}

// nothingRunning explains the empty state after a start: the daemon starts
// with every runner stopped.
function nothingRunning(runners) {
  if (!runners.length || runners.some((r) => !r.disabled)) return null;
  return h('p', { class: 'muted', 'data-nothing-running': '1' }, 'Nothing is running. gh-runners starts with every runner stopped.');
}

// ---------- this machine ----------
// ciActiveRows lists Local CI jobs running right now, like a busy runner.
function ciActiveRows(jobs) {
  return (jobs || []).map((a) => h('div', { class: 'mrunner' },
    h('div', { class: 'who' },
      h('div', { class: 'name' }, a.repo + (a.pr ? ' #' + a.pr : ''), h('span', { class: 'muted small' }, ' · ' + a.job)),
      h('div', { class: 'muted small' }, 'on ' + a.machine + (a.for ? ' for ' + a.for : '') + ' · started ' + ago(a.started))),
    h('div', { class: 'row' }, pill('busy'))));
}

// ciTaskCall moves a queued Local CI run to the front, or cancels it.
function ciTaskCall(method, id, suffix) {
  return guard(async () => { await api(method, '/api/ci/tasks/' + id + suffix); await refreshState(); });
}

// ciLiveCard is the Local CI part of This machine: queued and running runs,
// and each job that runs now on this computer or another machine for it.
function ciLiveCard(st) {
  const tasks = st.ci_tasks || [];
  const jobs = st.ci_active || [];
  return h('div', { class: 'card' },
    h('div', { class: 'card-head' }, h('h3', {}, 'Local CI'),
      jobs.length ? h('span', { class: 'pill info' }, jobs.length + (jobs.length === 1 ? ' job running' : ' jobs running')) : h('span', { class: 'pill plain' }, 'Idle')),
    tasks.map((t) => h('div', { class: 'row' }, pill(t.state), h('span', {}, t.title),
      t.state === 'queued' ? h('button', { class: 'btn small', onclick: () => ciTaskCall('POST', t.id, '/next') }, 'Run next') : null,
      h('button', { class: 'btn small', onclick: () => ciTaskCall('DELETE', t.id, '') }, 'Cancel'))),
    jobs.length ? h('div', { class: 'mrunners' }, ciActiveRows(jobs))
      : h('p', { class: 'muted small' }, tasks.length ? 'Getting the jobs ready ...' : 'No Local CI job is running. Runs started from Repositories, the watch or "gh-runners ci run" show up here while they run.'));
}

// memberNotice tells a fleet member where its machines are managed.
function memberNotice() {
  const m0 = S.fleet && !S.fleet.main_host && S.fleet.member;
  const m = m0 && Object.assign({}, m0, { can_manage: !!S.fleet.can_manage });
  if (!m0 || !m0.joined) return null;
  return h('div', { class: 'notice' }, h('div', { class: 'grow' },
    'This computer is part of ' + (m.host_name || 'a main host') + '\'s fleet' + (m.connected ? '' : ' (not connected right now)') +
    (m.can_manage ? '. You can manage the fleet from the Machines tab.' : '. Other machines are managed from the main host\'s dashboard; its Machines tab shows this computer too. The main host can allow this computer to manage the fleet.')));
}

function renderMachine() {
  const el = $('tab-machine');
  const st = S.state;
  if (!st) return;
  const cards = st.runners.map((r) => {
    const remote = S.remote[r.id];
    const runsOn = '[self-hosted, ' + (r.labels.find((l) => l !== 'gh-runners') || osName(st.os)) + ']';
    return h('div', { class: 'card runner' },
      h('div', { class: 'card-head' },
        h('div', {}, h('div', { class: 'title' }, r.kind === 'org' ? r.scope + ' (organization)' : who(r)), h('div', { class: 'muted small' }, 'runner name: ' + r.name)),
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
        r.disabled || st.paused ? null : h('button', { class: 'btn', title: 'Stop and start again, with the current PATH and other variables', onclick: () => restartRunner(r) }, 'Restart'),
        h('button', { class: 'btn', onclick: () => showLog(r) }, 'Logs'),
        r.kind !== 'org' && st.auth.signed_in ? h('button', { class: 'btn', onclick: () => editLocalLabels(r) }, 'Edit labels') : null,
        h('div', { class: 'grow' }),
        h('button', { class: 'btn danger', onclick: () => removeRunner(r) }, 'Remove')));
  });
  const od = st.on_demand_runners.length ? h('div', { class: 'card' },
    h('h3', {}, 'On-demand runners right now'),
    st.on_demand_runners.map((p) => h('div', { class: 'row' }, pill(p.state), h('span', { class: 'mono small' }, p.id), h('span', { class: 'muted small' }, 'since ' + ago(p.since))))) : null;
  mount(el,
    h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Runners on ' + st.hostname),
      h('span', { class: 'muted small' }, 'Folder: ', h('code', {}, st.runners_dir))),
    memberNotice(),
    foundBanner(),
    nothingRunning(st.runners),
    st.runners.some((r) => r.disabled) && !st.paused ? h('div', { class: 'row', 'data-gap': '1' }, startSetButtons(null, st.last_session, true)) : null,
    h('p', { class: 'muted' }, 'Target any runner of this computer from a workflow with ', h('code', {}, 'runs-on: [self-hosted, ' + st.machine_labels[3 + (st.config.labels || []).length] + ']'), ' or by its OS: ', h('code', {}, 'runs-on: [self-hosted, ' + osName(st.os) + ']'), '.'),
    od,
    ciLiveCard(st),
    cards.length ? h('div', { class: 'grid' }, cards) : h('div', { class: 'card empty' },
      h('h2', {}, 'No runners on this computer yet'),
      h('p', {}, 'Go to Repositories and click "Add runner" next to a repository, or turn on on-demand runners under Automation.'),
      h('button', { class: 'btn primary', onclick: () => selectTab('repos') }, 'Pick repositories')));
}

// ---------- machines ----------
// The main host manages other computers through their own dashboards:
// everything for machine <id> goes to /api/machines/<id> + the path the
// machine's dashboard knows.
function mbase(m) { return '/api/machines/' + encodeURIComponent(m.id); }

async function loadFleet() {
  try { S.fleet = await api('GET', '/api/machines'); } catch { return; }
  syncMachinesTab();
  if (S.tab === 'machines') { renderMachines(); loadMachineGh(); }
}

// syncMachinesTab shows the Machines tab only on a main host.
function syncMachinesTab() {
  const on = !!(S.fleet && (S.fleet.main_host || S.fleet.can_manage));
  document.querySelector('#tabs button[data-tab="machines"]').classList.toggle('hidden', !on);
  $('count-machines').textContent = on && S.fleet.machines.length ? String(S.fleet.machines.length) : '';
  if (!on && S.tab === 'machines') selectTab('machine');
}

// loadMachineGh asks each online machine which of its runners GitHub sees
// as busy.
async function loadMachineGh() {
  if (!S.fleet) return;
  await Promise.all(S.fleet.machines.filter((m) => m.online).map(async (m) => {
    try { S.mgh[m.id] = await api('GET', mbase(m) + '/api/github-runners'); } catch { /* shown as unknown */ }
  }));
  if (S.tab === 'machines') renderMachines();
}

// watchTask follows a task of a machine and reports how it ended.
function watchTask(m, res, title) {
  if (!res || !res.task) return;
  const key = m.id + ':' + res.task;
  if (S.mwatch.has(key)) return;
  S.mwatch.add(key);
  const poll = async () => {
    let t;
    try { t = await api('GET', mbase(m) + '/api/tasks/' + res.task); } catch (e) {
      if (e.status === 503) { S.mwatch.delete(key); return; } // the machine restarted: it shows up again when back
      // Any other answer from gh-runners will not change by asking again.
      if (e.status) { S.mwatch.delete(key); toast(machineName(m) + ': ' + title + ': ' + e.message, true); return; }
      return setTimeout(poll, 3000);
    }
    if (t.state === 'done') toast(machineName(m) + ': ' + (t.title || title) + ': done');
    else if (t.state === 'failed') toast(machineName(m) + ': ' + (t.title || title) + ': ' + (t.error || 'failed'), true);
    else return setTimeout(poll, 1500);
    S.mwatch.delete(key);
    loadFleet(); loadMachineGh();
  };
  setTimeout(poll, 1000);
}

function machineName(m) { return (m.info && m.info.hostname) || m.id; }

function renderMachines() {
  const el = $('tab-machines');
  const st = S.state;
  const f = S.fleet;
  if (!st || !f) return;
  const members = f.machines || [];
  if (!f.main_host) {
    // A member the main host lets manage the fleet: the host is in the list.
    mount(el, h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Machines')), members.map(machineCard));
    return;
  }
  const mine = h('div', { class: 'card' },
    h('div', { class: 'card-head' },
      h('div', {}, h('div', { class: 'row' }, h('h2', {}, st.hostname), h('span', { class: 'pill plain info' }, 'Main host'), st.paused ? h('span', { class: 'pill warn' }, 'Paused') : null),
        h('div', { class: 'muted small' }, 'gh-runners ' + st.version + ' · ' + osName(st.os) + ' · ' + st.arch + ' · this computer')),
      h('div', { class: 'row' },
        h('button', { class: 'btn small', onclick: () => setPaused(null, !st.paused) }, st.paused ? 'Resume machine' : 'Pause machine'),
        h('button', { class: 'btn', onclick: () => selectTab('machine') }, 'Manage runners'))),
    st.runners.length ? h('div', { class: 'mrunners' }, st.runners.map((r) => h('div', { class: 'mrunner' },
      h('div', { class: 'who' }, h('div', { class: 'name' }, r.kind === 'org' ? r.scope + ' (organization)' : who(r)), h('div', { class: 'muted small' }, r.name)),
      h('div', { class: 'row' }, S.remote[r.id] ? pill(S.remote[r.id]) : null, pill(r.state)))))
      : h('p', { class: 'muted' }, 'No runners on this computer.'),
    (st.ci_active || []).length ? [h('h3', {}, 'Local CI running now'), h('div', { class: 'mrunners' }, ciActiveRows(st.ci_active))] : null);
  mount(el,
    h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Machines'),
      h('button', { class: 'btn primary', onclick: addMachine }, 'Add a machine')),
    f.listening ? null : h('div', { class: 'notice bad' }, h('div', { class: 'grow' }, 'Not listening for machines' + (f.error ? ': ' + f.error : '') + '.'), h('button', { class: 'btn', onclick: () => selectTab('settings') }, 'Open settings')),
    mine,
    members.length ? members.map(machineCard) : h('div', { class: 'card empty' },
      h('h2', {}, 'No other machines yet'),
      h('p', {}, 'Add a machine to see and manage its runners from here.'),
      h('button', { class: 'btn primary', onclick: addMachine }, 'Add a machine')));
}

function machineCard(m) {
  const info = m.info || { runners: [] };
  const gh = S.mgh[m.id] || {};
  const off = !m.online;
  const why = 'Offline: this PC is not connected to the main host';
  const btn = (text, fn, cls) => h('button', { class: 'btn small' + (cls ? ' ' + cls : ''), disabled: off, title: off ? why : null, onclick: fn }, text);
  const rows = (info.runners || []).map((r) => {
    const stopped = r.state === 'stopped';
    const paused = r.state === 'paused';
    const extra = (r.labels || []).filter((l) => !BUILTIN.includes(l.toLowerCase()));
    return h('div', { class: 'mrunner', 'data-runner': r.id },
      h('div', { class: 'who' },
        h('div', { class: 'name' }, r.scope.includes('/') ? r.scope : r.scope + ' (organization)', h('span', { class: 'muted small' }, ' · ' + r.name)),
        h('div', { class: 'chips' }, extra.length ? extra.map((l) => h('span', { class: 'chip custom' }, l)) : h('span', { class: 'muted small' }, 'No custom labels'))),
      h('div', { class: 'row' }, off ? null : (gh[r.id] ? pill(gh[r.id]) : null), pill(r.state)),
      h('div', { class: 'acts' },
        stopped ? btn('Start', () => machineRunnerAction(m, r, 'start')) : btn('Stop', () => machineRunnerAction(m, r, 'stop')),
        stopped || paused ? null : btn('Restart', () => machineRunnerAction(m, r, 'restart')),
        btn('Logs', () => showLog(r, mbase(m), machineName(m) + ' · ' + r.name)),
        r.scope.includes('/') ? btn('Edit labels', () => editLocalLabels({ scope: r.scope, name: r.name }, mbase(m))) : null,
        btn('Remove', () => machineRunnerAction(m, r, 'remove'), 'danger')));
  });
  return h('div', { class: 'card machine-card', 'data-machine': m.id },
    h('div', { class: 'card-head' },
      h('div', {}, h('div', { class: 'row' }, h('h2', {}, machineName(m)), m.online ? h('span', { class: 'pill ok' }, 'Online') : h('span', { class: 'pill' }, 'Offline'), info.paused ? h('span', { class: 'pill warn' }, 'Paused') : null),
        h('div', { class: 'muted small' }, ['gh-runners ' + (info.version || '?'), osName(info.os) + (info.arch ? ' · ' + info.arch : ''),
          m.online ? 'live' : (m.last_seen ? 'last seen ' + ago(m.last_seen) : 'never connected')].join(' · '))),
      h('div', { class: 'row' },
        btn(info.paused ? 'Resume machine' : 'Pause machine', () => setPaused(m, !info.paused)),
        (info.paused || off) ? null : (info.runners || []).some((r) => r.state === 'stopped') ? [btn('Run what was on before it closed', () => startSet(m, 'last')), btn('Run all', () => startSet(m, 'all'))] : null,
        btn('Add runner', () => addToMachine(m)),
        btn('Update gh-runners', () => updateMachine(m)),
        (S.fleet.can_manage || m.id === 'host') ? null : h('button', { class: 'btn small danger', onclick: () => removeMachine(m) }, 'Remove machine'))),
    S.fleet.main_host && m.id !== 'host' ? h('div', { class: 'manage-switch' },
      sw(!!m.manage, (on) => setManage(m, on), 'Can manage the fleet'),
      h('p', { class: 'muted small' }, 'Lets that computer start, stop and watch runners here and on the other machines.')) : null,
    info.paused ? h('p', { class: 'muted small' }, 'Paused: no runner, on-demand runner, sync or update runs on this machine until you resume it.') : null,
    off ? h('p', { class: 'muted small' }, 'Offline: its runners keep running, but you can change them here only when it is back.') : null,
    rows.length ? h('div', { class: 'mrunners' }, rows) : h('p', { class: 'muted' }, 'No runners on this machine yet.'),
    info.ci && (info.ci.active || []).length ? [h('h3', {}, 'Local CI running now'), h('div', { class: 'mrunners' }, ciActiveRows(info.ci.active))] : null);
}

// machineRunnerAction runs start, stop, restart or remove on a runner of
// a machine. Stop, restart and remove ask first when GitHub says it runs a job.
async function machineRunnerAction(m, r, verb) {
  const path = `${mbase(m)}/api/runners/${encodeURIComponent(r.id)}/${verb}`;
  if (verb === 'remove') {
    const body = [h('p', {}, 'This stops the runner ', h('b', {}, r.name), ' on ', h('b', {}, machineName(m)), ', removes it from ', h('b', {}, r.scope), ' on GitHub and deletes its folder there.')];
    if (!await confirmDialog('Remove the runner ' + r.name + '?', body, 'Remove', true)) return;
  }
  await guard(async () => {
    const res = verb === 'start' ? await api('POST', path) : await confirmedPost(path, { stop: 'Stopping', restart: 'Restarting', remove: 'Removing' }[verb]);
    if (res === null) return;
    toast(r.name + ' on ' + machineName(m) + ': ' + { start: 'starting', stop: 'stopping', restart: 'restarting', remove: 'removing' }[verb] + ' ...');
    watchTask(m, res, verb + ' ' + r.name);
    setTimeout(loadFleet, 1500);
  });
}

async function updateMachine(m) {
  await guard(async () => {
    const res = await confirmedPost(mbase(m) + '/api/update', 'Updating');
    if (res === null) return;
    toast('Updating gh-runners on ' + machineName(m) + ' ...');
    watchTask(m, res, 'Update gh-runners');
  });
}

async function setManage(m, on) {
  await guard(async () => {
    await api('POST', mbase(m) + '/manage', { on });
    toast(machineName(m) + (on ? ' can now manage the fleet' : ' can no longer manage the fleet'));
    await loadFleet();
  });
}

async function removeMachine(m) {
  const ok = await confirmDialog('Remove ' + machineName(m) + '?', [
    h('p', {}, 'The runners on ', h('b', {}, machineName(m)), ' stay on that PC and keep running, but this main host can no longer manage them or make GitHub calls for it.'),
    h('p', { class: 'muted' }, 'To manage it again, add it as a machine again.')], 'Remove machine', true);
  if (!ok) return;
  await guard(async () => { await api('POST', mbase(m) + '/remove'); toast(machineName(m) + ' removed'); await loadFleet(); });
}

// addToMachine picks a repository of the machine's GitHub account, then
// its labels, and asks the machine to add the runner.
async function addToMachine(m) {
  await guard(async () => {
    const base = mbase(m);
    const [rs, st] = await Promise.all([api('GET', base + '/api/repos'), api('GET', base + '/api/state')]);
    const have = new Set((m.info.runners || []).map((r) => r.scope.toLowerCase()));
    // Repositories without admin rights take no runner, but Local CI can
    // still run their pull requests on the fleet, this machine included.
    const repos = (rs.repos || []).filter((r) => !r.archived);
    const q = h('input', { type: 'search', placeholder: 'Filter repositories', 'aria-label': 'Filter repositories' });
    const box = h('div', { class: 'list' });
    const draw = () => {
      const needle = q.value.toLowerCase();
      const shown = repos.filter((r) => !needle || r.full_name.toLowerCase().includes(needle)).slice(0, 100);
      mount(box, shown.length ? shown.map((r) => {
        const has = have.has(r.full_name.toLowerCase());
        return h('div', { class: 'item pick', 'data-repo': r.full_name },
          h('div', {}, h('div', { class: 'name' }, r.full_name),
            h('div', { class: 'meta' }, h('span', { class: 'pill plain ' + (r.private ? '' : 'warn') }, r.private ? 'Private' : 'Public'), has ? h('span', { class: 'pill plain ok' }, 'Has a runner there') : null,
              ciSettingsOf(r.full_name).watch ? h('span', { class: 'pill plain ok' }, 'Local CI on') : null,
              r.admin ? null : h('span', { class: 'pill plain', title: 'Only repository admins can add runners.' }, 'No admin rights'))),
          h('div', { class: 'actions' },
            h('button', { class: 'btn small' + (r.admin ? '' : ' primary'), title: 'Run this repo\'s ci.yml on your machines, no runner needed.', onclick: () => { closeDialog(); ciDialog(r.full_name); } }, 'Local CI'),
            r.admin ? h('button', { class: 'btn small primary', onclick: () => { closeDialog(); addRunnerTo(m, st, r, has); } }, has ? 'Add another' : 'Add runner') : null));
      }) : h('div', { class: 'empty small' }, repos.length ? 'Nothing matches.' : 'No repositories on this machine\'s GitHub account.'));
    };
    q.addEventListener('input', draw);
    draw();
    openDialog({ title: 'Add a runner or Local CI on ' + machineName(m), body: [h('p', { class: 'muted' }, 'Pick a repository. Add runner sets up a runner on ' + machineName(m) + '. Local CI needs no admin rights: it runs the repo\'s ci.yml on the machines that fit each job, ' + machineName(m) + ' included.'), h('div', { class: 'toolbar' }, q), box], foot: [h('button', { class: 'btn', onclick: closeDialog }, 'Cancel')] });
    setTimeout(() => q.focus(), 50);
  });
}

async function addRunnerTo(m, st, r, another) {
  if (!r.private && !another) {
    const ok = await confirmDialog('Add a runner to a public repository?', [
      h('p', {}, h('b', {}, r.full_name), ' is public.'),
      h('p', {}, 'Anyone on GitHub can open a pull request whose workflow then runs on ' + machineName(m) + ', with its files in reach. GitHub recommends self-hosted runners only for private repositories.')], 'Add anyway', true);
    if (!ok) return;
  }
  const picked = await pickLabels([r.full_name], another, st, mbase(m));
  if (picked === null) return;
  await guard(async () => {
    const res = await api('POST', mbase(m) + '/api/add', { repos: [r.full_name], labels: picked.labels, allow_public: !r.private, another: !!another, name: picked.name || undefined });
    toast('Adding a runner to ' + r.full_name + ' on ' + machineName(m) + ' ...');
    watchTask(m, res, 'Add ' + r.full_name);
  });
}

// addMachine makes a one-time code and shows the commands to paste on the
// other PC.
async function addMachine() {
  const body = h('div', {}, h('div', { class: 'row muted' }, h('div', { class: 'spinner' }), 'Making a code ...'));
  openDialog({ title: 'Add a machine', body, foot: [h('button', { class: 'btn primary', onclick: closeDialog }, 'Done')], onclose: loadFleet });
  let inv;
  try { inv = await api('POST', '/api/machines/invite'); } catch (e) { mount(body, h('p', { class: 'err' }, e.message)); return; }
  const cmd = (label, text) => {
    const code = h('code', { class: 'cmd' }, text);
    return h('div', { class: 'stack' }, h('b', {}, label), h('div', { class: 'snippet cmd-row' }, code, h('button', { class: 'btn small', onclick: () => copy(text, code) }, 'Copy')));
  };
  const at = inv.expires ? new Date(inv.expires) : null;
  const left = at && !isNaN(at) ? Math.max(1, Math.round((at - Date.now()) / 60000)) : 0;
  mount(body,
    h('p', {}, 'Paste one of these on the other PC. The code works once and expires ' + (left ? 'in ' + left + ' minutes (at ' + at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) + ')' : 'in about an hour') + '.'),
    cmd('Windows (PowerShell)', inv.windows),
    cmd('macOS / Linux (Terminal)', inv.unix),
    h('p', { class: 'muted small' }, 'The other PC tries these addresses: ' + (inv.addrs || []).map((a) => a + ':' + inv.port).join(', ') + '. It must reach one of them.'));
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

  mount(el, h('div', { class: 'two' }, h('div', {}, account, folder, defaults), h('div', {}, svc, hostCard(), updates, about)));
}

// hostCard is the Settings card for the main host: turn this computer into
// one, or, when it is managed by one, leave it.
function hostCard() {
  const f = S.fleet;
  if (!f) return null;
  const m = f.member;
  if (m && m.joined) {
    return h('div', { class: 'card', id: 'host-card' },
      h('div', { class: 'card-head' }, h('div', {}, h('h2', {}, 'Main host'), h('p', { class: 'muted' }, 'Managed by the main host ', h('b', {}, m.host_name || m.addr || '?'), '. It can see and change the runners on this computer, and its GitHub sign-in is used here when you have none of your own.')),
        m.connected ? h('span', { class: 'pill ok' }, 'Connected') : h('span', { class: 'pill bad' }, 'Not connected')),
      m.connected && m.since ? h('p', { class: 'muted small' }, 'Connected ' + ago(m.since) + (m.addr ? ' · ' + m.addr : '')) : null,
      m.error ? h('p', { class: 'err' }, m.error) : null,
      h('div', { class: 'row' }, h('button', { class: 'btn danger', onclick: leaveHost }, 'Leave the main host')));
  }
  return h('div', { class: 'card', id: 'host-card' },
    h('div', { class: 'card-head' }, h('div', {}, h('h2', {}, 'Main host'),
      h('p', { class: 'muted' }, 'One main host controls the runners on your other PCs: they connect to it, and you manage them all from its Machines tab. Turning it on opens TLS port ' + f.port + ' on this computer\'s network.')),
      f.main_host ? h('span', { class: 'pill ' + (f.listening ? 'ok' : 'bad') }, f.listening ? 'Listening' : 'Not listening') : null),
    f.main_host ? h('dl', { class: 'kv' }, h('dt', {}, 'Port'), h('dd', {}, String(f.port)), h('dt', {}, 'Machines'), h('dd', {}, String((f.machines || []).length))) : null,
    f.error ? h('p', { class: 'err' }, f.error) : null,
    h('div', { class: 'row' }, f.main_host
      ? h('button', { class: 'btn danger', onclick: () => setHost(false) }, 'Turn off')
      : h('button', { class: 'btn primary', onclick: () => setHost(true) }, 'Make this the main host')));
}

async function setHost(on) {
  if (!on && !await confirmDialog('Turn off the main host?', 'The machines you added disconnect and you can no longer manage them from here. They keep their runners and can be added again.', 'Turn off', true)) return;
  await guard(async () => {
    S.fleet = await api('POST', '/api/machines/host', { on });
    toast(on ? 'This computer is now the main host' : 'Main host turned off');
    syncMachinesTab();
    render();
  });
}

async function leaveHost() {
  if (!await confirmDialog('Leave the main host?', 'The main host can no longer manage the runners here. They keep running. GitHub calls here then need a sign-in of its own.', 'Leave', true)) return;
  await guard(async () => {
    const r = await api('POST', '/api/machines/leave');
    toast(r && r.told_host === false ? 'Left. The main host could not be told: remove this machine there too' : 'Left the main host');
    await loadFleet();
    await refreshState();
  });
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
  if (tab === 'machines') { renderMachines(); loadFleet(); loadRemote(); }
  if (tab === 'automation') renderAutomation();
  if (tab === 'activity') renderActivity(true);
  if (tab === 'settings') { renderSettings(); loadFleet(); }
  if (tab === 'health') renderHealth();
}

document.addEventListener('DOMContentLoaded', async () => {
  for (const b of document.querySelectorAll('#tabs button')) b.addEventListener('click', () => selectTab(b.dataset.tab));
  let tab = 'repos';
  try { tab = localStorage.getItem('ghr-tab') || 'repos'; } catch { /* ignore */ }
  await refreshState();
  await loadTasks();
  await loadFleet();
  selectTab(tab);
  loadFound(false);
  setInterval(() => { if (!document.hidden) refreshState(); }, 3000);
  setInterval(() => { if (!document.hidden || S.tasks.some((t) => t.state === 'running')) loadTasks(); }, 1500);
  setInterval(() => { if (!document.hidden && S.tab === 'machine') loadRemote(); }, 15000);
  setInterval(() => { if (!document.hidden && (S.tab === 'machines' || S.tab === 'settings')) loadFleet(); }, 10000);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) { refreshState(); loadTasks(); } });
});
