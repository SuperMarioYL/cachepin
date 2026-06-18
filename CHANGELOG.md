# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
