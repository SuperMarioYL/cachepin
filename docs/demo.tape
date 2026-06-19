# vhs tape — CachePin happy path.
# Render in CI via .github/workflows/demo.yml, or locally: `vhs docs/demo.tape`
# (needs the `cachepin` binary on PATH and the repo checked out for `go run ./bench`).
Output assets/demo.gif

Set Shell "bash"
Set FontSize 18
Set Width 1100
Set Height 640
Set Theme "Dracula"
Set Padding 18

Type "# 1) Drop CachePin between your coding agent and the model server"
Enter
Sleep 600ms
Type "cachepin --upstream http://localhost:8080   # listens on :8089"
Enter
Sleep 1.2s
Type "export OPENAI_BASE_URL=http://localhost:8089"
Enter
Sleep 1s

Type "# 2) Reproduce the before/after — replay a 50-turn transcript the harness keeps rewriting"
Enter
Sleep 600ms
Type "go run ./bench -turns 50"
Enter
Sleep 4s

Type "# Without --pin the curve climbs every turn; with --pin the upstream KV Cache survives."
Enter
Sleep 2.5s
