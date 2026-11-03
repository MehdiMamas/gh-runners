'use strict';
// Local CI: repo settings dialog, run details and the live card. Loaded
// before app.js; it only declares functions, which use app.js globals.

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

// ciElapsed is a duration since a time, as "1m 05s".
function ciElapsed(from, to) {
  const sec = Math.max(0, Math.round(((to ? new Date(to) : new Date()) - new Date(from)) / 1000));
  return sec >= 60 ? Math.floor(sec / 60) + 'm ' + String(sec % 60).padStart(2, '0') + 's' : sec + 's';
}

// ciOutcome says in plain words whether a run put a comment on the pull
// request, and when not, why and what to do.
function ciOutcome(r) {
  const o = r.outcome || {};
  if (!o.state) return null;
  const posted = o.state === 'posted';
  return h('div', { class: 'ci-outcome ' + (posted && !o.post_error ? 'ok' : 'warn'), 'data-outcome': o.state },
    h('div', {}, h('strong', {}, posted ? 'Posted. ' : 'Not posted. '), o.reason || ''),
    o.next ? h('div', { class: 'small' }, 'Next: ', o.next) : null,
    o.post_error ? h('div', { class: 'small err' }, 'GitHub said: ' + o.post_error) : null);
}

// ciEventList renders a run's feed, oldest first.
function ciEventList(events) {
  if (!events.length) return h('p', { class: 'muted small' }, 'No events recorded for this run.');
  return h('ul', { class: 'ci-events' }, events.map((e) => h('li', { class: e.kind === 'error' ? 'err' : '' },
    h('span', { class: 'muted small', title: e.time }, new Date(e.time).toLocaleTimeString()),
    ' ', e.text, e.machine ? h('span', { class: 'muted small' }, ' (' + e.machine + ')') : null)));
}

// ciRunDetails opens a run's outcome, jobs and event feed.
function ciRunDetails(run) {
  const feed = h('div', {}, h('p', { class: 'muted small' }, 'Loading ...'));
  const jobs = (run.results || []).map((x) => h('tr', {},
    h('td', {}, x.Job), h('td', {}, x.Machine || '-'),
    h('td', {}, ciStatusPill(x.Status), x.Reason && x.Status !== 'PASS' ? h('span', { class: 'muted small' }, ' ' + x.Reason) : null),
    h('td', {}, x.Log ? h('button', { class: 'linkbtn small', onclick: () => ciJobLog(run, x.Job) }, 'Log') : null)));
  openDialog({
    title: `${run.repo} #${run.pr}${run.sha ? ' · ' + run.sha.slice(0, 7) : ''}`, wide: true, onclose: () => ciDialog(run.repo),
    body: h('div', {}, ciOutcome(run),
      jobs.length ? h('table', { class: 'ci-table' }, h('thead', {}, h('tr', {}, ['Job', 'Machine', 'Result', ''].map((t) => h('th', {}, t)))), h('tbody', {}, jobs)) : null,
      h('h3', { class: 'ci-h' }, 'What happened'), feed),
  });
  api('GET', `/api/ci/runs/${encodeURIComponent(run.id)}/events`)
    .then((d) => mount(feed, ciEventList(d.events || [])))
    .catch((e) => mount(feed, h('p', { class: 'muted small' }, 'No event feed for this run (' + e.message + ').')));
}

// ciLiveRuns shows the runs of this repository going on now, each job with
// its machine, state and time, and a click-to-open log tail.
function ciLiveRuns(full, runs) {
  const mine = (runs || []).filter((r) => r.repo === full);
  if (!mine.length) return h('p', { class: 'muted small', 'data-ci-live-empty': '1' }, 'Nothing is running for this repository right now.');
  return h('div', { 'data-ci-live': '1' }, mine.map((r) => h('div', { class: 'ci-live' },
    h('div', {}, h('strong', {}, 'PR #' + r.pr), ' · ' + r.sha.slice(0, 7) + (r.title ? ' · ' + r.title : ''),
      h('span', { class: 'muted small' }, ' · running for ' + ciElapsed(r.started))),
    h('table', { class: 'ci-table' }, h('tbody', {}, (r.job_states || []).map((j) => h('tr', {},
      h('td', {}, j.job),
      h('td', {}, j.machine ? 'on ' + j.machine : '-'),
      h('td', {}, j.state === 'running' ? pill('busy') : j.state === 'queued' ? h('span', { class: 'muted small' }, 'waiting') : ciStatusPill(j.state),
        j.started ? h('span', { class: 'muted small' }, ' ' + ciElapsed(j.started, j.ended)) : null),
      h('td', {}, j.state === 'queued' ? null : h('button', { class: 'linkbtn small', onclick: () => ciJobLog({ id: r.id, repo: r.repo, pr: r.pr }, j.job) }, 'Log')))))),
    h('button', { class: 'linkbtn small', onclick: () => ciRunDetails({ id: r.id, repo: r.repo, pr: r.pr, sha: r.sha, results: [] }) }, 'What happened so far'))));
}

