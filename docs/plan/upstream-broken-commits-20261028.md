# 两处提交态不可构建（2026-10-28）——请作者补交

## 一句话

`internal/remotetools` 与 `cmd/aicli/commands` 在 **HEAD 的提交态**无法编译；两个仓库工作区之所以能构建，只是因为各自还压着未提交的配套改动。任何在干净检出上跑 CI、bisect 或 `git worktree` 的人都会撞到。

## 复现（干净检出，不用碰你的工作区）

```
git worktree add --detach ../probe HEAD
cd ../probe/backend
go build ./internal/remotetools/ ./cmd/aicli/commands/
```

## 案例 1：`wire.MountLabels` 字段未提交

```
internal/remotetools/gateway.go:634:4: unknown field Hostname in struct literal of type wire.MountLabels
        :635 OS  :636 Arch  :637 Version  :638 Instance  :639 Backend  :640 ReadOnly
        :643: labels.ToolCount undefined   :644: labels.Tools   :645: labels.Roots
```

- 引入使用方的提交：`8f325893 feat(remote-tools): improve shell diagnostics, policy tooling and CLI help`
- 缺的字段在 `internal/gateway/wire/messages.go`（仍在作者工作区未提交）
- 修法：**补交 `wire/messages.go`**（或把 `gateway.go` 的这几处对齐回退），二者取一但必须同一笔

## 案例 2：`importSessionResult` / `importRequest` 字段未提交

```
cmd/aicli/commands/transfer_import.go:214:3: unknown field SourceFile in struct literal of type importRequest
cmd/aicli/commands/transfer_import.go:227:10: result.SourceAgent undefined
cmd/aicli/commands/transfer_import.go:228:10: result.SourceView undefined
```

- 引入使用方的提交：`4c2f0f56 feat(aicli): add interactive cross-agent session transfer`
- 修法：补交结构体字段（同一笔）

## 案例 3（新增 2026-10-28，非构建问题而是 **Linux 上恒红**）：`TestGrepWalkerBudgetBoundsTheSearch`

```
--- FAIL: TestGrepWalkerBudgetBoundsTheSearch (0.03s)
    grep_engine_order_test.go:121: a search that ran past its budget must be marked TimedOut
```

- 归属：`41360369 perf(grep): answer with ripgrep first and bound the pure-Go walker`（作者请自查）
- **与 I/O seam 那条线无关，已实证**：把整个 seam 线之前的代码（`39658009^`，即 `89f2aeaf`）
  clone 到 WSL 的 ext4 上跑同一条用例，`-count=5` **同样 FAIL、错误信息一字不差**。
- 机理（高置信，非猜测）：该用例把 `GrepSearchBudget` 压到 **1ms** 墙钟，同时
  `Limit: 10`、400 个文件全部命中。`TimedOut` 只在 `visit()` 里置位，所以只要"取满一页 10 条"
  早于预算到期发生，遍历就会以 `SkipAll`（页满）而非"预算到期"结束 → `TimedOut == false`。
  Windows 的临时盘慢，赶在 1ms 内取不满一页，于是同一条用例在 Windows 绿、在快盘 Linux 红。
- 同一文件 **153 行已经有一条用 `GrepBudgetAfterVisits` 的确定性兄弟用例**，且该开关的自带注释
  写的就是"墙钟预算在速度未知的机器上天生有race，故提供访问计数替代"。因此本条建议：
  要么让这条也走确定性开关，要么把夹具大小与 `Limit` 的关系改成"任何机器都无法在 1ms 内取满一页"
  （前者更稳，推荐）。
- 我没有改这条测试：**不是我的回归，且改他人测试凑绿是把红点藏起来而不是修掉**。留此单给作者。
- 影响面：`searchcore` 在 Linux 上整包为红，任何在 Linux 跑 CI / 在 envd 目标机上验证的人都会撞到；
  envd 交付（S6）在真机上跑共享核测试前，需要先处理这一条，否则"真机全绿"永远做不到。

## 为什么现在就提

1. **bisect 断点**：从 `8f325893` 起，任何干净检出都构建不过，`git bisect` 与"在某个提交上验证"全部失效。
2. **阻塞别人的验证**：`envd-line-window-m2` 分支（行窗口 M2）已 rebase 到最新 main 并通过全部本地测试与真机验收，但**合并前必须在干净基线上重跑构建**；只要这两处未补交，这一步就无法完成。
3. 现在补的成本极低（各 1 个文件，字段已在他们工作区里）。

## 顺带（不是构建问题，但同源）

同一批工作区里 `backend/cmd/aicli-remote-tools/{main.go,help.go}`、`backend/internal/remotetools/config.go` 也带着未提交的 optional-tools/network 改动。它们与 M2 分支改的是**同一批文件的不同区域**：在你们提交之前，M2 无法合并（`git merge` 会正确地拒绝覆盖他人 WIP）。请一并落地。

## 谁需要动手

- 案例 1：`8f325893` 的作者（gateway/wire 线）
- 案例 2：`4c2f0f56` 的作者（cross-agent transfer 线）
- 案例 3：`41360369` 的作者（grep walker 预算线）——这条会让 `searchcore` 在 Linux 上整包红，
  也挡住 envd 真机验收里的"共享核测试全绿"

修复后请回话，我这边一条命令即可完成 rebase + 构建 + 测试 + 真机三套验收 + 合并（清单见 `envd-view-line-window-design-20261028.md` §13.6）。