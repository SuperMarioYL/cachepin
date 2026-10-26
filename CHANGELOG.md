# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.12.0] - 2026-10-27

This release fixes four defects found by reading the shipped v0.11.0 source and
reproducing them against the tag, plus one lockstep guard. With 0 open GitHub
issues (cold-start), v0.12.0 stays bug-hunt anchored, no new breadth.

### Fixed

- **The upstream redaction now drops query-string credentials, not just
  userinfo** (milestone `fix-redacted-upstream-query-credential`).
  `RedactedUpstream` (`internal/proxy/proxy.go`) exists so an authenticated
  upstream never leaks its credential into the startup log or a 502 body, and it
  dropped `url.User` — but an OpenAI-compatible gateway key commonly rides in the
  query as `?api_key=...`, which the reverse proxy forwards on every request.
  `cachepin --upstream 'http://localhost:8080?api_key=sk-SECRET123'` printed the
  key in cleartext in the startup line, and the same string is interpolated into
  every 502 body. The display URL now has its query cleared too; nothing
  functional changes because the forwarded upstream keeps its query.

- **`cachepin --help` / `-h` exits 0 instead of reporting a usage error**
  (milestone `fix-cli-help-exits-nonzero`). `parseFlags` builds its `FlagSet`
  with `flag.ContinueOnError`, so `--help` makes `Parse` return `flag.ErrHelp`
  after the usage block is printed — but `main` treated every parse error as
  fatal, printing `cachepin: flag: help requested` on top of the usage block and
  exiting 2. A help request is a clean exit; every real usage error (unknown
  flag, missing `--upstream`) still exits 2.

- **The context-layout linter names the unmodeled field that broke the prefix**
  (milestone `fix-linter-mislabels-raw-field-breaks`). `wireBytes` marshals
  through `Message.MarshalJSON`, which emits the modeled fields in a fixed order
  and the raw passthrough fields sorted — so when `firstDifferingField`
  (`internal/openai/linter.go`) fell through to its default branch the
  difference was always an unmodeled field, yet it reported `field-order` with
  the detail "field-order changed at byte N (msg[i])". A `cache_control` change —
  the exact break v0.9.0 folded `raw` into `Hash` to detect — was therefore
  detected but misnamed. The linter now reports the differing field's own name
  (e.g. `cache_control`).

- **`ParseChatRequest` rejects trailing data after the JSON object**
  (milestone `fix-parse-accepts-trailing-garbage`).
  `internal/openai/messages.go` consumed the closing brace and stopped, so
  `{"messages":[]} trailing-garbage` parsed clean — contradicting its own
  comment, and in `--pin` mode silently deleting the accepted bytes from the
  re-marshaled request. One end-of-input check now routes such a body to the
  interceptor's documented fail-open path (forwarded untouched).

### Quality

- **The version-lockstep guard now covers the site changelog head**
  (milestone `quality-lockstep-guard-site-changelog`). `TestVersionLockstep`
  asserted `VERSION` == `web/site.json` `content_version` == `CHANGELOG.md` head
  == the embedded `version.Version`, but not `web/site.json`'s own `changelog`
  array — the second copy of the same fact that the site actually renders, and
  the surface that sat frozen at `v0.7.0` across two releases. The test now also
  asserts `changelog[0].version` == `"v" + VERSION`.

[0.12.0]: https://github.com/SuperMarioYL/cachepin/releases/tag/v0.12.0

## [0.11.0] - 2026-09-19

This release fixes the process-lifecycle bug class: a long-running proxy that
aborts in-flight requests on Ctrl-C. With 0 open GitHub issues (cold-start),
v0.11.0 stays bug-hunt anchored, no new breadth.

### Fixed

