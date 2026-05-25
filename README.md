# AI 小说生成器

一个基于 OpenAI 兼容 API 的长篇小说自动生成工具。程序本身不包含任何小说内容，所有故事设定、写作风格、角色描述均由用户通过 Web UI 配置，或由 AI 在大纲阶段自动生成。

## 功能特性

- **零配置启动**：编译后单二进制即可直接运行，缺失配置文件时自动生成空白默认配置
- **Web UI**：浏览器操作，支持实时日志流（SSE）
- **内容无关**：程序代码不包含任何领域特定内容，适用于任意类型的小说创作
- **两阶段流程**：大纲生成 → 逐章创作
- **伏笔系统**：AI 自动规划伏笔方案，每章写作时注入活跃伏笔上下文，完成后自动追踪伏笔状态
- **用户审核**：每章完成后可审核、提出修改意见，AI 会修改当前章节并调整后续大纲
- **断点恢复**：任意步骤中断后重新运行，自动从上次中断处继续
- **事实核查**：每章写完后自动进行一致性检查，不通过则重新生成
- **零外部依赖**：仅使用 Go 标准库

## 快速开始

### 1. 编译

```bash
go build -o showmethestory .
```

### 2. 运行

```bash
./showmethestory
```

程序启动后会自动检测 `config.json`：
- 若不存在，自动生成空白默认配置并启动 Web UI
- 若已存在，直接加载

访问 `http://localhost:8080`，在 Web UI 中配置 API 地址、模型和故事设定后即可开始创作。

### 3. 可选：手动配置

也可手动创建 `config.json`：

```json
{
  "api_key": "your-api-key",
  "base_url": "https://api.example.com/v1/",
  "model": "gpt-4",

  "story": {
    "type": "奇幻/都市/科幻...",
    "chapter_count": 20,
    "target_words_per_chapter": 3000,
    "writing_style": "你的写作风格要求...",
    "character_setting": "角色设定...",
    "world_setting": "世界观设定...",
    "core_requirements": "核心写作要求..."
  }
}
```

`prompts` 字段可留空（`{}`），程序会使用内置的默认提示词模板。如需自定义，可覆盖任意 prompt，支持的模板变量见下方说明。

## 配置说明

### config.json 完整字段

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `api_key` | string | 否 | API Key，本地模型可留空 |
| `base_url` | string | 否 | OpenAI 兼容 API 地址，程序会自动补全 `/v1` |
| `model` | string | 否 | 模型名称 |
| `http_timeout_seconds` | int | 否 | HTTP 超时秒数，默认 300 |
| `story.type` | string | 否 | 故事类型标签 |
| `story.title` | string | 否 | 小说标题，留空则由 AI 生成 |
| `story.chapter_count` | int | 否 | 章节数，默认 30 |
| `story.target_words_per_chapter` | int | 否 | 每章目标字数，默认 2500 |
| `story.writing_style` | string | 否 | 写作风格描述 |
| `story.character_setting` | string | 否 | 角色设定 |
| `story.world_setting` | string | 否 | 世界观设定 |
| `story.core_requirements` | string | 否 | 核心写作要求 |
| `prompts.*` | string | 否 | 自定义提示词模板，留空使用默认值 |

### 自定义提示词模板

`prompts` 下可覆盖以下 7 个模板：

- `outline_generation` — 大纲生成
- `chapter_writing` — 章节创作
- `chapter_summary` — 摘要提炼
- `fact_check` — 事实核查
- `outline_revision` — 大纲修订
- `foreshadow_planning` — 伏笔规划
- `foreshadow_update` — 伏笔状态更新

模板中使用 `{{.KeyName}}` 作为占位符，可用变量取决于具体模板：

**outline_generation**：`StoryType`, `ChapterCount`, `TargetWords`, `WritingStyle`, `CharacterSetting`, `WorldSetting`, `CoreRequirements`

**chapter_writing**：`Title`, `ChapterNum`, `CorePrompt`, `CoreRequirements`, `HistorySummary`, `Foreshadows`, `ChapterTitle`, `ChapterOutline`, `WritingStyle`, `CharacterSetting`, `WorldSetting`, `TargetWords`

**chapter_summary**：`ChapterContent`

**fact_check**：`HistorySummary`, `ChapterContent`

**outline_revision**：`CurrentOutline`, `UserFeedback`, `LockedChapters`

**foreshadow_planning**：`Title`, `CorePrompt`, `CoreRequirements`, `Outline`, `CharacterSetting`, `WorldSetting`

**foreshadow_update**：`Title`, `ChapterNum`, `ChapterTitle`, `ChapterContent`, `HistorySummary`, `Foreshadows`, `CharacterSetting`, `WorldSetting`

## 运行流程

