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
