# AGENTS.md

## What it is
gh-runners: one static Go binary that sets up and supervises self-hosted GitHub Actions runners on macOS, Linux and Windows, with a browser dashboard and a CLI (`gh-runners add|sync|on-demand|service ...`). Also works as a `gh` extension. See `README.md`.

## Setup, build, test
- Go version comes from `go.mod`. No other setup.
- Build: `go build` (produces `./gh-runners`).
- Test: `go vet ./...` and `go test -race ./...`. Format check: `gofmt -l .` must print nothing.
- Release binaries: `scripts/build-release.sh <version>` (cross-compiles into `dist/`; optional env `GH_RUNNERS_OAUTH_CLIENT_ID`).
- Browser UI tests: `scripts/ui-test/` (Node scripts, own `package.json`, pnpm: `cd scripts/ui-test && pnpm install`, then e.g. `pnpm tour`).

## Where things live
- `main.go`: entry point. `internal/`: `cli`, `daemon`, `runner`, `service`, `store`, `github`, `web` (dashboard, assets in `internal/web/assets`), `fleet`, `localci`, `doctor`, `update`, and others.
- `install.sh`, `install.ps1`: installers. `docs/`: images and screenshots.

## CI
`.github/workflows`: `ci.yml` (gofmt, vet, race tests on Linux, macOS, Windows), `e2e.yml` (manual end-to-end), `release.yml` (manual release).

## Start behaviour (#144)
- The daemon writes `~/.gh-runners/daemon-live` at every start (`startStopped`) and removes it on a deliberate stop (`/shutdown` via `markExit`, `service.Stop`/`Uninstall`). A marker still there at start means a reboot, logout, crash or signal: runners that were on stay on. Otherwise a fresh start stops them (`stopAtStart`).

## Dashboard rules (also in CLAUDE.md)
- Test every new or changed input in a real browser: type, wait 8+ seconds, confirm it survives, save, check the saved value.
- Redraw tabs only through `redraw(tab)`, never call `renderX()` from timers or fetch callbacks (`TestAppJSRedrawsThroughGuard` enforces it).
- Inputs accept human forms ("10min", "1 hour"); show limits inline with Save disabled.
- Visual fixes get before and after screenshots on the issue.

## Windows keepalive (#156)
- `service install` on Windows sets the HKCU Run key (login) and a per-user scheduled task `gh-runners keepalive` (`internal/service/keepalive.go`, every 5 min, no elevation, `conhost.exe --headless` so no window; a copy with its own home gets `gh-runners keepalive (gh-runners-<hash>)`). The task runs `daemon --detach --keepalive`, which starts a daemon only when the `daemon-live` marker exists: a kill or crash is revived, a deliberate stop (`service stop`, `/shutdown`) is not. `uninstall` deletes the task; `doctor` warns when the Run key exists without it.

