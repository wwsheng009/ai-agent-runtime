package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// chat_screen_framework.go 是统一副屏框架（方案
// docs/plan/aicli-tui-alt-screen-framework-plan-20260927.md §4，批次 0）。
//
// 目标流程：主屏 → 命令 → 副屏（alternate screen）→ Esc → 主屏。
// 框架是**唯一**调用 AcquireAlternateScreen/Release 编排副屏的命令侧入口：
//   - 按 chatScreenKind 复用既有三族渲染原语（不新增渲染路径）；
//   - 生命周期统一为「post Open barrier → 渲染 → post Close barrier →
//     release → waitUIActorIdleBounded → 才允许 mutate Scene」（I2）；
//   - 租约等待预算与能力门只有一份实现（I7），忙时/空闲共用；
//   - Esc 契约由 chatScreenEscPolicy 收敛（默认 TopLevelClose，D-B）；
//   - 禁止嵌套（D-D / I9），能力不足或租约超时一律降级为主屏内联文档
//     单元格（D-F / I6），绝不落 legacy stdout 直写；
//   - 生命周期事件走 logpkg debug 通道（§8.1），计数器以原子计数暴露给
//     /debug display（§8.2）。
//
// 框架不读 stdin、不直接写 TTY（I8）：输入循环由 ui 包既有原语负责，
// 框架只持有租约与 actor 屏障。

const (
	// chatScreenFrameworkEnvName 是框架级特性开关（§6.1）。
	chatScreenFrameworkEnvName = "AICLI_CHAT_SCREEN_FRAMEWORK"

	// 副屏生命周期事件（§8.1，logpkg debug 通道；只含 ID/枚举/耗时）。
	chatEventScreenOpen      = "aicli.chat.screen.open"
	chatEventScreenClose     = "aicli.chat.screen.close"
	chatEventScreenDegrade   = "aicli.chat.screen.degrade"
	chatEventScreenError     = "aicli.chat.screen.error"
	chatEventScreenViolation = "aicli.chat.screen.violation"
)

// ---------------------------------------------------------------------------
// 开关（§6.1）
// ---------------------------------------------------------------------------
//
// 批次 5（D-E）后框架只有 unified 一条路径：AICLI_CHAT_SCREEN_FRAMEWORK 仅
// 接受空/unified；其他值不再回退旧实现，而是按 unified 运行并由 openChatScreen
// 记 unknown_env 降级事件（环境变量读取保留，便于观测配置漂移）。

type chatScreenFrameworkMode uint8

// chatScreenFrameworkUnified 是唯一模式：副屏一律经统一框架执行。
const chatScreenFrameworkUnified chatScreenFrameworkMode = iota

func (m chatScreenFrameworkMode) String() string { return "unified" }

// chatScreenFrameworkEnvLookup 抽成包级变量以便测试注入开关值。
var chatScreenFrameworkEnvLookup = func() string { return os.Getenv(chatScreenFrameworkEnvName) }

// chatScreenFrameworkModeResolve 解析开关：未知值按默认 unified 处理，
// 并以 unknown=true 提示调用方记 unknown_env 降级事件。
func chatScreenFrameworkModeResolve() (mode chatScreenFrameworkMode, raw string, unknown bool) {
	raw = strings.ToLower(strings.TrimSpace(chatScreenFrameworkEnvLookup()))
	switch raw {
	case "", "unified":
		return chatScreenFrameworkUnified, raw, false
	default:
		return chatScreenFrameworkUnified, raw, true
	}
}

// ---------------------------------------------------------------------------
// 核心抽象（§4.1）
// ---------------------------------------------------------------------------

// chatScreenKind 是副屏形态；渲染仍复用既有三族原语。
type chatScreenKind uint8

const (
	// chatScreenDocument 是只读文档/表格（B 族：debug overlay 原语）。
	chatScreenDocument chatScreenKind = iota
	// chatScreenList 是单阶段列表选择（A 族：fullscreen list 原语）。
	chatScreenList
	// chatScreenStages 是多阶段选择：阶段在**同一租约内**推进（D-D）。
	chatScreenStages
)

func (k chatScreenKind) String() string {
	switch k {
	case chatScreenList:
		return "list"
	case chatScreenStages:
		return "stages"
	default:
		return "document"
	}
}

// chatScreenEscPolicy 是副屏内 Esc 契约（D-B）。
type chatScreenEscPolicy uint8

const (
	// chatScreenEscTopLevelClose 是默认值：一次 Esc 关闭副屏回主屏。
	// 唯一例外是列表搜索态（先清查询/退出搜索），由 ui 原语自身实现。
	chatScreenEscTopLevelClose chatScreenEscPolicy = iota
	// chatScreenEscStepBack 保留为扩展点：首期任何命令不得启用（D-B）。
	chatScreenEscStepBack
)

func (p chatScreenEscPolicy) String() string {
	if p == chatScreenEscStepBack {
		return "step_back"
	}
	return "top_level_close"
}

// chatScreenRow 是 ScreenList/ScreenStages 的一行；SearchText 供搜索原语
// 建立索引，Detail 是次行说明。
type chatScreenRow struct {
	Title      string
	Detail     string
	SearchText string
}

