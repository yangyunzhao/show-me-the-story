# 本地开发、部署与 Fork 同步手册

本文档面向当前 fork：

- 自己的仓库：`https://github.com/yangyunzhao/show-me-the-story`
- 官方仓库：`https://github.com/Nigh/show-me-the-story`
- 本地目录示例：`D:\repositories\show-me-the-story`

目标是让你可以安全完成四件事：

1. 安装依赖、构建并启动服务。
2. 第一次 clone 后配置官方仓库 `upstream`。
3. 每次修改后提交并推送到自己的 GitHub。
4. 定期从官方仓库更新代码，本地合并冲突，再推回自己的 fork。

核心原则：

- 不在 GitHub 网页上处理冲突。
- 不执行 `git reset --hard upstream/main`。
- 不执行 `git push --force origin main`。
- 不提交 `api.json`、`storys/`、`sessions/`、`test-runtime/`、`show-me-the-story.exe`、`frontend/dist/` 等配置、运行数据或构建产物。
- 遇到冲突、验证失败、分支分叉时，保留现场，交给 Codex CLI 在本地处理。

## 1. 安装部署与启动服务

### 1.1 准备环境

本项目后端是 Go，前端是 Vite + Svelte。建议先确认命令可用：

```powershell
go version
node --version
npm --version
```

项目要求：

- Go：项目当前使用 `1.25.1`
- Node.js / npm：用于构建 `frontend/`
- 可选：`task` 命令，用于运行 `Taskfile.yml` 中的快捷任务
- 可选：`codex` 命令，用于 Codex Provider 或让 Codex CLI 帮你解决冲突

如果要使用本项目内的 Codex Provider，需要先在终端完成：

```powershell
codex login
codex doctor
```

`codex doctor` 可以查看当前 Codex CLI 默认模型。配置页里填写的 Codex model 应该使用当前账号可用的模型。

### 1.2 克隆自己的 fork

```powershell
cd D:\repositories
git clone https://github.com/yangyunzhao/show-me-the-story.git
cd show-me-the-story
```

### 1.3 构建前端

首次构建或前端依赖变化时执行：

```powershell
cd frontend
npm install
npm run build
cd ..
```

`npm run build` 会把前端产物写入 `frontend/dist/`。该目录是构建产物，不需要提交。

### 1.4 构建后端单文件程序

```powershell
go build -o show-me-the-story.exe .
```

如果你安装了 `task`，也可以直接运行：

```powershell
task build
```

### 1.5 启动服务

在仓库根目录运行：

```powershell
.\show-me-the-story.exe
```

浏览器打开：

```text
http://localhost:48090
```

默认端口是 `48090`。如果需要改端口：

```powershell
$env:PORT="48100"
.\show-me-the-story.exe
```

### 1.6 开发模式启动

如果你要改前端页面，建议开两个终端。

终端 1：启动 Go 后端：

```powershell
go build -o show-me-the-story.exe .
.\show-me-the-story.exe
```

终端 2：启动前端开发服务：

```powershell
cd frontend
npm install
npm run dev
```

前端开发服务默认地址通常是：

```text
http://localhost:5173
```

Vite 会把 `/api` 请求代理到 Go 后端 `:48090`。

## 2. 第一次 clone 后配置官方 upstream

这一步只需要做一次。

`origin` 是你自己的 fork，负责 push 你的代码；`upstream` 是官方仓库，只负责 fetch 官方代码。为了防止误推官方仓库，`upstream` 的 push 地址故意设置为 `no_push`。

### 2.1 查看当前远端

```powershell
git remote -v
```

第一次 clone 自己 fork 后，通常只会看到：

```text
origin  https://github.com/yangyunzhao/show-me-the-story.git (fetch)
origin  https://github.com/yangyunzhao/show-me-the-story.git (push)
```

### 2.2 添加官方仓库

```powershell
git remote add upstream https://github.com/Nigh/show-me-the-story.git
git remote set-url --push upstream no_push
git fetch upstream
git remote -v
```

正确结果应类似：

```text
origin    https://github.com/yangyunzhao/show-me-the-story.git (fetch)
origin    https://github.com/yangyunzhao/show-me-the-story.git (push)
upstream  https://github.com/Nigh/show-me-the-story.git (fetch)
upstream  no_push (push)
```

### 2.3 如果 upstream 已经存在

如果执行 `git remote add upstream ...` 时提示已经存在，不要重复添加，改为确认地址：

```powershell
git remote -v
```

如果地址不对，再修正：

```powershell
git remote set-url upstream https://github.com/Nigh/show-me-the-story.git
git remote set-url --push upstream no_push
git fetch upstream
```

## 3. 每次修改后提交到自己的 GitHub

这是日常开发最常用的流程。它只处理你自己的改动，不涉及官方仓库同步。

### 3.1 查看改动

```powershell
git status
git diff --stat
```

如果你想让 Codex CLI 帮你检查改动：

```powershell
codex
```

提示词：

```text
请检查当前仓库的未提交改动，按功能分组说明。
确认没有 api.json、storys/、sessions/、test-runtime/、show-me-the-story.exe、frontend/dist/ 或密钥 token 被纳入提交。
不要丢弃任何改动，不要执行 git reset --hard。
```

