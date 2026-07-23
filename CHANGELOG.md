# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
