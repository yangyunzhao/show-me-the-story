# Fork 同步官方仓库操作手册

本文档用于把官方仓库 `https://github.com/Nigh/show-me-the-story` 的最新代码，定期同步到自己的 fork `https://github.com/yangyunzhao/show-me-the-story`，同时保留本地新增功能。

核心原则：

- 不使用 GitHub 网页上的 Sync fork 或网页冲突编辑器。
- 不执行 `git reset --hard upstream/main`、强推覆盖、删除本地改动等操作。
- 使用“同步分支 + merge 官方 main”的方式，让 Git 记录双方历史，由 Codex CLI 在本地处理冲突。
- 每次同步前先保证本地改动已提交，避免未提交改动和上游合并混在一起。

## 一次性准备

在仓库根目录执行：

```powershell
git remote -v
```

如果只看到自己的 fork：

```text
origin  https://github.com/yangyunzhao/show-me-the-story.git (fetch)
origin  https://github.com/yangyunzhao/show-me-the-story.git (push)
```

添加官方仓库为 `upstream`：

```powershell
git remote add upstream https://github.com/Nigh/show-me-the-story.git
git remote set-url --push upstream no_push
git fetch upstream
git remote -v
```

之后应看到：

```text
origin    https://github.com/yangyunzhao/show-me-the-story.git (fetch)
origin    https://github.com/yangyunzhao/show-me-the-story.git (push)
upstream  https://github.com/Nigh/show-me-the-story.git (fetch)
upstream  no_push (push)
```

`upstream` 的 push 地址故意设置为 `no_push`，防止误把自己的修改推到官方仓库。

## 每次同步的推荐流程

### 1. 确认本地工作区干净

```powershell
git status
```

如果有未提交改动，先让 Codex CLI 帮你整理并提交：

```powershell
codex
```

给 Codex CLI 的提示词：

```text
请检查当前仓库的未提交改动，解释每类改动的用途，然后把属于同一功能的改动整理成合理的 git commit。
不要丢弃任何用户改动。不要执行 git reset --hard。提交前请运行必要的验证命令。
```

只有当 `git status` 显示 `working tree clean` 时，再继续同步。

### 2. 先把本地提交推到自己的 fork

如果你刚刚让 Codex CLI 把本地改动整理成 commit，这些 commit 仍然只在本机。同步官方仓库前，建议先推到自己的 fork，给当前功能留一个远端备份点：

```powershell
git push origin main
```

如果你不确定当前分支是不是 `main`，先执行：

```powershell
git branch --show-current
```

如果当前不在 `main`，不要随手强推。让 Codex CLI 检查分支关系：

```powershell
codex
```

提示词：

```text
我已经把本地功能提交成 commit，但还没有 push。
请检查当前分支、origin/main 和本地提交关系，帮我把这些本地提交安全推到自己的 fork。
不要强推，不要执行 git reset --hard。
```

### 3. 拉取两个远端的最新历史

```powershell
git fetch origin
git fetch upstream
```

先把本地 `main` 更新到自己 fork 的最新状态：

```powershell
git switch main
git pull --ff-only origin main
```

如果 `git pull --ff-only` 失败，说明本地 `main` 和远端 `origin/main` 已经分叉。不要强行覆盖，改用 Codex CLI 处理：

```powershell
codex
```

提示词：

```text
git pull --ff-only origin main 失败，说明本地 main 与 origin/main 分叉。
请检查 git log --graph --oneline --decorate --all，判断应该 merge 还是 rebase，并在不丢失本地改动的前提下修复。
不要使用 git reset --hard，不要强推，完成后说明处理结果。
```

### 4. 创建本次同步分支

分支名建议带日期和时间，方便回退和排查：

```powershell
$d = Get-Date -Format "yyyyMMdd-HHmm"
git switch -c "sync/upstream-$d"
```

### 5. 合并官方最新代码

```powershell
git merge --no-ff upstream/main
```

如果没有冲突，Git 会直接生成 merge commit，继续执行验证。

