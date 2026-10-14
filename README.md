# gh-runners

Self-hosted GitHub Actions runners on your own Mac, Linux box and Windows PC,
without downloading a runner and running `config.sh` by hand for every repository.

Run `gh-runners` and a dashboard opens in your browser: sign in to GitHub, see
all your repositories, click **Add runner** next to one, and it downloads,
registers and starts a runner on this computer. Everything else (stop, start,
logs, a runner for every repo, on-demand runners, where the runners live, start
at login) is a click away too.

Prefer a terminal? Every action is also a command:

```sh
gh-runners add MehdiMamas/my-app   # one repo
gh-runners sync --auto on          # every private repo you own, now and in future
gh-runners on-demand on            # or: no per-repo setup at all, runners only while a job runs
gh-runners service install         # keep it all running after reboots
```

It is one static binary for macOS (Intel and Apple Silicon), Linux (x64, arm64,
arm) and Windows (x64, arm64). It also works as a GitHub CLI extension:
`gh runners ...`.

## Why

GitHub has no *account-level* runners for personal accounts. A runner belongs to
one repository, one organization or one enterprise. For a person with many
private repos that means, per repo: open Settings > Actions > Runners, copy a
token, download the runner, run `config.sh`, then keep `run.sh` alive. gh-runners
does all of that and supervises the runners afterwards.

You get three ways to work. Pick one, or mix them.

| Mode | Setup per repo | Idle cost | Good for |
| --- | --- | --- | --- |
| `add` (persistent) | one command | one idle listener process per repo (~50 MB RAM) | a few repos, fastest job start |
| `sync --auto on` | none: new private repos get a runner within an hour | same as `add`, for each synced repo | many repos that run often |
| `on-demand on` | none | nothing until a job is queued | many repos that run rarely |

**Organization alternative.** A free GitHub organization can have org-level
runners that serve every repo in it: move your repos into an org and run
`gh-runners add --org my-org` once per machine.

## Install

**macOS / Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/MehdiMamas/gh-runners/main/install.sh | sh
```

Installs to `~/.local/bin/gh-runners` and verifies the SHA-256 checksum.

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/MehdiMamas/gh-runners/main/install.ps1 | iex
```

Installs to `%LOCALAPPDATA%\Programs\gh-runners` and adds it to your user PATH.

**As a GitHub CLI extension** (any OS)

```sh
gh extension install MehdiMamas/gh-runners
gh runners doctor
```

