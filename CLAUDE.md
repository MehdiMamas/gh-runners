# gh-runners

## Dashboard UI (internal/web/assets)
- Every new or changed input is typed into in a real browser before the PR: type a value, wait at least 8 seconds (past two of the 3 s background refreshes), confirm it is still there, then save it and check the saved value shows. `node --check` and Go tests are not a UI check. A helper report that says "not tested in a browser" is not done.
- Background refreshes redraw a tab only through `redraw(tab)`, which skips a tab while someone types in it or has unsaved edits (`data-dirty="1"`). Never call `renderX()` from a timer, poll or fetch callback. `TestAppJSRedrawsThroughGuard` fails on the bare pattern.
- Inputs take what people type, not the program's internal format. Durations are a number plus a unit select, or accept "10min", "1 hour", "2d" (`store.ParseHumanDuration`). Show what the value means as it is typed ("Checks every 10 minutes."), and show limits as an inline error next to the field with Save disabled, never only as a toast after Save.
- A visual fix gets before and after screenshots on its issue.
