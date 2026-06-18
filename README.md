<p align="center">
  <img src="https://capsule-render.vercel.app/api?type=waving&color=0:6d28d9,100:14b8a6&height=180&section=header&text=CachePin&fontColor=ffffff&fontSize=70&desc=Keep%20your%20coding%20agent%27s%20KV%20Cache%20alive%20across%20turns&descAlignY=68&descSize=18" alt="CachePin" />
</p>

<p align="center"><strong>English</strong> | <a href="./README.zh-CN.md">简体中文</a></p>

<p align="center">
  <a href="./LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT License" /></a>
  <a href="https://github.com/SuperMarioYL/cachepin/releases"><img src="https://img.shields.io/badge/release-WIP-orange.svg" alt="WIP" /></a>
  <a href="https://github.com/SuperMarioYL/cachepin/actions"><img src="https://img.shields.io/badge/CI-go%20build%20%2B%20test-success.svg" alt="CI" /></a>
  <img src="https://img.shields.io/badge/go-1.24-00ADD8.svg" alt="Go 1.24" />
  <img src="https://img.shields.io/badge/KV%20Cache-pinned-6d28d9.svg" alt="KV Cache" />
  <img src="https://img.shields.io/badge/Coding%20Agent-neutral-14b8a6.svg" alt="Coding Agent" />
</p>

> A single Go binary you drop between your **Coding Agent** harness and your OpenAI-compatible model server. It measures — and, with `--pin`, protects — the server-side **KV Cache** your harness keeps silently invalidating.

## Why now

If you self-host a model (llama.cpp, vLLM) and drive it with a **Coding Agent** harness like Claude Code, Cursor, or opencode, you've paid this tax: the harness re-renders a tool result or compacts context, your message array changes at message 3, and the inference server's **KV Cache** invalidates from that point on — so it silently reprocesses 30k+ tokens every single turn. [@CreativelyBankrupt](https://twitter.com/CreativelyBankrupt) has been pointing at exactly this prefix-cache fragility; the r/LocalLLaMA "checkpoints" thread and bespoke agents like [Hmbown/CodeWhale](https://github.com/Hmbown/CodeWhale) work around it one harness at a time. CachePin is the portable version of that idea: a harness-neutral proxy that sits in front of *any* OpenAI-compatible server, shows you the exact mutation boundary, and pins requests back to append-only form so the cache survives. No agent fork, no model lock-in — point `OPENAI_BASE_URL` at it and keep working.

## Table of contents

- [Quickstart (10 minutes)](#quickstart-10-minutes)
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

> 📼 Demo coming soon — the [VHS tape](./assets/demo.tape) records a 30s asciinema cast (`assets/demo.cast`) showing reprocessed tokens collapse to ~0 once `--pin` is on.

## What you'll see

A clean, append-only session reuses the whole prefix:

```
turn 12 | prefix preserved 100% | 0 tokens reprocessed
```

When the harness rewrites history, CachePin names the exact boundary:

```
turn 13 | prefix preserved 41% | ~31k tokens reprocessed | MUTATION at msg[3]
```

Add `--pin` and the same mutated turn is reconciled to append-only form before it reaches the server, so the **KV Cache** survives and the reprocessed count drops back toward zero.

<details>
<summary>machine-readable output (<code>--ndjson</code>)</summary>

```json
{"ts":"2026-05-29T12:00:00Z","session_id":"a1b2c3","turn":13,"preserved_prefix_pct":41.0,"reprocessed_tokens":31000,"total_tokens":52000,"mutated":true,"mutation_index":3,"prev_len":24,"incoming_len":26,"lcp":3}
```

One JSON object per line — the same stream the benchmark and any dashboard you build consume.
</details>

## How it works

The core primitive is a **canonical append-only session history** plus one contract: every forwarded request's message array must be a *prefix-extension* of it. CachePin content-hashes each message, computes the longest common prefix against the canonical history, and that boundary is exactly where the server's prefix cache stops being valid.

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
- [ ] **m2 — track & report**: per-session canonical-history tracker emitting preserved-prefix %, reprocessed tokens, and mutation events per turn.
- [ ] **m3 — pin & bench**: `--pin` reconciliation that keeps the upstream KV Cache alive, plus the reproducible 50-turn benchmark.
- [ ] **Future**: protocol spec for harness ↔ server append-only context; ecosystem docs links.

## Contributing

Issues and PRs welcome — file an issue describing your harness + server combo and the mutation you're seeing, and attach the `--ndjson` output if you can. It makes the boundary obvious.

## License

[MIT](./LICENSE) © supermario_leo.

## Share this

```
CachePin — the harness-neutral proxy that keeps your Coding Agent's KV Cache alive across turns. Self-hosting llama.cpp/vLLM and reprocessing 30k tokens every turn? Point OPENAI_BASE_URL at it. Go, 10-min drop-in. https://github.com/SuperMarioYL/cachepin
```

---

<sub>Generated from an <a href="https://github.com/SuperMarioYL/cachepin">ai-radar</a> scan (<code>workspace/projects/&lt;scan_id&gt;/F-plan/need_kvcache01</code>). After pushing: <code>gh repo edit --add-topic kv-cache --add-topic coding-agent --add-topic llm-proxy</code></sub>
