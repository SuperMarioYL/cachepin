<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/hero-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="./assets/hero-light.svg">
    <img src="./assets/hero-light.svg" width="880" alt="CachePin — keep your coding agent's KV Cache alive across turns" />
  </picture>
</p>

<p align="center"><strong>English</strong> | <a href="./README.zh-CN.md">简体中文</a></p>

<p align="center">
  <a href="./LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT License" /></a>
  <a href="https://github.com/SuperMarioYL/cachepin/releases"><img src="https://img.shields.io/badge/release-v0.2.0-6d28d9.svg" alt="release v0.2.0" /></a>
  <a href="https://github.com/SuperMarioYL/cachepin/actions"><img src="https://img.shields.io/badge/CI-go%20build%20%2B%20test-success.svg" alt="CI" /></a>
  <img src="https://img.shields.io/badge/go-1.24-00ADD8.svg" alt="Go 1.24" />
  <img src="https://img.shields.io/badge/KV%20Cache-pinned-6d28d9.svg" alt="KV Cache" />
  <img src="https://img.shields.io/badge/Coding%20Agent-neutral-14b8a6.svg" alt="Coding Agent" />
</p>

> A single Go binary you drop between your **Coding Agent** harness and your OpenAI-compatible model server. It measures — and, with `--pin`, protects — the server-side **KV Cache** your harness keeps silently invalidating.

## Why now