// chatScreenStage 是一个有序阶段（provider → model → reasoning）。
type chatScreenStage struct {
	ID    string
	Title string
	Rows  []chatScreenRow
}

// chatScreenSpec 是一次性快照（§4.2 所有权）：在 openChatScreen 之前构建
// 完毕，租约存续期只读。
type chatScreenSpec struct {
	ID    string
	Title string
	Kind  chatScreenKind
	// Subtitle 是 ScreenList 的副标题（显示在标题下、行列表上），
	// 用于引导用户操作（如 "选择 agent 查看详情与输出"）。
	Subtitle string
	// ConfirmLabel 覆盖 ScreenList 的确认键标签（默认"选择"）。
	ConfirmLabel string
	// Doc 是 ScreenDocument 的静态内容。
	Doc render.Document
	// Rows 是 ScreenList 的行。
	Rows []chatScreenRow
	// Stages 是 ScreenStages 的有序阶段。
	Stages []chatScreenStage
	// Esc 是 Esc 契约；零值即 TopLevelClose。
	Esc chatScreenEscPolicy
	// Refresh/RefreshHint 让只读页支持 B 族既有的按 r 刷新能力（收编时
	// 行为保持，不引入第二套刷新循环）。
	Refresh     ui.DebugOverlayRefreshFunc
	RefreshHint string
	// RunDocument 覆盖 ScreenDocument 的渲染原语：批次 1 起 transcript 页
	// （/history、Ctrl+T）复用既有分页器，但仍由框架持有租约与生命周期。
	RunDocument func(*ChatSession, ui.ScreenLease, chatScreenSpec) error
	// RunScreen 覆盖交互体（批次 2 起的 A 族 picker）：picker 的富交互
	// （即时搜索、实时预览、多阶段、窗口化分页）在框架租约内原样运行，租约
	// 获取/释放、open/close 屏障、计数器与降级仍由框架统一承担。
	RunScreen func(*ChatSession, ui.ScreenLease, chatScreenSpec) chatScreenOutcome
	// OpenAction/CloseAction 覆盖框架默认 barrier（OpenScreenOverlay /
	// CloseScreenOverlay）：picker 保留各自的 UI actor 动作身份
	// （OpenThemePicker 等），actor 侧状态机与批次 2 之前逐帧一致。
	OpenAction  func(leaseID uint64) ui.UIAction
	CloseAction func(leaseID uint64) ui.UIAction
	// AfterClose 在 openChatScreen 返回后执行，即 close 序列
	// （post Close → release → waitUIActorIdleBounded）完成之后（I3）：
	// 选中/取消后的会话变更与错误文案渲染都只能发生在这里，绝不与副屏帧
	// 或主屏恢复重叠。outcome 与 openChatScreen 的返回值一致。
	AfterClose func(*ChatSession, chatScreenOutcome)
	// Effect 是复合交互入口：dispatch 在命令渲染完成后调用 Effect(session)，
	// 而不是由 openChatScreen 直接开屏。仅用于一次命令需要多段副屏交互的
	// A 族 picker（/backtrack、/resume、/model、/skills、/export、/mcp）：
	// 它们的 opener 需要先做 actor/窗口预检、或需要 server→action→confirm
	// 这类多次连续租约，每段交互仍由 openChatScreen 承担租约与 close 序列
	// （A2/I1/I3 不变），本字段只负责"把 opener 推迟到命令提交之后"。
	// 它不是旧 Open* 字段的兼容通道：单屏 Spec 必须直接使用 RunScreen/Doc。
	Effect func(*ChatSession)
	// DegradeDoc 覆盖降级时的内联文档；为零值时按 Kind 投影。
	DegradeDoc render.Document
	// SilentDegrade 保持"能力不足时不渲染"的旧行为（如 /debug display 与
	// transcript pager）：只计数、只记 degrade 事件，不写内联单元格。
	SilentDegrade bool
	// ForceInline 让框架跳过租约，直接按降级路径渲染 Doc；
	// ForceInlineReason 是降级事件 reason（如 unavailable）。
	ForceInline       bool
	ForceInlineReason string
	// Trigger 标记触发来源（command / busy / hotkey），只进日志字段。
	Trigger string
}

// chatScreenCloseResult 是 close 事件的 result 枚举（§8.1）。
type chatScreenCloseResult string

const (
	chatScreenClosedEsc     chatScreenCloseResult = "esc"
	chatScreenClosedConfirm chatScreenCloseResult = "confirm"
	chatScreenClosedError   chatScreenCloseResult = "error"
)

// chatScreenOutcome 是一次副屏交互的结果；选择类结果只在租约释放后由调用
// 方应用（I3）。
type chatScreenOutcome struct {
	Result chatScreenCloseResult
	// Index 是最后一次确认的选择下标；未选择为 -1。
	Index int
	// StageID 是多阶段路径中最后确认阶段的 ID。
	StageID string
	// Degraded 表示未进入副屏（能力不足 / 租约忙 / 嵌套 / 非法 Spec）。
	Degraded      bool
	DegradeReason string
	Err           error
}