- **SIGINT/SIGTERM now drain gracefully instead of killing in-flight requests**
  (milestone `fix-sigint-kills-inflight-requests`). `run` blocked on a bare
  `ListenAndServe` with the default signal disposition, so Ctrl-C (or a
  supervisor's SIGTERM during a deploy/restart) aborted the process instantly:
  in-flight requests — including streaming `/chat/completions` generations
  mid-token — were cut off, and the deferred NDJSON sink close never ran.
  `shutdownOnSignal` now wires both signals to `http.Server.Shutdown` with a
  30-second drain window: handlers (SSE streams included) finish, remaining
  connections close, and `ErrServerClosed` is reported as a clean exit. A
  regression test proves the drain itself — an in-flight request held open
  across SIGTERM completes only after the handler releases it.

## [0.10.0] - 2026-09-04

This release fixes the version-drift bug class: the shipped code's version
surfaces were out of lockstep and the drift was unguarded, so two consecutive
releases (v0.8.0, v0.9.0) shipped with the live marketing site frozen at an
older version. With 0 open GitHub issues (cold-start), v0.10.0 stays
bug-hunt + version-drift anchored, no new breadth.

### Fixed

- **`web/site.json` `content_version` now tracks the shipped tag.** On the
  v0.9.0 tag `content_version` was `"v0.7.0"` — two minors behind — while
  `VERSION` = `0.9.0` and the `CHANGELOG.md` head = `[0.9.0]`. Neither the
  v0.8.0 nor the v0.9.0 release refreshed it, and no test caught the drift.
  `content_version` is now bumped to the shipped version in the no-`v`-prefix
  format `VERSION` and the `CHANGELOG` head already use (`"0.10.0"`), the
  hero eyebrow and on-site changelog are refreshed so the rebuilt site is
  internally consistent, and a version-lockstep test
  (`version_test.go`) asserts `VERSION` == `web/site.json` `content_version`
  == `CHANGELOG.md` head == the embedded `version.Version`. The test fails on
  the v0.9.0 tag (proving the drift was real).

- **`cachepin --version` now works; the release binary carries a real
  version.** `.goreleaser.yaml` injected the version via
  `-X main.version={{.Version}}`, but package `main` declared no `version`
  symbol and registered no `--version` flag, so the linker flag was a silent
  no-op (Go ignores `-X` on an absent symbol) and the release binary had no
  version surface — `cachepin --version` was an unknown flag. A root `version`
  package now `//go:embed`s the `VERSION` file as the single source of truth
  the binary embeds (independent of link-time injection), `cachepin --version`
  prints it, and the dead `-X main.version` ldflag is dropped.

## [0.9.0] - 2026-08-25

This release folds three repo-verified correctness fixes (each cited at an
exact file:line) rather than widening scope. Two complete the v0.8.0
`cache_control`-faithfulness mission the v0.8.0 re-marshal surface opened up:
`Message.Hash` now sees the passthrough fields the wire form carries, and the
`--pin` re-marshal preserves the harness's top-level field order instead of
alphabetizing it. The third restores the v0.4.0 eviction warning under exactly
the small `--max-sessions` it was tuned for. All three came out of an
autonomous bug-hunt; with 0 open GitHub issues (cold-start), v0.9.0 stays
bug-hunt + plan-deferral anchored, no new breadth.

### Fixed

- **`Message.Hash` now includes unmodeled per-message fields (`cache_control`,
  legacy `function_call`, provider extensions), so a `cache_control`
  toggle/change on an early message is detected as a cache break.** `Hash`
  (`internal/openai/messages.go:147`) was keyed only on the modeled
  `role`/`content`/`name`/`tool_calls`/`tool_call_id`, deliberately excluding
  the v0.8.0 passthrough `raw` map. But the upstream prefix cache keys on the
  full wire bytes, which DO include `raw` via `MarshalJSON`
  (`internal/openai/linter.go:99-107`). So when a harness changed
  `cache_control` on a message between turns (e.g.
  `{"type":"ephemeral"}` -> `{"type":"ephemeral","ttl":"5m"}`) while keeping
  `role`/`content` identical, `LongestCommonPrefix` counted the message as
  matching, the `Turn` reported `Mutated=false` / `ReprocessedTokens=0` /
  `PreservedPrefixPct=100`, and `pin.Reconcile` returned the incoming
  `UNCHANGED` — `--pin` forwarded the raw-field change verbatim, the upstream
  KV cache broke at that message and reprocessed the whole prefix while
  cachepin reported green (the byte-level `LintLayout` already flagged it as
  `field-order`, so the turn carried contradictory metrics). `Hash` now folds a
  sorted-keys representation of `m.raw` into the digest (key + `\x00` +
  raw bytes per field), mirroring `MarshalJSON`'s deterministic ordering, so any
  raw-field change mutates the hash and triggers reconciliation.

- **The `--pin` re-marshal now preserves the harness's original top-level field
  order, so the reconciled body stays byte-faithful to the verbatim turn-1
  body.** Under `--pin`, when a turn was reconciled (`changed=true`), the
  interceptor (`cmd/cachepin/main.go:197-204`) called `req.SetMessages
  (reconciled)` then `req.Marshal()`. `ChatRequest.Marshal`
  (`internal/openai/messages.go:221-248`) built a
  `map[string]json.RawMessage` and `json.Marshal(out)` serialized it with KEYS
  SORTED ALPHABETICALLY — so a harness body sent as
  `{"model":...,"messages":[...],"stream":...}` was re-emitted as
  `{"messages":[...],"model":...,"stream":...}`. Turn 1 (no prior canonical) is
  forwarded VERBATIM (harness field order); the first reconciled turn was
  re-marshaled (sorted order). The two bodies diverged at byte 3 (`'o'` from
  `"model"` vs `'e'` from `"messages"`), so the upstream prefix cache — which
  keys on the request body bytes — broke at the very start and reprocessed the
  entire history, the opposite of `--pin`'s purpose, while the tracker reported
  ~0 reprocessed (`Hash` is order-independent). `ChatRequest` now captures the
  original top-level key order in `ParseChatRequest` (a `[]string` alongside
  `raw`, populated via a streaming decoder) and `Marshal` emits in that order
  instead of via a map; modeled fields (`messages`/`model`/`stream`) are still
  re-marshaled to reflect the current state, and every other field is carried
  verbatim in its original position.

- **The pin-coverage evicted set is now sized independently of
  `--max-sessions`, so the eviction warning survives trivial churn under small
  configs.** `NewTrackerWithMaxTTL` (`internal/session/tracker.go:133-147`) set
  `evictedCap = max`, and `recordEvicted` (`internal/session/tracker.go:373-384`)
  dropped an arbitrary entry once the set was at cap. Under `--max-sessions 1`
  (the exact small-cap config `--pin` targets) with trivial churn — session A
  observed, then B (evicts A), then C (evicts B, and recording C aged A out of
  the size-1 evicted set) — when A returned, `ReconcileAndObserve` computed
  `coverageLost = prior==nil && wasEvicted`, but `wasEvicted` was false because
  A was no longer in the evicted set. So `PinCoverageLost` was false even
  though A's canonical WAS evicted and `--pin` forwarded A's mutated request
  unreconciled — the v0.4.0 warning that exists to surface the
  silent-green-while-reprocessing case was defeated under exactly the small cap
  `--max-sessions` is tuned for. `evictedCap` is now floored at 16 and scaled
  2x with the live cap, so a small `--max-sessions` no longer shrinks the
  warning set to uselessness.

### Tests

- `TestMessageHashIncludesRawPassthrough` — a `cache_control` field (and a
  change to its value) mutates `Hash`; the digest is stable across map
  iteration order. (Flips the pre-v0.9.0 `TestMessageHashIgnoresUnknownFields`
  assertion, which encoded the bug.)
- `TestObserveRawFieldChangeIsMutation` — a `cache_control` change on the
  system message between turns is caught as `Mutated=true` / `Reprocessed>0` /
  `PreservedPrefixPct<100` (no longer silent green), in-session as turn 2.
- `TestMarshalPreservesTopLevelFieldOrder` — a `SetMessages`+`Marshal`
  round-trip is byte-identical to the harness's original (non-alphabetical)
  body; `TestMarshalPreservesReorderedTopLevelFields` covers an
  anti-alphabetical order.
- `TestEvictedSetSurvivesTrivialChurnUnderSmallMaxSessions` — under
  `--max-sessions 1`, an A -> B -> C -> A churn keeps `PinCoverageLost=true`
  when A returns (the evicted set no longer ages A out).

Full suite green: `go build ./...`, `go vet ./...`, `go test -count=1 ./...`,
and `go test -race -count=1 ./...` all pass — 62 tests (58 prior + 4 new), 0
failed, across 6 packages.

## [0.8.0] - 2026-08-09

This release folds two repo-verified correctness fixes (each cited at an exact
file:line) rather than widening scope. The headline fix is a faithfulness fix to
the `--pin` reconcile re-marshal: it no longer strips `cache_control` (and other
unmodeled per-message fields) from the body forwarded upstream — dropping
`cache_control` defeats the upstream's own prompt-caching breakpoint, the
opposite of cachepin's mission. The second decouples the metrics sinks so a
broken stdout pipe can no longer silently drop the machine-readable NDJSON feed.
Both come out of an autonomous bug-hunt; with 0 open GitHub issues
(cold-start), v0.8.0 stays bug-hunt + plan-deferral anchored, no new breadth.

### Fixed

- **`--pin` no longer strips `cache_control` (and other unmodeled per-message
  fields) from the upstream body; the reconcile re-marshal is now faithful.**
  The `Message` struct (`internal/openai/messages.go:21`) modeled only
  `role`/`content`/`name`/`tool_calls`/`tool_call_id`, so `ParseChatRequest`
  dropped every other per-message field when unmarshaling into the struct. Under
  `--pin`, when a turn was reconciled (`changed=true`), the interceptor
  (`cmd/cachepin/main.go:197-204`) called `req.SetMessages(reconciled)` then
  `req.Marshal()`, re-serializing from the already-stripped structs, so
  `cache_control`, legacy `function_call`, and any provider-specific per-message
  field were silently stripped from the body forwarded upstream. Dropping
  `cache_control` can disable the upstream's own prompt-caching breakpoint. `Message`
  now carries unmodeled per-message fields in a passthrough (a raw
  `map[string]json.RawMessage` preserved on parse and re-emitted on `Marshal`),
  mirroring how `ChatRequest` already preserves unknown top-level fields. `Hash`
  stays keyed on the modeled content fields so `LongestCommonPrefix` /
  prefix-diffing is unchanged, and the modeled fields still serialize in
  declaration order so the byte-level linter's `wireBytes` is unchanged for
  messages without unknown fields.

- **The NDJSON record is now written independent of the human/stdout sink.**
  `metrics.Reporter.Report` returned early on a human (stdout) write error
  (`internal/metrics/report.go:77`) before reaching the NDJSON block, so a broken
  stdout pipe (EPIPE — cachepin piped to a process that exits, or a log collector
  that closes the fd) silently dropped every subsequent NDJSON record even though
  the NDJSON sink itself was healthy. The interceptor logged the error to stderr
  (`cmd/cachepin/main.go:205-207`, `216-218`), so it was not fully silent, but
  the machine-readable feed a dashboard/benchmark consumes stopped recording. The
  sinks are now decoupled: the NDJSON write is attempted regardless of the human
  sink's outcome, and any errors from both sinks are surfaced only after both
  have been tried.

### Tests

- `TestMessageRoundTripPreservesUnknownFields` — a message with `cache_control`
  and `function_call` round-trips through the `--pin` reconcile re-marshal
  (`SetMessages` + `Marshal`) intact.
- `TestMessageMarshalKnownFieldsUnchanged` — the modeled fields still serialize
  byte-identically in declaration order (no regression to the linter's
  `wireBytes` / `layout_byte_offset`).
- `TestMessageHashIgnoresUnknownFields` — unmodeled fields do not leak into
  `Hash`, so prefix-diffing is unchanged.
- `TestReportNDJSONSurvivesHumanWriteFailure` — a failing human writer does not
  drop the NDJSON record.
- `TestReportBothSinkErrorsSurface` — when both sinks fail, both errors surface
  and the NDJSON write was still attempted.

Full suite green: `go build ./...`, `go vet ./...`, `go test -count=1 ./...`,
and `go test -race -count=1 ./...` all pass — 58 tests (53 prior + 5 new), 0
failed, across 6 packages.

## [0.7.0] - 2026-08-04

This release folds two repo-verified correctness fixes (each cited at an exact
file:line in the `_grill_v0.7.0/_bug_findings.yaml` sidecar) rather than
widening scope. Both close a silent-green-while-reprocessing path reachable via
scenarios the README itself advertises the linter pinpoints, and the proxy gets
slowloris-hardened for the shared/team deployment posture. The launch hook also
pivots from copy re-ordering (the v0.5.0 license-lead and v0.6.0 benchmark-lead
both failed to close the clones-without-stars gap — clones widened 40 → 62 → 68
with 0 organic stars across all 6 versions) to leading with these two
substantive fixes, keeping the star CTA + 3-step quickstart.

### Fixed

- **`--pin` now survives system-prompt re-renders (session-id no longer
  forks).** `SessionID` (`internal/session/tracker.go:404`) hashed the system
  message plus the first user message, so when a coding harness re-rendered the
  system prompt (context compaction, or a tool-schema reorder embedded in the
  system message) the id changed and `observeLocked` treated it as a brand-new
  session: `PrevLen=0` → `PreservedPrefixPct` stayed `100.0`,
  `ReprocessedTokens=0`, `Mutated=false`, `Layout=NoDivergence`. CachePin printed
  "prefix preserved 100% | 0 tokens reprocessed" while the upstream actually
  invalidated its KV cache at byte 0 and reprocessed the entire prefix — the
  exact silent-green-while-reprocessing failure mode the v0.4.0/v0.6.0 warnings
  targeted. `--pin` was also defeated: `ReconcileAndObserve` ran with `prior=nil`
  so the raw mutated request was forwarded unreconciled and `PinCoverageLost`
  stayed false. `SessionID` now derives the id from the first USER message only
  (falling back to the first message of any role when no user is present),
  dropping the system bytes from the hash. A system-prompt re-render then stays
  in-session and is correctly caught as a content divergence at `msg[0]`
  (`mutated=true`, `reprocessed>0`, `layout_field=content` at byte 0), and pin
  mode gets a real prior canonical to reconcile against. Existing `SessionID`
  tests stay green (they key distinct sessions by the first-user text); the rare
  same-first-user-different-system collision is an accepted tradeoff versus the
  silent-green fork.

- **The proxy is slowloris-hardened (`ReadHeaderTimeout`/`IdleTimeout`).** `run()`
  started the listener with the bare `http.ListenAndServe(cfg.Listen, p)`
  (`cmd/cachepin/main.go:106`), which uses an `http.Server` with all timeouts at
  their zero values — no `ReadHeaderTimeout` and no `IdleTimeout` — so a slow or
  half-open client (or a harness connection left dangling after a crash) held a
  server goroutine and an idle keep-alive connection indefinitely. That is a
  resource-exhaustion / slowloris vector for exactly the "shared/team
  deployment" posture `DefaultMaxSessions` is written for: the LRU session cap
  bounds in-memory state but does nothing for the unbounded connection /
  goroutine count. The listener is now an explicit `http.Server` with
  `ReadHeaderTimeout: 10s` and `IdleTimeout: 60s` (`WriteTimeout` left at zero
  so SSE `/chat/completions` streams are not cut off mid-token); both timeouts
  are safe for the streaming response path. Extracted as `newHTTPServer` so the
  timeout contract is unit-testable without binding a real listener.

## [0.6.0] - 2026-07-28

### Fixed

- **Upstream URL credentials are redacted from 502 error responses and the
  startup log.** `errorHandler` formatted the upstream URL verbatim
  (`internal/proxy/proxy.go:107`) and `run()` printed the raw `cfg.Upstream` at
  startup (`cmd/cachepin/main.go:105`), so an operator running `cachepin
  --upstream http://user:pass@remote-model:8080` (or an API key as the username,
  `http://sk-xxx@host`) had the full credential written into the 502 response
  body and any captured stdout. `url.Redacted()` only masks the password,
  leaving a username-only API key exposed. Both sites now render the upstream via
  `proxy.RedactedUpstream`, which drops `User` entirely.

## [0.5.0] - 2026-07-23

This release folds two repo-verified correctness fixes into the v0.4.0
session/eviction story and finishes the long-deferred cacheguard archive,
rather than widening scope. Idle-TTL eviction now actually fires on every
`Observe` (not only on new-session creation), and `--pin`'s
Canonical→Reconcile→Observe is now atomic per request so a same-session
concurrent turn can no longer be silently dropped from the canonical history.

### Fixed

- **Idle-TTL eviction now runs on every `Observe`, not only when a new session
  is created.** `evictIfNeeded` (the idle-TTL back-of-list sweep) was invoked
  only on the new-session path of `Observe`, so re-observing an *existing*
  session — the common sparse long-uptime deployment where only sticky sessions
  return — never triggered it and idle back-of-list sessions leaked forever.
  Under `--max-sessions 0 --idle-ttl 10m` (the exact unbounded-count config
  `--idle-ttl` targets) this was an unbounded memory leak, and the shipped
  `TestTrackerIdleTTLEvictsQuietSession` only covered the new-session trigger.
  `evictIfNeeded` is now hoisted out of the `if s == nil` branch so it runs
  after the recency update on every `Observe`; the just-touched session is at
  the front with `lastSeen=now` so it is never a victim, but idle sessions
  expire on the next `Observe` of any session.

- **`--pin`'s Canonical+Reconcile+Observe is now atomic for the same session.**
  The `--pin` interceptor read `tracker.Canonical(sid)` (lock-clone-unlock),
  ran `pin.Reconcile` unlocked, then `tracker.Observe` (re-lock-store). Two
  concurrent goroutines for the SAME session id (parallel tool calls / batched
  chat-completions a multi-agent harness fires against one session) could
  interleave in that window — both read the same stale prior, each reconciled
  against it, and the later `Observe` overwrote the earlier, dropping a turn
  from the canonical history so the next `Reconcile` was computed against the
  wrong ground truth (silent turn-loss / wrong pin rewrite). This is the "real
  but narrow same-session concurrent race" the v0.4.0 changelog deferred to
  v0.5.0 as too big for the time budget. A new `Tracker.ReconcileAndObserve`
  method holds `t.mu` across the Canonical read, the reconcile callback, and the
  Observe store + `PinCoverageLost` computation in a single critical section;
  `pin.Reconcile` is passed as a callback (it is a pure function) to avoid a
  `session`↔`pin` import cycle. One critical section per request, no
  architecture change. A `-race` run under concurrent same-session `--pin`
  requests now keeps every turn in canonical.

### Added

- **`cacheguard` archived.** The v0.3.0 `m4-context-layout-linter` milestone's
  archive action ("set the cacheguard GitHub repo to `archived=true`") never
  executed; `SuperMarioYL/cacheguard` was still a live, recently-pushed second
  surface in the saturated KV-cache niche, contradicting the niche-WIDENING
  ban. v0.5.0 executes `gh repo archive` and verifies `archived=true` via
  `gh api`, retiring the duplicate so cachepin is the single canonical
  byte-level prefix linter. (No new repo or top-level surface is shipped — this
  retires one.)

### Changed

- **`LICENSE` is now part of the shipped-surface list.** The repo's `LICENSE`
  file (already present) is now declared in the plan's `target_files` for
  accurate bookkeeping, and the plan self-declares its GitHub repo
  (`SuperMarioYL/cachepin`) in the frontmatter to reconcile the
  orphan-oracle linkage. No code behavior changes.

## [0.4.0] - 2026-07-17

This release hardens the v0.3.0 multi-session / eviction story rather than
widening scope. The `--ndjson` dashboard log survives restarts and concurrent
multi-session traffic, `--pin` no longer reports silent-green metrics when its
coverage was just voided by eviction, and a new `--idle-ttl` bounds memory for
sparse long-uptime deployments.

### Fixed

- **`--ndjson` no longer truncates on restart or interleaves under concurrent
  traffic.** The sink was opened with `os.Create` (`O_TRUNC`), so every cachepin
  restart silently destroyed the prior benchmark/dashboard log a user asked to
  accumulate. It was also opened without `O_APPEND`, so per-turn writes from the
  proxy's per-request goroutines shared one file offset and the kernel served
  them in non-deterministic lock-grab order — a concurrent multi-session
  deployment (exactly what `--max-sessions` targets) could write turn N+1's line
  ahead of turn N's. The sink is now opened with
  `O_CREATE|O_WRONLY|O_APPEND`, and `metrics.Reporter` gained a `sync.Mutex`
  guarding both the human line and the NDJSON line so concurrent turns write
  whole, in-call-order lines. Restarting cachepin with the same `--ndjson` path
  now keeps prior turns.

- **`--pin` coverage is no longer silently voided when an LRU-evicted session
  returns.** Under `--pin` the interceptor reads `tracker.Canonical(sid)` then
  `pin.Reconcile(prior, …)`; when `sid` was evicted, `Canonical` returns `nil`
  and `Reconcile(nil, …)` short-circuits to a no-op — so the raw mutated request
  was forwarded unreconciled while `Observe` reported `turn 1` / `100% preserved`
  / `0 reprocessed` / not mutated. The headline `--pin` guarantee
  (`reprocessed_tokens ~0` because pin is working) was silently voided by the
  very eviction v0.3.0 added, with no signal. The tracker now records evicted
  session ids in a bounded set (`WasEvicted`), and the `--pin` interceptor emits
  a `WARN: canonical evicted, --pin not applied this turn (raise --max-sessions)`
  line — plus a `pin_coverage_lost:true` NDJSON field — when a returning
  session's canonical was lost, so the silent-green-while-reprocessing case is
  surfaced instead of hidden.

### Added

- **`--idle-ttl` flag** evicts sessions whose last observed turn is older than
  the given duration (e.g. `--idle-ttl 10m`, `--idle-ttl 1h`). v0.3.0's
  `--max-sessions` is a pure LRU count cap, so a session is evicted only when a
  *new* session pushes it out — a long-lived proxy serving a handful of sessions
  that each go idle kept all of them pinned in memory forever. `--idle-ttl`
  reuses the existing LRU order list for lazy idle-expiry on the next `Observe`
  (no background goroutine), so sparse long-uptime / shared deployments keep
  memory bounded even with zero new traffic. `0` (default) disables idle expiry
  and preserves the v0.3.0 count-cap-only behavior.

## [0.3.0] - 2026-06-28

This release ships a batch of repo-verified correctness fixes and finishes
consolidating the byte-level linter niche, rather than widening scope. The proxy
is now safe to leave running under real multi-session load, its `--pin` metrics
finally agree with the benchmark, and long uptime no longer leaks memory.

### Fixed

- **Concurrent multi-session traffic no longer crashes the process.** The
  per-session `canonical` map in `main` was a plain map with no mutex, but the
  interceptor runs in `httputil.ReverseProxy`'s per-request goroutine — so two
  overlapping requests for different sessions triggered a Go runtime "concurrent
  map read and map write" fatal that `net/http`'s per-request `recover` does not
  catch. The reconciled-canonical store is now folded into the (already
  mutex-guarded) session tracker, so the per-request critical section touches one
  lock and the parallel map is gone entirely.
- **`--pin` metrics now match the benchmark.** The interceptor called
  `tracker.Observe` on the raw, mutated request *before* `pin.Reconcile` ran, so
  under `--pin` the per-turn `reprocessed_tokens` / `MUTATION at msg[N]` were
  computed against two consecutive mutated requests and persistently overstated
  reprocessing — a user dashboarding `--ndjson` under `--pin` would conclude pin
  was broken when it was in fact working, directly contradicting `bench/` which
  feeds the reconciled array to the pinned tracker. The interceptor now
  reconciles first and observes the reconciled array under `--pin` (updating the
  canonical store from that same array), so a mutated-but-reconcilable turn
  reports ~0 reprocessing, matching the benchmark.
- **Stale sessions are evicted, so memory stays bounded.** Both the tracker's
  `sessions` map and `main`'s canonical map were keyed by session id and only
  ever inserted into — never pruned — so a long-lived proxy serving many distinct
  sessions accumulated entries forever, each pinning the full canonical history.
  The tracker now applies an LRU cap (`--max-sessions`, default 1024); past the
  cap the least-recently-used session is dropped. Folding the canonical store
  into the tracker gives eviction a single owner.

### Changed

- **Context-layout linter coordinates are always emitted.** `layout_byte_offset`
  and `layout_msg_index` dropped their `omitempty` tags, and the no-divergence
  case now resolves to a single `-1` sentinel everywhere. Previously a divergence
  at offset 0 / `msg[0]` lost its coordinates (0 read as empty under `omitempty`)
  and the first turn omitted `layout_msg_index` while a clean turn 2+ emitted
  `-1`, so every turn now carries the same NDJSON field set with offset 0
  preserved when it is the real coordinate. This finishes folding the byte-level
  prefix fingerprint+diff into cachepin as the portfolio's single canonical
  linter home.

### Added

- **`--max-sessions` flag** caps the number of conversations tracked at once
  (LRU eviction past it); `0` disables the cap for short-lived processes.

[0.5.0]: https://github.com/SuperMarioYL/cachepin/releases/tag/v0.5.0
[0.4.0]: https://github.com/SuperMarioYL/cachepin/releases/tag/v0.4.0
[0.3.0]: https://github.com/SuperMarioYL/cachepin/releases/tag/v0.3.0

## [0.2.0] - 2026-06-22

This release makes the proxy actually run end-to-end from the CLI and sharpens
CachePin's one differentiator — protecting the upstream KV Cache prefix — rather
than widening scope.

### Fixed

- **The binary now actually serves.** `run()` was an inert stub that parsed
  flags, printed `"proxy not yet wired"`, and returned — so the shipped CLI
  started no listener and proxied zero requests despite `internal/` being fully
  implemented. `run()` now builds the proxy, wires the session tracker + metrics
  reporter (and the pin reconciler when `--pin`) in as an interceptor, opens the
  `--ndjson` sink, and starts `http.ListenAndServe`.
- **Pin no longer drops genuinely-new turns under context compaction.** The
  reconciler used a last-N tail slice (`incoming[len(incoming)-newCount:]`) that
  only matched the §2 LCP contract for count-preserving in-place edits. When a
  harness dropped an earlier message and appended new ones, it undercounted the
  new tail and silently dropped needed turns, corrupting the upstream request.
  Reconciliation now reconstructs from the common-prefix boundary as specified:
  `canonical + incoming[lcp:]`.

### Added

- **Context-layout linter (m4).** A byte-level prefix-diff that reports the exact
  byte offset where two consecutive requests' prefixes first diverge and which
  message field broke prefix-stability (`role`, `content`, `tool_calls`,
  `tool_call_id`, `name`, `field-order`, or a dropped message). Surfaced in the
  per-turn human line (`… | content broke prefix at byte 1423`) and in the
  NDJSON record (`layout_diverged`, `layout_byte_offset`, `layout_msg_index`,
  `layout_field`).

[0.2.0]: https://github.com/SuperMarioYL/cachepin/releases/tag/v0.2.0

## [0.1.0] - 2026-05-29

Initial release.

### Added

- Transparent, OpenAI-compatible reverse proxy (`--upstream`, `--listen`) that
  relays `/v1/chat/completions` including streaming SSE chunk-by-chunk (m1).
- Per-session canonical-history tracker that content-hashes each message,
  computes the longest common prefix, and flags the mutation boundary (m2).
- Per-turn human-readable line plus optional `--ndjson` machine-readable output:
  preserved-prefix %, reprocessed tokens, mutation index (m2).
- `--pin` mode that reconciles mutated requests to append-only form so the
  upstream KV cache survives (m3).
- `bench/` replay tool that produces before/after CSV chart data over a fixed
  multi-turn transcript (m3).
- Bilingual README (English + 简体中文).

[0.1.0]: https://github.com/SuperMarioYL/cachepin/releases/tag/v0.1.0
