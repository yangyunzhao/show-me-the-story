# show-me-the-story 新增 Codex Provider 开发说明

## 1. 目标

在 `show-me-the-story` 中新增一种模型调用方式：`Codex Provider`。

当前项目只支持 OpenAI 兼容 API，例如 OpenAI、DeepSeek、Ollama、LM Studio。新的目标是：在不破坏现有 OpenAI 兼容 API 功能的前提下，允许本机自用场景通过 Codex CLI / Codex SDK / Codex app-server 使用已经登录的 Codex / ChatGPT 订阅额度。

最终用户可以在配置页选择：

```text
模型提供方：
1. OpenAI Compatible API
2. Codex Subscription
```

当选择 `OpenAI Compatible API` 时，保持项目原有行为不变。

当选择 `Codex Subscription` 时，不再要求填写 OpenAI API Key，而是通过本机 Codex 登录态调用模型。

---

## 2. 开发边界

### 2.1 必须遵守的边界

1. 不要实现通用 OpenAI-compatible API 代理。
2. 不要暴露 `/v1/chat/completions` 给其他程序使用。
3. 不要读取、复制、打印、上传、保存 Codex token。
4. 不要读取 `~/.codex/auth.json` 中的具体凭据内容。
5. 不要共享账号，不要支持多人共用。
6. 不要绕过 Codex / ChatGPT 的速率限制、权限限制或安全限制。
7. 默认只允许本机使用。
8. 如果启动本地辅助服务，只能监听 `127.0.0.1`。
9. 优先使用 `stdio` 方式调用 Codex app-server，避免开放网络端口。
10. 现有 OpenAI-compatible API 功能必须完全保留。

### 2.2 明确不做的事情

本次不要做这些功能：

1. 不做公网服务。
2. 不做多用户支持。
3. 不做账号共享。
4. 不做 API Key 池。
5. 不做反向代理。
6. 不做完整 OpenAI API 模拟层。
7. 不绕过 Codex 的确认机制。
8. 不自动修改用户的 Codex 全局配置。
9. 不改变 show-me-the-story 的小说数据格式。
10. 不重写前端 UI，只做必要配置项扩展。

---

## 3. 推荐总体方案

新增一个内部模型调用抽象层：

```text
show-me-the-story
  ├── OpenAIProvider
  │     └── 继续调用现有 OpenAI-compatible API
  │
  └── CodexProvider
        └── 调用本机 Codex
              ├── 优先方案：Codex app-server stdio
              └── 备选方案：Node sidecar + @openai/codex-sdk
```

推荐先实现最小可用版本：

```text
Phase 1：抽象 LLMProvider，不改变行为
Phase 2：新增 CodexProvider，先支持非流式生成
Phase 3：支持流式输出
Phase 4：接入配置页
Phase 5：补充测试和安全检查
```

---

## 4. 代码阅读任务

在修改前，先阅读并理解这些文件。文件名可能随版本变化，如果找不到，请用搜索功能查找对应逻辑。

重点阅读：

```text
api.go
config.go
settings.go
handlers.go
web.go
messages.go
writing.go
outline.go
continue.go
editing.go
postprocess.go
reconcile.go
agent.go
chat.go
state.go
```

重点确认以下问题：

1. 当前 OpenAI-compatible API 请求是在哪里发出的。
2. 当前请求结构是否统一经过某个函数。
3. 当前是否支持 SSE 流式输出。
4. 当前模型配置保存在哪里。
5. `api.json` 的结构是什么。
6. 前端配置页如何读取和保存 API 配置。
7. 写作、大纲、事实核查、润色、助理聊天是否复用同一套模型调用逻辑。
8. 错误处理和重试逻辑在哪里。
9. 停止生成任务的机制在哪里。
10. 同一时间只允许一个 AI 任务运行的限制在哪里实现。

修改前先输出一份简短分析，说明：

```text
1. 当前模型调用链路
2. 应该插入 Provider 抽象的位置
3. 需要改动的文件列表
4. 不需要改动的文件列表
5. 第一阶段最小改动方案
```

---

## 4.1 当前代码现状分析与阶段计划

本节基于当前代码阅读结果补充，目标是先明确 `show-me-the-story` 现有模型调用链路，再把 Codex Provider 改造拆成可验证的小阶段。第一阶段只做 Provider 抽象，不改变现有 OpenAI-compatible 行为。

### 4.1.1 当前模型调用链路

当前模型请求基本集中在 `api.go`：

```text
业务动作 handler
  → tryStartTask() 创建 taskCtx / token tracker / 任务互斥锁
  → 具体 Action 函数（outline.go / writing.go / foreshadow.go / postprocess.go / agent.go 等）
  → CallAPI / CallAPIMessages / CallAPIStream / CallAPIWithRetry*
  → api.go 构造 OpenAI-compatible /v1/chat/completions 请求
  → OpenAI-compatible 服务
  → 流式 chunk 或同步 response
  → token 统计 + SSE 日志 / 正文流式输出
  → progress.json / config.json / settings.json 等持久化
```

关键点：

1. `api.go` 是唯一真正发出模型 HTTP 请求的位置。同步入口 `CallAPI` / `CallAPIMessages` 会优先调用 `CallAPIStreamMessages` 缓冲全文，流式失败且非致命错误时回退 `callAPIMessagesSync`。
2. 流式入口 `CallAPIStream` / `CallAPIStreamMessages` 读取 OpenAI-compatible SSE 的 `data:` 行，解析 `choices[0].delta.content`，通过调用方传入的 `onChunk` 推送到现有 SSE。
3. 重试入口在 `CallAPIWithRetry`、`CallAPIWithRetryLog`、`CallAPIStreamWithRetry`、`CallAPIStreamWithRetryLog`，通过 `isFatalAPIError` 区分致命错误和可重试错误。
4. 任务取消由 `handlers.go` 的 `tryStartTask()` / `PostTaskStop()` / `taskCancel()` 实现，所有模型调用都接收 `context.Context`，HTTP 请求使用 `NewRequestWithContext`，重试 sleep 也监听 `ctx.Done()`。
5. token 统计通过 `tokens.go` 的 `TaskTokenUsage` 挂到 `taskCtx`；`api.go` 在每次调用开始、流式更新、调用结束时累计。
6. 全局 API 配置保存到程序目录 `api.json`，结构由 `config.go` 的 `APIConfig` 定义；项目级写作配置保存到项目目录 `config.json`。
7. 前端配置页 `frontend/src/pages/Config.svelte` 通过 `GET /api/config/api` 读取、`PUT /api/config/api` 保存、`POST /api/config/api/test` 测试连接；当前 UI 只暴露 OpenAI-compatible 的 `base_url` / `model` / `api_key` / timeout / max tokens / context budget。
8. 写作、大纲、伏笔、续写、设定协调、全书优化、Agent 聊天都复用 `api.go` 的调用函数，没有发现直接绕过 `api.go` 自建模型 HTTP 请求的路径。

### 4.1.2 主要调用点

| 功能 | 入口文件 | 模型调用方式 | 备注 |
|------|----------|--------------|------|
| 大纲生成/修订 | `outline.go` | `CallAPIWithRetry` | 期望 JSON，使用 `cleanJSONResponse` 后解析 |
| 章节生成 | `writing.go` | `CallAPIStream` + 自定义重试 | `onChunk` 调用 `logger.ContentChunk`，开始前发 `StreamStart` |
| 章节摘要/事实核查/大纲一致性检查 | `writing.go` | `CallAPI` / `CallAPIWithRetryLog` | 混合同步文本与 JSON 解析 |
| 章节修订/去 AI 味/衔接优化 | `writing.go` | `CallAPIStream` 或 `CallAPIWithRetryLog` | 修订和润色依赖流式正文展示 |
| 伏笔建议/更新/一致性检查 | `foreshadow.go`、`foreshadow_consistency.go` | `CallAPIWithRetryLog` | 多数期望 JSON |
| 续写导入/后续大纲 | `continue.go` | `CallAPIWithRetry` / `CallAPIWithRetryLog` | 期望结构化结果 |
| 设定协调 | `reconcile.go` | `CallAPIWithRetry` | 期望结构化结果 |
| 全书优化 | `postprocess.go` | `CallAPIWithRetryLog` | 诊断文本、核查文本、路线图 JSON |
| Agent 聊天 | `agent.go` | `CallAPIStreamMessages`，失败回退 `CallAPIMessages` | 多轮 messages，要求工具调用格式稳定 |
| API 连接测试 | `handlers.go` | `CallAPIMessages` | 发送单条 `Hi` |

### 4.1.3 Provider 抽象插入位置

Provider 抽象应插在 `api.go` 内部边界，而不是改所有业务 Action 的函数签名。

推荐保持这些公开入口不变：

```go
CallAPI(ctx, apiCfg, system, user)
CallAPIMessages(ctx, apiCfg, messages)
CallAPIStream(ctx, apiCfg, system, user, onChunk)
CallAPIStreamMessages(ctx, apiCfg, messages, onChunk)
CallAPIWithRetry*
CallAPIStreamWithRetry*
```

在这些函数内部根据 `apiCfg.Provider` 选择具体 provider：

```text
CallAPI* 兼容入口
  → providerFromConfig(apiCfg)
  → OpenAIProvider（Phase 1 默认且唯一实现）
  → CodexProvider（后续阶段新增）
```

这样可以最大限度减少第一阶段改动面：`outline.go`、`writing.go`、`postprocess.go` 等业务文件继续调用原函数；Phase 1 只保证 `api.go` 内部结构可替换，行为完全不变。

### 4.1.4 需要改动的文件

Phase 1 最小改动预计涉及：

1. `api.go`：新增 Provider 接口/请求响应结构/OpenAIProvider 封装；将现有 HTTP 实现移动到 OpenAIProvider 方法或私有函数中；保留现有 `CallAPI*` 入口。
2. `config.go`：新增 provider 类型字段和默认值，旧 `api.json` 缺字段时默认 `openai_compatible`。Phase 1 可只加入 OpenAI-compatible provider，不接入 Codex 字段。
3. `handlers.go`：`PutAPIConfig` / `PostAPITest` 需要走配置默认化/校验函数，避免旧配置或前端未传 provider 时保存为空值。
4. `CODEX_PROVIDER_DEVELOPMENT.md`：记录现状分析、阶段计划和第一阶段 TODO。

后续阶段才需要改动：

1. `codex_provider.go` 或类似新文件：实现 Codex app-server stdio 客户端。
2. `frontend/src/pages/Config.svelte`：新增 provider 选择和 Codex 字段。
3. `frontend/src/lib/i18n/zh.js`、`frontend/src/lib/i18n/en.js`：新增配置页文案。
4. `messages.go` / `locale.go`：新增后端错误和日志 key。
5. `api_provider_test.go`、`codex_provider_test.go` 等测试文件：覆盖配置兼容、prompt 转换和错误映射。

### 4.1.5 暂不需要改动的文件

第一阶段不建议改动以下文件，因为它们已经通过 `api.go` 间接调用模型：

```text
outline.go
writing.go
foreshadow.go
foreshadow_consistency.go
continue.go
reconcile.go
postprocess.go
agent.go
chat.go
logger.go
tokens.go
state.go
settings.go
skills.go
web.go
frontend/src/lib/sse.js
frontend/src/lib/api.js
frontend/src/lib/stores.js
```

如果第一阶段需要改这些文件，通常说明抽象边界放错了，应优先回到 `api.go` 调整。

### 4.1.6 最小改动方案

分阶段推进：

```text
Phase 1：Provider 抽象，仅 OpenAIProvider，现有行为不变
Phase 2：CodexProvider 非流式 Generate 原型，仅后端可用
Phase 3：CodexProvider 流式输出和停止任务
Phase 4：前端配置页接入 provider 选择
Phase 5：测试、日志、安全和回归验收
```

第一阶段具体策略：

1. 先定义内部统一请求结构，但尽量复用现有 `Message`、`tokenUsage`、`ChatRequest`、`ChatResponse`，避免无意义迁移。
2. 保留 `CallAPI*` 函数签名，业务代码零改动。
3. 新增 `OpenAIProvider`，把当前 `normalizeURL`、同步 HTTP、流式 HTTP、usage 解析、SSE delta 解析等逻辑收进去或通过私有方法调用。
4. `CallAPIMessages` 继续保持“先流式缓冲，失败后同步回退”的行为。
5. `CallAPIStreamMessages` 继续保持现有空流式响应错误、usage 优先统计、无 usage 时估算的行为。
6. `validateAPIConfig` 先支持 provider 分支，但 Phase 1 只有 `openai_compatible` 生效；旧配置没有 provider 时默认 OpenAI-compatible。
7. 不在 Phase 1 增加 Codex CLI 调用、不改前端 UI、不改业务 prompt、不改小说数据格式。

### 4.1.7 风险点

1. **流式回退行为变化**：当前非流式入口实际先走流式，失败后才同步；抽象时如果直接调用同步，会影响 token 统计和等待体验。
2. **token 统计丢失或重复**：`TaskTokenUsage.beginCall` / `updateStreamContent` / `finishCall` 必须仍然只围绕一次模型调用执行。
3. **Agent 工具调用稳定性**：`agent.go` 依赖多轮 messages 和足够 `MaxTokens`，Provider 抽象不能破坏 message role 顺序和回退逻辑。
4. **JSON 任务输出污染**：大纲、伏笔、路线图等依赖 JSON；Codex 后续阶段可能输出解释文字，必须在后续阶段加强 prompt 包装和 JSON 提取。
5. **配置兼容性**：旧 `api.json` 没有 `provider` 字段，保存时不能丢失现有 `base_url`、`api_key`、`model`。
6. **连接测试副作用**：`PostAPITest` 当前会调用 `FetchModelContextWindow` 相关默认逻辑；后续 Codex provider 不应访问 OpenAI `/models`。
7. **取消语义**：Codex app-server 后续阶段必须绑定 `context.Context` 并在取消时释放子进程，否则 `PostTaskStop` 会表现不一致。
8. **日志安全**：新增 provider 后不能在日志里输出 API Key、Codex token、完整 prompt 或完整小说正文。
9. **前端字段条件显示**：Phase 4 之前若后端已支持 codex provider 但前端未接入，必须避免保存空 provider 导致不可用。
10. **app-server 协议不稳定**：Codex app-server JSON-RPC 字段可能随版本变化，Phase 2 前需要用本机 schema 或实际响应确认。

### 4.1.8 Phase 1 详细 TODO

Phase 1 目标：新增 Provider 抽象并封装现有 OpenAI-compatible 调用，编译后行为保持一致。

- [x] 在 `config.go` 中新增 `APIProviderType`、`ProviderOpenAICompatible` 常量和 `APIConfig.Provider` 字段。
- [x] 新增 `normalizeAPIConfig(cfg *APIConfig)` 或等价方法，负责填充默认 provider、timeout、context budget 等默认值。
- [x] 调整 `DefaultAPIConfig()`，默认 `Provider = openai_compatible`。
- [x] 调整 `LoadAPIConfig()`，旧 `api.json` 缺 provider 时自动补默认值。
- [x] 调整 `handlers.go` 的 `PutAPIConfig()`，保存前执行默认化，避免前端未传 provider 时写入空 provider。
- [x] 调整 `handlers.go` 的 `PostAPITest()`，测试前执行默认化和 provider-aware 校验。
- [x] 在 `api.go` 中新增内部 provider 抽象，例如 `type LLMProvider interface { Generate(...); Stream(...) }`，但保留现有外部函数签名。
- [x] 在 `api.go` 中新增 `OpenAIProvider`，持有 `*APIConfig` 或配置副本。
- [x] 将现有 `callAPIMessagesSync` 的 HTTP 逻辑迁入 `OpenAIProvider.Generate` 或由其调用的私有函数。
- [x] 将现有 `CallAPIStreamMessages` 的 HTTP 流式逻辑迁入 `OpenAIProvider.Stream` 或由其调用的私有函数。
- [x] 保持 `CallAPIMessages()` 的现有策略：先流式缓冲，失败后同步回退，致命错误不回退。
- [x] 保持 `CallAPIStreamMessages()` 对 `onChunk`、`tracker.updateStreamContent`、`stream_options.include_usage` 的行为不变。
- [x] 保持 `CallAPIWithRetry*` 与 `CallAPIStreamWithRetry*` 的等待、日志 key、致命错误判断不变。
- [x] 增加 provider 选择函数，例如 `providerFromConfig(apiCfg)`；Phase 1 中除 `openai_compatible` 外均返回明确错误。
- [x] 更新 `validateAPIConfig()`，按 provider 校验；OpenAI-compatible 仍要求 `BaseURL` 和 `Model`，暂不强制 API Key。
- [x] 检查 `FetchModelContextWindow()` 只对 OpenAI-compatible provider 执行；未知 provider 返回 0。
- [x] 运行 `go test ./...`。
- [x] 运行 `go build ./...` 或项目推荐的完整构建命令。
- [ ] 手工回归 OpenAI-compatible：API 测试、大纲生成、章节生成流式输出、停止任务、Agent 聊天。（待可用真实 API 配置后执行）
- [x] 确认 `git diff` 中没有业务 Action 改动、没有前端 UI 改动、没有 Codex token 相关读取逻辑。