// 框架错误哨兵（§4.5）。
var (
	// ErrScreenNested 表示重入/嵌套打开：不排队、不阻塞（D-D / I9）。
	ErrScreenNested               = errors.New("chat screen framework: nested screen open rejected")
	errChatScreenInvalidSpec      = errors.New("chat screen framework: invalid screen spec")
	errChatScreenActorNotIdle     = errors.New("chat screen framework: actor did not reach idle after close")
	errChatScreenStateUncommitted = errors.New("chat screen framework: open barrier was not committed")
	errChatScreenRenderNotReady   = errors.New("chat screen framework: screen render not ready after lease barrier")
	errChatScreenEmptyStage       = errors.New("chat screen framework: stage has no selectable rows")
)

// ---------------------------------------------------------------------------
// 生命周期入口
// ---------------------------------------------------------------------------

// chatScreenLeaseWaitBudget 是副屏租约等待预算（§4.2：沿用忙时计划的 2s）。
const chatScreenLeaseWaitBudget = chatBusyScreenWaitBudget

// chatScreenOpenActive 是进程内框架副屏计数：>0 即已有框架副屏在途，
// 任何再次打开都是嵌套（D-D）。
var chatScreenOpenActive atomic.Int32

// chatScreenOpenActiveForTest 只读计数，供测试断言无泄漏。
func chatScreenOpenActiveForTest() int32 { return chatScreenOpenActive.Load() }

// chatScreenCapability 是副屏能力门（fail-closed，I7 单一实现；忙时/空闲共用）。
// 生产实现与 /debug display 同款判定：统一渲染面 + 已启用的统一 surface +
// 无在途租约/弹层 + 终端支持全屏列表。抽成包级变量以便生命周期测试注入
// 替身（终端能力无法在单测环境真实满足）。
var chatScreenCapability = func(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput ||
		session.Interaction == nil || session.Surface == nil {
		return false
	}
	if !unifiedDirectInteractiveOutput(session) {
		return false
	}
	if !session.Surface.Enabled() || !session.Surface.OwnedViewport() ||
		session.Surface.LeaseActive() || chatSessionPopupPort(session).HasActivePopup() {
		return false
	}
	return ui.CanUseFullScreenList(resumeFullScreenTerminal(session))
}

// openChatScreen 打开一次副屏并完成整个生命周期（§4.3）。
//
// 返回的 outcome 在 close 序列（post Close → release → actor idle）完成之后
// 才交给调用方，因此调用方在返回后应用选择结果是安全的（I3）。
func openChatScreen(session *ChatSession, spec chatScreenSpec) chatScreenOutcome {
	outcome := chatScreenOutcome{Result: chatScreenClosedError, Index: -1}
	startedAt := time.Now()

	_, raw, unknown := chatScreenFrameworkModeResolve()
	if unknown {
		_ = raw
		chatScreenEmitDegrade(spec, "unknown_env")
		chatScreenCounters.degradeUnknownEnv.Add(1)
	}

	if err := spec.validate(); err != nil {
		chatScreenEmitViolation("I1", spec.ID, "invalid_spec")
		outcome = chatScreenDegradeInline(session, spec, "invalid_spec")
		outcome.Err = errors.Join(errChatScreenInvalidSpec, err)
		return outcome
	}

	if spec.ForceInline {
		reason := strings.TrimSpace(spec.ForceInlineReason)
		if reason == "" {
			reason = "unavailable"
		}
		return chatScreenDegradeInline(session, spec, reason)
	}

	// 嵌套检测：不排队、不取第二个租约（D-D）。
	if chatScreenOpenActive.Load() > 0 {
		chatScreenEmitViolation("I9", spec.ID, "fail_closed_close")
		outcome = chatScreenDegradeInline(session, spec, "nested")
		outcome.Err = ErrScreenNested
		return outcome
	}

	if !chatScreenCapability(session) {
		reason := "unavailable"
		if session != nil && session.Surface != nil && session.Surface.LeaseActive() {
			reason = "busy"
		}
		return chatScreenDegradeInline(session, spec, reason)
	}

	if !chatScreenOpenActive.CompareAndSwap(0, 1) {
		chatScreenEmitViolation("I9", spec.ID, "fail_closed_close")
		outcome = chatScreenDegradeInline(session, spec, "nested")
		outcome.Err = ErrScreenNested
		return outcome
	}
	defer chatScreenOpenActive.Store(0)

	lease, waitMs, err := chatScreenAcquireLease(session, spec)
	if err != nil {
		stage, reason := "acquire", "unavailable"
		if errors.Is(err, ui.ErrScreenLeaseBusy) {
			stage, reason = "acquire", "busy"
			chatScreenCounters.waitTimeouts.Add(1)
		}
		chatScreenEmitError(spec, stage, err)
		chatScreenCounters.errorAcquire.Add(1)
		outcome = chatScreenDegradeInline(session, spec, reason)
		outcome.Err = err
		return outcome
	}

	chatScreenCounters.opens.Add(1)
	chatScreenEmitOpen(spec, lease, waitMs)

	closed := false
	closeStartedAt := time.Now()
	var closeErr error
	closeOnce := func() error {
		if closed {
			return closeErr
		}
		closed = true
		closeErr = chatScreenCloseLease(session, lease, spec)
		return closeErr
	}
	// panic / 错误展开也必须执行 close 序列（§4.2 defer 保证 / T9）。
	defer func() { _ = closeOnce() }()

	outcome = chatScreenRun(session, lease, spec)
	if runErr := closeOnce(); runErr != nil {
		// close（release/barrier）失败不覆盖用户可见结果，但必须记录并
		// 计入错误；选择结果本身已拿不到有效屏，按 error 收敛。
		chatScreenEmitError(spec, "close", runErr)
		chatScreenCounters.errorClose.Add(1)
		if outcome.Err == nil {
			outcome.Err = runErr
		}
	}

	heldMs := time.Since(startedAt).Milliseconds()
	repaintMs := time.Since(closeStartedAt).Milliseconds()
	chatScreenCounters.closes.Add(1)
	chatScreenCounters.addCloseResult(outcome.Result)
	chatScreenCounters.observeHold(heldMs)
	chatScreenCounters.observeRepaint(repaintMs)
	chatScreenEmitClose(spec, lease, outcome.Result, heldMs, repaintMs)
	return outcome
}

