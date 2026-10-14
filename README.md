# gh-runners

Self-hosted GitHub Actions runners on your own Mac, Linux box and Windows PC,
without downloading a runner and running `config.sh` by hand for every repository.

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
`chmod +x` it. Double-clicking it opens the interactive menu in Terminal.

**From source:** `go install github.com/MehdiMamas/gh-runners@latest`

## Sign in

gh-runners needs a GitHub token with admin rights on the repos it manages. It
uses the first of:

1. `GH_RUNNERS_TOKEN`, `GH_TOKEN` or `GITHUB_TOKEN`
2. the GitHub CLI (`gh auth login` once; nothing else to do)
3. a token saved with `gh-runners login`

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

Run `gh-runners` with no arguments for an interactive menu: pick repos from a
list, see status, toggle sync and on-demand mode, install the service.

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
`gh-runners`, the machine's hostname label and any `--labels` you pass.

## Commands

| Command | What it does |
| --- | --- |
| `add [owner/repo]` | Download, register and start a runner for a repo (`--org` for an org, `--labels`, `--name`, `--group`, `--force`). |
| `remove [owner/repo]` | Stop, unregister and delete it (`--all`, `--force` if GitHub can't be reached). |
| `stop` / `start [owner/repo]` | Pause a runner without unregistering it. |
| `list` | Local state (running, restarting, stopped) and GitHub state (online, busy, offline). `--json`. |
| `sync` | Add a runner for every private repo you admin; `--dry-run`, `--auto on` to repeat hourly. |
| `on-demand on\|off\|status` | Watch your repos and start a one-job runner when a matching job is queued (`--max`, `--owners`). |
| `service install\|uninstall\|start\|stop\|status` | Run the daemon at login. |
| `logs [owner/repo]` | Runner output; `--daemon` for the supervisor, `-f` to follow. |
| `doctor` | Check this machine. |
| `login` | Save a personal access token (for machines without `gh`). |
| `config` | Print the config file. |

## How it works

- **State** lives in `~/.gh-runners` (override with `--home` or `GH_RUNNERS_HOME`):
  `config.json`, `runners/<owner>__<repo>/` (one actions/runner install each),
  a shared download cache, and logs.
- **Downloads** come from GitHub's official
  [actions/runner](https://github.com/actions/runner) releases and are checked
  against GitHub's SHA-256 checksums. Each runner updates itself, as usual.
- **The daemon** (`gh-runners daemon`) starts each runner's `run.sh`/`run.cmd`,
  restarts it with backoff if it exits, rotates logs and listens only on
  127.0.0.1 with a per-start secret. The CLI talks to it to start and stop runners.
- **Service**: macOS LaunchAgent (`~/Library/LaunchAgents/io.github.mehdimamas.gh-runners.plist`),
  Linux systemd user unit (`~/.config/systemd/user/gh-runners.service`, with
  lingering enabled so it runs without a login; a `@reboot` crontab entry where
  systemd isn't available), Windows `HKCU\...\Run` entry. None of them need
  root/admin. On Windows the runners therefore run while you are signed in.
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

## Upgrading

Run the install command again. It replaces the binary and restarts the
background service if it was running. Runners themselves auto-update.

## Uninstall

```sh
gh-runners remove --all
gh-runners service uninstall
rm -rf ~/.gh-runners   # and the binary
```

## Development

```sh
go test ./...
go build .
GOOS=darwin go vet ./... && GOOS=linux go vet ./...
```

CI runs tests and a service install/uninstall smoke test on Linux, macOS and
Windows for every push. Releases are built by the manually started `release`
workflow (`gh workflow run release.yml -f version=v0.2.0`).

## License

MIT, by El Mehdi Mamas.