### 4.1.9 Phase 2 详细 TODO：CodexProvider 非流式版本

Phase 2 目标：新增 CodexProvider 的非流式 `Generate` 原型，通过本机 `codex app-server` stdio 完成一次文本生成，并能被现有 `CallAPIMessages` 回退路径调用。Phase 2 暂不实现前端配置页、章节流式显示、长连接复用，也不开放任何 HTTP 代理端点。

Phase 2 当前实现状态（2026-06-21）：后端配置模型、`CodexProvider.Generate`、newline JSON-RPC app-server 客户端、prompt 包装、只读 sandbox/approval never、安全工作目录、流式未实现错误、单元测试已完成；`go test ./...` 已通过。真实 Codex 登录态下的 `/api/config/api/test`、大纲 JSON 生成、章节流式生成尚未手工验证，其中章节流式按阶段边界留到 Phase 3。

#### 4.1.9.1 阶段边界

- [x] 只实现 CodexProvider 的非流式 `Generate`。
- [x] `Stream` 可暂时返回明确错误，例如“CodexProvider stream is not implemented in Phase 2”。
- [x] 不修改 `outline.go`、`writing.go`、`postprocess.go`、`agent.go` 等业务 Action 的函数签名。
- [x] 不新增 `/v1/chat/completions`、反向代理、本地 WebSocket 服务或公网监听。
- [x] 不读取、不解析、不打印、不保存 `~/.codex/auth.json` 或任何 Codex token。
- [x] 不自动修改用户的 Codex 全局配置。
- [x] 不在 Phase 2 改前端 UI；如需测试 codex provider，先通过手动编辑 `api.json` 或后端临时测试路径完成。
- [x] 保持 OpenAI-compatible provider 行为不变，Phase 2 每次改动后仍需跑 OpenAI-compatible 回归编译/测试。

#### 4.1.9.2 需要改动的文件

- [x] `config.go`：新增 `ProviderCodex` 常量和 Codex 相关配置字段。
- [x] `api.go`：让 `providerFromConfig` 能选择 CodexProvider；让 `validateAPIConfig` 支持 Codex provider 校验；保持 `CallAPI*` 对外签名不变。
- [x] 新增 `codex_provider.go`：实现 CodexProvider、app-server stdio JSON-RPC 客户端、messages → Codex prompt 转换。
- [x] 新增 `codex_provider_test.go` 或类似测试文件：覆盖 prompt 转换、配置默认化、错误映射、JSON-RPC 事件解析等不依赖真实 Codex 登录态的逻辑。
- [x] `handlers.go`：确认 `PutAPIConfig` / `PostAPITest` 对 Codex provider 不会调用 OpenAI `/models`，且错误能返回前端。
- [x] `messages.go` / `locale.go`：如新增用户可见错误 key，补充双语错误信息；如果 Phase 2 只返回普通 error，可暂不改。
- [x] `AGENTS.md`：Phase 2 完成后同步说明 CodexProvider 非流式状态、配置字段、安全边界。
- [x] `CODEX_PROVIDER_DEVELOPMENT.md`：执行时逐项更新本 TODO 状态。

#### 4.1.9.3 配置模型 TODO

- [x] 在 `config.go` 中新增 `ProviderCodex APIProviderType = "codex"`。
- [x] 在 `APIConfig` 中新增 `CodexModel string json:"codex_model,omitempty"`。
- [x] 在 `APIConfig` 中新增 `CodexWorkingDir string json:"codex_working_dir,omitempty"`。
- [x] 在 `APIConfig` 中新增 `CodexUseStreaming bool json:"codex_use_streaming,omitempty"`，Phase 2 默认可保存但暂不生效。
- [x] 决定 Codex 默认模型值：Phase 2 要求显式配置 `codex_model`，避免猜测用户可用模型。
- [x] 决定 Codex 默认工作目录：为空时使用系统临时目录下 `show-me-the-story-codex`；不得默认使用源码目录。
- [x] `normalizeAPIConfig` 对 `provider == ""` 仍默认 `openai_compatible`。
- [x] `normalizeAPIConfig` 对 `provider == codex` 不调用 `FetchModelContextWindow`。
- [x] `FetchModelContextWindow` 对 `provider == codex` 返回 0。
- [x] `validateAPIConfig` 对 `provider == codex` 校验 `CodexModel`，并校验或补齐 `CodexWorkingDir`。
- [x] `validateAPIConfig` 对 `provider == codex` 不要求 `BaseURL`、`APIKey`、`Model`。
- [x] `PutAPIConfig` 保存 Codex 配置时不得清空旧 OpenAI-compatible 字段，允许用户来回切换 provider。
- [ ] 准备旧 `api.json` 兼容测试：缺 `provider` 时仍为 `openai_compatible`。
- [x] 准备 Codex `api.json` 示例：

```json
{
  "provider": "codex",
  "codex_model": "gpt-5-codex",
  "codex_working_dir": "D:\\show-me-the-story-codex-work",
  "codex_use_streaming": false,
  "http_timeout_seconds": 300,
  "context_budget_tokens": 900000
}
```

#### 4.1.9.4 app-server 协议确认 TODO

- [x] 运行 `codex --version`，记录本机 Codex CLI 版本：`codex-cli 0.141.0`。
- [x] 检查 `codex app-server --help` 是否存在且支持 stdio 模式：默认 `--listen stdio://`，`--stdio` 等价。
- [x] 优先运行 `codex app-server generate-json-schema` 或 `codex app-server generate-ts`，保存或摘录 Phase 2 需要的 JSON-RPC 方法和事件字段。
- [x] 确认 initialize 请求格式、响应格式、protocol version 字段要求：newline JSON-RPC，请求 `initialize`，响应含 `userAgent` / `codexHome` / `platformFamily` / `platformOs`。
- [x] 确认 initialized notification 是否必需、method 名称和 params 格式：发送 `initialized` notification。
- [x] 确认 `thread/start` 的 method 名称、params 字段、返回 thread id 的位置：`result.thread.id`。
- [x] 确认 `thread/start` 中安全相关字段的真实名称：`approvalPolicy`、`sandbox`、`cwd`、`model`、`serviceName`。
- [x] 确认 `turn/start` 的 method 名称、输入字段结构、如何传纯文本 input：`input` 为 `{type:"text", text, text_elements:[]}` 数组。
- [x] 确认 agent message delta / final message / completed / failed 事件的 method 名称和字段路径：`item/agentMessage/delta` 的 `params.delta`，`turn/completed` 的 `params.turn.items[].type=="agentMessage"` / `text`。
- [x] 确认 app-server 错误响应格式，包括 JSON-RPC error code/message/data。
- [x] 确认取消或 context 结束时，仅杀掉当前子进程是否足够。
- [x] 将确认后的协议字段写入 `codex_provider.go` 注释或文档，避免后续凭猜测维护。

#### 4.1.9.5 CodexProvider 结构 TODO

- [x] 新建 `codex_provider.go`，保持 Go 标准库实现，不引入第三方依赖。
- [x] 定义 `type CodexProvider struct { cfg APIConfig }`。
- [x] 实现 `func (p *CodexProvider) Generate(ctx context.Context, req LLMRequest) (*LLMResponse, error)`。
- [x] 实现 `func (p *CodexProvider) Stream(...) (*LLMResponse, error)`，Phase 2 返回明确未实现错误。
- [x] 在 `api.go` 的 `providerFromConfig` 中增加 `ProviderCodex` 分支。
- [x] CodexProvider 每次 `Generate` 启动一个新的 `codex app-server` 子进程；Phase 2 不做进程池或长连接复用。
- [x] 使用 `exec.CommandContext(ctx, "codex", "app-server", "--stdio")` 绑定任务取消。
- [x] 为 stdin/stdout/stderr 建立 pipe，启动失败时清理已打开资源。
- [x] stderr 只保存摘要，不输出 token、完整 prompt 或完整正文。
- [x] stdout 使用 `bufio.Scanner` 读取 newline JSON-RPC 消息；根据 app-server 实际 framing 选择实现。
- [x] 如果 app-server 使用逐行 JSON-RPC，限制 scanner buffer，必要时增大到合理大小，避免大响应截断。
- [x] 如果 app-server 使用 Content-Length framing，按 LSP/JSON-RPC header 解析，不用逐行假设。实际确认不是 Content-Length，Phase 2 不实现该分支。
- [x] 请求 id 使用单调递增整数，避免响应和通知混淆。
- [x] 实现 `initialize` → `initialized` → `thread/start` → `turn/start` 顺序。
- [x] 读取事件直到 turn completed / failed / ctx canceled / 进程退出。
- [x] 汇总 agent message delta 或 final message 为完整文本。
- [x] completed 后优雅关闭 stdin/等待进程退出；如无法退出，使用 context cancel/kill。
- [x] failed/error 时返回明确错误，不返回半截内容作为成功正文。
- [x] ctx canceled 时返回 `ctx.Err()`，并确保子进程释放。
- [x] 所有错误路径 defer 清理 pipe 和 process。

#### 4.1.9.6 JSON-RPC 客户端 TODO

- [x] 定义 JSON-RPC request 结构：`jsonrpc`、`id`、`method`、`params`。
- [x] 定义 JSON-RPC response 结构：`jsonrpc`、`id`、`result`、`error`。
- [x] 定义 notification/event 结构：`method`、`params`。
- [x] 实现 `sendRequest(method string, params any) (id int, error)`。
- [x] 实现 `sendNotification(method string, params any) error`。
- [x] 实现 response dispatcher：能按 id 等待 initialize/thread/turn 请求响应。
- [x] 实现 event collector：能从通知事件中提取文本 delta 和完成状态。
- [x] 实现 JSON-RPC error 转 Go error，包含 code/message，但截断 data。
- [x] 实现 stderr 摘要收集，限制最大长度，例如 4KB。实际限制为 8KB。
- [x] 实现超时/取消时的读循环退出，避免 goroutine 泄漏。
- [ ] 单元测试 response dispatcher 和事件解析，不依赖真实 `codex` 命令。已测试 final agentMessage 提取和 unsupported stream；fake app-server/dispatcher 细测留作 Phase 2 后续加固。

#### 4.1.9.7 Prompt 转换 TODO

- [x] 新增 `formatCodexPrompt(messages []Message, lang string, wantsJSON bool) string` 或等价函数（实现为 `buildCodexPrompt`）。
- [x] 保留所有原始 message 的 role 和 content 顺序。
- [x] 在包装 prompt 中声明 Codex 仅作为本地小说写作工具的文本后端。
- [x] 明确禁止运行命令、修改文件、检查仓库、提到 Codex、解释实现细节。
- [x] 明确要求只返回任务需要的正文或 JSON。
- [x] 对中文项目使用中文包装，对英文项目使用英文包装。
- [x] Phase 2 若 `LLMRequest` 还没有 language/purpose 信息，可先基于 messages 内容或默认中文包装；当前按消息中是否含汉字推断中英文，Phase 3/4 可补 `purpose/lang`。
- [x] 实现 JSON 任务识别策略：若 system/user prompt 包含“JSON”、“合法 JSON”、“json” 等关键字，则追加严格 JSON 输出规则。
- [x] JSON 输出规则：不要 Markdown 代码块，不要 JSON 外解释。
- [x] 不在日志中输出完整包装 prompt。
- [x] 为中文普通任务、英文普通任务、JSON 任务、多轮 messages 写单元测试。

#### 4.1.9.8 安全参数 TODO

- [x] `thread/start` 或等价请求中设置 `approvalPolicy = "never"`。
- [x] 设置只读 sandbox：`thread/start` 使用 `sandbox = "read-only"`，`turn/start` 使用 `sandboxPolicy.type = "readOnly"`。
- [x] 设置 `cwd` 为配置的 `CodexWorkingDir` 或安全默认目录。
- [x] 如果 `CodexWorkingDir` 不存在，决定是自动创建还是报错；Phase 2 自动创建目录，默认位置为系统临时目录，不在源码目录内。
- [ ] 校验 `CodexWorkingDir` 不为空后不能是用户 home 根目录、磁盘根目录或源码目录；如无法可靠判断，至少在文档中标明风险。
- [x] 不传入任何 MCP 配置。
- [x] 不传入允许 shell/文件写入的选项。
- [x] 不开放任何端口。
- [x] 不把 OpenAI API Key 或 Codex 相关凭据传给 app-server。
- [x] 错误日志只包含 provider 类型、模型名、错误类型、stderr 摘要。

#### 4.1.9.9 错误处理 TODO

- [x] codex 命令不存在：返回“未找到 codex 命令。请先安装 Codex CLI，并确认 codex 可以在当前终端中运行。”当前实现为英文错误 `codex executable not found: install and log in to Codex CLI first`，Phase 4 前端双语化时再本地化。
- [x] app-server 子命令不存在：返回“Codex app-server 启动失败。请确认 Codex CLI 版本支持 app-server。”当前通过 start/initialize 阶段错误和 stderr 摘要返回。
- [ ] 未登录或登录失效：根据 stderr/JSON-RPC error 映射为“Codex 尚未登录或登录已失效...”。Phase 2 当前保留 app-server 原始阶段错误摘要，精确本地化留到后续。
- [ ] 模型不可用：根据 app-server error 映射为“当前 Codex 模型不可用...”。Phase 2 当前保留 app-server 原始阶段错误摘要，精确本地化留到后续。
- [x] JSON-RPC initialize 失败：返回明确阶段名。
- [x] thread/start 失败：返回明确阶段名，并包含截断后的错误摘要。
- [x] turn/start 失败：返回明确阶段名，并包含截断后的错误摘要。
- [x] turn failed：返回模型生成失败，不把失败信息写入正文。
- [x] 输出为空：返回“Codex 返回内容为空”。
- [x] context canceled：保持与现有任务取消一致，返回 `ctx.Err()`。
- [x] 未实现流式：`Stream` 返回非致命还是致命需明确；Phase 2 将 unsupported stream 标记为直接流式重试路径的致命错误，但 `CallAPIMessages` 专门允许它回退到 `Generate`。
- [x] `isFatalAPIError` 如需加入 Codex 配置错误，避免无限重试“未安装 codex / 未登录 / 模型不可用”等不可恢复错误。

#### 4.1.9.10 接入路径 TODO

- [x] `providerFromConfig` 支持 `ProviderCodex` 返回 `&CodexProvider{cfg: cfg}`。
- [x] `CallAPIMessages` 在 Codex provider 下仍会先调用 `CallAPIStreamMessages`；Phase 2 因 Stream 未实现会回退 Generate。确认该错误不会被 `isFatalAPIError` 误判为致命。
- [x] 直接调用 `CallAPIStream` 的业务路径在 Phase 2 不能使用 Codex provider；文档中明确此限制。
- [ ] `PostAPITest` 使用 Codex provider 时可以完成一条简单 `Hi` 测试。
- [x] 大纲生成使用 `CallAPIWithRetry`，可通过 Codex 非流式路径工作。代码路径已接通，真实 JSON 生成待手工验证。
- [x] 章节生成使用 `CallAPIStream`，Phase 2 暂不支持；需等 Phase 3。
- [x] Agent 聊天 `callAgentAPI` 优先流式，失败后回退 `CallAPIMessages`，理论上可走 Codex 非流式，但工具调用稳定性需手工验证。
- [x] 全书优化等非流式调用理论上可走 Codex，但 Phase 2 只验证简单文本和大纲生成。