```
启动
 │
 ├─ config.json 不存在？
 │   └─ 自动生成空白默认配置，提示用户通过 Web UI 配置 API
 │
 ├─ 检测 progress.json？
 │   ├─ 是 → 加载进度，跳转到对应阶段
 │   └─ 否 → 进入大纲阶段
 │
 ▼
阶段一：大纲生成 (phase = "outline")
 │
 ├─ 调用 AI 生成大纲（标题、核心提示词、各章标题+大纲）
 ├─ 用户在 Web UI 审核大纲
 │   ├─ 确认 → 进入伏笔规划
 │   └─ 提出修改意见 → AI 修订后重新展示
 │
 ├─ [可选] 伏笔规划
 │   ├─ AI 根据大纲建议 3-8 条伏笔方案
 │   ├─ 用户在 Web UI 编辑、确认、删除伏笔
 │   └─ 确认后进入写作阶段
 │
 ▼
阶段二：逐章创作 (phase = "writing")
 │
 └─ 对每一章（从 current_chapter_index 开始）：
     ├─ 生成正文（注入前情摘要 + 活跃伏笔上下文）
     ├─ 提炼本章摘要
     ├─ 事实核查（FAIL 则自动重写，最多 3 次）
     ├─ 自动更新伏笔状态（planted → progressing → resolved）
     ├─ 伏笔超期告警（如有）
     ├─ 保存 Chapter_XX.md
     └─ 等待用户审核
         ├─ 确认 → 锁定该章节，推进到下一章
         └─ 提出修改意见 → AI 修改当前章节 + 修订后续大纲，重新审核
```

## 伏笔系统

伏笔系统用于在长篇小说中管理叙事线索的埋设与回收，解决模型缺乏长期记忆的问题。

### 工作原理

1. **规划阶段**：大纲确认后，AI 分析完整大纲，建议 3-8 条伏笔方案（含埋设章节、预计回收章节）
2. **写作注入**：每章写作时，所有活跃伏笔（planted/progressing）以结构化文本注入 prompt，包含描述、已有进展、回收建议
3. **自动追踪**：每章完成后，AI 分析正文自动更新伏笔状态（新增进展、推进、回收）
4. **超期告警**：若伏笔超过预计回收章节 3 章以上仍未回收，系统自动告警

### 伏笔生命周期

```
planted（已埋设）→ progressing（推进中）→ resolved（已回收）
                                         → abandoned（已放弃）
```

### API 端点

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/foreshadows` | 获取所有伏笔 |
| `POST` | `/api/foreshadows/suggest` | AI 建议伏笔（异步，结果通过 SSE 推送） |
| `POST` | `/api/foreshadows/confirm` | 批量确认 AI 建议的伏笔 |
| `POST` | `/api/foreshadows` | 手动创建伏笔 |
| `PUT` | `/api/foreshadows/{id}` | 更新伏笔 |
| `DELETE` | `/api/foreshadows/{id}` | 删除伏笔 |

## 断点恢复

程序在每个关键步骤后立即保存 `progress.json`。重新运行时：

| 上次中断位置 | 恢复行为 |
|-------------|---------|
| 大纲生成中（未展示） | 重新生成大纲 |
| 大纲等待用户确认 | 展示大纲，等待确认 |
| 章节正在生成 (`status=writing`) | 重新生成该章节 |
| 章节等待审核 (`status=review`) | 展示章节，等待审核 |
| 章节已确认 (`status=accepted`) | 跳过，继续下一章 |

## 输出文件

- `progress.json` — 完整的创作进度状态（含伏笔数据）
- `config.json` — 运行时配置（可通过 Web UI 修改）
- `Chapter_01.md`, `Chapter_02.md`, ... — 每章独立的 Markdown 文件，包含标题和摘要

## 文件结构

```
show-me-the-story/
├── main.go              # 入口 + 流程分发
├── config.go            # 配置结构体 + 加载 + 自动生成默认配置
├── api.go               # OpenAI 兼容 API 调用 + 重试
├── outline.go           # 大纲阶段：生成、修订
├── writing.go           # 写作阶段：创作、核查、摘要、伏笔注入
├── foreshadow.go        # 伏笔系统：建议、更新、注入、告警
├── state.go             # 进度/伏笔结构体 + 读写持久化
├── handlers.go          # HTTP API handlers
├── web.go               # Web 服务器 + 路由
├── logger.go            # SSE 日志广播
├── prompts.go           # 提示词模板渲染 + 内置默认模板
├── filesys.go           # 文件操作抽象
├── static/              # 前端静态文件（内嵌）
├── config.json          # 运行时配置（自动生成或手动创建）
├── config.example.json  # 完整配置示例
└── go.mod
```

## 注意事项

- API 地址兼容各种格式：带/不带 `/v1`、带/不带尾部斜杠均可
- 程序使用无限重试机制，网络故障时会自动等待并重试
- 修改 `config.json` 中的 `story` 字段不会影响已生成的进度（程序使用快照）
- 修改 `config.json` 中的 `prompts` 字段会在下次运行时生效
- `*.json` 已被 `.gitignore` 排除，不会被版本控制
