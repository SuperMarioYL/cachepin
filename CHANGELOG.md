# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