#### 4.1.9.11 测试 TODO

- [ ] 添加 `normalizeAPIConfig` 测试：旧配置默认 OpenAI-compatible。
- [ ] 添加 Codex 配置读取测试：`provider=codex`、`codex_model`、`codex_working_dir` 字段保留。
- [x] 添加 `validateAPIConfig` 测试：Codex 不要求 BaseURL/APIKey/Model，但要求 CodexModel。
- [x] 添加 `FetchModelContextWindow` 测试或代码检查：Codex provider 返回 0，不访问 `/models`。
- [ ] 添加 `providerFromConfig` 测试：OpenAI provider、Codex provider、未知 provider。
- [x] 添加 prompt 转换测试：role 顺序、中文包装、英文包装、JSON 强约束。
- [ ] 添加 JSON-RPC event parse 测试：delta 汇总、completed、failed、error response。当前已覆盖 final agentMessage 提取，delta/failed/error 细测留作后续。
- [ ] 添加错误摘要截断测试。
- [ ] 如果抽象 exec 启动器，添加 fake app-server 测试：模拟成功响应、启动失败、turn failed、空输出、ctx cancel。
- [x] 运行 `go test ./...`。
- [x] 运行 `task build`。

#### 4.1.9.12 手工验证 TODO

- [ ] 确认 `codex --version` 可用。
- [ ] 确认本机已完成 Codex / ChatGPT 登录。
- [ ] 准备一个安全空工作目录，例如 `D:\show-me-the-story-codex-work`。
- [ ] 手动编辑测试用 `api.json` 为 `provider=codex`，保留原 OpenAI-compatible 字段备份。
- [ ] 调用 `/api/config/api/test`，确认简单测试成功。
- [ ] 创建测试项目，填写最小故事配置。
- [ ] 生成大纲，确认能得到合法 JSON 并落盘。
- [ ] 故意配置不存在的 `codex_model`，确认错误明确。
- [ ] 临时移除 PATH 中 codex 或改名测试，确认“未找到 codex 命令”错误明确。
- [ ] 取消运行中的测试请求，确认子进程退出。
- [ ] 检查日志没有 API Key、Codex token、完整 prompt、完整小说正文。
- [ ] 切回 `provider=openai_compatible`，确认原 API 测试仍可用。

#### 4.1.9.13 Phase 2 完成标准

- [x] OpenAI-compatible 模式 `go test ./...` 和 `task build` 通过。
- [ ] Codex provider 可通过 `CallAPIMessages` 完成一次非流式文本生成。
- [ ] Codex provider 可用于大纲生成并解析 JSON。
- [x] Codex provider 不读取、不保存、不打印 Codex token。
- [x] 程序没有新增任何通用 API 代理端点。
- [x] 程序没有新增任何本地网络监听。
- [x] 任务取消会释放 app-server 子进程。
- [x] 所有不可恢复错误不会进入无限重试。
- [x] 文档明确标注 Phase 2 暂不支持章节流式生成，Phase 3 才支持 `CallAPIStream`。
- [x] `AGENTS.md` 与本开发文档同步更新。

### 4.1.10 Phase 3 详细 TODO：CodexProvider 流式输出与任务停止

Phase 3 目标：在不修改业务 Action 函数签名的前提下，为 `CodexProvider.Stream` 接入 `codex app-server` 的增量事件，把 `item/agentMessage/delta` 转换为现有 `onChunk` 回调，从而复用项目现有 SSE、前端尾部窗口、任务 token 估算和停止任务机制。Phase 3 仍不做前端配置页 UI，不新增 HTTP 代理端点，不开放任何本地监听端口；如需启用 Codex 流式，先通过手动编辑 `api.json` 的 `codex_use_streaming` 完成。

Phase 3 当前实现状态（2026-06-21）：`CodexProvider.Stream` 已由 `codex_use_streaming=true` 启用，使用 app-server delta 事件调用现有 `onChunk`，并复用任务 token 估算；`Generate` 非流式路径保留。已新增 final-only 兜底、delta/final 去重、turn id 过滤、危险工作目录拒绝和不依赖真实 Codex 的流式单元测试。真实 Codex 登录态下的写作页流式生成、停止任务孤儿进程检查、自动确认模式验证尚未执行。

#### 4.1.10.1 阶段边界

- [x] 实现 `CodexProvider.Stream(ctx, req, onChunk)`，让 Codex provider 可以用于直接调用 `CallAPIStream*` 的业务路径。
- [x] 保留 `CodexProvider.Generate` 作为非流式后备路径，不在 Phase 3 删除或重写 Phase 2 已验证逻辑。
- [x] 保持 `CallAPI` / `CallAPIMessages` / `CallAPIStream` / `CallAPIStreamMessages` 对外函数签名不变。
- [x] 不修改 `outline.go`、`writing.go`、`postprocess.go`、`agent.go` 等业务 Action 的函数签名；仅在必要时调整调用端错误提示或日志，不改变业务数据结构。
- [x] 不新增 `/v1/chat/completions`、反向代理、本地 WebSocket 服务、公网监听或任意通用模型代理端点。
- [x] 不在 Phase 3 做前端配置页 provider 切换 UI；配置页接入仍留到 Phase 4。
- [x] 不读取、不解析、不打印、不保存 `~/.codex/auth.json` 或任何 Codex token。
- [x] 不自动修改用户的 Codex 全局配置。
- [x] 保持 OpenAI-compatible provider 行为不变，流式和非流式回归都必须通过。

#### 4.1.10.2 前置条件

- [x] Phase 2 后端代码已合入且 `go test ./...`、`task build` 通过。
- [x] 本机 `codex --version` 可用，并确认支持 `codex app-server --stdio`。
- [ ] 本机已完成 Codex / ChatGPT 登录；如未登录，Phase 3 真实流式手工验证无法完成。
- [ ] 准备安全空工作目录，例如 `D:\show-me-the-story-codex-work`，不要使用源码目录、用户 home 根目录或磁盘根目录。
- [ ] 准备手工测试用 `api.json`，包含 `provider=codex`、`codex_model`、`codex_working_dir`、`codex_use_streaming=true`。
- [x] 记录当前 Codex app-server schema 中流式相关事件字段，至少包括 `item/agentMessage/delta`、`turn/completed`、`error`。

#### 4.1.10.3 需要改动的文件

- [x] `codex_provider.go`：实现 `CodexProvider.Stream`，复用或抽取 Phase 2 JSON-RPC client、thread/turn 启动、事件循环、stderr 摘要、prompt 包装和安全参数。
- [x] `api.go`：确认 `errProviderStreamUnsupported` 语义在 Phase 3 后仍合理；必要时调整 `CallAPIMessages` 的流式失败回退判断，确保 Codex 真流式失败时可按错误类型决定是否回退 `Generate`。
- [x] `config.go`：确认 `CodexUseStreaming` 的 Phase 3 语义；如果为 `false`，`CodexProvider.Stream` 应返回明确未启用错误，让 `CallAPIMessages` 回退非流式，但直接 `CallAPIStream` 的章节生成应提示配置未启用。
- [ ] `handlers.go`：如需让 `/api/config/api/test` 明确测试流式能力，可增加或调整测试结果字段；不强制改 API 结构。
- [ ] `messages.go` / `locale.go`：如新增用户可见错误 key，补充双语文案；如果继续返回普通 error，可暂不改。
- [x] `codex_provider_test.go`：补充流式事件、取消、错误、空输出、重复 final 文本等单元测试。
- [x] `AGENTS.md`：Phase 3 完成后同步 CodexProvider 流式状态、停止任务语义和仍未做的 UI 限制。
- [x] `CODEX_PROVIDER_DEVELOPMENT.md`：执行 Phase 3 时逐项更新本 TODO 状态。

#### 4.1.10.4 `CodexUseStreaming` 行为 TODO

- [x] 明确 `CodexUseStreaming=false` 时的行为：`CodexProvider.Stream` 返回 `errProviderStreamUnsupported` 包装错误，`CallAPIMessages` 可回退 `Generate`。
- [x] 明确 `CodexUseStreaming=true` 时的行为：`CodexProvider.Stream` 启动 app-server 并消费 delta 事件。
- [x] 明确默认值：旧配置或未设置时为 `false`，避免升级后直接让章节生成进入尚未手工验证的 Codex 流式路径。
- [x] 手工测试启用方式写入文档：Phase 3 未做 UI，需手动编辑 `api.json`。
- [x] 确认 `PutAPIConfig` 保存时保留 `codex_use_streaming` 字段，不清空 OpenAI-compatible 旧字段。
- [x] 如 `provider != codex`，`CodexUseStreaming` 不应影响 OpenAI-compatible 行为。

#### 4.1.10.5 流式事件处理 TODO

- [x] 在 `CodexProvider.Stream` 中调用 `tracker.beginCall(req.Messages)`，保持任务 token 估算起点一致。
- [x] 启动 app-server 后执行 `initialize` → `initialized` → `thread/start` → `turn/start`，参数与 Phase 2 保持一致。
- [x] 在事件循环中处理 `item/agentMessage/delta`：按 `threadId` / `turnId` 过滤当前请求事件。
- [x] 每收到非空 `delta`，追加到本地 `strings.Builder`，并立即调用 `onChunk(delta)`。
- [x] 每收到 delta 后调用 `tracker.updateStreamContent(fullContent)`，让前端 token badge 在等待期间更新。
- [x] 处理 `turn/completed`：读取 `turn.status`，completed 时返回完整 `LLMResponse{Content: fullContent}`。
- [x] 如果没有收到 delta，但 `turn/completed.turn.items` 中有 `agentMessage.text`，使用 final message 作为返回内容；如 `onChunk != nil`，仅在确认为没有任何 delta 时一次性推送 final text，避免前端空白。
- [x] 如果同时收到 delta 和 final message，优先使用 delta 汇总；final message 只用于校验或兜底，不重复推送给 `onChunk`。
- [ ] 如果 final message 与 delta 汇总不一致，记录为内部风险；Phase 3 可先以 delta 汇总为准，必要时在文档中记录 Codex schema 行为。
- [x] 处理 `turn.status == failed`：返回明确错误，不把失败信息当正文成功返回。
- [x] 处理 `turn.status == interrupted`：返回已有 partial content + `ctx.Err()` 或明确 interrupted 错误，不能保存为成功正文。
- [x] 处理 app-server `error` notification：如果属于当前 thread/turn，返回阶段化错误；如已产生 partial content，仍应作为错误返回，交给调用方决定是否展示。
- [x] 处理 server request（例如 approval/tool/user input request）：Phase 3 应统一返回 unsupported/denied response，避免 app-server 阻塞等待用户输入。
- [x] 处理未知 notification：默认忽略，但不得阻塞事件循环。
- [x] 处理 stdout EOF：如未 completed，返回 `codex app-server exited before turn completed` 类错误。
- [ ] 处理 scanner buffer 超限：返回明确错误，提示 app-server 消息过大。

#### 4.1.10.6 输出去重与正文污染防护 TODO

- [x] 确保 `onChunk` 只推送模型正文 delta，不推送 reasoning、plan、tool、command output、error message 或 stderr。
- [x] 确保 `turn/completed` 的 final text 不会在已有 delta 的情况下重复推送，避免前端显示双份正文。
- [x] 确保 app-server stderr 不进入章节正文、streamingContent 或 progress。
- [x] 确保 JSON 任务流式时不插入额外 Markdown 包裹、日志前缀或解释文字。
- [x] 对空白 delta 做过滤，不触发无意义 SSE 刷新。
- [x] 如果 Codex 输出包含首尾元信息行，仍交由现有 `stripChapterMetaProse` 等业务后处理负责；Provider 层不做小说正文语义清洗。
- [x] 如果流式中途失败，不能把 partial content 写入已完成章节；只允许前端作为临时流式预览显示。

#### 4.1.10.7 停止任务与资源释放 TODO

- [x] `CodexProvider.Stream` 必须使用 `context.Context` 驱动 app-server 子进程生命周期。
- [x] 当 `ctx.Done()` 触发时，立即停止读取循环并返回 `ctx.Err()`。
- [x] 取消时优先关闭 stdin；如 app-server 未退出，调用 context cancel/kill 当前子进程。
- [ ] 可选：如 app-server 支持 `turn/interrupt`，先发送 `turn/interrupt`，短等待后再 kill；Phase 3 若不实现该优雅中断，需在文档注明当前策略为杀掉本次 app-server 子进程。
- [x] 确保 stdout/stderr reader goroutine 不泄漏；进程退出后通道应关闭。
- [x] `PostTaskStop` 触发后，前端应收到现有 task_end / taskRunning 变化，不需要新增 SSE 事件。
- [ ] 自动确认模式下停止任务：当前章停止后不得继续自动确认或继续下一章。
- [ ] 停止任务后可立即重新启动 OpenAI-compatible 或 Codex 新任务，不残留 activeWork。
- [ ] Windows 下确认子进程退出行为正常，不留下 `codex app-server` 孤儿进程。

#### 4.1.10.8 安全参数 TODO

- [x] `thread/start` 继续设置 `approvalPolicy = "never"`。
- [x] `thread/start` 继续设置 `sandbox = "read-only"`。
- [x] `turn/start` 继续设置 `sandboxPolicy.type = "readOnly"`、`networkAccess=false`。
- [x] 继续设置 `ephemeral=true`，避免 materialize 长期 thread 数据。
- [x] 继续使用 `CodexWorkingDir` 或安全默认目录作为 `cwd`。
- [x] 补齐工作目录危险值校验：拒绝磁盘根目录、用户 home 根目录、仓库源码根目录；无法可靠判断时至少给出明确错误或文档警告。
- [x] 不传入任何 MCP 配置。
- [x] 不传入允许 shell/文件写入的选项。
- [x] 不传 OpenAI API Key、Codex token 或任何凭据给 app-server。
- [x] 不记录完整 prompt、完整章节正文、API Key、Codex token 或 auth 路径内容。

#### 4.1.10.9 错误分类与重试 TODO

- [x] 区分“流式未启用”和“流式执行失败”：未启用可让 `CallAPIMessages` 回退 `Generate`；执行失败是否回退需按错误类型判断。
- [x] codex 命令不存在：作为致命错误，避免无限重试。
- [x] app-server 不支持或启动失败：作为致命错误，避免无限重试。
- [ ] 未登录 / 登录失效：根据 stderr 或 JSON-RPC error 做模式匹配，返回明确错误，并作为致命错误。
- [ ] 模型不可用 / 模型名错误：返回明确错误，并作为致命错误。
- [x] 只读 sandbox / approval 被拒绝：返回明确错误，说明 Codex 不应执行工具；必要时加强 prompt 或 server request 拒绝逻辑。
- [x] ctx canceled：作为任务停止，不按普通失败反复重试。
- [ ] 临时 app-server EOF / transient 网络问题：如果无法判断，Phase 3 可先归为可重试，但需限制日志安全。
- [x] `CallAPIStreamWithRetryLog` 下 Codex 不可恢复错误必须停止重试。
- [x] `CallAPIMessages` 下 Codex 流式失败但 Generate 可用时，日志不应重复输出完整错误正文。
- [x] 所有错误信息都应包含阶段名：start、initialize、thread/start、turn/start、stream read、turn failed、process exit。

#### 4.1.10.10 Token 统计与 SSE TODO

- [x] Codex app-server 若不提供 usage，继续使用 rune 估算；不得阻塞 Phase 3。
- [x] 流式开始时 `tracker.beginCall` 应正常推送初始 prompt 估算。
- [x] 每次 delta 后 `tracker.updateStreamContent` 应节流推送 token_usage。
- [x] completed 后调用 `tracker.finishCall(0, 0, false, req.Messages, result)`，把 pending 估算提交。
- [x] 确认前端 `sse.js` 的 `stream_start`、`content_chunk`、尾部窗口逻辑无需修改即可显示 Codex 流式内容。
- [x] 确认 `CallAPIStream` 调用方仍负责 `logger.StreamStart` / `ContentChunk`，Provider 不直接发 SSE。
- [x] 如果 `onChunk == nil`，`Stream` 仍应完整缓冲并返回全文，供 `CallAPIMessages` 优先流式缓冲路径使用。
- [ ] 确认 long chapter 流式不会让 Go scanner 或前端尾部窗口 O(n²) 放大。