// chatScreenOpenAndApply 打开一次副屏并在 close 序列完成后应用结果：
// AfterClose（若有）只在 openChatScreen 返回后调用，保证会话变更/文案渲染
// 与副屏帧、主屏恢复互不重叠（I3）。派发方（dispatchChatScreenEffects 与
// 忙时只读副屏执行器）统一走这里，不再各自拼接生命周期。
func chatScreenOpenAndApply(session *ChatSession, spec chatScreenSpec) chatScreenOutcome {
	outcome := openChatScreen(session, spec)
	if spec.AfterClose != nil {
		spec.AfterClose(session, outcome)
	}
	return outcome
}

// chatScreenAcquireLease 只负责取租约 + open 屏障；close 由 chatScreenCloseLease
// 对称完成。等待预算内聚在这里（忙时/空闲共用，I7）。
func chatScreenAcquireLease(session *ChatSession, spec chatScreenSpec) (ui.ScreenLease, int64, error) {
	startedAt := time.Now()
	lease, err := session.Surface.AcquireAlternateScreenWait(context.Background(), ui.FullscreenRequest{
		Title: spec.screenTitle(),
	}, chatScreenLeaseWaitBudget)
	waitMs := time.Since(startedAt).Milliseconds()
	if err != nil {
		return nil, waitMs, err
	}
	// 显式 open 屏障（批次 0 给 B 族补齐的统一契约）：surface 已随 acquire
	// 提交 LeaseAcquired，这里再提交一次框架级 OpenScreenOverlay（picker 用
	// 自己的动作身份，见 spec.openAction），保证首帧之前 actor 已观察完在途
	// 状态。
	if !session.Interaction.postUIAction(spec.openAction(lease.ID())) {
		_ = lease.Release(context.Background())
		return nil, waitMs, errChatScreenStateUncommitted
	}
	if !session.Interaction.waitUIActorIdleBounded("open chat screen") {
		_ = lease.Release(context.Background())
		return nil, waitMs, errChatScreenRenderNotReady
	}
	return lease, waitMs, nil
}

// chatScreenCloseLease 是唯一的释放顺序实现（I2）：
// post Close → release → waitUIActorIdleBounded → 返回（此后才允许 mutate Scene）。
func chatScreenCloseLease(session *ChatSession, lease ui.ScreenLease, spec chatScreenSpec) error {
	if session == nil || session.Interaction == nil || lease == nil {
		return nil
	}
	_ = session.Interaction.postUIAction(spec.closeAction(lease.ID()))
	releaseErr := lease.Release(context.Background())
	if !session.Interaction.waitUIActorIdleBounded("close chat screen") {
		return errors.Join(errChatScreenActorNotIdle, releaseErr)
	}
	return releaseErr
}

// openAction/closeAction 取本 Spec 的 barrier 动作：默认框架级动作，picker
// 通过 OpenAction/CloseAction 保留各自动作身份（actor 状态机不变）。
func (s chatScreenSpec) openAction(leaseID uint64) ui.UIAction {
	if s.OpenAction != nil {
		return s.OpenAction(leaseID)
	}
	return ui.OpenScreenOverlay{LeaseID: leaseID, ScreenID: s.ID}
}

func (s chatScreenSpec) closeAction(leaseID uint64) ui.UIAction {
	if s.CloseAction != nil {
		return s.CloseAction(leaseID)
	}
	return ui.CloseScreenOverlay{LeaseID: leaseID, ScreenID: s.ID}
}

