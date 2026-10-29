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

## Dashboard rules (also in CLAUDE.md)
- Test every new or changed input in a real browser: type, wait 8+ seconds, confirm it survives, save, check the saved value.
- Redraw tabs only through `redraw(tab)`, never call `renderX()` from timers or fetch callbacks (`TestAppJSRedrawsThroughGuard` enforces it).
- Inputs accept human forms ("10min", "1 hour"); show limits inline with Save disabled.
- Visual fixes get before and after screenshots on the issue.

## Local CI `ci run` flags
- `--machine <name or id>`: all jobs go to that machine (this PC or a linked account PC), unknown name is an error before anything runs (`control.SelectCIMachine`). `--json`: one JSON summary on stdout, table on stderr, exit 0 only if every job passed (`internal/cli/cirunjson.go`). `--workflow <path>`: read jobs from another file under `.github/workflows/` (`localci.ValidateWorkflowPath`). Example: `gh-runners ci run owner/repo 42 --machine DESKTOP-MEHDI --jobs mobile --approve --json`.
- A machine that runs a remote CI job keeps its log under `~/.gh-runners/ci/<owner-repo>/pr<N>-<sha>-<time>-remote/logs/` and in its `ci/history.json` (`keepRemoteLog` in `internal/fleet/ci.go`).

## Disk use (#109)
- `internal/runner/janitor.go` (old runner versions, idle `_work`) and `localci.PruneRuns` (CI runs older than 14 days) run from `internal/daemon/disk.go` at start and hourly. A runner counts as busy while a `Runner.Worker` process runs under its folder. Local CI removes each job's `src` and the run's tarball when it ends.

## Account machines through the relay (#94)
- PCs on one GitHub account link directly first. When the direct dial fails and a link is wanted, `internal/fleet/relay.go` links through a WebSocket relay (`GH_RUNNERS_RELAY_URL`, default `fleet.DefaultRelayURL`; empty turns it off). The same mutual TLS runs inside the pipe, so the relay sees no plaintext. Close codes 4408/4409 are normal failed attempts, retried on the next round without a log line.
- Wake files: `wants/<target id>.json` in `<login>/gh-runners-machines`, `{"from":{"<dialer id>":"<RFC3339 until>"}}`. The dialer writes its entry (2 min), the target polls its own file every 10 s and connects as `accept`.
- A link is "wanted" for 2 minutes after a Machines tab request (`Peers.WantAll`), and a `Peers.Call` or `PeerExecutor.RunJob` to an unlinked peer waits up to 25 s for it. A relay link idle for 2 minutes and not wanted is closed.
- Tests use an in-process fake relay and a fake wake store (`relay_test.go`); never the real relay or GitHub account.