#### 4.1.10.11 单元测试 TODO

- [x] 为 JSON-RPC client 抽象 reader/writer 或 app-server transport，允许不启动真实 `codex` 进行测试。
- [x] 测试 `item/agentMessage/delta` 汇总：多个 delta 按顺序拼接并逐块调用 `onChunk`。
- [x] 测试 `turn/completed` final-only：无 delta 时使用 `agentMessage.text` 并一次性 `onChunk`。
- [x] 测试 delta + final 同时存在：不重复推送 final，返回 delta 汇总。
- [x] 测试 failed turn：返回错误，不作为成功内容。
- [ ] 测试 `error` notification：当前 thread/turn 的 error 会中断并返回阶段错误。
- [ ] 测试未知 notification：忽略且不中断。
- [ ] 测试 server request：返回 unsupported/denied response，避免阻塞。
- [x] 测试 `CodexUseStreaming=false`：`Stream` 返回 unsupported，`CallAPIMessages` 可回退 `Generate`。
- [ ] 测试 `CodexUseStreaming=true`：fake transport 可走完整流式成功路径。
- [ ] 测试 context cancel：返回 `context.Canceled`，reader loop 退出。
- [ ] 测试 stderr 摘要截断：不超过限制，不包含注入的敏感样本。
- [x] 测试 dangerous working dir 校验：磁盘根、home 根、源码根被拒绝。
- [ ] 测试 `providerFromConfig`：Codex provider 可构建，OpenAI-compatible 不受影响。
- [x] 运行 `go test ./...`。
- [x] 运行 `task build`。

#### 4.1.10.12 手工验证 TODO

- [ ] 使用真实 Codex 登录态运行 `/api/config/api/test`，确认 `provider=codex`、`codex_use_streaming=true` 可返回样例文本。
- [ ] 创建最小测试项目，手动配置 Codex provider，生成大纲，确认 JSON 可解析并落盘。
- [ ] 生成章节，确认写作页实时显示 Codex 流式正文。
- [ ] 长章节生成中观察前端尾部窗口：页面不卡顿，内容不重复。
- [ ] 生成章节过程中点击停止，确认任务结束、按钮恢复、无孤儿 `codex app-server` 进程。
- [ ] 自动确认模式下使用 Codex 流式生成，确认每章生成/确认/下一章循环正常；停止后不会继续下一章。
- [ ] 故意配置错误模型名，确认错误明确且不会无限重试。
- [ ] 故意关闭 Codex 登录或使用未登录环境，确认错误明确且不会无限重试。
- [ ] 故意设置危险 `codex_working_dir`，确认后端拒绝。
- [ ] 切回 `provider=openai_compatible`，确认 API 测试、大纲生成、章节流式生成、停止任务仍正常。
- [ ] 检查后端日志、前端日志、SSE 内容不含 API Key、Codex token、完整 prompt、完整小说正文。
- [ ] Windows 下用任务管理器或 `Get-Process` 检查停止后无残留 app-server 子进程。

#### 4.1.10.13 Phase 3 完成标准

- [x] `CodexProvider.Stream` 在 `codex_use_streaming=true` 时可以通过 app-server delta 事件返回流式内容。
- [x] `CallAPIStream` / `CallAPIStreamMessages` 在 Codex provider 下可用于章节生成。
- [ ] 写作页可实时显示 Codex 章节生成内容，不重复、不夹杂错误/工具/日志文本。
- [ ] `PostTaskStop` 可以中断 Codex 流式生成并释放 app-server 子进程。
- [x] Codex 流式失败不会把 partial content 持久化为成功章节。
- [x] `CallAPIMessages` 在 Codex provider 下仍可作为非流式兼容入口使用。
- [x] OpenAI-compatible provider 的同步、流式、重试、token 统计行为不回归。
- [x] 不新增任何网络监听或模型代理端点。
- [x] 不读取、不保存、不打印 Codex token。
- [x] `go test ./...` 通过。
- [x] `task build` 通过。
- [x] `AGENTS.md` 与本开发文档同步更新。

### 4.1.11 Phase 4 详细 TODO：前端配置页接入 Provider 选择

Phase 4 目标：在前端配置页正式接入模型提供方选择，让用户可以在 `OpenAI Compatible API` 与 `Codex Subscription` 之间切换，并通过现有后端 `GET /api/config/api`、`PUT /api/config/api`、`POST /api/config/api/test` 完成配置读取、保存与连接测试。Phase 4 不新增模型代理端点，不改小说业务 Action，不改 Codex provider 核心调用逻辑；重点是 UI、i18n、配置兼容、字段保留、安全提示和手工验证。

Phase 4 当前实现状态（2026-06-21）：`frontend/src/pages/Config.svelte` 已接入 provider 分段选择；`openai_compatible` 模式显示原 OpenAI-compatible 字段，`codex` 模式显示 `codex_model`、`codex_working_dir`、`codex_use_streaming` 与 Codex 登录/安全提示；保存和连接测试都会提交完整 `APIConfig`，保存成功后用后端返回值覆盖本地状态。新增可见文案已补齐 `zh.js` / `en.js`。已通过 `npm run build`。真实 OpenAI-compatible API 回归、真实 Codex 登录态连接测试、浏览器手工 UI/移动端检查仍需在可用环境下执行。

#### 4.1.11.1 阶段边界

- [x] 只做配置页 provider 选择和 Codex 配置字段接入，不改大纲、写作、伏笔、全书优化、Agent 等业务 Action。
- [x] 不新增 `/v1/chat/completions`、反向代理、本地 WebSocket、公网监听或任意模型代理端点。
- [x] 不读取、不解析、不打印、不保存 Codex token。
- [x] 不要求用户填写 OpenAI API Key 才能使用 `provider=codex`。
- [x] 保持旧 OpenAI-compatible 配置默认可用；旧 `api.json` 缺 `provider` 时仍显示为 OpenAI-compatible。
- [x] 切换 provider 时不清空另一种 provider 的字段，允许用户来回切换。
- [ ] Phase 4 不强制真实 Codex 登录态可用；未登录时连接测试应显示明确错误。
- [x] 所有新增可见文案必须同时补充中文和英文 i18n key。
- [x] 任务运行期间仍遵守现有禁用规则：配置页输入、provider 切换、保存、连接测试都应 `disabled={$taskRunning}`。

#### 4.1.11.2 需要改动的文件

- [x] `frontend/src/pages/Config.svelte`：新增 provider 选择 UI、Codex 字段区域、条件显示、保存 payload、测试连接显示逻辑。
- [x] `frontend/src/lib/i18n/zh.js`：新增中文文案 key。
- [x] `frontend/src/lib/i18n/en.js`：新增英文文案 key。
- [x] `frontend/src/lib/api.js`：通常不需要改；若测试错误展示需要补通用处理，必须保持现有错误翻译逻辑兼容。
- [x] `frontend/src/lib/stores.js`：通常不需要改；如配置页已有 store 结构限制，才补充 API config 默认字段。
- [x] `handlers.go`：原则上不改；如发现 `PostAPITest` 返回字段不够前端展示，可补充非破坏性字段。
- [x] `config.go`：原则上不改；如发现默认值或 JSON tag 与 UI 不一致，先确认是否属于 Phase 4 必需修正。
- [x] `AGENTS.md`：Phase 4 完成后同步配置页已支持 provider 选择、Codex 字段和仍需手工登录的限制。
- [x] `CODEX_PROVIDER_DEVELOPMENT.md`：执行 Phase 4 时逐项更新本 TODO 状态。

#### 4.1.11.3 API 配置数据模型 TODO

- [x] 前端 API config 对象需要完整保留以下字段：`provider`、`base_url`、`api_key`、`model`、`max_tokens`、`http_timeout_seconds`、`context_budget_tokens`、`codex_model`、`codex_working_dir`、`codex_use_streaming`。
- [x] 读取 `/api/config/api` 后，如果 `provider` 为空，前端显示为 `openai_compatible`，与后端兼容逻辑一致。
- [x] 保存 `provider=openai_compatible` 时，payload 仍保留 Codex 字段，避免用户切换回来后丢配置。
- [x] 保存 `provider=codex` 时，payload 仍保留 OpenAI-compatible 字段，避免用户切换回来后丢配置。
- [x] `api_key` 输入框不应在 provider 切换为 Codex 时被清空；仅隐藏或折叠。
- [x] `codex_use_streaming` 默认显示后端返回值；新配置未返回时前端默认 `false`，不要擅自开启。
- [x] `codex_working_dir` 为空时允许保存，让后端使用安全默认目录；UI 需要说明默认目录语义。
- [x] `codex_model` 为空时允许用户编辑但保存/测试应由后端校验返回明确错误；前端可做轻量 required 提示，但不能与后端规则冲突。
- [x] 不在 LocalStorage、sessionStorage、日志或 toast 中保存/展示 API Key 全文。
- [x] 不在前端保存或展示任何 Codex token 字段；UI 不提供 token 输入框。

#### 4.1.11.4 Provider 选择 UI TODO

- [x] 在配置页 API 配置区域顶部新增 provider 选择控件。
- [x] 使用现有设计系统优先选择分段按钮或 select；避免新引入组件库。
- [x] provider 选项一：`OpenAI Compatible API`，值为 `openai_compatible`。
- [x] provider 选项二：`Codex Subscription`，值为 `codex`。
- [x] 当前 provider 状态应直接绑定 `apiConfig.provider`。
- [x] 切换 provider 只影响字段显示，不立即保存；用户仍需点击保存。
- [x] 任务运行中 provider 选择控件禁用。
- [x] provider 区域增加简短说明：OpenAI-compatible 使用用户配置的 API；Codex 使用本机已登录 Codex CLI。
- [x] provider 选择不应影响故事配置、角色设定、世界观设定等项目级配置。
- [ ] 移动端 provider 控件不应溢出或挤压现有配置表单。

#### 4.1.11.5 OpenAI-compatible 字段区域 TODO

- [x] 当 `provider=openai_compatible` 时显示 OpenAI-compatible 字段区域。
- [x] 显示并保留现有字段：Base URL、Model、API Key、HTTP timeout、Max tokens、Context budget tokens。
- [x] 保持原有保存逻辑、测试连接按钮、错误展示方式不回归。
- [x] `api_key` 继续使用 password 类型或现有隐藏策略。
- [x] 如果当前 provider 是 Codex，OpenAI-compatible 字段可以折叠/隐藏，但不能从 payload 删除。
- [x] 如果用户从 Codex 切回 OpenAI-compatible，之前填写的 Base URL / Model / API Key 应恢复显示。
- [x] OpenAI-compatible 的连接测试继续走 `/api/config/api/test`，不受 Codex 字段影响。
- [x] 旧配置没有 `provider` 字段时，打开配置页应看到 OpenAI-compatible 字段区域。

#### 4.1.11.6 Codex 字段区域 TODO

- [x] 当 `provider=codex` 时显示 Codex 字段区域。
- [x] 显示 `codex_model` 输入框，标签建议：中文“Codex 模型”，英文“Codex model”。
- [x] `codex_model` placeholder 可使用 `gpt-5-codex` 或“填写当前 Codex CLI 可用模型名”；不要硬编码为默认值保存。
- [x] 显示 `codex_working_dir` 输入框，标签建议：中文“Codex 工作目录”，英文“Codex working directory”。
- [x] `codex_working_dir` help text 说明：为空时使用后端安全默认目录；不要设置为源码目录、用户主目录或磁盘根目录。
- [x] 显示 `codex_use_streaming` toggle，标签建议：中文“启用 Codex 流式输出”，英文“Enable Codex streaming”。
- [x] `codex_use_streaming` help text 说明：开启后章节生成可实时显示；关闭时非流式任务仍可工作，但直接章节流式路径不可用。
- [x] 显示 Codex 登录说明：需先在终端运行 `codex` 并完成 ChatGPT/Codex 登录。
- [x] 显示安全说明：本应用不会读取或保存 Codex token。
- [x] 不显示 API Key 输入框，不要求 Base URL / Model。
- [x] 如果后端返回 Codex 工作目录危险值错误，UI 应按现有错误机制展示，不吞掉详细原因。
- [x] Codex 字段区域在任务运行中全部禁用。

#### 4.1.11.7 保存行为 TODO

- [x] 点击保存时构造完整 `APIConfig` payload，而不是只提交当前可见字段。
- [x] 保存前对 provider 做归一化：空值按 `openai_compatible` 处理。
- [x] 保存 `openai_compatible` 时，前端不应因为 Codex 字段为空而阻止保存。
- [x] 保存 `codex` 时，前端可提示 `codex_model` 为空，但最终以后端校验为准。
- [x] 保存成功后使用后端返回的 config 覆盖本地 config，确保后端默认值（如工作目录、timeout、context budget）同步到 UI。
- [x] 保存失败时保持用户当前输入，不回滚到旧配置。
- [ ] 保存期间按钮显示 loading 或沿用现有保存状态；避免重复提交。
- [x] 任务运行中保存按钮禁用。
- [ ] 保存后重新加载页面，provider 和对应字段显示应保持一致。
- [ ] 保存 Codex 配置后切回 OpenAI-compatible 保存，再切回 Codex，Codex 字段不丢失。

#### 4.1.11.8 连接测试 TODO

- [x] 连接测试按钮继续调用 `POST /api/config/api/test`。
- [x] 测试 payload 必须包含完整 config，尤其是当前 provider 和隐藏字段。
- [x] OpenAI-compatible 测试成功时保持现有成功提示。
- [x] Codex 测试成功时显示成功提示，并展示 `codex_model` 或后端返回的 `model` 字段。
- [ ] Codex 未安装 CLI 时显示后端错误，提示安装/确认 `codex` 命令可用。
- [ ] Codex 未登录或登录失效时显示后端错误，提示先在终端完成登录。
- [ ] Codex 模型为空或不可用时显示后端错误，提示检查模型名称。
- [ ] Codex 工作目录危险或不可创建时显示后端错误，提示更换安全目录。
- [x] 测试中按钮显示 loading，避免重复点击。
- [x] 任务运行中测试按钮禁用。
- [x] 测试失败不应清空表单。
- [x] 测试结果不应把 API Key、完整 prompt 或完整模型返回内容展示出来；sample 仍按后端截断结果显示。

#### 4.1.11.9 i18n 文案 TODO

- [x] 在 `zh.js` 和 `en.js` 添加 provider 选择相关 key。
- [x] 添加 `OpenAI Compatible API` 与 `Codex Subscription` 的显示文案。
- [x] 添加 Codex 模型、工作目录、流式 toggle 标签。
- [x] 添加 Codex 登录说明文案。
- [x] 添加 Codex 安全说明文案：不会读取或保存 Codex token。
- [x] 添加 Codex 工作目录安全提示文案。
- [x] 添加 Codex 流式说明文案。
- [x] 添加保存/测试成功提示中 provider 相关文案（如现有 toast 已通用，可不新增）。
- [x] 中英文 key 命名保持现有扁平字典风格。
- [x] 不新增硬编码中文/英文到 `Config.svelte`。

#### 4.1.11.10 可用性与布局 TODO

- [x] 配置页首屏布局保持紧凑，provider 选择不应把常用 API 配置推得过低。
- [x] Codex 说明文案控制长度，避免大段说明压迫表单。
- [x] 使用现有 DaisyUI/Tailwind 样式，不新增全局 CSS，除非确有布局问题。
- [x] 卡片、表单控件、按钮风格与当前配置页一致。
- [ ] 移动端下 provider 选项、输入框、toggle 不溢出。
- [ ] API Key 字段隐藏时不会造成布局跳动过大。
- [x] Codex 工作目录输入框适配 Windows 路径长度。
- [x] 错误提示、loading、disabled 状态与现有配置页一致。
- [x] 不在配置页加入营销式说明或长篇教程；只保留必要运行前提和安全提示。

#### 4.1.11.11 后端兼容性检查 TODO

