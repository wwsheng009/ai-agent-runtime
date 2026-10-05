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

1. 设计并实现控制序列旁路 API（`TerminalSession`/presenter + output port control 提交），
   含并发/顺序测试：控制序列不得插入一帧的字节中间，且帧写失败时不得静默吞掉模式切换。
2. 迁移 `inputbox_editor` `:264/267`（+ `:208/214`），hooks 增加 `ControlWriter`；
   迁移后从基线删除 `readPromptWithHooksContext`、`readPrompt` 两条目（-2 条目 / -6 refs）。
3. 迁移 transient/modal/agent-panel composer 的编辑器出口（补 hooks/sink）。
4. 迁移标题/铃装配到 control sink（commands 侧，审计 §2.2）。
5. `status.go` 兜底路径与 stderr 收编（交互期统一走动态状态行/日志文件）。
6. 单写端断言测试（注入计数 writer，断言交互期物理 writer 计数=1）+ 门禁运行说明文档化。

## 4. 验收

- 每次迁移：`go test ./cmd/aicli/ui/ -run TestUIInteractiveDirectWriterInventory`（计数必须按预期下降）+
  目标包回归 + 真机 e2e（粘贴、焦点切换、标题、铃、长文本粘贴）。
- 完成态：ui 生产文件直写基线只剩白名单类（被栅栏 surface / TRACE / 启动期 probe），
  交互期物理 writer 计数 = 1；CI 中门禁测试常开。
