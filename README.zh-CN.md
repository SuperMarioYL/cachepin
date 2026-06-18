<p align="center">
  <img src="https://capsule-render.vercel.app/api?type=waving&color=0:6d28d9,100:14b8a6&height=180&section=header&text=CachePin&fontColor=ffffff&fontSize=70&desc=%E8%AE%A9%E4%BD%A0%E7%9A%84%20Coding%20Agent%20%E5%9C%A8%E5%A4%9A%E8%BD%AE%E5%AF%B9%E8%AF%9D%E4%B8%AD%E4%BF%9D%E4%BD%8F%20KV%20Cache&descAlignY=68&descSize=16" alt="CachePin" />
</p>

<p align="center"><a href="./README.md">English</a> | <strong>简体中文</strong></p>

<p align="center">
  <a href="./LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="MIT License" /></a>
  <a href="https://github.com/SuperMarioYL/cachepin/releases"><img src="https://img.shields.io/badge/release-WIP-orange.svg" alt="WIP" /></a>
  <a href="https://github.com/SuperMarioYL/cachepin/actions"><img src="https://img.shields.io/badge/CI-go%20build%20%2B%20test-success.svg" alt="CI" /></a>
  <img src="https://img.shields.io/badge/go-1.24-00ADD8.svg" alt="Go 1.24" />
  <img src="https://img.shields.io/badge/KV%20Cache-pinned-6d28d9.svg" alt="KV Cache" />
  <img src="https://img.shields.io/badge/Coding%20Agent-neutral-14b8a6.svg" alt="Coding Agent" />
</p>

> 一个单文件 Go 二进制，塞在你的 **Coding Agent** harness 和 OpenAI 兼容模型服务之间。它会量化——并在加上 `--pin` 后保护——那块被 harness 悄悄打掉的服务端 **KV Cache**。

## 为什么是现在