- [x] 确认 `GET /api/config/api` 返回 `provider` 和 Codex 字段。
- [x] 确认 `PUT /api/config/api` 可保存 `provider=codex` 和 Codex 字段。
- [x] 确认 `PUT /api/config/api` 保存 Codex 配置时不清空 OpenAI-compatible 字段。
- [x] 确认 `PUT /api/config/api` 保存 OpenAI-compatible 配置时不清空 Codex 字段。
- [x] 确认 `PostAPITest` 对 `provider=codex` 返回 `model=codex_model`。
- [x] 确认 `validateAPIConfig` 的 Codex 错误能通过现有前端错误展示出来。
- [x] 如后端错误文案需要本地化，优先补 `locale.go` / `messages.go` key；不在前端用字符串包含关系硬翻译。
- [x] 确认 task running 时配置保存/测试仍被后端 `rejectIfTaskRunning` 拒绝。

#### 4.1.11.12 测试 TODO

- [x] 运行 `go test ./...`，确认后端未回归。
- [x] 运行 `task build`，确认前端构建通过。
- [ ] 手工打开配置页，旧 `api.json` 无 provider 时显示 OpenAI-compatible。
- [ ] 手工切换到 Codex，确认只显示 Codex 字段和说明。
- [ ] 手工切回 OpenAI-compatible，确认旧 OpenAI-compatible 字段仍在。
- [ ] 手工保存 Codex 配置并刷新页面，确认 provider 和 Codex 字段持久化。
- [ ] 手工保存 OpenAI-compatible 配置并刷新页面，确认 provider 和 OpenAI-compatible 字段持久化。
- [ ] 手工验证隐藏字段未丢失：切换 provider 多次后检查 `api.json` 两类字段仍存在。
- [ ] 手工测试 OpenAI-compatible 连接测试不回归（需要真实或本地兼容 API）。
- [ ] 手工测试 Codex 连接测试成功路径（需要真实 Codex 登录态）。
- [ ] 手工测试 Codex 未登录/模型错误/危险工作目录错误展示。
- [ ] 任务运行中打开配置页，确认 provider、输入框、保存、测试全部禁用。
- [ ] 检查浏览器控制台无错误。
- [ ] 检查 Network payload 不包含不存在的 token 字段。
- [ ] 检查 UI 中不展示 API Key 全文或任何 Codex token。
- [ ] 检查中文 UI 和英文 UI 下新增文案都完整。
- [ ] 移动端或窄屏下检查配置页不横向溢出。

#### 4.1.11.13 Phase 4 完成标准

- [x] 配置页可以选择 `OpenAI Compatible API` / `Codex Subscription`。
- [x] 旧配置自动显示为 OpenAI-compatible，不需要用户手工迁移。
- [x] OpenAI-compatible 字段只在对应模式显示，且原功能不回归。
- [x] Codex 字段只在对应模式显示，且可保存、刷新后保留。
- [x] 切换 provider 不会清空另一种 provider 的配置字段。
- [x] Codex 模式不要求 API Key / Base URL。
- [ ] Codex 模式连接测试可调用本机 Codex CLI，并能展示成功或明确失败原因。
- [x] 任务运行期间配置页编辑与测试操作被禁用。
- [x] UI 不读取、不保存、不展示 Codex token。
- [x] 新增文案中英文完整。
- [x] `go test ./...` 通过。
- [x] `task build` 通过。
- [x] `AGENTS.md` 与本开发文档同步更新。

### 4.1.12 Phase 5 详细 TODO：测试、日志、安全与回归验收

Phase 5 目标：不再扩展核心功能，集中补齐自动化测试、手工验收、日志与错误提示、安全审计、文档收口和发布前回归，确保 `openai_compatible` 与 `codex` 两种 provider 都能以可解释、可回滚、可维护的方式进入可用状态。Phase 5 可以修改测试、错误文案、日志 key、少量安全校验和文档；除非测试暴露明确 bug，否则不应继续改动 provider 调用协议、业务 Action 流程或前端交互结构。

Phase 5 当前实现状态（2026-06-21）：已新增 `api_provider_test.go`，扩展 `codex_provider_test.go`，覆盖 provider 默认值、旧配置兼容、provider 选择、OpenAI/Codex 校验规则、Codex 安全工作目录、致命错误分类、OpenAI-compatible 错误 body 脱敏、Codex prompt 角色保留、Codex delta/final 事件处理、未知事件忽略、JSON-RPC 错误、非法 JSON 行和 context cancel。`api.go` 已将 Codex turn failed/interrupted 与 turn/start failed 归类为致命错误；OpenAI-compatible HTTP 错误详情已脱敏。`codex_provider.go` 已对 app-server stderr 与 JSON-RPC 错误消息进行敏感信息脱敏。README / README.en / AGENTS 已完成收口；已执行 `go test ./...` 和 `task build` 通过。真实 OpenAI-compatible / Codex 登录态手工验收仍需在可用环境中执行。

#### 4.1.12.1 阶段边界

- [x] Phase 5 默认不新增模型调用能力，不新增业务功能，不改变小说项目数据格式。
- [x] 不新增 `/v1/chat/completions` 兼容代理、不新增反向代理、不新增本地 WebSocket、不新增公网监听端口。
- [x] 不引入新的 Go 第三方依赖；如必须引入前端测试依赖，需要先说明原因、范围和替代方案。
- [x] 不读取、不解析、不打印、不保存 `~/.codex/auth.json` 或任何 Codex token。
- [x] 不把 OpenAI-compatible `api_key` 传入 Codex app-server，不在日志/toast/测试快照中暴露 API Key。
- [x] 不把完整 prompt、完整章节正文、完整模型响应写入日志；需要调试时只允许截断摘要。
- [x] 不将真实 OpenAI API Key、真实 Codex token、真实小说正文提交进测试 fixture。
- [x] Phase 5 允许根据测试结果修复 bug，但修复范围必须局限在 provider 抽象、配置校验、错误映射、日志安全或 UI 配置展示。
- [ ] Phase 5 完成后必须能明确列出：自动化测试结果、手工验证结果、未验证项和残余风险。

#### 4.1.12.2 需要重点检查或可能改动的文件

- [x] `api.go`：补齐 provider 选择、致命错误分类、流式回退、token 统计、配置校验相关测试暴露的问题。
- [x] `codex_provider.go`：补齐 Codex app-server 事件解析、错误映射、取消/清理、安全工作目录和日志脱敏相关问题。
- [x] `codex_provider_test.go`：扩展无真实 Codex 登录态也能运行的单元测试。
- [x] 可新增 `api_provider_test.go` 或类似文件：覆盖 `normalizeAPIConfig`、`providerFromConfig`、`validateAPIConfig`、`isFatalAPIError`。
- [x] `config.go`：只在测试暴露默认值/兼容性问题时修复。
- [x] `handlers.go`：只在 `GET/PUT/POST /api/config/api*` 行为或错误返回不符合验收时修复。
- [x] `messages.go` / `locale.go`：如果新增后端用户可见错误 key 或日志 key，必须补齐中英文。
- [x] `frontend/src/pages/Config.svelte`：只在 Phase 4 手工验收发现字段丢失、禁用状态、移动端布局或错误展示问题时修复。
- [x] `frontend/src/lib/i18n/zh.js`、`frontend/src/lib/i18n/en.js`：只在文案缺失、错误提示不清晰或新增 key 时补齐。
- [x] `doc/CODEX_PROVIDER_DEVELOPMENT.md`：执行 Phase 5 时逐项更新本 TODO 状态、记录验证结果。
- [x] `AGENTS.md`：若 Phase 5 对项目实际行为、安全边界、运行方式或文件职责有任何改动，必须同步更新。

#### 4.1.12.3 自动化单元测试 TODO

- [x] 覆盖旧 `api.json` 缺 `provider` 时 `normalizeAPIConfig` 默认 `openai_compatible`。
- [x] 覆盖 `DefaultAPIConfig()` 默认 provider、timeout、context budget 与现有行为一致。
- [x] 覆盖 `provider=openai_compatible` 时 `validateAPIConfig` 要求 `base_url` 与 `model`，不强制 API Key。
- [x] 覆盖 `provider=codex` 时 `validateAPIConfig` 要求 `codex_model`，不要求 `base_url` / `api_key` / `model`。
- [x] 覆盖 `provider=codex` 且 `codex_working_dir` 为空时会补安全默认目录。
- [x] 覆盖 `provider=codex` 且 `codex_working_dir` 为磁盘根目录时拒绝。
- [x] 覆盖 `provider=codex` 且 `codex_working_dir` 为用户 home 根目录时拒绝。
- [x] 覆盖 `provider=codex` 且 `codex_working_dir` 为当前源码根目录时拒绝。
- [x] 覆盖 `provider=codex` 且 `codex_working_dir` 为专用临时目录时通过。
- [x] 覆盖未知 provider 返回明确错误，并被 `isFatalAPIError` 归为致命错误。
- [x] 覆盖 Codex CLI 不存在、app-server 启动失败、JSON-RPC 初始化失败等错误被归为致命错误，避免无限重试。
- [x] 覆盖网络超时、连接重置等 OpenAI-compatible 瞬时错误仍可重试。
- [x] 覆盖 `FetchModelContextWindow` 在 `provider=codex` 时返回 0，且不访问 OpenAI `/models`。
- [ ] 覆盖 `PostAPITest` 或其下层逻辑在 `provider=codex` 时返回 `model=codex_model`。
- [ ] 覆盖保存 `provider=codex` 时不清空 OpenAI-compatible 字段。
- [ ] 覆盖保存 `provider=openai_compatible` 时不清空 Codex 字段。

#### 4.1.12.4 CodexProvider 协议与事件解析测试 TODO

- [x] 覆盖 `Message` 数组转换为 Codex prompt 时保留 system/user/assistant/tool 角色顺序。
- [x] 覆盖 prompt 包装明确要求“只输出最终文本或 JSON”，不要求 Codex 调用工具或读写文件。
- [x] 覆盖中文项目与英文项目的普通文本 prompt 不被破坏。
- [x] 覆盖 JSON 输出场景：大纲/伏笔/路线图类 prompt 不额外包入会破坏 JSON 解析的固定前后缀。
- [x] 覆盖 app-server `item/agentMessage/delta` 事件能提取增量文本。
- [x] 覆盖多个 delta 事件按顺序拼接。
- [x] 覆盖 final-only 响应会以一次性 chunk 输出。
- [x] 覆盖已有 delta 后 final message 不重复追加全文。
- [x] 覆盖未知 app-server 事件被忽略或安全记录，不中断正常生成。
- [x] 覆盖 app-server 返回 error object 时转换为明确错误。
- [x] 覆盖 app-server 输出非 JSON 行时返回可诊断错误，且不包含敏感内容。
- [x] 覆盖 context cancel 时子进程被结束，`Stream` / `Generate` 返回 context cancellation 类错误。
- [x] 覆盖 `codex_use_streaming=false` 时 `CodexProvider.Stream` 返回明确的未启用/不支持错误。
- [ ] 覆盖 `codex_use_streaming=true` 时 `CodexProvider.Stream` 进入 app-server 流式路径。

#### 4.1.12.5 API 调用链路回归 TODO

- [ ] `CallAPIMessages` 在 OpenAI-compatible 模式下仍保持“先流式缓冲，失败后同步回退”。
- [ ] `CallAPIMessages` 遇到致命错误时不进入同步回退。
- [ ] `CallAPIMessages` 在 Codex 且 `codex_use_streaming=false` 时能从 stream unsupported 回退到 `Generate`。
- [ ] `CallAPIStreamMessages` 在 OpenAI-compatible 模式下继续推送 `onChunk`，并使用 API usage 或估算累计 token。
- [ ] `CallAPIStreamMessages` 在 Codex 且 `codex_use_streaming=false` 时返回明确错误，不静默生成空内容。
- [ ] `CallAPIStreamMessages` 在 Codex 且 `codex_use_streaming=true` 时将 Codex delta 转成现有 `onChunk`。
- [ ] `CallAPIWithRetry` / `CallAPIStreamWithRetry` 在 context cancel 时立即停止等待和重试。
- [ ] `CallAPIWithRetryLog` / `CallAPIStreamWithRetryLog` 的 SSE 重试日志不输出 API Key、Codex token、完整 prompt 或完整正文。
- [ ] `TaskTokenUsage` 在 OpenAI-compatible 同步、OpenAI-compatible 流式、Codex 非流式、Codex 流式四类路径中不重复累计。
- [ ] 空流式响应仍返回现有错误，不写入空章节正文。
- [ ] 事实核查失败重试、章节生成失败重试、Agent tool loop 等业务调用不需要感知 provider 类型。

#### 4.1.12.6 后端 HTTP 端点验收 TODO

- [ ] `GET /api/config/api` 返回 `provider`、OpenAI-compatible 字段、Codex 字段和默认值。
- [ ] `PUT /api/config/api` 旧 payload 不带 `provider` 时仍保存为 `openai_compatible`。
- [ ] `PUT /api/config/api` 可保存 `provider=codex`、`codex_model`、`codex_working_dir`、`codex_use_streaming`。
- [ ] `PUT /api/config/api` 保存失败时不写入部分损坏的 `api.json`。
- [ ] `POST /api/config/api/test` 在 OpenAI-compatible 模式下继续走 OpenAI-compatible provider。
- [ ] `POST /api/config/api/test` 在 Codex 模式下不访问 OpenAI `/models`。
- [ ] `POST /api/config/api/test` 在 Codex 模式下模型为空时返回明确错误。
- [ ] `POST /api/config/api/test` 在 Codex CLI 不存在时返回明确错误。
- [ ] `POST /api/config/api/test` 在 Codex 未登录或登录失效时返回明确错误。
- [ ] `POST /api/config/api/test` 在 Codex 工作目录危险时返回明确错误。
- [ ] `POST /api/config/api/test` 成功响应不包含 API Key、Codex token、完整 prompt 或完整响应。
- [ ] 任务运行中 `PUT /api/config/api` 和 `POST /api/config/api/test` 仍被互斥保护拒绝。
- [ ] `POST /api/task/stop` 对 Codex 流式章节生成能取消当前任务并释放子进程。

#### 4.1.12.7 前端配置页回归 TODO

- [ ] 无 `provider` 的旧配置打开配置页时默认选中 `OpenAI Compatible API`。
- [ ] OpenAI-compatible 模式显示 Base URL、Model、API Key、HTTP timeout、Max tokens、Context budget。
- [ ] Codex 模式显示 Codex model、Codex working directory、Enable Codex streaming、登录与安全提示、Context budget。
- [ ] Codex 模式不显示 API Key 输入框，不要求 Base URL / Model。
- [ ] provider 切换只影响显示，不自动保存。
- [ ] provider 来回切换不清空另一种 provider 的隐藏字段。
- [ ] 保存成功后使用后端返回的 config 覆盖本地状态，刷新页面后仍显示正确 provider。
- [ ] 保存失败时保留用户当前输入。
- [ ] 连接测试按钮在测试中显示 loading 并避免重复点击。
- [ ] 后端错误通过现有 toast/错误机制展示，不吞掉详细原因。
- [ ] 任务运行时 provider 按钮、输入框、toggle、保存按钮、测试按钮全部禁用。
- [ ] 中文 UI 下新增文案完整。
- [ ] 英文 UI 下新增文案完整。
- [ ] 窄屏/移动端下 provider 分段按钮不横向溢出。
- [ ] Codex streaming 说明文案可换行，不被固定高度裁剪。
- [ ] Codex working directory 输入框能容纳 Windows 路径。
- [ ] 浏览器控制台无 Svelte 运行时错误。
- [ ] Network payload 不包含任何 Codex token 字段。

#### 4.1.12.8 OpenAI-compatible 手工回归 TODO

- [ ] 使用真实或本地兼容 OpenAI API，配置 `provider=openai_compatible` 后连接测试成功。
- [ ] 生成大纲成功，返回 JSON 能被现有 `cleanJSONResponse` / 大纲解析逻辑处理。
- [ ] 修订大纲成功。
- [ ] 确认大纲成功。
- [ ] 生成章节成功，并在写作页看到流式输出。
- [ ] 章节生成期间点击停止，任务可取消且 UI 状态恢复。
- [ ] 章节事实核查失败/重试路径不回归。
- [ ] 章节确认成功，并写入 markdown。
- [ ] 去 AI 味/润色成功。
- [ ] 伏笔建议与伏笔更新成功。
- [ ] 全局助理聊天成功，Agent 工具调用格式不回归。
- [ ] 全书优化诊断/核查/路线图至少完成一次短书样例验证。
- [ ] token usage badge 在生成期间有合理变化。
- [ ] 日志面板不显示 API Key、完整 prompt 或不必要的完整正文。

