# aicli 渲染 P0（写端归一）实施台账

> 分支：`feat/render-p0-writer-unification`（基于 `76e6f8cc`）
> 依据：`docs/plan/aicli-unified-render-architecture-audit-20261005.md` §2/§6（P0）。
> 硬规则：**迁移一处 → 从 `backend/cmd/aicli/ui/writer_inventory_test.go` 基线删除对应条目；
> 不得为任何新交互功能新增条目。** 门禁测试：`go test ./cmd/aicli/ui/ -run TestUIInteractiveDirectWriterInventory`。

## 1. 已完成

- [x] **ui 直写守卫 + 64 项债务基线**（commit `09190ee8`）。
  - 扫描语义：`ui/*.go` 生产文件的 `os.Stdout/os.Stderr` 触碰点（纯 `Fd()` 探测除外）、`fmt.Print*`、`TerminalOutput()`。
  - 基线分组：P0 实活目标（inputbox_editor ×3 组、status 兜底、osc_live）；lease/全屏已改道的 raw 兜底；
    legacy 死链打印机；被栅栏的 FixedBottomSurface；`TERM_SESSION_TRACE` 调试追踪。
- [x] **控制序列旁路 API + inputbox_editor 模式序列迁移**（commit `c159a118`）。
  - `TerminalSession.WritePromptEditorControl(sequence)`：以 `TransactionPromptEditor` kind 提交，
    与帧/历史共用 `transactionMu`（控制字节不可能插入帧字节中间）。
  - `LineEditorHooks.OnTerminalControl func(sequence string) bool`：宿主认领接口；未认领保留 raw 回退。
  - `inputbox_editor` 四处序列（`readPromptWithHooksContext` 启用/禁用、`readPrompt` 启用/禁用）改经钩子；
    写端基线 `readPromptWithHooksContext` 3→1、`readPrompt` 3→1、新增 `writeEditorControlSequence` 1（net -3 refs）。
  - 生产接线：主 / busy / merged / selection composer 全部经 `chatInteractionCoordinator.WritePromptEditorControl`
    → session（无 unified 会话时返回 false，回退 raw）。
  - 测试：`TestWriteEditorControlSequence{ClaimsViaHook,FallsBackToRawWriter}`、
    `TestTerminalSessionWritePromptEditorControl`；ui 全量绿。

## 2. 关键侦察结论（决定迁移顺序）

1. **bracketed-paste / focus-change 序列是承重写，不能 claim 后丢弃。**
   `inputbox_editor.go:264/267`（生产活路径）与 `:208/214`（无 hooks 包装器路径）直写
   `\x1b[?2004h/l`、`\x1b[?1004h/l`；unified 路径下没有其他组件启用这两个模式
   （`terminal.go` 的 `EnableBracketedPaste/EnableFocusChange` 只服务 legacy `Terminal`）。
   因此必须先提供**控制序列旁路 API**：session/presenter 内部把控制序列与帧写串行化
   （例如经 output port 的 control 通道或在同一 `terminalWriteMu` 临界区内提交），
   再由 `LineEditorHooks` 暴露 `ControlWriter`（或控制序列回调）。
2. `readPromptWithHooksContext`（`:264/267`）是生产活路径
   （`chat_input_queue.go:1205→1220` → `chat_composer.go:96`）；
   无 hooks 的 `readPrompt`（`:208/214`）只被本文件 3 个公共包装器（`:131/:139/:167`）调用，
   commands 侧未见调用——迁移时确认是否死链，一并改道或删除。
3. `writeEditorText`（`:439-451`）：主/busy/merged composer 已被 `OnTerminalWrite` 吞掉（无字节）；
   transient/modal/agent-panel composer 传空 hooks → raw 直写。需为这些 composer 补 hooks/sink。
4. 标题/铃：`terminal_title.go:52/74`、`terminal_bell.go:27` 拿的是构造时注入的 writer，
   `chat_setup.go:702/703` 传入 `os.Stdout`。这不是 ui 包内直写（不在本守卫基线），
   但属审计 §2.2 的 P0 项——需要 session/port 级 control sink 后改装配。
5. `terminal_session.go` 的 3 处 `fmt.Print` 由 `TERM_SESSION_TRACE` 门控，属诊断通道；
   迁移目标是把诊断输出与交互输出彻底分离（日志文件或显式诊断 sink），不阻塞主流程。

## 3. 下一步（按序执行）

1. [x] 控制序列旁路 API（`c159a118`）。
2. [x] `inputbox_editor` 模式序列迁移（`c159a118`）；余下 1 ref/函数是编辑器读循环的 stdin/stdout 绑定。
3. 迁移 transient/modal/agent-panel composer 的编辑器出口（补 hooks/sink；主/busy/merged/selection 已接线）。
4. 迁移标题/铃装配到 control sink（commands 侧，审计 §2.2）。
5. `status.go` 兜底路径与 stderr 收编（交互期统一走动态状态行/日志文件）。
6. 单写端断言测试（注入计数 writer，断言交互期物理 writer 计数=1）+ 门禁运行说明文档化。

## 3.1 已知基线问题（非本分支引入）

- `TestSuccessfulRequestBoundaryPreservesFortyLineFinalInNativeHistory`（commands）在分支基线 HEAD
  （`09190ee8`，不含本轮改动）上 A/B 对照同样失败：`BOUNDARY-FINAL-32` 未与 33 相邻（原生历史缺行）。
  该测试落在 `a75d1c89` 的 active 归档/贴底区域，需单独定位；本分支的 P0 改动与其失败无因果关系
  （commands 全量仅此 1 项失败，其余全绿）。

## 4. 验收

- 每次迁移：`go test ./cmd/aicli/ui/ -run TestUIInteractiveDirectWriterInventory`（计数必须按预期下降）+
  目标包回归 + 真机 e2e（粘贴、焦点切换、标题、铃、长文本粘贴）。
- 完成态：ui 生产文件直写基线只剩白名单类（被栅栏 surface / TRACE / 启动期 probe），
  交互期物理 writer 计数 = 1；CI 中门禁测试常开。