如果出现冲突，不要去 GitHub 网页处理。保持现场，直接启动 Codex CLI：

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
4. 修改后运行验证命令，至少包括 go test ./... 和 go build -o show-me-the-story.exe .；如果前端相关文件变化，也运行前端构建。
5. 验证通过后完成 git add 和 git commit，生成清晰的 merge commit 信息。

限制：
- 不要执行 git reset --hard。
- 不要删除我新增的功能，除非能证明它已经被官方等价实现替代，并在最终说明中列出。
- 不要用 GitHub 网页处理冲突。
```

## 验证

同步分支完成合并后，至少执行：

```powershell
go test ./...
go build -o show-me-the-story.exe .
```

如果上游或本地改动涉及前端，继续执行：

```powershell
cd frontend
npm install
npm run build
cd ..
go build -o show-me-the-story.exe .
```

如果你不确定是否涉及前端，可以让 Codex CLI 判断：

```powershell
codex
```

提示词：

```text
请检查当前同步分支相对 main 和 upstream/main 的改动，判断需要运行哪些验证命令。
请直接运行验证，修复发现的问题。不要丢弃本地功能。
```

## 推送到自己的 fork

验证通过后，将同步分支推进自己的 fork：

```powershell
git push -u origin HEAD
```

如果你希望先保留一个可审查分支，到这里即可。以后确认没问题再合并。

如果你确认同步分支可用，并希望直接更新自己 fork 的 `main`：

```powershell
git switch main
git merge --ff-only sync/upstream-YYYYMMDD-HHMM
git push origin main
```

把 `YYYYMMDD-HHMM` 换成实际分支名里的日期时间。

如果 `git merge --ff-only` 失败，不要强行合并，交给 Codex CLI：

```powershell
codex
```

提示词：

```text
我准备把 sync/upstream-YYYYMMDD-HHMM 合并回 main，但 git merge --ff-only 失败。
请检查原因，在不丢失 main 和同步分支任何改动的前提下完成合并。
不要使用 git reset --hard，不要强推。完成后运行验证并推送 origin main。
```

## 日常同步频率

推荐节奏：

- 官方仓库活跃时：每 1 到 2 周同步一次。
- 自己准备开发大功能前：先同步一次，减少后续冲突。
- 自己刚完成大功能后：先提交自己的功能，再同步官方，避免未提交工作混入冲突。
- 官方有安全修复、重要 bug 修复或 release 时：尽快同步。

## 常见异常处理

### 合并后功能看起来被覆盖

不要立即回滚。先让 Codex CLI 对比历史：

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

### 验证失败

```powershell
codex
```

提示词：

```text
上游同步后验证失败。请读取失败日志，定位是官方变更、本地功能还是冲突解决导致的问题。
请修复并重新运行验证。不要删除本地功能来绕过验证。
```

### 想放弃本次同步但保留原有本地功能

如果同步分支还没有合并回 `main`，可以直接切回 `main`：

```powershell
git switch main
```

保留同步分支以便以后继续分析：

```powershell
git branch
```

如果确定这个同步分支不要了，可以删除本地分支：

```powershell
git branch -D sync/upstream-YYYYMMDD-HHMM
```

删除分支只会删除本次同步尝试，不会影响已经提交在 `main` 上的功能。执行前如果不确定，先问 Codex CLI：

```powershell
codex
```

提示词：

```text
我想放弃本次 upstream 同步尝试，但必须保留 main 上已有的所有功能。
请检查当前分支、未提交改动和分支关系，告诉我可以安全删除哪个 sync 分支。
不要执行 git reset --hard。
```

## 禁止操作

除非你完全知道后果，否则不要执行：

```powershell
git reset --hard upstream/main
git push --force origin main
git checkout -- .
```

这些命令容易把 fork 中的本地新增功能覆盖掉。遇到需要“回退”“清理”“解决冲突”的场景，优先把现场交给 Codex CLI。

## 一句话版本

每次同步都按这个顺序做：

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
```

有冲突就打开 `codex`，让它本地解决；验证通过后：

```powershell
go test ./...
go build -o show-me-the-story.exe .
git push -u origin HEAD
git switch main
git merge --ff-only sync/upstream-YYYYMMDD-HHMM
git push origin main
```