#### 4.1.12.9 Codex 非流式手工验收 TODO

- [ ] 确认本机 `codex` 命令可用。
- [ ] 确认已在终端完成 `codex login`，且不需要应用读取 token。
- [ ] 配置 `provider=codex`、填写 `codex_model`、关闭 `codex_use_streaming` 后保存成功。
- [ ] `POST /api/config/api/test` 成功时显示后端返回的 model。
- [ ] Codex 未登录或登录失效时，连接测试显示可理解错误。
- [ ] `codex_model` 为空时，连接测试显示模型必填错误。
- [ ] 危险 `codex_working_dir` 被拒绝，并提示更换安全目录。
- [ ] 安全临时工作目录可被创建并用于 app-server。
- [ ] 非流式 Codex 模式下生成大纲成功，JSON 能解析。
- [ ] 非流式 Codex 模式下修订大纲成功。
- [ ] 非流式 Codex 模式下全局助理普通聊天成功。
- [ ] 非流式 Codex 模式下需要直接流式的章节生成路径如仍不可用，应显示明确错误而不是空白卡住。
- [ ] 非流式 Codex 模式下日志不显示 Codex token、完整 prompt 或完整正文。

#### 4.1.12.10 Codex 流式手工验收 TODO

- [ ] 配置 `provider=codex`、填写 `codex_model`、开启 `codex_use_streaming` 后保存成功。
- [ ] 连接测试成功。
- [ ] 生成章节时写作页能看到流式增量文本。
- [ ] 流式增量不会重复追加 final 全文。
- [ ] 章节生成完成后 progress 刷新展示完整正文。
- [ ] 章节生成期间 token usage badge 有合理估算变化。
- [ ] 点击停止后后端任务结束，前端任务状态恢复，app-server 子进程释放。
- [ ] 取消后不会保存半截章节为 accepted 状态。
- [ ] Codex 流式发生 app-server 错误时，UI 展示明确错误，日志不泄露敏感信息。
- [ ] Codex 流式生成大纲或其他 JSON 类任务时，输出可被 JSON 清理/解析逻辑处理。
- [ ] 自动确认模式下 Codex 流式生成至少验证 2 章，确认任务锁和连续生成状态正常。
- [ ] 长章节流式输出不会导致前端明显卡顿，尾部窗口策略仍生效。

#### 4.1.12.11 安全审计 TODO

- [x] 全仓搜索 `auth.json`，确认没有读取、解析、打印或保存 Codex auth 文件的逻辑。
- [x] 全仓搜索 `api_key`、`APIKey`、`Authorization`，确认日志、toast、SSE 不输出完整密钥。
- [x] 全仓搜索 `codex` 相关日志，确认不输出 token、完整 prompt、完整正文。
- [x] 检查 Codex app-server 启动参数，确认 `approvalPolicy=never`。
- [x] 检查 Codex app-server sandbox，确认使用只读或等价保守配置。
- [x] 检查 Codex working directory 默认值，确认不使用源码目录、项目目录、用户主目录或磁盘根目录。
- [x] 检查用户自定义 working directory 的路径清理和危险目录拒绝逻辑。
- [x] 检查 Codex prompt 包装，不要求模型调用工具、读文件、写文件或执行命令。
- [ ] 检查 `PostTaskStop` / context cancel 后子进程不会残留。
- [x] 检查测试 fixture 中没有真实 API Key、token、用户小说正文。
- [ ] 检查 `frontend/dist` 或构建产物中没有硬编码 secret。
- [x] 检查没有新增公网监听、HTTP 代理或通用模型转发端点。

#### 4.1.12.12 日志、错误提示与 i18n TODO

- [x] Codex CLI 未安装错误应提示检查 `codex` 命令是否在 PATH 中。
- [ ] Codex 未登录/认证失败错误应提示先在终端完成 `codex login`。
- [x] Codex 模型为空或不可用错误应提示检查 `codex_model`。
- [x] Codex 工作目录危险或不可创建错误应提示更换安全专用目录。
- [ ] app-server 协议异常错误应提示 Codex CLI 版本或 app-server 协议可能不兼容。
- [ ] context cancel / 用户停止任务应展示为取消，而不是误报 provider 故障。
- [x] OpenAI-compatible 原有 401/403/404 等错误提示不回归。
- [ ] 所有新增用户可见错误如使用 key，必须在 `locale.go` / `messages.go` 中补齐中英文。
- [ ] 所有新增前端文案必须在 `zh.js` / `en.js` 中补齐，不在 Svelte 中硬编码中文或英文长句。
- [ ] SSE 日志中的 provider 信息允许显示 provider 类型和模型名，但不能显示 API Key、token、完整 prompt。
- [x] 连接测试成功 toast 只显示模型名，不显示测试 prompt 或完整响应。
- [x] 错误对象中的 stderr/stdout 如包含敏感内容，需要截断或脱敏。

#### 4.1.12.13 性能与资源管理 TODO

- [ ] Codex 每次调用启动 app-server 的开销需要记录在风险说明中；Phase 5 不做长连接复用，除非测试证明不可用。
- [ ] 确认多次 Codex 调用后没有累积残留进程。
- [ ] 确认 context cancel、超时、app-server 错误、JSON-RPC 初始化失败四类路径都会释放 stdin/stdout/stderr 和子进程。
- [ ] 确认长章节流式时后端不会无限缓存完整日志。
- [ ] 确认前端流式尾部窗口仍只保留尾部内容，避免 O(n²) 渲染。
- [ ] 确认自动确认模式连续生成时不会并发启动多个 app-server 任务突破任务锁。
- [x] 确认失败重试不会对 Codex 配置类致命错误无限重试。
- [x] 确认 token 估算不会因 delta 和 final 同时到达而重复计算。

#### 4.1.12.14 文档与用户说明 TODO

- [x] 更新 `README.md`：说明 provider 选择、OpenAI-compatible 与 Codex Subscription 的差异。
- [x] 更新 `README.md`：说明 Codex 模式需要本机安装 Codex CLI 并完成 `codex login`。
- [x] 更新 `README.md`：说明 Codex 模式不需要 OpenAI API Key，应用不读取/保存 Codex token。
- [x] 更新 `README.md`：说明 Codex working directory 应使用空目录或专用目录。
- [x] 更新 `README.md`：说明 `codex_use_streaming` 的用途和限制。
- [x] 同步更新 `README.en.md`。
- [x] 更新 `AGENTS.md` 中 Phase 5 后的最终行为、限制、测试要求。
- [x] 更新 `CODEX_PROVIDER_DEVELOPMENT.md` 的 Phase 5 TODO 状态和最终验收记录。
- [ ] 如有新增命令或环境要求，更新“编译与运行”章节。
- [x] 记录未完成的真实环境验证项，不把未验证内容写成已通过。

#### 4.1.12.15 最终命令验证 TODO

- [x] 运行 `go test ./...`。
- [x] 运行 `npm run build`。（通过 `task build` 的 `frontend:build` 步骤执行）
- [x] 运行 `task build`。
- [x] 运行 `git diff --check`。
- [x] 检查 `git status --short`，确认没有无关锁文件或构建产物差异。
- [x] 如 `npm install` 自动修改 `frontend/package-lock.json` 的无关 `peer` 标记，确认是否应还原。
- [x] 如果新增测试依赖或锁文件变更，必须说明原因并确认不是 npm 自动噪声。
- [ ] 如有可用真实 OpenAI-compatible API，记录连接测试、大纲、章节、停止任务回归结果。
- [ ] 如有可用真实 Codex 登录态，记录非流式/流式连接测试、大纲、章节、停止任务结果。

#### 4.1.12.16 Phase 5 完成标准

- [x] Provider 配置兼容、默认值、安全工作目录、错误分类均有自动化测试覆盖。
- [x] Codex app-server 事件解析、delta 去重、final-only 兜底、context cancel 均有自动化测试或明确手工验证。
- [ ] OpenAI-compatible 主要链路手工回归通过，或记录无法验证原因。
- [ ] Codex 非流式主要链路手工验收通过，或记录无法验证原因。
- [ ] Codex 流式主要链路手工验收通过，或记录无法验证原因。
- [x] 所有新增错误提示和 UI 文案中英文完整。
- [x] 安全审计确认不读取/保存/展示 Codex token，不泄露 OpenAI API Key。
- [x] 没有新增模型代理端点、WebSocket、公网监听或不必要第三方 Go 依赖。
- [x] `go test ./...` 通过。
- [x] `task build` 通过。
- [x] `git diff --check` 通过。
- [x] `README.md`、`README.en.md`、`AGENTS.md`、本开发文档与最终实现一致。
- [ ] 最终回复中明确列出已验证项、未验证项和残余风险。

### 4.1.13 Phase 6 详细 TODO：实用化验收与可排障交付

Phase 6 目标：把 Codex Provider 从“本地测试和构建通过”推进到“真实可用、可排障、可日常写作”的状态。Phase 6 不以继续扩展核心能力为主，而是完成真实环境端到端验收、补齐 handler/UI 自动化覆盖、验证 Codex 子进程生命周期、改善错误提示和文档排障路径，确保普通用户可以按文档配置并完成至少一条完整写作链路。

#### 4.1.13.1 当前离实用还缺的模块/功能

- [ ] **真实端到端验收模块**：当前主要是单元测试和构建验证，缺真实 OpenAI-compatible / Codex 登录态下的完整写作验证。
- [ ] **Codex 环境诊断能力**：当前连接测试只做一次模型调用，缺更明确的 `codex` 可执行文件、登录态、模型可用性、工作目录安全/可写等诊断。
- [x] **后端 handler 级测试**：已补 `GET/PUT/POST /api/config/api*` 端点级测试，覆盖 provider 切换时隐藏字段不丢失。
- [ ] **Codex 停止任务/进程回收验证**：当前依赖 context cancel + `exec.CommandContext`，但缺真实长任务取消后的进程残留检查。
- [ ] **配置页浏览器验收**：前端构建通过，但缺真实浏览器验证：切换 provider、保存、刷新、错误显示、移动端布局、Network payload。
- [ ] **错误提示实用化**：Codex 未登录、模型不存在、app-server 协议异常等错误仍偏底层，用户不一定知道下一步该做什么。
- [ ] **可复现验收样例**：缺短篇测试项目/最小验收流程，用来稳定验证“大纲 → 章节 → 停止 → 恢复”。

#### 4.1.13.2 阶段边界

- [x] 不新增模型代理端点。
- [x] 不读取、不保存 Codex token。
- [x] 不引入新的 Go 第三方依赖。
- [x] 优先补测试、验收脚本、错误提示和小范围修复。
- [x] 只有真实验收暴露 bug 时才改 provider 核心协议。
- [x] 所有代码或行为改动后同步 `AGENTS.md` 和本开发文档。
- [x] 不把未真实验证的能力标记为已完成。
- [ ] Phase 6 完成时必须明确列出已验证项、未验证项和残余风险。

#### 4.1.13.3 后端配置端点测试 TODO

- [x] 增加 `GET /api/config/api` 测试，确认返回 `provider`、OpenAI-compatible 字段、Codex 字段。
- [x] 增加 `PUT /api/config/api` 旧 payload 测试，缺 `provider` 时保存为 `openai_compatible`。
- [x] 增加 `PUT /api/config/api` Codex payload 测试，保存 `codex_model`、`codex_working_dir`、`codex_use_streaming`。
- [x] 测试保存 Codex 配置时不清空 `base_url` / `model` / `api_key`。
- [x] 测试保存 OpenAI-compatible 配置时不清空 Codex 字段。
- [x] 测试 `PUT /api/config/api` 保存失败时不写入部分损坏的 `api.json`。
- [x] 测试任务运行中 `PUT /api/config/api` 返回 409。
- [x] 测试任务运行中 `POST /api/config/api/test` 返回 409。
- [x] 测试 `PostAPITest` 在 `provider=codex` 时成功响应使用 `codex_model` 作为 `model`。
- [x] 测试 `PostAPITest` 在 `provider=codex` 时不会访问 OpenAI `/models`。
- [x] 测试 `PostAPITest` 成功响应不包含 API Key、Codex token、完整 prompt 或完整响应。

#### 4.1.13.4 Codex 环境诊断 TODO

- [ ] 梳理当前 `codex executable not found`、未登录、模型错误、协议错误的实际错误文本。
- [ ] 为 Codex CLI 不存在提供明确提示：检查是否安装 Codex CLI、是否在 PATH。
- [ ] 为未登录提供明确提示：先运行 `codex login`。
- [ ] 为模型不可用提供明确提示：检查配置页 `Codex model`。
- [ ] 为工作目录错误提供明确提示：换成空目录或专用目录。
- [ ] 为 app-server 协议异常提供明确提示：Codex CLI 版本可能不兼容。
- [ ] 为 context cancel / 用户停止任务提供明确提示：任务已取消，而不是误报 provider 故障。
- [x] 确认这些错误不会进入无限重试（自动化覆盖 fatal/retry 分类；真实 CLI 错误文本仍需实测补充）。
- [x] 确认错误不包含 token、API Key、完整 prompt（自动化覆盖 OpenAI-compatible HTTP 错误、Codex JSON-RPC 错误和 stderr 脱敏）。
- [ ] 如新增错误 key，补齐 `locale.go` / `messages.go` 中英文文案。

#### 4.1.13.5 Codex 非流式真实验收 TODO

- [ ] 在真实机器上确认 `codex` 命令可用。
- [ ] 完成 `codex login`。
- [ ] 配置 `provider=codex`、填写 `codex_model`、关闭 `codex_use_streaming`。
- [ ] 执行连接测试，确认返回成功。
- [ ] 创建一个短篇测试项目。
- [ ] 生成大纲，确认 JSON 可解析。
- [ ] 修订大纲，确认能保存。
- [ ] 确认大纲。
- [ ] 使用 Codex 非流式执行助理普通聊天。
- [ ] 记录非流式模式下章节生成路径是否会因直接流式要求而失败。
- [ ] 如果章节生成失败，明确 UI 错误是否可理解。
- [ ] 记录耗时和失败模式。
- [ ] 确认非流式模式日志不显示 Codex token、完整 prompt 或完整正文。

#### 4.1.13.6 Codex 流式真实验收 TODO

- [ ] 配置 `provider=codex`、开启 `codex_use_streaming`。
- [ ] 执行连接测试。
- [ ] 生成章节，确认写作页实时出现增量文本。
- [ ] 确认流式增量不会重复追加 final 全文。
- [ ] 章节完成后确认 progress 刷新展示完整正文。
- [ ] 确认 token usage badge 有合理估算变化。
- [ ] 章节生成中点击停止，确认任务结束。
- [ ] 停止后检查没有残留 `codex app-server` 子进程。
- [ ] 停止后确认章节状态没有误变成 accepted。
- [ ] 自动确认模式下连续生成至少 2 章。
- [ ] 长章节生成时确认前端不卡死。
- [ ] app-server 错误时 UI 展示明确错误，日志不泄露敏感信息。

#### 4.1.13.7 OpenAI-compatible 回归 TODO

- [ ] 使用真实或本地兼容 API 执行连接测试。
- [ ] 生成大纲。
- [ ] 修订大纲。
- [ ] 确认大纲。
- [ ] 生成章节并观察流式输出。
- [ ] 章节生成中停止任务。
- [ ] 确认章节。
- [ ] 执行去 AI 味/润色。
- [ ] 执行伏笔建议。
- [ ] 执行全局助理聊天。
- [ ] 至少执行一次短书全书优化分析。
- [ ] 确认新增 provider 抽象没有破坏旧链路。
- [ ] 确认 OpenAI-compatible 错误 body 脱敏不影响正常错误排查。

#### 4.1.13.8 前端配置页验收 TODO