### 3.2 运行验证

后端验证：

```powershell
go test ./...
```

如果改了前端，继续运行：

```powershell
cd frontend
npm run build
cd ..
```

最终确认 Go 二进制可构建：

```powershell
go build -o show-me-the-story.exe .
```

### 3.3 暂存改动

建议先看文件列表：

```powershell
git status --short
```

如果确认所有改动都应该提交：

```powershell
git add .
```

再次检查暂存区：

```powershell
git diff --cached --stat
git diff --cached --name-only
```

如果看到配置、运行数据或构建产物被误暂存，例如 `api.json`、`storys/`、`sessions/`、`show-me-the-story.exe`，不要提交。把它从暂存区移出：

```powershell
git restore --staged path\to\file
```

### 3.4 提交

```powershell
git commit -m "这里写清楚本次改动"
```

提交信息示例：

```powershell
git commit -m "Add Codex provider support"
git commit -m "Document fork upstream sync workflow"
git commit -m "Fix chapter polish validation"
```

如果改动很多，建议让 Codex CLI 帮你拆成多个 commit：

```powershell
codex
```

提示词：

```text
请把当前已验证的改动按功能拆成合理的 git commit。
提交前再次确认没有配置文件、运行数据、构建产物或密钥被提交。
不要丢弃任何改动，不要执行 git reset --hard。
```

### 3.5 推送到自己的 GitHub

通常你在 `main` 分支：

```powershell
git branch --show-current
git push origin main
```

如果 push 成功，会看到类似：

```text
To https://github.com/yangyunzhao/show-me-the-story.git
   oldsha..newsha  main -> main
```

如果出现：

```text
fatal: Unable to persist credentials with the 'wincredman' credential store.
```

但后面仍然出现 `main -> main` 或 `[new branch]`，说明 push 实际成功，只是 Git Credential Manager 没能保存凭据。以后可能还会要求重新登录，但本次推送已经完成。

如果 push 被拒绝，不要加 `--force`。交给 Codex CLI：

```powershell
codex
```

提示词：

```text
git push origin main 被拒绝。
请检查本地 main、origin/main 和未推送提交的关系，在不丢失本地提交的前提下安全推送。
不要强推，不要执行 git reset --hard。
```

## 4. 从官方仓库更新代码并推回自己的 fork

推荐在以下场景同步官方代码：

- 官方仓库有新 release。
- 官方修复了你需要的 bug。
- 你准备开发大功能，想先减少后续冲突。
- 每隔 1 到 2 周定期同步一次。

### 4.1 同步前确认本地干净

```powershell
git status
```

必须先保证没有未提交改动。如果有改动，先按第 3 章提交并推送到自己的 GitHub。

同步官方前建议至少确认：

```powershell
git branch --show-current
git status --short
git push origin main
```

如果 `git status --short` 没有输出，表示工作区干净。

### 4.2 拉取远端历史

```powershell
git fetch origin
git fetch upstream
```

把本地 `main` 更新到自己 fork 的最新状态：

```powershell
git switch main
git pull --ff-only origin main
```

如果 `git pull --ff-only origin main` 失败，说明本地 `main` 和 `origin/main` 已经分叉。不要强行覆盖，交给 Codex CLI：

```powershell
codex
```

提示词：

```text
git pull --ff-only origin main 失败，说明本地 main 与 origin/main 分叉。
请检查 git log --graph --oneline --decorate --all，判断应该 merge 还是 rebase，并在不丢失本地改动的前提下修复。
不要使用 git reset --hard，不要强推。
```

### 4.3 创建同步分支

不要直接在 `main` 上合并官方仓库。新建一个同步分支：

```powershell
$d = Get-Date -Format "yyyyMMdd-HHmm"
git switch -c "sync/upstream-$d"
```

示例分支名：

```text
sync/upstream-20260622-2225
```

### 4.4 合并官方 main

```powershell
git merge --no-ff upstream/main
```

可能出现三种结果。

结果 1：没有新代码需要合并：

```text
Already up to date.
```

这表示官方仓库当前没有你需要合并的新提交。仍然建议运行验证，然后推送你自己的 `main`，确保本地提交已保存到 GitHub。

结果 2：自动合并成功：

Git 会生成一个 merge commit。继续执行验证。

结果 3：出现冲突：

Git 会提示 `CONFLICT`。此时不要去 GitHub 网页，不要手工乱选 `ours` 或 `theirs`，不要执行 `git reset --hard`。保持冲突现场，打开 Codex CLI：

```powershell
codex
```

提示词：

```text
当前仓库正在执行 git merge --no-ff upstream/main，并且出现冲突。

目标：
1. 保留我 fork 中已有的本地功能和行为。
2. 合并官方 upstream/main 的最新修复和改进。
3. 对所有冲突文件逐个分析，不要简单选择 ours 或 theirs。
4. 修复后运行 go test ./...、npm run build、go build -o show-me-the-story.exe .。
5. 验证通过后完成 git add 和 git commit，生成清晰的 merge commit 信息。

限制：
- 不要执行 git reset --hard。
- 不要删除我新增的功能，除非能证明它已经被官方等价实现替代，并在最终说明中列出。
- 不要使用 GitHub 网页处理冲突。
```

