# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