// chatScreenRun 在已持有的租约内按 Kind 推进渲染循环；选择/取消是正常结果，
// 渲染错误按 error 收敛（close 由 openChatScreen 统一保证）。
func chatScreenRun(session *ChatSession, lease ui.ScreenLease, spec chatScreenSpec) chatScreenOutcome {
	if spec.RunScreen != nil {
		// 自定义交互体：错误在 runner 内收敛为 outcome，框架不再二次映射，
		// close 序列仍由 openChatScreen 统一保证。
		return spec.RunScreen(session, lease, spec)
	}
	switch spec.Kind {
	case chatScreenDocument:
		var err error
		if spec.RunDocument != nil {
			err = spec.RunDocument(session, lease, spec)
		} else {
			err = chatScreenDocumentRunner(session, lease, ui.DebugOverlayOptions{
				Title:       spec.screenTitle(),
				Body:        ui.RenderDocumentPlain(spec.Doc),
				Refresh:     spec.Refresh,
				RefreshHint: spec.RefreshHint,
			})
		}
		if err != nil {
			chatScreenEmitError(spec, "render", err)
			chatScreenCounters.errorRender.Add(1)
			return chatScreenOutcome{Result: chatScreenClosedError, Index: -1, Err: err}
		}
		return chatScreenOutcome{Result: chatScreenClosedEsc, Index: -1}
	case chatScreenList:
		index, cancelled, err := chatScreenRunListStage(session, lease, spec.screenTitle(), spec.Subtitle, spec.ConfirmLabel, spec.Rows)
		if err != nil {
			chatScreenEmitError(spec, "render", err)
			chatScreenCounters.errorRender.Add(1)
			return chatScreenOutcome{Result: chatScreenClosedError, Index: -1, Err: err}
		}
		if cancelled {
			return chatScreenOutcome{Result: chatScreenClosedEsc, Index: -1}
		}
		return chatScreenOutcome{Result: chatScreenClosedConfirm, Index: index}
	default: // chatScreenStages
		index := -1
		stageID := ""
		for _, stage := range spec.Stages {
			if len(stage.Rows) == 0 {
				err := fmt.Errorf("%w: stage %q", errChatScreenEmptyStage, stage.ID)
				chatScreenEmitError(spec, "render", err)
				chatScreenCounters.errorRender.Add(1)
				return chatScreenOutcome{Result: chatScreenClosedError, Index: -1, StageID: stageID, Err: err}
			}
			picked, cancelled, err := chatScreenRunListStage(session, lease, stage.titleOrFallback(spec.Title), spec.Subtitle, spec.ConfirmLabel, stage.Rows)
			if err != nil {
				chatScreenEmitError(spec, "render", err)
				chatScreenCounters.errorRender.Add(1)
				return chatScreenOutcome{Result: chatScreenClosedError, Index: -1, StageID: stage.ID, Err: err}
			}
			if cancelled {
				return chatScreenOutcome{Result: chatScreenClosedEsc, Index: -1, StageID: stage.ID}
			}
			index = picked
			stageID = stage.ID
		}
		return chatScreenOutcome{Result: chatScreenClosedConfirm, Index: index, StageID: stageID}
	}
}

// chatScreenRunListStage 是列表阶段 runner：复用 ui 全屏列表原语（不新增渲染路径）。
func chatScreenRunListStage(session *ChatSession, lease ui.ScreenLease, title, subtitle, confirmLabel string, rows []chatScreenRow) (int, bool, error) {
	items := make([]ui.FullScreenListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, ui.FullScreenListItem{
			Title:      row.Title,
			Detail:     row.Detail,
			SearchText: row.SearchText,
		})
	}
	if len(items) == 0 {
		return -1, false, fmt.Errorf("没有可选项")
	}
	opts := ui.FullScreenListOptions{
		Title: title,
		Items: items,
	}
	if subtitle != "" {
		opts.Subtitle = subtitle
	}
	if confirmLabel != "" {
		opts.ConfirmLabel = confirmLabel
	}
	result, err := chatScreenListRunner(session, lease, opts)
	if err != nil {
		return -1, false, err
	}
	if result.Cancelled || result.Index < 0 || result.Index >= len(items) {
		return -1, true, nil
	}
	return result.Index, false, nil
}

// ---------------------------------------------------------------------------
// 降级（D-F / I6）
// ---------------------------------------------------------------------------

// chatScreenDegradeInline 关闭副屏路径并把 Spec 内容降级为主屏内联命令
// 单元格：绝不落 legacy stdout 直写，也绝不静默吞掉输出。
func chatScreenDegradeInline(session *ChatSession, spec chatScreenSpec, reason string) chatScreenOutcome {
	chatScreenCounters.addDegrade(reason)
	chatScreenEmitDegrade(spec, reason)
	doc := spec.DegradeDoc
	if chatScreenDocumentEmpty(doc) {
		doc = spec.fallbackDocument()
	}
	if session != nil && !spec.SilentDegrade && !chatScreenDocumentEmpty(doc) {
		hint := ""
		switch reason {
		case "busy", "nested":
			hint = "当前无法打开全屏视图，已内联显示"
		}
		if hint != "" {
			doc = chatScreenPrependPlainLine(doc, hint)
		}
		_ = renderChatCommandResult(session, CommandResult{
			Blocks: []RenderBlock{{Document: doc}},
			Action: CommandContinue,
		}, false)
	}
	return chatScreenOutcome{
		Result:        chatScreenClosedEsc,
		Index:         -1,
		Degraded:      true,
		DegradeReason: reason,
	}
}

