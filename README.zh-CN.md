[English](./README.md) · [Website](https://cachepin.lei6393.com) · [GitHub](https://github.com/SuperMarioYL/cachepin)

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/hero-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/hero-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/hero-dark.svg">
  <img src="./assets/presentation/hero-light.svg" width="960" alt="Hero diagram">
</picture>

# cachepin

**看见调用框架何时重写会话历史。**

CachePin 跟踪请求之间的消息前缀，并可将变化历史改写为规范历史的追加扩展。

## 为什么需要它

早期消息重排或改写会改变输入前缀，即使大部分会话看起来没有变化。消息级跟踪让你在解释缓存行为之前看到这些变化。

- **定位变化边界** — 公共前缀索引指向首条变化消息。
- **检查改写语义** — 规范历史与追加后缀均明确可见。
- **通过 HTTP 接入** — 代理位于调用框架与兼容端点之间。

## 架构

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/architecture-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/architecture-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/architecture-dark.svg">
  <img src="./assets/presentation/architecture-light.svg" width="960" alt="Architecture diagram">
</picture>

代理解析聊天请求并保留未知字段；会话跟踪对消息计算哈希并寻找最长公共前缀。pin 模式保留全部规范历史，再从变化边界追加传入后缀。

| 组件 | 职责 |
| --- | --- |
| `Chat proxy` | internal/proxy |
| `Message tracker` | internal/session |
| `History reconciler` | internal/pin |
| `Prefix metrics` | internal/metrics |

## 安装与快速上手

使用仓库清单指定的运行时版本构建，并在仓库根目录运行示例。

```bash
git clone https://github.com/SuperMarioYL/cachepin.git
cd cachepin
go build ./cmd/cachepin
```

比较一条助手回答发生变化的两份明确历史，再打印生产协调器实际保留与追加的内容。

```bash
go run ./examples/presentation-demo
```

## 实际运行示例

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/process-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/process-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/process-dark.svg">
  <img src="./assets/presentation/process-light.svg" width="960" alt="Process diagram">
</picture>

Reconciliation expands four incoming messages to five by retaining the old answer and appending the changed suffix.

```text
common prefix: 2 messages
rewritten: true
message count: 4 -> 5
0 system "Be precise."
1 user "Review code."
2 assistant "Original answer."
3 assistant "Corrected answer."
4 user "Continue."
```

完整命令与输出保存在 [docs/demo-results.json](./docs/demo-results.json). 输入和复现代码均随仓提供。

![已有终端录制](./assets/demo.gif)

保留已有录制供参考；上方文字示例给出当前可复现的操作。

## 用法

CLI 提供以下操作。示例之外的命令需要替换成你的文件路径或标识。

```bash
go run ./cmd/cachepin --upstream http://localhost:8080
# Enable only after reviewing append-only semantics:
go run ./cmd/cachepin --upstream http://localhost:8080 --pin
```

## 配置

配置上游服务器，再把调用框架 base URL 指向 CachePin 监听地址。pin 模式需要显式开启。框架支持时应使用明确会话标识，并在启用改写前检查请求语义。

## 集成与职责分工

<picture>
  <source media="(max-width: 600px) and (prefers-color-scheme: dark)" srcset="./assets/presentation/integrations-mobile-dark.svg">
  <source media="(max-width: 600px)" srcset="./assets/presentation/integrations-mobile-light.svg">
  <source media="(prefers-color-scheme: dark)" srcset="./assets/presentation/integrations-dark.svg">
  <img src="./assets/presentation/integrations-light.svg" width="960" alt="Integrations diagram">
</picture>

以下路径已有源码实现。按任务选择输入，并把生成的结果与项目一起保存。

| 路径 | 已实现职责 |
| --- | --- |
| Chat completions HTTP | OpenAI-compatible proxy route |
| Message arrays | Content and extension fields |
| Streaming responses | Chunk forwarding |
| Terminal metrics | Prefix and token estimates |

## 限制与后续方向

- 前缀百分比与重新处理 token 值是基于消息的观察和估计，不是直接 KV 缓存遥测。
- --pin 会改变请求语义：被编辑或删除的旧消息可能仍保留在规范历史，追加内容也会增加上下文长度。
- 示例仅协调消息数组，不证明服务器缓存复用或吞吐收益。

直接服务器缓存测量与调用框架集成测试需要单独验证。

## 许可与贡献

许可见 [LICENSE](./LICENSE). 反馈问题时请提供最小输入、执行命令和实际输出。
