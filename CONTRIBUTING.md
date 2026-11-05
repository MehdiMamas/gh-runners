# Contributing

Thanks for helping. Bug reports, small fixes and ideas are all welcome.

## Build and test

Go is the only requirement; the version is in `go.mod`.

```sh
go build              # builds ./gh-runners
go vet ./...
go test -race ./...
gofmt -l .            # must print nothing
```

CI runs the same checks on Linux, macOS and Windows.

## Dashboard changes

The dashboard lives in `internal/web/assets`. Go tests and `node --check` do not
show whether it works, so every new or changed input is tried in a real browser:

1. Type a value, wait at least 8 seconds (past two background refreshes) and
   confirm it is still there.
2. Save it and check the saved value shows.

`scripts/ui-test` drives the dashboard in headless Chrome (`cd scripts/ui-test && pnpm install`,
then for example `pnpm tour`). Run it against a throwaway home, never your real runners.

Rules for the page code:

- Redraw a tab only through `redraw(tab)`; never call `renderX()` from a timer or fetch callback.
- Inputs accept what people type ("10min", "1 hour"), and limits show as an inline error next to the field.
- A visual fix comes with before and after screenshots.

## Pull requests

- Open an issue first for anything bigger than a small fix, so we can agree on the approach.
- Keep a pull request to one change, with tests where the code can be tested.
- Make sure the four commands above are clean before you push.
- Say in the description what changed and why, and link the issue (`Closes #123`).

## Reporting a bug

Use the issue form. Include your OS, the gh-runners version (`gh-runners --version`)
and the output of `gh-runners doctor`. Leave out tokens and private repository names.