- [ ] 旧 `api.json` 无 provider 时默认显示 OpenAI-compatible。
- [ ] OpenAI-compatible 字段显示完整。
- [ ] Codex 字段显示完整。
- [ ] Codex 模式隐藏 API Key / Base URL / Model。
- [ ] 切换 provider 不自动保存。
- [ ] 切换 provider 不清空隐藏字段。
- [ ] 保存后刷新页面，provider 和字段仍正确。
- [ ] 测试中按钮 loading 正常。
- [ ] 后端错误 toast 可读。
- [ ] 任务运行中全部输入、toggle、保存、测试按钮禁用。
- [ ] 中文 UI 文案完整。
- [ ] 英文 UI 文案完整。
- [ ] 窄屏下 provider 按钮和 Codex streaming 文案不溢出。
- [ ] Network payload 不包含 Codex token 字段。
- [ ] 浏览器控制台无 Svelte 运行时错误。

#### 4.1.13.9 进程生命周期和资源管理 TODO

- [ ] 构造长时间 Codex 生成任务。
- [ ] 点击停止后检查 app-server 子进程退出。
- [ ] app-server 初始化失败时检查子进程退出。
- [ ] thread/start 失败时检查子进程退出。
- [ ] turn/start 失败时检查子进程退出。
- [ ] JSON-RPC 非法响应时检查子进程退出。
- [ ] 多次生成后检查没有进程累积。
- [ ] 自动确认连续生成时确认任务锁仍只允许一个任务。
- [ ] 记录每次启动 app-server 的耗时，判断是否需要 Phase 7 做长连接复用。
- [x] 确认 context cancel、超时、app-server 错误、JSON-RPC 初始化失败四类路径都会释放 stdin/stdout/stderr 和子进程（已用 fake app-server 覆盖 context cancel；真实进程残留仍需实测核对）。

#### 4.1.13.10 实用性修复候选 TODO

这些不是预先承诺全部实现，Phase 6 真实验收后按问题决定是否进入修复：

- [ ] 如果非流式 Codex 不能写章节，配置页明确提示“章节生成需要开启 Codex streaming”。
- [ ] 如果 app-server 启动很慢，增加更明显的“正在启动 Codex”日志。
- [ ] 如果错误文本过底层，增加后端错误 key 和双语翻译。
- [ ] 如果模型经常输出非 JSON，增强 Codex JSON prompt 包装或 JSON 清理。
- [ ] 如果停止任务不稳定，增加更强的进程终止/超时清理。
- [ ] 如果工作目录默认路径不直观，在配置页显示后端实际默认路径。
- [ ] 如果每次启动 app-server 的耗时不可接受，评估长连接复用，但不能牺牲任务取消和安全隔离。
- [ ] 如果 Codex 模式下 Agent 工具调用格式不稳定，增加 Agent/Codex 专用输出约束或后处理。

#### 4.1.13.11 可复现验收样例 TODO

- [ ] 设计一个 3 章以内的短篇测试项目。
- [ ] 记录故事类型、标题、章节数、目标字数、最小角色/世界观设定。
- [ ] 记录 OpenAI-compatible 验收步骤和期望结果。
- [ ] 记录 Codex 非流式验收步骤和期望结果。
- [ ] 记录 Codex 流式验收步骤和期望结果。
- [ ] 记录停止任务验收步骤和期望结果。
- [ ] 确认验收样例不包含真实 API Key、token 或用户私有小说正文。
- [ ] 将验收结果写回本开发文档，不把未验证内容标成已完成。

#### 4.1.13.12 文档收口 TODO

- [x] 在 `CODEX_PROVIDER_DEVELOPMENT.md` 更新 Phase 6 TODO 状态和真实验收结果。
- [x] 在 `README.md` 增加 Codex 实用配置与排障小节。
- [x] 在 `README.en.md` 同步英文说明。
- [x] 在 `AGENTS.md` 同步真实验收后的最终行为、限制和测试要求。
- [ ] 记录真实验收使用的 Codex CLI 版本、操作系统、模型名。
- [ ] 明确未完成的真实环境验证项，不把未验证内容写成已完成。
- [ ] 如 Phase 6 引入新的命令或环境要求，更新“编译与运行”章节。

#### 4.1.13.13 命令验证 TODO

- [x] 运行 `go test ./...`。
- [x] 运行 `task build`。
- [x] 运行 `git diff --check`。
- [ ] 检查 `git status --short`，确认没有无关锁文件或构建产物差异。
- [x] 如 `npm install` 自动修改 `frontend/package-lock.json` 的无关 `peer` 标记，确认并还原。
- [x] 如新增测试依赖或锁文件变更，必须说明原因（未新增测试依赖；锁文件内容无最终差异）。

#### 4.1.13.14 Phase 6 开发执行结果

已完成的开发项：

- [x] 新增 `handlers_api_provider_test.go`，用 fake Codex app-server 覆盖配置端点、Codex 非流式回退、Codex 流式 delta 和 context cancel。
- [x] `PostAPITest` 成功响应不再返回模型正文 `sample`，只返回 `response_chars`。
- [x] `PostAPITest` Codex 模式响应 `model=codex_model`，并通过测试确认不会访问 OpenAI-compatible `/models`。
- [x] `PUT /api/config/api` 保存失败时不替换内存配置，避免磁盘写入失败后运行态配置与文件不一致。
- [x] `README.md` / `README.en.md` 增加 Codex 配置和排障说明。
- [x] `AGENTS.md` 同步 Phase 6 后端行为和新增测试覆盖。
- [x] `go test ./...` 通过。
- [x] `task build` 通过。
- [x] `git diff --check` 通过，仅有 CRLF 提示。

尚未替代真实环境实测的项：

- [ ] 真实 `codex` CLI 安装、`codex login`、真实账号模型可用性。
- [ ] Codex 非流式真实连接测试和至少一个非章节任务。
- [ ] Codex 流式章节生成、停止任务后无残留 app-server 子进程。
- [ ] OpenAI-compatible 真实或本地兼容 API 端到端写作回归。
- [ ] 浏览器中配置页 provider 切换、刷新、窄屏布局和 Network payload 验收。

阶段判断：Phase 6 的开发准备项已完成，可以进入实测阶段；但不能把“Codex 真实可用”标记为完成，必须在真实 Codex CLI 登录环境和浏览器中跑完上面的未验证项后才能关闭 Phase 6。

#### 4.1.13.15 Phase 6 实测 TODO

实测原则：每次只推进一个小步骤；完成后把命令输出、页面现象或错误信息记录回来，再进入下一步。

- [x] 1. 确认部署/实测所需工具可用。
  - Windows PowerShell 路径：`D:\repositories\show-me-the-story`
  - `go version`：`go1.25.4 windows/amd64`
  - `node --version`：`v25.0.0`
  - `npm --version`：`11.6.2`
  - `task --version`：`3.51.1`
  - `codex --version`：`codex-cli 0.141.0`
  - `where.exe codex`：
    - `C:\Users\ZNMLR\AppData\Local\Programs\OpenAI\Codex\bin\codex.exe`
    - `C:\Users\ZNMLR\AppData\Roaming\npm\codex`
    - `C:\Users\ZNMLR\AppData\Roaming\npm\codex.cmd`
- [x] 2. 重新构建发布二进制。
  - 命令：`task build`
  - 结果：`npm install` 完成；`vite build` 成功，生成 `dist/index.html`、`dist/assets/index-D3VLLNQ7.css`、`dist/assets/index-CfJwtpEr.js`；`go build -o show-me-the-story.exe .` 成功。
  - 备注：npm audit 提示 8 个漏洞（7 moderate，1 high），不影响本次构建通过，后续可单独评估依赖升级风险。
- [x] 3. 启动本地服务并确认页面可访问。
  - 命令：`.\show-me-the-story.exe .\test-runtime`
  - 命令行结果：服务启动成功，访问地址 `http://localhost:48090`；浏览器请求 `/`、静态资源、`/api/version`、`/api/projects`、`/api/projects/current` 均返回。
  - 截图：`result/1.png`，显示项目选择页，已有项目数为 0。
  - 实测发现：`.\test-runtime` 当时不存在，`main.go` 只在传入目录已存在时才使用该目录，因此本次实际回退到了仓库根目录，日志显示项目目录为 `D:\repositories\show-me-the-story\storys`。
- [x] 3.1 重新使用预先创建的 `test-runtime` 目录启动，确保实测数据隔离到 `test-runtime/storys`。
  - 命令：`New-Item -ItemType Directory -Force .\test-runtime` 后执行 `.\show-me-the-story.exe .\test-runtime`
  - 结果：浏览器页面仍为项目选择页；命令行确认 `程序目录: D:\repositories\show-me-the-story\test-runtime`，`项目目录: D:\repositories\show-me-the-story\test-runtime\storys`。
- [x] 4. 新建 3 章短篇实测项目。
  - 项目名：`phase6-smoke-zh`
  - 语言：中文
  - 结果：命令行无报错；截图 `result/2.png` 显示已进入配置页，顶部项目 badge 为 `phase6-smoke-zh`，语言 badge 为 `ZH`，当前为大纲阶段。
- [x] 4.1 配置短篇冒烟测试项目的故事参数。
  - 故事类型：科幻悬疑
  - 小说标题：雾港信号
  - 章节数量：3
  - 每章目标字数：800
  - 结果：保存成功；命令行无报错；顶部仍为大纲阶段。
- [ ] 5. OpenAI-compatible 路径回归测试。
  - 当前结果：用户没有可用 OpenAI-compatible API，本轮实测暂跳过；不作为 Codex 订阅链路实测阻塞项，后续如有兼容 API 再补测旧链路回归。
- [ ] 6. Codex 登录状态与非流式连接测试。
  - 第一次实测配置：`provider=codex`，`codex_model=gpt-5-codex`，`codex_use_streaming=false`，`codex_working_dir=D:\story-codex-work`，上下文预算 `300000`。
  - 第一次结果：页面 toast 显示 `连接超时（15秒）`，命令行无 provider 错误详情；截图：`result/3.png`、`result/4.png`。
  - 实测结论：15 秒对 Codex app-server 首次启动/真实账号响应过短，且同步连接测试失败时命令行缺少足够排障信息。
  - 修复：Codex 连接测试超时调整为 90 秒；OpenAI-compatible 保持 15 秒；连接测试开始/成功/失败/超时会通过 logger 输出到命令行和右侧日志面板，日志包含 provider、model、timeout/response_chars，不包含 API Key、Codex token、prompt 或完整响应。
  - 验证：新增 `TestPostAPITestCodexTimeoutLogsProviderContext`；`go test ./...` 通过；`task build` 通过。
  - 第二次结果：日志链路生效，约 6.9 秒后返回明确错误：`The 'gpt-5-codex' model is not supported when using Codex with a ChatGPT account.`；命令行和右侧日志面板均显示 provider/model/error。
  - 本地诊断：`codex doctor` 显示 `stored auth mode chatgpt`，配置默认模型为 `gpt-5.5 · openai`。
  - 下一步：将配置页 `Codex 模型` 从 `gpt-5-codex` 改为 `gpt-5.5` 后重新保存并测试连接。
  - 第三次结果：`gpt-5.5` 能进入真实连接流程，但 app-server 发出临时通知 `Reconnecting... 2/5`，stderr 含 `tls handshake eof`；旧逻辑把该临时重连通知当成致命错误，约 5.3 秒提前失败。
  - 修复：`CodexProvider` 将以 `Reconnecting...` 开头的 app-server `error` notification 视为临时通知并继续等待，直到 turn 最终完成、最终失败或连接测试超时。
  - 验证：新增 `TestCodexStartTurnIgnoresTransientReconnectNotification` 覆盖该事件序列。
  - 第四次结果：重启新二进制后，`gpt-5.5` 非流式连接测试成功；右侧日志显示 `模型连接测试成功：provider=codex model=gpt-5.5 response_chars=3`，耗时约 12 秒。
- [x] 7. Codex 非章节任务测试。
  - 操作：右侧助理发送“请用一句话说明你现在能帮助我做什么，不要调用工具。”
  - 结果：收到回复 `{"response":"我可以帮助你配置故事设定、创建角色与世界观、生成或修订大纲，并在确认后推进章节写作。"}`
  - 结论：Codex 非流式普通聊天链路可用；观察到回复带 JSON 包装，后续如影响 UI 阅读可作为格式优化项处理。
- [x] 7.1 Codex 非流式生成大纲。
  - 操作：在“大纲”页点击“生成大纲”。
  - 结果：截图 `result/5.png` 显示已生成 3 章大纲，章节状态均为“待写作”，并出现“确认大纲，开始写作”按钮。
  - 结论：Codex 非流式大纲生成链路可用，JSON 解析和前端展示正常。
- [x] 7.2 确认大纲进入写作阶段。
  - 操作：点击“确认大纲，开始写作”。
  - 结果：截图 `result/6.png` 显示顶部阶段已切换为“写作阶段”，进度为 `0/3章`，第 1 章处于“当前/待写作”，页面显示“生成本章”按钮。
- [ ] 8. Codex 流式章节生成测试。
  - 准备：在配置页开启 `codex_use_streaming=true` 并保存成功；模型 `gpt-5.5`，工作目录 `D:\story-codex-work`；命令行无报错。
- [ ] 9. 任务停止与子进程残留检查。
- [ ] 10. 浏览器配置页切换/刷新/Network payload 验收。
- [ ] 11. 汇总结果，决定关闭 Phase 6 或进入修复。

#### 4.1.13.16 Phase 6 完成标准

- [ ] OpenAI-compatible 端到端写作链路通过。
- [ ] Codex 非流式连接测试和至少一个非章节任务通过。
- [ ] Codex 流式章节生成通过。
- [ ] Codex 停止任务不残留子进程。
- [ ] 配置页 provider 切换、保存、刷新通过。
- [x] 后端配置端点测试覆盖字段保留。
- [ ] 错误提示足够用户排障。
- [x] 文档包含实用配置步骤和常见问题。
- [x] `go test ./...` 通过。
- [x] `task build` 通过。
- [ ] `git diff --check` 通过。
- [ ] 最终回复明确列出真实已验证项、未验证项和残余风险。

---

## 5. 新增配置设计

### 5.1 配置字段

在现有 API 配置结构中新增 Provider 类型。

建议结构：

```go
type APIProviderType string

const (
    ProviderOpenAICompatible APIProviderType = "openai_compatible"
    ProviderCodex            APIProviderType = "codex"
)

type APIConfig struct {
    Provider APIProviderType `json:"provider"`

    // Existing OpenAI-compatible fields
    BaseURL string `json:"base_url,omitempty"`
    APIKey  string `json:"api_key,omitempty"`
    Model   string `json:"model"`

    // New Codex fields
    CodexModel       string `json:"codex_model,omitempty"`
    CodexWorkingDir  string `json:"codex_working_dir,omitempty"`
    CodexUseStreaming bool `json:"codex_use_streaming,omitempty"`
}
```

如果项目已有类似结构，不要强行照搬。应尽量保持现有字段兼容，避免破坏旧的 `api.json`。

### 5.2 向后兼容

旧版配置没有 `provider` 字段时，默认视为：

```text
provider = openai_compatible
```

旧配置示例：

```json
{
  "base_url": "https://api.deepseek.com",
  "api_key": "...",
  "model": "deepseek-chat"
}
```

加载后应等价为：

```json
{
  "provider": "openai_compatible",
  "base_url": "https://api.deepseek.com",
  "api_key": "...",
  "model": "deepseek-chat"
}
```

### 5.3 前端配置页

配置页增加一个下拉框：

```text
模型提供方：
- OpenAI Compatible API
- Codex Subscription
```

当选择 `OpenAI Compatible API`：

```text
显示：
- API 地址
- 模型名称
- API Key
```

当选择 `Codex Subscription`：

```text
显示：
- Codex 模型名称
- Codex 工作目录
- 是否启用流式输出
```

默认建议：

```text
Codex 工作目录：留空使用后端安全默认目录，或填写专用空目录；不要使用源码目录、用户 home 根目录或磁盘根目录
是否启用流式输出：默认关闭，由用户确认后手动开启
```

说明文字建议：