function ciDialog(full) {
  const cur = ciSettingsOf(full);
  const set = { watch: !!cur.watch, post: !!cur.post, merge: !!cur.merge };
  let timer = null;
  let liveTimer = null;
  const allowList = cur.allow || [];
  const isCollab = (a) => a.toLowerCase() === 'collaborators';
  const hasCollab = allowList.some(isCollab);
  let collabOn = hasCollab;
  const allow = h('input', { type: 'text', value: list(allowList.filter((a) => !isCollab(a))), placeholder: (S.state && S.state.auth.login) || 'you', 'aria-label': 'Allowed authors', onchange: () => save() });
  const save = () => guard(async () => {
    await api('PUT', '/api/ci/' + full.split('/').map(encodeURIComponent).join('/'), { ...set, allow: [...split(allow.value).filter((a) => !isCollab(a)), ...(collabOn ? ['collaborators'] : [])] });
    toast('Local CI settings saved for ' + full);
    await loadCI();
  });
  const opt = (key, title, hint) => sw(set[key], (v) => { set[key] = v; save(); },
    h('span', { class: 'ci-sw-text' }, h('span', {}, title), h('span', { class: 'hint' }, hint)));
  const settings = h('div', { class: 'ci-settings' },
    opt('watch', 'Run pull requests automatically', 'Every new commit on an open pull request runs here, if its author is allowed below.'),
    sw(hasCollab, (v) => { collabOn = v; save(); },
      h('span', { class: 'ci-sw-text' }, h('span', {}, 'Also run pull requests from repo collaborators'),
        h('span', { class: 'hint' }, 'People with write access to the repo. Forks never run on their own. Jobs that use secrets still wait for an approval from an author listed below.'))),
    opt('post', 'Post results on the pull request', 'Adds a check with the result of each job. Off: results stay on this machine.'),
    opt('merge', 'Merge when everything passes', 'Only pull requests that are approved and green on every job.'),
    h('label', { class: 'field' }, 'Authors whose pull requests run on their own', allow,
      h('span', { class: 'hint' }, 'Comma separated GitHub logins. Empty means only you. Their approval of a commit lets jobs that use secrets run.')));

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
  const liveBox = h('div', {}, h('p', { class: 'muted small' }, 'Looking ...'));
  let liveCount = 0;
  const pollLive = async () => {
    try {
      const d = await api('GET', '/api/ci/live');
      const n = (d.runs || []).filter((r) => r.repo === full).length;
      mount(liveBox, ciLiveRuns(full, d.runs));
      if (liveCount > 0 && n === 0) { await loadCI(); mount(recent, ciRecent(full)); }
      liveCount = n;
    } catch (e) { /* the next poll tries again */ }
  };
  pollLive();
  liveTimer = setInterval(pollLive, 2000);
  // A run of this repo still going on: follow it.
  const live = S.ci && S.ci.tasks.find((t) => (t.scopes || []).includes(full) && (t.state === 'queued' || t.state === 'running'));
  openDialog({
    title: 'Local CI: ' + full, wide: true, onclose: () => { clearInterval(timer); clearInterval(liveTimer); },
    body: h('div', {},
      h('p', { class: 'muted small' }, 'Runs this repository\'s .github/workflows/ci.yml on this machine and your other machines, without adding a runner. Only your pull requests and those of the authors below run on their own.'),
      h('h3', { class: 'ci-h' }, 'Settings'), settings,
      h('h3', { class: 'ci-h' }, 'Run a pull request'), runForm,
      h('h3', { class: 'ci-h' }, 'Running now'), liveBox,
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
    const oc = ciOutcome(r);
    if (oc) rows.push(h('tr', { class: 'ci-outcome-row' }, h('td', { colspan: 6 }, oc,
      r.dir ? h('button', { class: 'linkbtn small', onclick: () => ciRunDetails(r) }, 'Details and event feed') : null)));
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
