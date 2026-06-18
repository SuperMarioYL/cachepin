# VHS tape for the CachePin demo cast.
# Render with: vhs assets/demo.tape   (https://github.com/charmbracelet/vhs)
# Produces assets/demo.cast (asciinema) targeting ~30s.

Output assets/demo.cast

Set Theme "Catppuccin Mocha"
Set FontSize 18
Set Width 1100
Set Height 600
Set TypingSpeed 60ms
Set PlaybackSpeed 1.0

# --- Start CachePin in measure-only mode ---------------------------------
Type "cachepin --upstream http://localhost:8080"
Enter
Sleep 1s

# --- Point the coding agent at it ----------------------------------------
Type "export OPENAI_BASE_URL=http://localhost:8089"
Enter
Sleep 800ms

# --- Run a multi-turn session; the harness mutates history mid-way -------
# (these lines are emitted by CachePin as the agent works)
Type "# turn 12 | prefix preserved 100% | 0 tokens reprocessed"
Enter
Sleep 700ms
Type "# turn 13 | prefix preserved 41% | ~31k tokens reprocessed | MUTATION at msg[3]"
Enter
Sleep 1500ms

# --- Now turn on pinning and re-run --------------------------------------
Ctrl+C
Sleep 500ms
Type "cachepin --upstream http://localhost:8080 --pin"
Enter
Sleep 1s
Type "# turn 13 | prefix preserved 100% | 0 tokens reprocessed"
Enter
Sleep 2s