```text
Codex Subscription 使用本机已登录的 Codex CLI。请先在终端运行 codex 并完成 ChatGPT 登录。本功能仅适合本机自用，不会读取或保存 Codex token。
```

---

## 6. Provider 抽象设计

新增统一接口，例如：

```go
type LLMProvider interface {
    Generate(ctx context.Context, req LLMRequest) (*LLMResponse, error)
    Stream(ctx context.Context, req LLMRequest) (<-chan LLMStreamEvent, error)
}
```

建议请求结构：

```go
type LLMMessage struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type LLMRequest struct {
    Model       string
    Messages    []LLMMessage
    Temperature *float64
    MaxTokens   *int
    Stream      bool
    Purpose     string
}

type LLMResponse struct {
    Content string
}

type LLMStreamEvent struct {
    Delta string
    Done  bool
    Err   error
}
```

`Purpose` 用于调试和日志，例如：

```text
outline_generation
chapter_writing
fact_check
polish
assistant_chat
foreshadowing
```

如果现有代码已有类似结构，优先复用，不要重复造太多结构。

---

## 7. OpenAIProvider 改造要求

将现有 OpenAI-compatible 调用逻辑移动或封装为：

```go
type OpenAIProvider struct {
    Config APIConfig
}
```

要求：

1. 行为和现有版本一致。
2. SSE 流式输出保持一致。
3. 错误提示保持一致。
4. 重试策略保持一致。
5. API Key 仍然只用于 OpenAI-compatible provider。
6. 不要改变用户现有配置的含义。

完成后，先运行现有功能，确认 OpenAI-compatible 模式完全可用。

---

## 8. CodexProvider 实现方案

### 8.1 优先实现方式：Codex app-server stdio

推荐通过 `codex app-server` 的 `stdio` 模式调用 Codex。

结构：

```text
Go backend
  -> os/exec 启动 codex app-server
  -> stdin 写入 JSON-RPC 请求
  -> stdout 读取 JSON-RPC 响应与通知
  -> 转换为 LLMResponse / LLMStreamEvent
```

不要默认使用 WebSocket。

不要默认开放本地端口。

### 8.2 启动方式

Go 端启动：

```go
cmd := exec.CommandContext(ctx, "codex", "app-server")
cmd.Stdin = ...
cmd.Stdout = ...
cmd.Stderr = ...
```

要求：

1. `stderr` 写入程序日志。
2. 如果找不到 `codex` 命令，返回明确错误。
3. 如果用户没有登录 Codex，返回明确错误。
4. 不要解析、读取、保存 Codex token。
5. 生成任务结束后，应释放进程资源。
6. 如果为了性能保留长连接，需要实现健康检查和退出清理。

第一版可以每次请求启动一个 app-server，先保证功能正确。后续再优化为复用进程。

### 8.3 JSON-RPC 调用流程

大致流程：

```text
1. 启动 codex app-server
2. 发送 initialize
3. 发送 initialized notification
4. 发送 thread/start
5. 发送 turn/start
6. 读取 item/agentMessage/delta 等事件
7. 直到 turn completed / failed
8. 汇总输出文本
```

`thread/start` 建议参数：

```json
{
  "method": "thread/start",
  "id": 10,
  "params": {
    "model": "用户配置的 Codex 模型",
    "cwd": "用户配置的 Codex 工作目录",
    "approvalPolicy": "never",
    "sandbox": "readOnly",
    "personality": "neutral",
    "serviceName": "show-me-the-story"
  }
}
```

如果 app-server 当前版本使用 `sandboxPolicy` 而不是 `sandbox`，应根据本机 Codex 版本生成的 schema 或实际响应调整。

`turn/start` 输入应包含完整的用户请求文本：

```json
{
  "method": "turn/start",
  "id": 20,
  "params": {
    "threadId": "...",
    "input": [
      {
        "type": "text",
        "text": "..."
      }
    ]
  }
}
```

### 8.4 Prompt 转换

现有 OpenAI-compatible API 使用 messages 数组：

```text
system
user
assistant
user
```

Codex app-server 的 turn 输入更接近单段用户输入。

因此需要将 messages 转换为一个完整文本。

建议格式：

```text
You are being used as the language model backend for a local personal novel-writing tool.

Important rules:
- Only return the requested writing content or requested JSON.
- Do not run shell commands.
- Do not edit files.
- Do not inspect the repository.
- Do not mention Codex.
- Do not explain implementation details.
- Follow the user's writing instructions exactly.

Conversation messages:

[system]
...

[user]
...

[assistant]
...

[user]
...
```

中文项目可以使用中文包装：

```text
你现在作为本机小说写作工具的模型后端使用。

重要规则：
- 只返回本次任务需要的正文或 JSON。
- 不要执行命令。
- 不要修改文件。
- 不要查看项目源码。
- 不要提到 Codex。
- 不要解释实现细节。
- 严格遵守写作任务要求。

下面是原始对话消息：
...
```

如果原始提示词要求返回 JSON，必须强调：

```text
本次任务要求返回合法 JSON。不要使用 Markdown 代码块。不要输出 JSON 以外的解释。
```

### 8.5 禁止工具行为

Codex 是 agent，不是普通聊天模型。这里必须尽量限制它只生成文本。

建议：

1. `approvalPolicy` 使用 `never`。
2. sandbox 使用 `readOnly`。
3. prompt 中明确禁止运行命令、修改文件、检查仓库。
4. Codex 工作目录不要设置为包含隐私文件的目录。
5. 第一版中不要启用任何 MCP。
6. 不要把 show-me-the-story 项目源码目录作为默认工作目录，建议使用用户数据目录或一个空临时目录。

---

## 9. 流式输出支持

show-me-the-story 现有功能强调实时流式输出，因此 CodexProvider 最终也需要支持流式输出。

Codex app-server 会产生增量事件，例如：

```text
item/agentMessage/delta
```

应将这些 delta 转换为项目现有 SSE 输出格式。

要求：

1. 前端体验尽量与 OpenAI-compatible 模式一致。
2. 每收到一段 delta，就推送到现有日志 / 正文展示通道。
3. 如果 CodexProvider 暂时无法稳定流式输出，第一版可以先实现非流式，但配置页应提示“Codex 流式输出暂未启用”。
4. 流式模式下，停止按钮必须能中断当前生成任务。
5. 中断时要杀掉或关闭对应 app-server 子进程。

---

## 10. 错误处理

### 10.1 codex 命令不存在

错误提示：

```text
未找到 codex 命令。请先安装 Codex CLI，并确认 codex 可以在当前终端中运行。
```

### 10.2 未登录 Codex

错误提示：

```text
Codex 尚未登录或登录已失效。请先在终端运行 codex，并选择 Sign in with ChatGPT 完成登录。
```

### 10.3 app-server 启动失败

错误提示：

```text
Codex app-server 启动失败。请确认 Codex CLI 版本支持 app-server，并查看程序日志中的 stderr 输出。
```

### 10.4 模型不可用

错误提示：

```text
当前 Codex 模型不可用。请检查 Codex 配置中的模型名称，或先在 Codex CLI 中确认该模型可以使用。
```

### 10.5 输出不是合法 JSON

如果某些任务要求 JSON，但 Codex 输出不合法 JSON：

1. 先尝试提取 JSON。
2. 如果仍失败，返回明确错误。
3. 不要静默吞掉错误。
4. 不要把错误内容写入章节正文。

错误提示：

```text
模型返回内容不是合法 JSON，任务无法继续。请重试，或切换为 OpenAI-compatible API 模式。
```

---

## 11. 安全要求

### 11.1 Token 安全

绝对不要做这些事情：

```text
读取 ~/.codex/auth.json 的 token
打印 token
保存 token
上传 token
把 token 写入 api.json
把 token 放入前端
```

程序只应调用本机 `codex` 命令，由 Codex CLI 自己管理登录态。

### 11.2 网络安全

默认不要开放任何本地端口。

如果后续为了调试或性能使用 WebSocket 模式：

```text
只能监听 127.0.0.1
必须有本地鉴权 token
不得监听 0.0.0.0
不得写成公网服务
```

### 11.3 文件安全

CodexProvider 只用于文本生成，不允许 Codex 修改文件。

建议：

```text
approvalPolicy = never
sandbox = readOnly
cwd = 用户数据目录或临时空目录
```

不要默认设置为源码目录，避免 Codex 误把自己当作开发助手去读写项目文件。

### 11.4 日志安全

日志中允许记录：

```text
provider 类型
模型名称
任务类型
耗时
错误类型
stderr 摘要
```

日志中禁止记录：

```text
Codex token
OpenAI API Key
完整 auth.json
完整用户小说正文
完整提示词
```

如果为了调试需要记录 prompt，应增加显式 debug 开关，默认关闭。

---

## 12. 合规要求

本功能定位为：

```text
本机个人自用的 Codex Provider。
```

不是：

```text
Codex 订阅转 OpenAI API 服务。
```

开发中应遵守：

1. 不共享账号。
2. 不共享凭据。
3. 不绕过速率限制。
4. 不绕过权限限制。
5. 不绕过安全措施。
6. 不把功能做成公网 API。
7. 不让其他人通过该功能使用当前用户的 Codex 订阅。
8. 不把该功能宣传为“免费 API 替代品”。

代码注释中可以加入说明：

```go
// CodexProvider is intended for local personal use only.
// It calls the local Codex CLI/app-server and does not expose
// an OpenAI-compatible API endpoint.
```

---

## 13. 测试计划

### 13.1 单元测试

新增测试：

```text
1. 旧 api.json 没有 provider 字段时，默认 OpenAI-compatible。
2. 新 api.json provider=openai_compatible 时，字段读取正确。
3. 新 api.json provider=codex 时，字段读取正确。
4. messages -> Codex prompt 转换正确。
5. JSON 输出提取逻辑正确。
6. codex 命令不存在时，错误信息明确。
7. app-server 返回错误时，错误信息明确。
```

### 13.2 手工测试

#### OpenAI-compatible 回归测试

```text
1. 配置 DeepSeek / OpenAI / Ollama。
2. 创建新项目。
3. 生成大纲。
4. 确认大纲。
5. 生成第一章。
6. 做事实核查。
7. 做章节润色。
8. 导出 TXT。
```

确认行为与修改前一致。

#### CodexProvider 测试

```text
1. 先在终端运行 codex，确认已通过 ChatGPT 登录。
2. 启动 show-me-the-story。
3. 配置 provider=Codex Subscription。
4. 填写 Codex 模型名称。
5. 创建测试小说项目。
6. 生成大纲。
7. 确认大纲。
8. 生成第一章。
9. 检查流式输出是否正常。
10. 检查停止按钮是否能中断生成。
11. 检查章节文件是否正确保存。
12. 检查日志中是否没有 token 和 API Key。
```

### 13.3 安全测试

```text
1. 搜索代码，确认没有读取 auth.json 的逻辑。
2. 搜索日志输出，确认不会打印 token。
3. 检查是否没有新增 /v1/chat/completions 之类通用 API 路由。
4. 检查没有监听 0.0.0.0 的新服务。
5. 检查 CodexProvider 默认不使用 WebSocket。
6. 检查 CodexProvider 默认不允许命令执行和文件修改。
```

---

## 14. 分阶段开发任务

### Phase 0：现状分析

任务：

```text
阅读当前代码，整理模型调用链路。
```

输出：

```text
1. 当前 API 请求结构
2. 当前流式输出结构
3. 当前配置保存结构
4. 推荐插入 Provider 抽象的位置
5. 预计改动文件
```

不要修改代码。

---

### Phase 1：抽象 Provider，不改变行为

任务：

```text
新增 LLMProvider 抽象。
将现有 OpenAI-compatible 调用封装为 OpenAIProvider。
```

要求：

```text
1. 所有现有功能保持不变。
2. 编译通过。
3. 现有测试通过。
4. 手工测试 OpenAI-compatible 模式可用。
```

---

### Phase 2：新增 CodexProvider 非流式版本

详细执行清单见本文 `4.1.9 Phase 2 详细 TODO：CodexProvider 非流式版本`。以下仅保留阶段摘要。

任务：

```text
新增 CodexProvider。
先实现非流式 Generate。
通过 codex app-server stdio 调用 Codex。
```

要求：

```text
1. 能完成一次简单文本生成。
2. 能用于生成大纲。
3. 出错时提示明确。
4. 不读取或保存 Codex token。
5. 不开放本地端口。
```

---

### Phase 3：支持流式输出

详细执行清单见本文 `4.1.10 Phase 3 详细 TODO：CodexProvider 流式输出与任务停止`。以下仅保留阶段摘要。

任务：

```text
读取 app-server delta 事件。
转换为项目现有 SSE 输出。
```

要求：

```text
1. 前端可以实时显示生成文本。
2. 停止按钮可以中断任务。
3. 中断后进程资源释放正常。
4. 出错时不会污染章节正文。
```

---

### Phase 4：配置页接入

详细执行清单见本文 `4.1.11 Phase 4 详细 TODO：前端配置页接入 Provider 选择`。以下仅保留阶段摘要。

任务：

```text
前端配置页增加 provider 选择。
后端配置读写支持 provider。
```

要求：

```text
1. 旧配置自动兼容。
2. OpenAI-compatible 字段只在对应模式显示。
3. Codex 字段只在对应模式显示。
4. 配置保存后重启仍然正确。
```

---

### Phase 5：测试、日志、安全检查

详细执行清单见本文 `4.1.12 Phase 5 详细 TODO：测试、日志、安全与回归验收`。以下仅保留阶段摘要。

任务：

```text
补充测试。
补充错误提示。
补充安全检查。
```

要求：

```text
1. OpenAI-compatible 模式回归通过。
2. CodexProvider 模式手工测试通过。
3. 日志不泄露 token / API Key。
4. 没有新增通用 API 代理。
5. 没有新增公网监听。
```

---

## 15. 建议的代码提交顺序

建议拆成多个 commit：

```text
commit 1: refactor: introduce LLM provider abstraction
commit 2: refactor: wrap existing OpenAI-compatible client
commit 3: feat: add Codex provider config model
commit 4: feat: add Codex app-server client
commit 5: feat: support Codex non-stream generation
commit 6: feat: support Codex streaming output
commit 7: feat: expose provider selection in settings UI
commit 8: test: add provider config and prompt conversion tests
commit 9: docs: document local Codex provider limitations
```

每个 commit 尽量保持可以编译。

---

## 16. 验收标准

最终完成后，必须满足：

```text
1. 原有 OpenAI-compatible API 模式正常可用。
2. 旧 api.json 不需要手工迁移。
3. 配置页可以选择 Codex Subscription。
4. Codex 模式不要求 OpenAI API Key。
5. Codex 模式通过本机已登录 Codex CLI 工作。
6. Codex 模式可以生成大纲。
7. Codex 模式可以生成章节。
8. Codex 模式支持停止当前任务。
9. Codex 模式不读取、不保存、不打印 Codex token。
10. 程序没有新增通用 OpenAI-compatible API 代理。
11. 程序没有新增公网服务。
12. 编译通过。
13. 基本手工测试通过。
```

---

## 17. 给 Codex 的执行要求

开发时请遵守：

```text
1. 先阅读代码，再给出修改计划。
2. 不要一次性大规模重写。
3. 每一步修改后运行编译或测试。
4. 不要删除现有功能。
5. 不要改变小说数据格式，除非必须。
6. 不要破坏旧配置。
7. 不要引入不必要的第三方 Go 依赖。
8. 如果需要 Node sidecar，先说明原因，再实现。
9. 安全相关代码要尽量保守。
10. 不确定 app-server 协议字段时，优先调用 codex app-server generate-json-schema 或 generate-ts 查看本机版本。
```

---

## 18. 第一条 Codex 指令建议

可以先对 Codex 输入：

```text
请阅读 CODEX_PROVIDER_DEVELOPMENT.md，并先不要修改代码。请先分析当前 show-me-the-story 的模型调用链路，列出需要改动的文件、最小改动方案、风险点和第一阶段修改计划。
```

等 Codex 输出分析后，再输入：

```text
请按 Phase 1 开始修改：新增 LLMProvider 抽象，并将现有 OpenAI-compatible API 调用封装为 OpenAIProvider。要求保持现有行为不变，修改后运行编译或测试。
```