If you self-host a model (llama.cpp, vLLM) and drive it with a **Coding Agent** harness like Claude Code, Cursor, or opencode, you've paid this tax: the harness re-renders a tool result or compacts context, your message array changes at message 3, and the inference server's **KV Cache** invalidates from that point on — so it silently reprocesses 30k+ tokens every single turn. [@CreativelyBankrupt](https://twitter.com/CreativelyBankrupt) has been pointing at exactly this prefix-cache fragility; the r/LocalLLaMA "checkpoints" thread and bespoke agents like [Hmbown/CodeWhale](https://github.com/Hmbown/CodeWhale) work around it one harness at a time. CachePin is the portable version of that idea: a harness-neutral proxy that sits in front of *any* OpenAI-compatible server, shows you the exact mutation boundary, and pins requests back to append-only form so the cache survives. No agent fork, no model lock-in — point `OPENAI_BASE_URL` at it and keep working.

## <img src="https://api.iconify.design/tabler:topology-star-3.svg?color=%236d28d9&width=24" height="22" align="absmiddle" alt=""> Architecture

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/atlas-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="./assets/atlas-light.svg">
    <img src="./assets/atlas-light.svg" width="880" alt="A coding-agent harness sends OpenAI-compatible requests to the CachePin proxy, whose session tracker content-hashes each message to find the longest-common-prefix mutation boundary; pin mode reconciles mutated turns to append-only form; metrics are emitted per turn; requests forward to the upstream model server whose KV cache survives">
  </picture>
</p>

Your coding agent points `OPENAI_BASE_URL` at CachePin instead of the model server. Inside the proxy boundary, the **session tracker** content-hashes every message and computes the longest common prefix against the canonical history — that boundary is exactly where the upstream prefix cache stops being valid. The **metrics** unit emits preserved-prefix %, reprocessed tokens, and the mutation index per turn; with `--pin` the **reconciler** rewrites a mutated request back to append-only form so the server-side **KV Cache** survives. Streaming `/v1/chat/completions` responses are relayed chunk-by-chunk, so the harness can't tell the proxy is there.

## Table of contents

- [Quickstart (10 minutes)](#quickstart-10-minutes)
- [Demo](#demo)
- [What you'll see](#what-youll-see)
- [How it works](#how-it-works)
- [Configuration](#configuration)
- [Benchmark](#benchmark)
- [vs CodeWhale](#vs-codewhale)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)
- [Share this](#share-this)

## Quickstart (10 minutes)

```bash
# 1. install the single binary
go install github.com/SuperMarioYL/cachepin/cmd/cachepin@latest

# 2. point it at your OpenAI-compatible server (llama.cpp, vLLM, ...)
cachepin --upstream http://localhost:8080      # listens on :8089

# 3. tell your coding agent to talk through CachePin
export OPENAI_BASE_URL=http://localhost:8089
```

Use your coding agent exactly as before. CachePin prints one line per turn; nothing else changes. When you want the cache *protected* instead of just *measured*, restart with `--pin`.

## <img src="https://api.iconify.design/tabler:photo.svg?color=%236d28d9&width=24" height="22" align="absmiddle" alt=""> Demo

![CachePin demo — reprocessed tokens collapse to ~0 once --pin is on](assets/demo.gif)

The [VHS tape](./docs/demo.tape) records the happy path: start CachePin in measure-only mode, watch a mutated turn reprocess ~31k tokens, then restart with `--pin` and watch the same turn drop back to zero.

## What you'll see

A clean, append-only session reuses the whole prefix:

```
turn 12 | prefix preserved 100% | 0 tokens reprocessed
```

When the harness rewrites history, CachePin names the exact boundary — and the
**context-layout linter** pinpoints the precise byte offset and the message field
that broke prefix-stability (the system prompt, a re-ordered tool schema, a
whitespace re-render, …):

```
turn 13 | prefix preserved 41% | ~31k tokens reprocessed | MUTATION at msg[3] | content broke prefix at byte 14237
```

Add `--pin` and the same mutated turn is reconciled to append-only form before it reaches the server, so the **KV Cache** survives and the reprocessed count drops back toward zero.

<details>
<summary>machine-readable output (<code>--ndjson</code>)</summary>

```json
{"ts":"2026-06-22T12:00:00Z","session_id":"a1b2c3","turn":13,"preserved_prefix_pct":41.0,"reprocessed_tokens":31000,"total_tokens":52000,"mutated":true,"mutation_index":3,"prev_len":24,"incoming_len":26,"lcp":3,"layout_diverged":true,"layout_byte_offset":14237,"layout_msg_index":3,"layout_field":"content"}
```

One JSON object per line — the same stream the benchmark and any dashboard you build consume. The `layout_*` fields are the linter's byte-level diagnosis: `layout_field` is one of `role`, `content`, `name`, `tool_calls`, `tool_call_id`, `field-order` (JSON framing / key order changed), or `message-count` (an earlier message was dropped).
</details>

## How it works

The core primitive is a **canonical append-only session history** plus one contract: every forwarded request's message array must be a *prefix-extension* of it. CachePin content-hashes each message, computes the longest common prefix against the canonical history, and that boundary is exactly where the server's prefix cache stops being valid. The **context-layout linter** then drills into that boundary at the byte level and names which field broke prefix-stability, so you can fix the cache-busting churn at its source.

```
harness ──HTTP──▶ proxy ──▶ session tracker ──▶ metrics ──▶ stdout / NDJSON
                    │              │
                    │        pin/reconcile (when --pin)
                    ▼
             upstream model server (llama.cpp / vLLM / API)
```

One binary, one process, standard library only — no containers, no Kubernetes, no model-specific tokenizer. Streaming `/v1/chat/completions` responses (SSE) pass through chunk-by-chunk, so the harness can't tell CachePin is there.

## Configuration

CachePin is configured entirely by flags — no config file.

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `--upstream` | string | *(required)* | Base URL of the OpenAI-compatible model server, e.g. `http://localhost:8080` |
| `--listen` | string | `:8089` | Address CachePin's proxy binds to |
| `--pin` | bool | `false` | Reconcile mutated requests to append-only form so the upstream KV Cache survives |
| `--ndjson` | string | *(off)* | Path to also write one machine-readable metrics object per turn |

## Benchmark

Reproduce the before/after chart yourself — it replays a fixed 50-turn transcript whose harness rewrites an early message every turn, once without pinning and once with:

```bash
go run ./bench -turns 50 -out chart.csv
```

It writes `turn,reprocessed_no_pin,reprocessed_pin,cumulative_no_pin,cumulative_pin` as CSV and prints a savings summary to stderr. The whole point: the curve that climbs linearly without `--pin` goes flat with it.

## vs CodeWhale

Honest positioning — CachePin is a shim, not a competing agent.

| | CachePin | [Hmbown/CodeWhale](https://github.com/Hmbown/CodeWhale) |
| --- | --- | --- |
| Harness-neutral (works with Claude Code, Cursor, opencode) | ✓ | ✗ (is its own agent) |
| Full coding-agent experience (planning, tools, edits) | ✗ (proxy only) | ✓ |
| Pins KV Cache across *any* OpenAI-compatible server | ✓ | partial (its own model path) |
| Drop-in: keep your current agent | ✓ | ✗ (you switch agents) |
| Measures the exact mutation boundary | ✓ | — |

If you want a batteries-included agent, CodeWhale is the better answer. If you want to keep the agent you already use and just stop burning the cache, that's CachePin.

## Roadmap

- [x] **m1 — proxy passthrough**: transparent OpenAI-compatible reverse proxy with SSE streaming; the harness can't tell it's there.
- [x] **m2 — track & report**: per-session canonical-history tracker emitting preserved-prefix %, reprocessed tokens, and mutation events per turn.
- [x] **m3 — pin & bench**: `--pin` reconciliation that keeps the upstream KV Cache alive, plus the reproducible 50-turn benchmark.
- [x] **m4 — context-layout linter** *(v0.2.0)*: byte-level prefix diff that names the exact offset and field that broke prefix-stability.
- [ ] **Future**: protocol spec for harness ↔ server append-only context; ecosystem docs links.

## Contributing

Issues and PRs welcome — file an issue describing your harness + server combo and the mutation you're seeing, and attach the `--ndjson` output if you can. It makes the boundary obvious.

## License

MIT © 2026 SuperMarioYL

## Share this

```
CachePin — the harness-neutral proxy that keeps your Coding Agent's KV Cache alive across turns. Self-hosting llama.cpp/vLLM and reprocessing 30k tokens every turn? Point OPENAI_BASE_URL at it. Go, 10-min drop-in. https://github.com/SuperMarioYL/cachepin
```
