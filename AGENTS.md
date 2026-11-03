# AGENTS.md

## What it is
gh-runners: one static Go binary that sets up and supervises self-hosted GitHub Actions runners on macOS, Linux and Windows, with a browser dashboard and a CLI (`gh-runners add|sync|on-demand|service ...`). Also works as a `gh` extension. See `README.md`.

## Setup, build, test
- Go version comes from `go.mod`. No other setup.
- Build: `go build` (produces `./gh-runners`).
- Test: `go vet ./...` and `go test -race ./...`. Format check: `gofmt -l .` must print nothing.
- Release binaries: `scripts/build-release.sh <version>` (cross-compiles into `dist/`; optional env `GH_RUNNERS_OAUTH_CLIENT_ID`).
- Browser UI tests: `scripts/ui-test/` (Node scripts, own `package.json`).

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

## Local CI `ci run` flags
- `--machine <name or id>`: all jobs go to that machine (this PC or a linked account PC), unknown name is an error before anything runs (`control.SelectCIMachine`). `--json`: one JSON summary on stdout, table on stderr, exit 0 only if every job passed (`internal/cli/cirunjson.go`). `--workflow <path>`: read jobs from another file under `.github/workflows/` (`localci.ValidateWorkflowPath`). Example: `gh-runners ci run owner/repo 42 --machine DESKTOP-MEHDI --jobs mobile --approve --json`.
- Local CI job limits and verdicts (#137): a job ends `FAIL` "timed out after ..." after its `timeout-minutes` (default `localci.DefaultJobTimeout`, 60 min); a step may have its own `timeout-minutes`. A step whose context ended never passes (`runCmd`). A step that exits 0 but prints a failure marker (`testFailMarkers`, `internal/localci/markers.go`) fails; a failed step printing an `infraMarkers` line (Dart compiler crash) is rerun once (`::retry step N`), then the job fails with `Result.Infra` and the PR comment says "Infrastructure:".
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

## Disk use (#109)
- `internal/runner/janitor.go` (old runner versions, idle `_work`) and `localci.PruneRuns` (CI runs older than 14 days) run from `internal/daemon/disk.go` at start and hourly. A runner counts as busy while a `Runner.Worker` process runs under its folder. Local CI removes each job's `src` and the run's tarball when it ends. Leftover remote job folders under `ci/fleet/jobs` (untouched for 6 h) are pruned in the same pass (`fleet.PruneJobDirs`, #117).
- Every local CI job's processes join one Windows job object with kill-on-close (`proc.Group`, `localci.RunJob`, #122), so nothing a step started outlives the job, its cancel or the daemon.

## Account machines through the relay (#94)
- PCs on one GitHub account link directly first. When the direct dial fails and a link is wanted, `internal/fleet/relay.go` links through a WebSocket relay (`GH_RUNNERS_RELAY_URL`, default `fleet.DefaultRelayURL`; empty turns it off). The same mutual TLS runs inside the pipe, so the relay sees no plaintext. Close codes 4408/4409 are normal failed attempts, retried on the next round without a log line.
- Wake files: `wants/<target id>.json` in `<login>/gh-runners-machines`, `{"from":{"<dialer id>":"<RFC3339 until>"}}`. The dialer writes its entry (2 min), the target polls its own file every 10 s and connects as `accept`.
- A link is "wanted" for 2 minutes after a Machines tab request (`Peers.WantAll`), and a `Peers.Call` or `PeerExecutor.RunJob` to an unlinked peer waits up to 25 s for it. A relay link idle for 2 minutes and not wanted is closed.
- Tests use an in-process fake relay and a fake wake store (`relay_test.go`); never the real relay or GitHub account.

Windows jobs read tool variables (JAVA_HOME, ANDROID_HOME, Path, ...) fresh from the registry, so a new install needs no logoff or daemon restart.