// fallbackDocument 把任意 Kind 投影为可内联的文档（行文本）。
func (s chatScreenSpec) fallbackDocument() render.Document {
	switch s.Kind {
	case chatScreenDocument:
		return s.Doc
	case chatScreenList:
		return chatScreenRowsDocument(s.Rows)
	default:
		lines := make([]string, 0, len(s.Stages)*2)
		for _, stage := range s.Stages {
			lines = append(lines, strings.TrimSpace(stage.titleOrFallback(s.Title)))
			lines = append(lines, chatScreenRowLines(stage.Rows)...)
		}
		return textLinesDocument(lines)
	}
}

func chatScreenRowsDocument(rows []chatScreenRow) render.Document {
	lines := chatScreenRowLines(rows)
	if len(lines) == 0 {
		lines = []string{"没有可选项"}
	}
	return textLinesDocument(lines)
}

func chatScreenRowLines(rows []chatScreenRow) []string {
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		title := strings.TrimSpace(row.Title)
		if detail := strings.TrimSpace(row.Detail); detail != "" {
			title = strings.TrimSpace(title + " — " + detail)
		}
		if title != "" {
			lines = append(lines, title)
		}
	}
	return lines
}

func chatScreenDocumentEmpty(doc render.Document) bool {
	return strings.TrimSpace(ui.RenderDocumentPlain(doc)) == ""
}

// chatScreenInlineLineBudget 是 §5.1 的"短输出留主屏"预算：≤ 3 行视为
// 短确认/单行状态，不为一屏空态闪一次全屏。
const chatScreenInlineLineBudget = 3

// chatScreenDocumentLineCount 统计降级/投影后文档的有效行数。
func chatScreenDocumentLineCount(doc render.Document) int {
	body := strings.TrimSpace(ui.RenderDocumentPlain(doc))
	if body == "" {
		return 0
	}
	return len(strings.Split(body, "\n"))
}

// chatScreenShouldOpenForDocument 是命令侧准入判定（§5.1）：文档超过内联
// 预算且当前具备副屏能力时返回 true；短文档与能力不足都保持主屏内联，
// 由调用方直接返回 Blocks（不进框架，不记 degrade）。
func chatScreenShouldOpenForDocument(session *ChatSession, doc render.Document) bool {
	if chatScreenDocumentLineCount(doc) <= chatScreenInlineLineBudget {
		return false
	}
	return chatScreenCapability(session)
}

// chatScreenPrependPlainLine 在文档首行之前插入一行提示，保持其余内容不变。
func chatScreenPrependPlainLine(doc render.Document, line string) render.Document {
	body := ui.RenderDocumentPlain(doc)
	if strings.TrimSpace(body) == "" {
		return render.SingleLineDoc(render.TextSpan(line))
	}
	return textLinesDocument(append([]string{line}, strings.Split(body, "\n")...))
}

// ---------------------------------------------------------------------------
// Spec 校验（L0）
// ---------------------------------------------------------------------------

func (s chatScreenSpec) validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("%w: empty screen id", errChatScreenInvalidSpec)
	}
	if s.RunScreen != nil {
		// 自定义交互体自带内容校验（如窗口化列表可在首帧后加载行）。
		if s.Esc == chatScreenEscStepBack {
			return fmt.Errorf("%w: step-back esc is not enabled for %q (D-B)", errChatScreenInvalidSpec, s.ID)
		}
		return nil
	}
	switch s.Kind {
	case chatScreenDocument:
		if s.ForceInline {
			return nil
		}
		if chatScreenDocumentEmpty(s.Doc) {
			return fmt.Errorf("%w: document screen %q has empty content", errChatScreenInvalidSpec, s.ID)
		}
	case chatScreenList:
		if len(s.Rows) == 0 {
			return fmt.Errorf("%w: list screen %q has no rows", errChatScreenInvalidSpec, s.ID)
		}
	case chatScreenStages:
		if len(s.Stages) == 0 {
			return fmt.Errorf("%w: stage screen %q has no stages", errChatScreenInvalidSpec, s.ID)
		}
		for i, stage := range s.Stages {
			if len(stage.Rows) == 0 && !s.ForceInline {
				return fmt.Errorf("%w: stage screen %q stage %d has no rows", errChatScreenInvalidSpec, s.ID, i)
			}
		}
	default:
		return fmt.Errorf("%w: unknown screen kind %d", errChatScreenInvalidSpec, s.Kind)
	}
	if s.Esc == chatScreenEscStepBack {
		return fmt.Errorf("%w: step-back esc is not enabled for %q (D-B)", errChatScreenInvalidSpec, s.ID)
	}
	return nil
}

func (s chatScreenSpec) screenTitle() string {
	if title := strings.TrimSpace(s.Title); title != "" {
		return title
	}
	return strings.TrimSpace(s.ID)
}

func (s chatScreenSpec) triggerOrCommand() string {
	if trigger := strings.TrimSpace(s.Trigger); trigger != "" {
		return trigger
	}
	return "command"
}

func (s chatScreenStage) titleOrFallback(fallback string) string {
	if title := strings.TrimSpace(s.Title); title != "" {
		return title
	}
	if title := strings.TrimSpace(fallback); title != "" {
		return title
	}
	return "选择"
}

// ---------------------------------------------------------------------------
// 可注入渲染原语（B/A 族既有实现；测试注入替身）
// ---------------------------------------------------------------------------