**By hand:** download the file for your machine from
[Releases](https://github.com/MehdiMamas/gh-runners/releases/latest)
(`gh-runners-darwin-universal` runs on every Mac). On macOS, a file downloaded
with a browser is quarantined because it is not notarized; clear that once with
`xattr -d com.apple.quarantine ./gh-runners-darwin-universal`, then
`chmod +x` it. Double-clicking it opens the dashboard.

**From source:** `go install github.com/MehdiMamas/gh-runners@latest`

## Dashboard

```sh
gh-runners          # or: gh-runners ui
```

starts the background service if it isn't running and opens
`http://127.0.0.1:47321` in your browser. The installers also add a
**gh-runners** entry to the Start Menu (Windows), `~/Applications` (macOS) or
your app menu (Linux) that does the same; `gh-runners shortcut install|uninstall`
manages it.

| Tab | What you do there |
| --- | --- |
| Repositories | Every repo you can reach, filter by owner and name, **Add runner** on one or tick several and add them all. Each repo shows the runners GitHub already has for it (from any machine) and their labels, and under it one row per runner on this machine with its labels, **Stop**/**Start**, **Logs**, **Edit labels** and **Remove**. Public repos ask first. |
| This machine | Each runner here: state on this computer and on GitHub, labels (**Edit labels**), a `runs-on:` line to copy, **Stop**, **Start**, **Logs**, **Remove**. |
| Automation | A runner for every repo that matches your rules (with preview), or on-demand runners. |
| Activity | What the dashboard did, with the output of each step, and the service log. |
| Settings | GitHub account (sign in, switch, sign out), the **runners folder** (pick any folder or disk; existing runners are moved there), runner name and labels, start at login. |
| Health | The same checks as `gh-runners doctor`. |

**Labels.** Before adding a runner the dashboard asks which labels it gets. It
reads the repo's workflows, lists the label sets their `runs-on:` lines ask for
(for example `[self-hosted, heavy]` or `[self-hosted, light]`), shows which of
those jobs this machine would take, and preselects the most used set. Labels of
any runner, here or on another machine, can be changed later with **Edit
labels**; the change goes straight to GitHub, no re-registration.

**Several at once.** Click **Add runner** on as many repos as you like, or tick
them and add them together. Each repo is queued; its row says *Queued* or
*Adding…* and can't be added twice, and work on different repos runs side by
side.

**Several runners for one repo.** A runner takes one job at a time, so a quick
job (say `runs-on: [self-hosted, light]`) waits behind a long test run on the
same runner. Give the repo a second runner: **Add runner** on a repo that
already has one adds another, with its own labels (for example `heavy` on the
first, `light` on the second) and a name on GitHub (empty picks the next free
one: `<machine>-2`, `<machine>-3`, …). Clicking it again while one is still
being added queues the next. Each runner has its own folder, process and log,
and its own row under the repo; stopping or removing one never touches the
others. A runner added before this existed stays as it is: it remains the
repo's first runner, with the same folder and registration.

**Runners set up by hand.** The dashboard looks for runners made with GitHub's
own `config.sh` / `config.cmd` (in your home folder, the top of the system drive
on Windows, `/opt`, `/srv` and `/Users/Shared`). They show in a banner and on their repo's row
as *Set up by hand here*. **Move into gh-runners** stops the old runner, moves
its folder into the runners folder, keeps its registration and labels, and from
then on gh-runners runs it. A runner installed as a Windows service needs one
UAC prompt to remove that service. A system launchd daemon (macOS), or a
systemd unit (Linux) when `sudo` asks for a password, comes with the `sudo`
commands to run.

The dashboard only listens on `127.0.0.1`. The link `gh-runners ui` prints
carries a secret that unlocks it in your browser once (it is kept in a cookie);
other web pages and other users on the machine can't use it. On a remote
machine, forward the port: `ssh -L 47321:127.0.0.1:47321 that-machine`, then
open the link the command printed there. Change the port with
`"dashboard_port"` in `~/.gh-runners/config.json`.

## Sign in

gh-runners needs a GitHub token with admin rights on the repos it manages. It
uses the first of:

1. `GH_RUNNERS_TOKEN`, `GH_TOKEN` or `GITHUB_TOKEN`
2. a token you signed in with in the dashboard or with `gh-runners login`
3. the GitHub CLI (`gh auth login` once; nothing else to do)

In the dashboard, **Settings > GitHub account** takes a token you paste (the
button opens GitHub's token page with the right scopes ticked). Builds with an
OAuth app configured also offer **Sign in with GitHub**: you type a short code
on github.com and never handle a token.

For a classic token, the `repo` scope is enough for repo runners; organization
runners also need `admin:org`. A fine-grained token needs *Administration: read
and write* on the repos (and *Actions: read* for on-demand mode).

No token on a machine? Register one repo with a token from the repo's
**Settings > Actions > Runners > New self-hosted runner** page:

```sh
gh-runners add owner/repo --registration-token AAAA...
```

## Quick start

```sh
gh-runners doctor                 # checks token, platform, dependencies
cd ~/code/my-app && gh-runners add   # uses the repo of the current directory
gh-runners service install        # start now and at every login
gh-runners list                   # local and GitHub state of every runner
```

Run `gh-runners` with no arguments for the dashboard, or `gh-runners menu` for
an interactive menu in the terminal.

Then target the runner in a workflow:

```yaml
jobs:
  build:
    runs-on: [self-hosted, macOS]        # any of your Macs
    # runs-on: [self-hosted, Linux, X64]
    # runs-on: [self-hosted, mehdis-macbook-pro]   # one machine (its hostname, lowercased)
    # runs-on: [self-hosted, gh-runners]           # any machine set up by gh-runners
```

Every runner gets the labels GitHub adds (`self-hosted`, the OS, the CPU) plus
`gh-runners`, the machine's hostname label and any `--labels` you pass (for
example `--labels heavy` for jobs with `runs-on: [self-hosted, heavy]`). A job
only lands on a runner that has every label in its `runs-on:` list.

## Commands

| Command | What it does |
| --- | --- |
| `add [owner/repo]` | Download, register and start a runner for a repo (`--org` for an org, `--labels`, `--name`, `--group`, `--force`). `--another` adds one more runner to a repo that already has one (`--name` picks its name on GitHub). |
| `remove [owner/repo]` | Stop, unregister and delete it (`--all`, `--force` if GitHub can't be reached, `--runner <name>` when the repo has several here). |
| `stop` / `start [owner/repo]` | Pause a runner without unregistering it (every runner of the repo, or one with `--runner`). |
| `labels [owner/repo]` | Show or change a runner's labels on GitHub, no re-registration (`--set`, `--add`, `--remove`, `--runner`). |
| `list` | Local state (running, restarting, stopped) and GitHub state (online, busy, offline). `--json`. |
| `sync` | Add a runner for every private repo you admin; `--dry-run`, `--auto on` to repeat hourly. |
| `on-demand on\|off\|status` | Watch your repos and start a one-job runner when a matching job is queued (`--max`, `--owners`). |
| `ui` | Open the dashboard (starts the service if needed; `--no-browser` only prints the link). |
| `menu` | Interactive menu in the terminal. |
| `service install\|uninstall\|start\|stop\|restart\|status` | Run the daemon at login (`uninstall --keep-running` stops only the start at login). |
| `shortcut install\|uninstall` | App-menu entry that opens the dashboard. |
| `logs [owner/repo]` | Runner output (`--runner` to pick one); `--daemon` for the supervisor, `-f` to follow. |
| `update` | Install the latest release now (`--check` only looks). The service also does this by itself. |
| `doctor` | Check this machine. |
| `login` | Save a personal access token (for machines without `gh`). |
| `config` | Print the config file. |

## How it works

- **State** lives in `~/.gh-runners` (override with `--home` or `GH_RUNNERS_HOME`):
  `config.json`, `runners/<owner>__<repo>/` (one actions/runner install each;
  more runners of the same repo go in `runners/<owner>__<repo>__<name>/`;
  the dashboard can move this folder anywhere), a shared download cache, logs
  and the dashboard secret.
- **Downloads** come from GitHub's official
  [actions/runner](https://github.com/actions/runner) releases and are checked
  against GitHub's SHA-256 checksums. Each runner updates itself, as usual.
- **The daemon** (`gh-runners daemon`) starts each runner's `run.sh`/`run.cmd`,
  restarts it with backoff if it exits, rotates logs and listens only on
  127.0.0.1 with a per-start secret. The CLI talks to it to start and stop
  runners, and it serves the dashboard on the same port.
- **Service**: macOS LaunchAgent (`~/Library/LaunchAgents/io.github.mehdimamas.gh-runners.plist`),
  Linux systemd user unit (`~/.config/systemd/user/gh-runners.service`, with
  lingering enabled so it runs without a login; a `@reboot` crontab entry where
  systemd isn't available), Windows `HKCU\...\Run` entry. None of them need
  root/admin. On Windows the runners therefore run while you are signed in.
  A copy with its own state directory (`--home` or `GH_RUNNERS_HOME`) gets its
  own entry and app shortcut, named with a short hash of the directory
  (`gh-runners-1a2b3c4d`), so it never replaces or removes the main one's.
- **Sync** lists the repos you can admin (`owner`, `collaborator`, `organization_member`),
  keeps the ones that match the filter (default: private, not archived, not a
  fork, owned by you) and adds a runner for each. With `prune`, it removes
  runners it created for repos that no longer match; runners you added by hand
  are never pruned.
- **On-demand** polls recently pushed repos with conditional requests (ETags, so
  unchanged answers don't count against the API rate limit), counts queued jobs
  whose `runs-on` labels this machine has, and starts a
  [just-in-time runner](https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/autoscaling-with-self-hosted-runners#using-just-in-time-runners)
  for each. A JIT runner takes exactly one job and unregisters itself. Repos
  that have a persistent runner on this machine are skipped. Expect a job to
  start within one poll interval (60 s by default) plus about 10 s.

Tune sync and on-demand in `config.json` (`gh-runners config` prints it):

```json
{
  "labels": ["gh-runners"],
  "sync": { "auto": true, "interval": "1h", "owners": ["MehdiMamas", "my-org"], "exclude": ["*-archive"], "prune": false },
  "on_demand": { "enabled": true, "poll": "30s", "max_concurrent": 2, "include": ["api-*"], "active_within": "336h" }
}
```

`include` / `exclude` take globs on `owner/repo` or just `repo`.

## Security

- **Do not use self-hosted runners on public repositories.** Anyone can open a
  pull request whose workflow then runs on your machine. gh-runners refuses public
  repos unless you pass `--allow-public`, and sync and on-demand skip them unless
  `include_public` is set.
- Runners run as your user, with your files and credentials in reach. Only
  run workflows you trust; consider a separate user account or a VM.
- The token is read from the GitHub CLI or the environment when possible;
  `gh-runners login` stores it in `~/.gh-runners/token` with mode 0600.

## Cost

Self-hosted runners don't use your GitHub Actions minutes. GitHub announced a
$0.002/minute platform fee for self-hosted runners on private repos from
March 2026 and then postponed it after feedback; check
[GitHub's pricing](https://docs.github.com/en/billing/managing-billing-for-your-products/managing-billing-for-github-actions/about-billing-for-github-actions)
for the current state.

## Updates

gh-runners updates itself. The background service checks for a new release
every few hours, downloads it, checks it against the release's SHA-256
checksums, replaces its own binary and restarts. It only does this while no
runner on the machine is running a job (it asks GitHub whether your runners
are busy), so jobs are never cut off; a pending update waits and retries.
Runners themselves auto-update too, as GitHub's runner program always does.

- Turn it off, or update right away, under **Settings > Updates** in the
  dashboard, or run `gh-runners update` (`--check` only looks).
- Versions before v0.3.0 don't have the updater: run the install command
  once more to get it. After that, updates are automatic.
- The install command still works any time and always installs the latest
  release.

## Uninstall

```sh
gh-runners remove --all
gh-runners service uninstall
gh-runners shortcut uninstall
rm -rf ~/.gh-runners   # and the binary (and your runners folder, if you moved it)
```

## Development

```sh
go test ./...
go build .
GOOS=darwin go vet ./... && GOOS=linux go vet ./...
```

CI runs tests, a service install/uninstall smoke test and the install scripts
on Linux, macOS and Windows for every push. The manual `e2e` workflow registers
real runners on hosted macOS and Linux (x64 and arm64) machines for a private
test repo, two per machine, waits for each to run a job and removes one (see
the comments in `.github/workflows/e2e.yml`). `scripts/ui-test` drives the
dashboard in headless Chrome (`npm install` there, then `node tour.js`,
`node flow.js` or `node multi.js` with the unlock link, a screenshot folder and
a test repo); run it against `gh-runners --home <temp dir> ui --no-browser`,
never your real runners, and remove what it leaves with `service uninstall` and
`shortcut uninstall` on that home. Releases are built by the manually started `release`
workflow (`gh workflow run release.yml -f version=v0.2.0`).

## License

MIT, by El Mehdi Mamas.