### 4.5 验证同步分支

至少运行：

```powershell
go test ./...
```

如果官方或本地改动涉及前端，运行：

```powershell
cd frontend
npm run build
cd ..
```

最终运行：

```powershell
go build -o show-me-the-story.exe .
```

如果验证失败，交给 Codex CLI：

```powershell
codex
```

提示词：

```text
上游同步后验证失败。
请读取失败日志，定位是官方变更、本地功能还是冲突解决导致的问题。
请修复并重新运行验证。不要删除本地功能来绕过验证。
不要执行 git reset --hard。
```

### 4.6 推送同步分支到自己的 fork

```powershell
git push -u origin HEAD
```

这会把当前同步分支推到自己的 GitHub，例如：

```text
origin/sync/upstream-20260622-2225
```

这个分支可以作为备份和审查点。你不需要在 GitHub 网页上创建 PR，也不需要在网页上处理冲突。

### 4.7 合并同步分支回 main

如果同步分支验证通过：

```powershell
git switch main
git merge --ff-only sync/upstream-YYYYMMDD-HHMM
git push origin main
```

把 `YYYYMMDD-HHMM` 换成实际分支名。

如果 `git merge --ff-only` 显示：

```text
Already up to date.
```

说明 `main` 已经包含同步分支内容，可以直接确认 `git status` 并结束。

如果 `git merge --ff-only` 失败，说明 `main` 和同步分支关系不再是快进合并。不要强行处理，交给 Codex CLI：

```powershell
codex
```

提示词：

```text
我准备把 sync/upstream-YYYYMMDD-HHMM 合并回 main，但 git merge --ff-only 失败。
请检查原因，在不丢失 main 和同步分支任何改动的前提下完成合并。
不要使用 git reset --hard，不要强推。完成后运行验证并推送 origin main。
```

### 4.8 同步完成后的确认

```powershell
git status
git log --oneline -5
git remote -v
```

期望结果：

- `git status` 显示工作区干净。
- 最新提交包含你的本地功能提交、同步文档提交或上游 merge commit。
- `origin` 指向你的 fork。
- `upstream` fetch 指向官方仓库，push 是 `no_push`。

## 5. 常见异常处理

### 5.1 合并后怀疑本地功能被覆盖

不要立即回滚。让 Codex CLI 对比历史：

```powershell
codex
```

提示词：

```text
同步官方 upstream/main 后，我怀疑本地功能被覆盖。
请对比合并前后的提交和相关文件，找出功能是否丢失。
如果确实丢失，请从历史提交中恢复，并解释恢复了哪些文件和行为。
不要执行 git reset --hard。
```

### 5.2 想放弃本次同步

如果同步分支还没有合并回 `main`，可以切回 `main`：

```powershell
git switch main
```

保留同步分支以便以后分析：

```powershell
git branch
```

如果确定不要这个同步分支：

```powershell
git branch -D sync/upstream-YYYYMMDD-HHMM
```

不确定就先问 Codex CLI：

```powershell
codex
```

提示词：

```text
我想放弃本次 upstream 同步尝试，但必须保留 main 上已有的所有功能。
请检查当前分支、未提交改动和分支关系，告诉我可以安全删除哪个 sync 分支。
不要执行 git reset --hard。
```

### 5.3 GitHub 凭据保存失败

如果 push 时出现：

```text
fatal: Unable to persist credentials with the 'wincredman' credential store.
```

但后面仍然出现：

```text
main -> main
```

或：

```text
[new branch] HEAD -> sync/upstream-...
```

说明 push 已成功，只是凭据没有保存到 Windows 凭据库。以后可能需要重新登录 GitHub。

如果完全无法认证，重新登录 GitHub CLI 或 Git Credential Manager，或者让 Codex CLI 根据具体错误处理。

## 6. 禁止操作

除非你完全知道后果，否则不要执行：

```powershell
git reset --hard upstream/main
git push --force origin main
git checkout -- .
```

这些命令容易覆盖 fork 中的本地新增功能。遇到“回退”“清理”“解决冲突”“push 被拒绝”等问题，优先保留现场并交给 Codex CLI。

## 7. 快速命令清单

日常提交：

```powershell
git status
go test ./...
cd frontend
npm run build
cd ..
go build -o show-me-the-story.exe .
git add .
git diff --cached --stat
git commit -m "描述本次改动"
git push origin main
```

同步官方：

```powershell
git status
git push origin main
git fetch origin
git fetch upstream
git switch main
git pull --ff-only origin main
$d = Get-Date -Format "yyyyMMdd-HHmm"
git switch -c "sync/upstream-$d"
git merge --no-ff upstream/main
go test ./...
cd frontend
npm run build
cd ..
go build -o show-me-the-story.exe .
git push -u origin HEAD
git switch main
git merge --ff-only sync/upstream-YYYYMMDD-HHMM
git push origin main
```