var chatScreenDocumentRunner = func(session *ChatSession, lease ui.ScreenLease, options ui.DebugOverlayOptions) error {
	return ui.RunDebugOverlayWithLease(context.Background(), resumeFullScreenTerminal(session), options, lease)
}

var chatScreenListRunner = func(session *ChatSession, lease ui.ScreenLease, options ui.FullScreenListOptions) (ui.FullScreenListResult, error) {
	return ui.SelectFullScreenListWithLease(context.Background(), resumeFullScreenTerminal(session), options, lease)
}

// ---------------------------------------------------------------------------
// 计数器（§8.2，原子计数；只在 /debug display 呈现）
// ---------------------------------------------------------------------------

type chatScreenCounterSnapshot struct {
	Opens              uint64
	Closes             uint64
	CloseEsc           uint64
	CloseConfirm       uint64
	CloseError         uint64
	LeaseWaitTimeouts  uint64
	DegradeBusy        uint64
	DegradeUnavailable uint64
	DegradeNested      uint64
	DegradeUnknownEnv  uint64
	DegradeInvalid     uint64
	ErrorMessage       uint64
	ErrorAcquire       uint64
	ErrorBarrier       uint64
	ErrorRender        uint64
	ErrorClose         uint64
	HoldSamples        uint64
	HoldTotalMs        uint64
	HoldMaxMs          uint64
	RepaintSamples     uint64
	RepaintTotalMs     uint64
	RepaintMaxMs       uint64
}

type chatScreenFrameworkCounters struct {
	opens              atomic.Uint64
	closes             atomic.Uint64
	closeEsc           atomic.Uint64
	closeConfirm       atomic.Uint64
	closeError         atomic.Uint64
	waitTimeouts       atomic.Uint64
	degradeBusy        atomic.Uint64
	degradeUnavailable atomic.Uint64
	degradeNested      atomic.Uint64
	degradeUnknownEnv  atomic.Uint64
	degradeInvalid     atomic.Uint64
	errorMessage       atomic.Uint64
	errorAcquire       atomic.Uint64
	errorBarrier       atomic.Uint64
	errorRender        atomic.Uint64
	errorClose         atomic.Uint64
	holdSamples        atomic.Uint64
	holdTotalMs        atomic.Uint64
	holdMaxMs          atomic.Uint64
	repaintSamples     atomic.Uint64
	repaintTotalMs     atomic.Uint64
	repaintMaxMs       atomic.Uint64
}

var chatScreenCounters chatScreenFrameworkCounters

func (c *chatScreenFrameworkCounters) addCloseResult(result chatScreenCloseResult) {
	switch result {
	case chatScreenClosedConfirm:
		c.closeConfirm.Add(1)
	case chatScreenClosedError:
		c.closeError.Add(1)
	default:
		c.closeEsc.Add(1)
	}
}

func (c *chatScreenFrameworkCounters) addDegrade(reason string) {
	switch reason {
	case "busy":
		c.degradeBusy.Add(1)
	case "nested":
		c.degradeNested.Add(1)
	case "unknown_env":
		c.degradeUnknownEnv.Add(1)
	case "invalid_spec":
		c.degradeInvalid.Add(1)
	default:
		c.degradeUnavailable.Add(1)
	}
}

func (c *chatScreenFrameworkCounters) observeHold(ms int64) {
	if ms < 0 {
		ms = 0
	}
	c.holdSamples.Add(1)
	c.holdTotalMs.Add(uint64(ms))
	for {
		current := c.holdMaxMs.Load()
		if uint64(ms) <= current || c.holdMaxMs.CompareAndSwap(current, uint64(ms)) {
			break
		}
	}
}

func (c *chatScreenFrameworkCounters) observeRepaint(ms int64) {
	if ms < 0 {
		ms = 0
	}
	c.repaintSamples.Add(1)
	c.repaintTotalMs.Add(uint64(ms))
	for {
		current := c.repaintMaxMs.Load()
		if uint64(ms) <= current || c.repaintMaxMs.CompareAndSwap(current, uint64(ms)) {
			break
		}
	}
}

// chatScreenCounterSnapshotForDebug 返回当前计数器快照（/debug display 用）。
func chatScreenCounterSnapshotForDebug() chatScreenCounterSnapshot {
	return chatScreenCounterSnapshot{
		Opens:              chatScreenCounters.opens.Load(),
		Closes:             chatScreenCounters.closes.Load(),
		CloseEsc:           chatScreenCounters.closeEsc.Load(),
		CloseConfirm:       chatScreenCounters.closeConfirm.Load(),
		CloseError:         chatScreenCounters.closeError.Load(),
		LeaseWaitTimeouts:  chatScreenCounters.waitTimeouts.Load(),
		DegradeBusy:        chatScreenCounters.degradeBusy.Load(),
		DegradeUnavailable: chatScreenCounters.degradeUnavailable.Load(),
		DegradeNested:      chatScreenCounters.degradeNested.Load(),
		DegradeUnknownEnv:  chatScreenCounters.degradeUnknownEnv.Load(),
		DegradeInvalid:     chatScreenCounters.degradeInvalid.Load(),
		ErrorMessage:       chatScreenCounters.errorMessage.Load(),
		ErrorAcquire:       chatScreenCounters.errorAcquire.Load(),
		ErrorBarrier:       chatScreenCounters.errorBarrier.Load(),
		ErrorRender:        chatScreenCounters.errorRender.Load(),
		ErrorClose:         chatScreenCounters.errorClose.Load(),
		HoldSamples:        chatScreenCounters.holdSamples.Load(),
		HoldTotalMs:        chatScreenCounters.holdTotalMs.Load(),
		HoldMaxMs:          chatScreenCounters.holdMaxMs.Load(),
		RepaintSamples:     chatScreenCounters.repaintSamples.Load(),
		RepaintTotalMs:     chatScreenCounters.repaintTotalMs.Load(),
		RepaintMaxMs:       chatScreenCounters.repaintMaxMs.Load(),
	}
}