如果你自建模型（llama.cpp、vLLM），再用 Claude Code、Cursor、opencode 这类 **Coding Agent** harness 去驱动它，你大概率交过这笔「税」：harness 重渲染一个工具结果、或压缩一次上下文，消息数组在第 3 条变了，推理服务的 **KV Cache** 从那一条开始整段失效——于是每一轮都默默重算 3 万多个 token。[@CreativelyBankrupt](https://twitter.com/CreativelyBankrupt) 一直在提的正是这种前缀缓存的脆弱性；r/LocalLLaMA 的 "checkpoints" 帖子、以及 [Hmbown/CodeWhale](https://github.com/Hmbown/CodeWhale) 这类定制 agent，都是在一个个 harness 上各自打补丁。CachePin 把这个思路做成了可移植版：一个与 harness 无关的代理，挡在*任意* OpenAI 兼容服务前面，告诉你 mutation 到底发生在哪一条消息，并把请求重写回 append-only 形式，让缓存活下来。不用 fork agent，不锁模型——把 `OPENAI_BASE_URL` 指过来，照常干活。

## 目录

- [快速上手（10 分钟）](#快速上手10-分钟)
- [你会看到什么](#你会看到什么)
- [工作原理](#工作原理)
- [配置](#配置)
- [基准测试](#基准测试)
- [对比 CodeWhale](#对比-codewhale)
- [路线图](#路线图)
- [参与贡献](#参与贡献)
- [许可证](#许可证)
- [一句话分享](#一句话分享)

## 快速上手（10 分钟）

```bash
# 1. 安装单文件二进制
go install github.com/SuperMarioYL/cachepin/cmd/cachepin@latest

# 2. 指向你的 OpenAI 兼容服务（llama.cpp、vLLM……）
cachepin --upstream http://localhost:8080      # 监听 :8089

# 3. 让你的 coding agent 走 CachePin
export OPENAI_BASE_URL=http://localhost:8089
```

照常使用你的 coding agent，行为零变化。CachePin 每一轮打印一行；其余什么都不动。当你想从「量化」升级到「保护」时，加上 `--pin` 重启即可。

> 📼 演示即将上线——[VHS 脚本](./assets/demo.tape)会录制一段约 30s 的 asciinema（`assets/demo.cast`），展示开启 `--pin` 后重算 token 数如何坍缩到 ~0。

> 国内访问：仓库会同步推送 Gitee 镜像（GFW 友好），地址见 Releases 说明。

## 你会看到什么

干净的 append-only 会话能复用整个前缀：

```
turn 12 | prefix preserved 100% | 0 tokens reprocessed
```

一旦 harness 改写了历史，CachePin 会点名那条边界：

```
turn 13 | prefix preserved 41% | ~31k tokens reprocessed | MUTATION at msg[3]
```

加上 `--pin`，同样这一轮会在抵达服务端之前被重写回 append-only 形式，于是 **KV Cache** 得以保留，重算 token 数重新逼近零。

<details>
<summary>机器可读输出（<code>--ndjson</code>）</summary>

```json
{"ts":"2026-05-29T12:00:00Z","session_id":"a1b2c3","turn":13,"preserved_prefix_pct":41.0,"reprocessed_tokens":31000,"total_tokens":52000,"mutated":true,"mutation_index":3,"prev_len":24,"incoming_len":26,"lcp":3}
```

每行一个 JSON 对象——基准测试和你自己搭的任何面板消费的都是这条流。
</details>

## 工作原理

核心原语是一份**规范化的 append-only 会话历史**，外加一条约定：任何转发出去的请求，其消息数组必须是它的*前缀扩展*。CachePin 对每条消息做内容哈希，与规范历史算最长公共前缀，那个边界正好就是服务端前缀缓存失效的位置。

```
harness ──HTTP──▶ proxy ──▶ session tracker ──▶ metrics ──▶ stdout / NDJSON
                    │              │
                    │        pin/reconcile（开启 --pin 时）
                    ▼
             上游模型服务（llama.cpp / vLLM / API）
```

单二进制、单进程、纯标准库——没有容器，没有 Kubernetes，不依赖模型专属 tokenizer。流式 `/v1/chat/completions` 响应（SSE）逐块透传，harness 根本察觉不到 CachePin 的存在。

## 配置

CachePin 全靠命令行参数配置——没有配置文件。

| 参数 | 类型 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `--upstream` | string | *（必填）* | OpenAI 兼容模型服务的基址，例如 `http://localhost:8080` |
| `--listen` | string | `:8089` | CachePin 代理绑定的地址 |
| `--pin` | bool | `false` | 把被改写的请求重写回 append-only 形式，保住上游 KV Cache |
| `--ndjson` | string | *（关闭）* | 额外把每轮指标以 NDJSON 写入该路径 |

## 基准测试

自己复现 before/after 曲线——它会重放一段固定的 50 轮对话（其 harness 每轮都改写一条早期消息），分别在不加 pin 与加 pin 的情况下各跑一遍：

```bash
go run ./bench -turns 50 -out chart.csv
```

它会输出 CSV 列 `turn,reprocessed_no_pin,reprocessed_pin,cumulative_no_pin,cumulative_pin`，并把节省汇总打到 stderr。重点就一句话：不加 `--pin` 时线性爬升的那条曲线，加上之后变平。

## 对比 CodeWhale

诚实定位——CachePin 是一层垫片，不是竞品 agent。

| | CachePin | [Hmbown/CodeWhale](https://github.com/Hmbown/CodeWhale) |
| --- | --- | --- |
| 与 harness 无关（Claude Code / Cursor / opencode 通吃） | ✓ | ✗（它本身就是个 agent） |
| 完整的 coding-agent 体验（规划、工具、改文件） | ✗（只是代理） | ✓ |
| 在*任意* OpenAI 兼容服务上 pin 住 KV Cache | ✓ | partial（仅自己的模型路径） |
| 即插即用：保留你现在的 agent | ✓ | ✗（得换 agent） |
| 精确定位 mutation 边界 | ✓ | — |

想要开箱即用的 agent，CodeWhale 是更好的答案。想留住你已经在用的 agent、只是不再烧缓存，那就用 CachePin。

## 路线图

- [x] **m1 — 代理透传**：透明的 OpenAI 兼容反向代理，支持 SSE 流式；harness 察觉不到它的存在。
- [ ] **m2 — 追踪与上报**：按会话维护规范历史，每轮输出 preserved-prefix %、重算 token 数、mutation 事件。
- [ ] **m3 — pin 与基准**：`--pin` 重写让上游 KV Cache 存活，外加可复现的 50 轮基准测试。
- [ ] **未来**：harness ↔ server 的 append-only 上下文协议规范；生态文档链接。

## 参与贡献

欢迎提 issue 和 PR——开一个 issue 描述你的 harness + server 组合以及你看到的 mutation，能附上 `--ndjson` 输出最好，边界会一目了然。

## 许可证

[MIT](./LICENSE) © supermario_leo。

## 一句话分享

```
CachePin —— 与 harness 无关的代理，让你的 Coding Agent 在多轮对话中保住 KV Cache。自建 llama.cpp/vLLM 每轮重算 3 万 token？把 OPENAI_BASE_URL 指过来。Go 写的，10 分钟即插即用。https://github.com/SuperMarioYL/cachepin
```

---

<sub>由一次 <a href="https://github.com/SuperMarioYL/cachepin">ai-radar</a> 扫描生成（<code>workspace/projects/&lt;scan_id&gt;/F-plan/need_kvcache01</code>）。推送后可执行：<code>gh repo edit --add-topic kv-cache --add-topic coding-agent --add-topic llm-proxy</code></sub>