## Local CI `ci run` flags
- `--machine <name or id>`: all jobs go to that machine (this PC or a linked account PC), unknown name is an error before anything runs (`control.SelectCIMachine`). `--json`: one JSON summary on stdout, table on stderr, exit 0 only if every job passed (`internal/cli/cirunjson.go`). `--workflow <path>`: read jobs from another file under `.github/workflows/` (`localci.ValidateWorkflowPath`). Example: `gh-runners ci run owner/repo 42 --machine build-pc --jobs mobile --approve --json`.
- Local CI job limits and verdicts (#137): a job ends `FAIL` "timed out after ..." after its `timeout-minutes` (default `localci.DefaultJobTimeout`, 60 min); a step may have its own `timeout-minutes`. A step whose context ended never passes (`runCmd`). A step that exits 0 but prints a failure marker (`testFailMarkers`, `internal/localci/markers.go`) fails; a failed step printing an `infraMarkers` line (Dart compiler crash) is rerun once (`::retry step N`), then the job fails with `Result.Infra` and the PR comment says "Infrastructure:".
- `--jobs` names are checked before anything is created (`validateJobs`, `internal/localci/run.go`, #148): an unknown name or an empty selection is a `Run` error, so nothing is posted. A remote machine that drops mid-job sets `Result.Offline`; `Dispatcher.RunJob` then runs the job once on another machine (log line `::retry machine ...`), else the result is `Blocked`, never a failure. Test failures and `Infra` crashes are not retried.
- Job env `GH_RUNNERS_NEEDS: "flutter java"` (space or comma separated, names from `ToolNames`) adds tools to `Job.NeedTools`, so `Requires`/`Route` only pick machines that have them; an unknown name fails the workflow parse (`parseNeedTools`, `internal/localci/workflow.go`, #152).
- `ci queue` takes `--machine` and `--jobs` too; the daemon runs them (`Server.pinnedDispatcher`, `internal/web/ci.go`, #128).
- Commit statuses: a blocked or unsupported job (and the aggregate/`ci-passed`) ends in `error` with "Blocked: <reason>", a filter-skipped job in `success` "Skipped: ...", and each status links (`target_url`) to the dashboard of the machine that hosts the run (`Options.LogURL`, set in `internal/web/ci.go`; opens only on that machine) (#126).
- A machine that runs a remote CI job keeps its log under `~/.gh-runners/ci/<owner-repo>/pr<N>-<sha>-<time>-remote/logs/` and in its `ci/history.json` (`keepRemoteLog` in `internal/fleet/ci.go`).
- The daemon's auto-update waits for remote CI jobs (`Member.CIBusy`, `Peers.CIBusy`), `ci run` runs (`localci.RunningRuns`) and active CI jobs (`localci.ActiveJobs`), as well as runner jobs (#120).
- `actions/checkout` in local CI makes the job's `src` a one-commit git repo (`gitInit`, `internal/localci/checkout.go`, #124); that commit's sha differs from `GITHUB_SHA`, use `GITHUB_SHA` for the real one.

- A script or session that starts Firebase emulators or other fixed-port servers on a gh-runners machine wraps the start in `gh-runners ci lock [--name emulator] [--timeout 30m] -- <command>` (same file lock as local CI and remote jobs; waits, exits 75 on timeout; waiters are served in arrival order, ticket queue `<name>.queue/`, #132), e.g. `gh-runners ci lock -- npx firebase emulators:exec ...` (#130).

## Local CI secrets (#138)
- Store: `internal/store/secrets.go`, `~/.gh-runners/secrets/<owner>/<repo>.json` (0600, dir 0700), `{"NAME":{"value"}|{"file"}}`; CLI `ci secret set|rm|list` (value from stdin).
- Gate: `Run` sets `Options.SecretsOK` when `Approve` is set or `PullHeadApproved` finds an APPROVED review with `commit_id == head sha` by a login named in `Allowlist` (`collaborators` does not count, a later change request or dismissal takes it back). `decideJob` (`Decide` is the no-secrets wrapper) skips a `NeedsSecrets()` job "needs secrets; runs after <who> approves commit <sha>" or "needs secrets: missing NAME" (`GITHUB_TOKEN` is never provided), else `ApplySecrets` (`internal/localci/secrets.go`) substitutes `${{ secrets.NAME }}` in step run/with/env and job env before the `${{` unsupported check. The values travel in `JobSpec.SecretEnv`/`SecretValues` (`json:"-"`); `LocalExecutor.RunJob` appends `SecretEnv` after the scrubbed env and writes the log through `maskWriter` (`mask.go`, `***`, handles split writes). `Run` also masks `Result.Reason` and adds the values to the comment's `Secrets`.
- Never leaves the machine: `Dispatcher.RunJob` routes a job with secrets through `Route(..., localOnly=true)`; no local fit or a `--machine` pin elsewhere ends `Blocked` "needs secrets, which stay on this machine".
- Watcher: `Server.approvalRerun` (`internal/web/ci.go`) queues a head that already ran once more, limited to its secret jobs, when it gets the approval (seen keys `+approved`, `+plain`).
- Security note: an approved PR runs its own code with the secrets and the user's keychain, like a self-hosted runner.

## Machine in use (#164)
- Config `busy_check` (argv, config file only, `store.Config.BusyCheck`). `Daemon.deskLoop` (`internal/daemon/deskbusy.go`) runs it every 45 s (not while manually paused) with a 15 s timeout (`proc.KillTree` on timeout; `deskbusy_windows.go` hides the window). Exit 75 = in use, 0 = free, anything else (also a missing program) = free, logged once per change (`busy_check: ...`). `d.inUseCheck` is the test seam. Log lines: `machine in use: runners take no new jobs`, `machine free: runners back on`.
- `d.deskBusy` feeds the `max_jobs` hold (`hold.go`): `applyLimit(maxJobs, inUse)` and `holdIdle(..., inUse)` hold every idle always-on runner (`[id] holding: machine in use`), never one running a job, and lift `holdTicks` ticks after free. `jitAllowed(..., inUse)` is 0 and `jitManager.stopIdle` stops on-demand runners waiting for a job. Never touches the manual pause.
- State on disk: `~/.gh-runners/in-use` (`since <RFC3339>`, `store.InUsePath/InUse/SetInUse`), cleared at daemon start. `localci.LocalMachine` reads it, `fleet.Info.InUse` (`in_use`) carries it to members and peers (`Host.CIMachines`, `Peers.CIMachines`), `control.Status.InUse/InUseSince`. `localci.Route` skips an `InUse` machine ("<name> is in use"); `control.SelectCIMachine` clears `InUse` on a `--machine` pick. No dashboard UI for it yet.

## Memory (#149)
- The Linux systemd unit (`Unit`, `internal/service/service_linux.go`) caps the daemon and every runner job under it: `MemoryHigh=70%`, `MemoryMax=80%`, `MemorySwapMax=1G`, `OOMPolicy=continue`, so a runaway job is killed alone instead of freezing the machine (a WSL VM froze when a test step's leftover node processes filled it). `gh-runners service install` rewrites the unit.
- Job time limit: `Config.MaxJobMinutes` (`max_job_minutes` in config.json; 0 means 180, negative means off; `JobLimit()`). `Daemon.jobLimitLoop` (`internal/daemon/disk.go`) checks every minute; `runner.LongJobs` finds `Runner.Worker` processes older than the limit (`Stray.Start`, from ps `etime=` on unix, process creation time on Windows) and `proc.KillTree` kills each with all its descendants. Config file only, no dashboard input.
- GitHub health check (`internal/daemon/health.go`, `healthLoop` every 2 min, skipped while paused and on machines without their own sign-in): lists each scope's runners once and matches by name. A running runner (over 5 min) that GitHub shows `offline` gets `ProcState.GitHubOffline`/`GitHubOfflineSince`; after 5 min offline, with no job running (`runner.Busy`) and no health restart in the last 15 min, the listener is killed (PID and start time rechecked under `d.mu`) and restarts through `backoff` with `Restarts` reset. A failed listing changes no flags. `Status.GitHubCheck` (`github_check`) holds the last check; the dashboard state has `github_offline` (count), `github_check_at`, `github_check_error` and per runner `github_offline`, `github_offline_since`.
- WSL watchdog (systemd install path only, none for the cron fallback): `service install` writes `%LOCALAPPDATA%\gh-runners\wsl-watchdog.ps1` and a per-user scheduled task "gh-runners WSL watchdog" every 5 minutes (`internal/service/watchdog_linux.go`, no admin, no window via `conhost --headless`). The script runs `wsl.exe -d <distro> -u root -- systemctl start user@<uid>.service` with a 90 s timeout; two timeouts in a row terminate the distro and start again. It logs to `wsl-watchdog.log` next to it. After an `ok` start it runs an interop probe (`Test-Interop`: `sh -c 'command -v cmd.exe ...; cmd.exe /c exit 0'` in the distro, as the default user; a timeout counts as broken; a distro without Windows programs on its PATH reads as ok) that writes `wsl-interop.state` (`ok` or `broken <ISO time>`, ascii) next to the script and logs only changes (`interop broken`, `interop ok again`); it never touches the counter and never terminates the distro. `service install` also records the script folder's unix path in `~/.gh-runners/watchdog-dir` (`store.WatchdogDirPath`), because `watchdogDir()` needs `cmd.exe`, which is what breaks; `service.InteropBroken()` reads the state through that record (no interop needed), and `gh-runners doctor` shows a Fix line (`interopCheck`, `internal/doctor/doctor.go`) under WSL when it is broken (`wsl --shutdown` from Windows brings it back). At start the daemon calls `service.EnsureWatchdog()` (`watchdog_other.go` has the non-Linux stubs): when `wsl-watchdog.ps1` already exists and differs from `renderWatchdog`, it keeps the old one as `wsl-watchdog.ps1.bak` and replaces it by rename (never creates a script, never touches the task; an error is logged and retried at the next start). `service uninstall` removes the task, the script, `.bak`, the state file and `watchdog-dir` (folder from the record when `cmd.exe` fails); the update path (`uninstallKeepRunning`) keeps them.

## Disk use (#109)
- `internal/runner/janitor.go` (old runner versions, idle `_work`) and `localci.PruneRuns` (CI runs older than 14 days) run from `internal/daemon/disk.go` at start and hourly. A runner counts as busy while a `Runner.Worker` process runs under its folder. Local CI removes each job's `src` and the run's tarball when it ends. Leftover remote job folders under `ci/fleet/jobs` (untouched for 6 h) are pruned in the same pass (`fleet.PruneJobDirs`, #117).
- Every local CI job's processes join one Windows job object with kill-on-close (`proc.Group`, `localci.RunJob`, #122), so nothing a step started outlives the job, its cancel or the daemon.

## Account machines through the relay (#94)
- PCs on one GitHub account link directly first. When the direct dial fails and a link is wanted, `internal/fleet/relay.go` links through a WebSocket relay (`GH_RUNNERS_RELAY_URL`, default `fleet.DefaultRelayURL`; empty turns it off). The same mutual TLS runs inside the pipe, so the relay sees no plaintext. Close codes 4408/4409 are normal failed attempts, retried on the next round without a log line.
- Wake files: `wants/<target id>.json` in `<login>/gh-runners-machines`, `{"from":{"<dialer id>":"<RFC3339 until>"}}`. The dialer writes its entry (2 min), the target polls its own file every 10 s and connects as `accept`.
- A link is "wanted" for 2 minutes after a Machines tab request (`Peers.WantAll`), and a `Peers.Call` or `PeerExecutor.RunJob` to an unlinked peer waits up to 25 s for it. A relay link idle for 2 minutes and not wanted is closed.
- Tests use an in-process fake relay and a fake wake store (`relay_test.go`); never the real relay or GitHub account.

Windows jobs read tool variables (JAVA_HOME, ANDROID_HOME, Path, ...) fresh from the registry, so a new install needs no logoff or daemon restart.

## Listen addresses (#175)

By default the machines ports (the main host's 47322 and the account
machines' 47323) listen on loopback and on this PC's Tailscale addresses
only, never on the LAN, and the LAN addresses are not advertised. Set
`"fleet_lan": true` in `config.json` to listen on every interface as before.
PCs without Tailscale still reach each other through the relay. A PC that
joined a main host over the LAN, with no Tailscale, cannot reach a host that
has `fleet_lan` unset: set `"fleet_lan": true` on that host. A change takes
effect at the next daemon start (or the next time the main host is switched
on).