// resetChatScreenCountersForTest 清空计数器，保证各测试互不影响。
func resetChatScreenCountersForTest() {
	chatScreenCounters = chatScreenFrameworkCounters{}
}

// appendChatDebugScreenFrameworkLines 把副屏框架计数器写进 /debug display
// （§8.2：计数只在此呈现，不含正文/凭据）。
func appendChatDebugScreenFrameworkLines(builder *chatDebugDocumentBuilder, session *ChatSession) {
	if builder == nil {
		return
	}
	mode, raw, unknown := chatScreenFrameworkModeResolve()
	builder.heading("Chat Screen Framework (batch 0): (GET /debug/chat/status#screen_framework)")
	modeLine := mode.String()
	switch {
	case unknown:
		modeLine = fmt.Sprintf("unified (unknown_env=%q)", raw)
	case raw == "":
		modeLine += " (default)"
	}
	builder.meta("Framework Mode:", modeLine)
	builder.meta("Active Screens:", fmt.Sprintf("%d", chatScreenOpenActiveForTest()))
	snapshot := chatScreenCounterSnapshotForDebug()
	builder.meta("Lifecycle:", fmt.Sprintf(
		"opens=%d closes=%d esc=%d confirm=%d error=%d",
		snapshot.Opens, snapshot.Closes, snapshot.CloseEsc, snapshot.CloseConfirm, snapshot.CloseError))
	builder.meta("Degrades:", fmt.Sprintf(
		"busy=%d unavailable=%d nested=%d unknown_env=%d invalid=%d",
		snapshot.DegradeBusy, snapshot.DegradeUnavailable, snapshot.DegradeNested,
		snapshot.DegradeUnknownEnv, snapshot.DegradeInvalid))
	builder.meta("Errors:", fmt.Sprintf(
		"message=%d acquire=%d barrier=%d render=%d close=%d",
		snapshot.ErrorMessage, snapshot.ErrorAcquire, snapshot.ErrorBarrier,
		snapshot.ErrorRender, snapshot.ErrorClose))
	builder.meta("Lease Wait Timeouts:", fmt.Sprintf("%d", snapshot.LeaseWaitTimeouts))
	builder.meta("Hold (ms):", fmt.Sprintf(
		"samples=%d total=%d max=%d", snapshot.HoldSamples, snapshot.HoldTotalMs, snapshot.HoldMaxMs))
	builder.meta("Repaint (ms):", fmt.Sprintf(
		"samples=%d total=%d max=%d", snapshot.RepaintSamples, snapshot.RepaintTotalMs, snapshot.RepaintMaxMs))
}

// ---------------------------------------------------------------------------
// 事件（§8.1：只含 ID/枚举/耗时，无正文与凭据）
// ---------------------------------------------------------------------------

func chatScreenEmitOpen(spec chatScreenSpec, lease ui.ScreenLease, waitMs int64) {
	leaseID := uint64(0)
	if lease != nil {
		leaseID = lease.ID()
	}
	logpkg.Debugf("%s screen_id=%s kind=%s lease_id=%d wait_ms=%d trigger=%s",
		chatEventScreenOpen, spec.ID, spec.Kind, leaseID, waitMs, spec.triggerOrCommand())
}

func chatScreenEmitClose(spec chatScreenSpec, lease ui.ScreenLease, result chatScreenCloseResult, heldMs, repaintMs int64) {
	leaseID := uint64(0)
	if lease != nil {
		leaseID = lease.ID()
	}
	logpkg.Debugf("%s screen_id=%s lease_id=%d result=%s held_ms=%d repaint_ms=%d",
		chatEventScreenClose, spec.ID, leaseID, result, heldMs, repaintMs)
}

func chatScreenEmitDegrade(spec chatScreenSpec, reason string) {
	logpkg.Debugf("%s screen_id=%s reason=%s trigger=%s",
		chatEventScreenDegrade, spec.ID, reason, spec.triggerOrCommand())
}

func chatScreenEmitError(spec chatScreenSpec, stage string, err error) {
	logpkg.Debugf("%s screen_id=%s stage=%s err=%v", chatEventScreenError, spec.ID, stage, err)
}

func chatScreenEmitViolation(invariant, screenID, action string) {
	logpkg.Debugf("%s invariant=%s screen_id=%s action=%s",
		chatEventScreenViolation, invariant, screenID, action)
}
