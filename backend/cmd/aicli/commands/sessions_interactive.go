package commands

// sessions_interactive.go 把 `aicli sessions` 从「打印一行一条」升级为可操作的
// 会话浏览器：搜索、翻页、进入某会话的动作菜单，其中最重要的是
// 「在新终端里打开这个会话」（分离启动，独立窗口）与「在当前终端恢复」。
//
// 为什么用两段式（列表 → 动作菜单）而不是给全屏列表加一堆单键动作：
// 全屏列表（ui/fullscreen_list.go）是 /model、/login、/resume、/export 共用的
// 组件，它把「非导航按键」一律当作搜索输入。想加自定义快捷键就得改这个公共输入
// 回路，代价是所有选择器都要重新回归。两段式零改动公共层，交互成本也只是多一次
// Enter；同时保留 Delete 键（x）作为「删除会话」的直达快捷键——该键位本来就是
// 列表契约的一部分。
//
// 可测试性：真正的交互循环 runSessionsBrowser 只依赖 sessionsBrowserActions
// （动作表）与 sessionsBrowserListRunner（一屏渲染）。两者都可注入，单测不需要
// TTY；ui.RunStartupFullScreenList 只在生产装配点出现一次。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/termopen"
)

// sessionsBrowserPageSize 是一次向存储要多少条会话。与 --limit 解耦：交互界面靠
// 搜索与滚动访问全量，不该被文本列表的展示上限截断（同 /resume 分页选择器）。
const sessionsBrowserPageSize = 200

// sessionsBrowserSearchPageSize 是搜索态下的展示上限（避免整库 materialize）。
const sessionsBrowserActionBack = "__back__"

// sessionsBrowserAction 是动作菜单里的一项。
type sessionsBrowserAction string

const (
	sessionsActionOpenTerminal sessionsBrowserAction = "open_terminal"
	sessionsActionResumeHere   sessionsBrowserAction = "resume_here"
	sessionsActionDetail       sessionsBrowserAction = "detail"
	sessionsActionRename       sessionsBrowserAction = "rename"
	sessionsActionArchive      sessionsBrowserAction = "archive"
	sessionsActionActivate     sessionsBrowserAction = "activate"
	sessionsActionDelete       sessionsBrowserAction = "delete"
	sessionsActionBack         sessionsBrowserAction = "back"
)

// sessionsBrowserOutcome 是一次交互浏览的结束状态。
type sessionsBrowserOutcome struct {
	// ExitCode 复用 sessions 命令的退出码约定（0/1/2）。
	ExitCode int
	// Message 是给用户看的收尾一句话（错误或被忽略的动作结果）。
	Message string
}

// sessionsBrowserActions 是交互界面能做的全部事情。字段为 nil 表示该动作不可用，
// 菜单里会相应隐藏（而不是点了报错）——终端后端缺失、无会话管理器等都是这种场景。
type sessionsBrowserActions struct {
	// List 返回当前筛选与搜索条件下的会话（按更新优先）。
	List func(query string) ([]sessionsCommandItem, error)
	// OpenTerminal 在新终端里分离启动该会话，返回一句结果说明。
	OpenTerminal func(item sessionsCommandItem) (string, error)
	// ResumeHere 在当前终端里恢复该会话（前台子进程），返回子进程退出说明。
	ResumeHere func(item sessionsCommandItem) (string, error)
	// Detail 读取单个会话的详情（含启动参数）。
	Detail func(sessionID string) (sessionsSessionDetail, error)
	// Rename / Delete / Archive / Activate 是写动作。
	Rename   func(sessionID, title string) error
	Delete   func(sessionID string) error
	Archive  func(sessionID string) error
	Activate func(sessionID string) error
	// TerminalSummary 描述当前会用哪个终端后端，用于菜单标注与提示。
	TerminalSummary string
	// CanResumeHere 报告当前终端能否承载前台恢复（TTY + 非嵌套）。
	CanResumeHere bool
}

// sessionsSessionDetail 是「查看详情」一屏的内容。
type sessionsSessionDetail struct {
	SessionID    string
	Title        string
	State        string
	Workspace    string
	Provider     string
	Model        string
	Protocol     string
	MessageCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Summary      string
	// 启动参数（来自会话 metadata；老会话可能整段为空）。
	LaunchEntry    string
	LaunchArgs     []string
	LaunchArgv     []string
	LaunchCwd      string
	LaunchBinary   string
	LaunchCommand  string
	LaunchRecorded string
	LaunchHistory  []string
	// Mesh 是本会话已知的窗口/节点说明（gateway/mesh 接线点，可选）。
	Mesh string
}

// lines 把详情渲染成全屏列表的一批只读行（最后一行是返回）。
func (d sessionsSessionDetail) lines() []string {
	rows := []string{
		"会话 ID   " + blankToDash(d.SessionID),
		"标题      " + blankToDash(d.Title),
		"状态      " + blankToDash(d.State),
		"工作目录  " + blankToDash(d.Workspace),
		"provider  " + blankToDash(d.Provider),
		"模型      " + blankToDash(d.Model),
		"协议      " + blankToDash(d.Protocol),
		fmt.Sprintf("消息数    %d", d.MessageCount),
		"创建      " + d.CreatedAt.Local().Format(time.RFC3339),
		"更新      " + d.UpdatedAt.Local().Format(time.RFC3339),
	}
	if summary := strings.TrimSpace(d.Summary); summary != "" {
		rows = append(rows, "摘要      "+fitDisplayText(summary, sessionsCommandBriefMaxWidth))
	}
	if mesh := strings.TrimSpace(d.Mesh); mesh != "" {
		rows = append(rows, "窗口      "+mesh)
	}
	rows = append(rows,
		"— 启动参数 —",
		"入口      "+blankToDash(d.LaunchEntry),
		"重放旗标  "+joinLaunchTokens(d.LaunchArgs),
		"原始参数  "+joinLaunchTokens(d.LaunchArgv),
		"启动目录  "+blankToDash(d.LaunchCwd),
		"可执行    "+blankToDash(d.LaunchBinary),
		"记录时间  "+blankToDash(d.LaunchRecorded),
		"启动命令  "+blankToDash(d.LaunchCommand),
	)
	for _, entry := range d.LaunchHistory {
		rows = append(rows, "历次启动  "+fitDisplayText(entry, sessionsCommandBriefMaxWidth*2))
	}
	return rows
}

func joinLaunchTokens(tokens []string) string {
	if len(tokens) == 0 {
		return "(未记录)"
	}
	return fitDisplayText(strings.Join(tokens, " "), sessionsCommandBriefMaxWidth*2)
}

// runSessionsBrowser 是纯逻辑的交互主循环：渲染一屏 → 解析动作 → 执行 → 重开。
// runner 负责一次全屏渲染（生产环境是 ui.RunStartupFullScreenList 的闭包）。
func runSessionsBrowser(actions sessionsBrowserActions, runner sessionsBrowserListRunner) sessionsBrowserOutcome {
	state := sessionsBrowserState{}
	for {
		// 搜索由全屏列表自己做（客户端模糊匹配 SearchText，含会话 ID/标题/目录/
		// provider/model/启动参数），这里只按范围与 --state/--provider/--model
		// 拉取可浏览的一批。
		items, err := actions.List("")
		if err != nil {
			return sessionsBrowserOutcome{ExitCode: sessionsExitFailure, Message: err.Error()}
		}
		rows, index := buildSessionsBrowserItems(items, state.selectedID)
		result, runErr := runner(sessionsBrowserListSpec{
			Title:      "会话列表",
			Subtitle:   state.subtitle(actions, len(items)),
			Items:      rows,
			InitialRow: sessionsBrowserRowForID(items, state.selectedID),
		})
		if runErr != nil {
			return sessionsBrowserOutcome{ExitCode: sessionsExitFailure, Message: runErr.Error()}
		}
		if result.Cancelled {
			return sessionsBrowserOutcome{ExitCode: sessionsExitOK, Message: state.message}
		}
		if result.DeleteRequested {
			// x 键直达删除确认：与 Enter 走菜单等价，只是少一步。
			if item, ok := index.at(result.Index); ok {
				state.apply(runSessionsBrowserDeleteFlow(actions, runner, item))
				continue
			}
		}
		item, ok := index.at(result.Index)
		if !ok {
			continue
		}
		state.selectedID = item.SessionID
		outcome, handled := runSessionsBrowserActionFlow(actions, runner, state, item)
		if handled != nil {
			return *handled
		}
		state.apply(outcome)
	}
}

// sessionsBrowserState 是跨屏保留的浏览状态（搜索词、上次结果提示、选中行）。
type sessionsBrowserState struct {
	query string
	// selectedID 是上一次动作落在哪个会话上（重新开屏后用它标记该行）。
	selectedID string
	lastAction sessionsBrowserAction
	lastItem   sessionsCommandItem
	lastResult string
	lastError  string
	message    string
}

func (s *sessionsBrowserState) apply(outcome sessionsBrowserActionResult) {
	if outcome.Message != "" {
		s.lastResult = outcome.Message
		s.message = outcome.Message
	}
	if outcome.Err != nil {
		s.lastError = outcome.Err.Error()
		s.message = outcome.Err.Error()
	}
	s.lastAction = outcome.Action
	// 删除后该会话已不存在：清掉选中项，否则重开列表时光标既找不到它（会静默
	// 回到首行），▸ 标记又会在后续另一个同名会话上误现。
	if outcome.Action == sessionsActionDelete && outcome.Err == nil {
		s.selectedID = ""
	}
}

func (s *sessionsBrowserState) subtitle(actions sessionsBrowserActions, count int) string {
	base := "输入即搜索 · ↑/↓ 选择 · Enter 动作 · x 删除 · Esc 退出"
	if actions.OpenTerminal == nil {
		base += " · 无可用终端后端"
	} else if summary := strings.TrimSpace(actions.TerminalSummary); summary != "" {
		base += " · 终端后端 " + summary
	}
	if count == 0 {
		return base + " · 没有匹配的会话"
	}
	return fmt.Sprintf("%s · 共 %d 条", base, count)
}

// sessionsBrowserActionResult 是一次动作的结果。
type sessionsBrowserActionResult struct {
	Action  sessionsBrowserAction
	Message string
	Err     error
	// Fatal 非 nil 时整个浏览结束（例如在当前终端恢复后用户退出、或退出码已定）。
	Fatal *sessionsBrowserOutcome
}

// sessionsBrowserListSpec 是一次全屏渲染的请求（与 ui 层解耦的最小描述）。
type sessionsBrowserListSpec struct {
	Title    string
	Subtitle string
	Items    []uiFullScreenListItemLike
	// InitialRow 是重新开屏后光标应停在的行下标（Items 下标）。会话列表在每次
	// 动作后都会重开，不带这个字段时光标永远回到第一行。
	InitialRow int
	// FreeText 非 nil 表示这一屏是单值输入（重命名）。
	FreeText *sessionsBrowserFreeText
	// ConfirmLabel 覆盖默认 Enter 文案。
	ConfirmLabel string
}

// sessionsBrowserFreeText 描述单值输入屏。
type sessionsBrowserFreeText struct {
	Hint     string
	Value    string
	Validate func(text string) error
}

// uiFullScreenListItemLike 是渲染层无关的行描述（字段与 ui.FullScreenListItem 对齐）。
type uiFullScreenListItemLike struct {
	Leading    string
	Title      string
	Detail     string
	SearchText string
	Disabled   bool
	NoIndex    bool
	Preview    string
}

// sessionsBrowserListRunner 渲染一屏并返回结果。
type sessionsBrowserListRunner func(sessionsBrowserListSpec) (sessionsBrowserListPick, error)

// sessionsBrowserListPick 是一次渲染结果。
type sessionsBrowserListPick struct {
	Index           int
	Cancelled       bool
	DeleteRequested bool
	Text            string
}

// sessionsBrowserRowIndex 把渲染行号映射回会话（动作行的行号不参与映射）。
type sessionsBrowserRowIndex struct {
	items []sessionsCommandItem
	// rowOffset 是第一个会话行的行号（前面可能有提示行）。
	rowOffset int
}

func (index sessionsBrowserRowIndex) at(row int) (sessionsCommandItem, bool) {
	shifted := row - index.rowOffset
	if shifted < 0 || shifted >= len(index.items) {
		return sessionsCommandItem{}, false
	}
	return index.items[shifted], true
}

// buildSessionsBrowserItems 把会话投影成全屏行。
//
// 一行的可读顺序固定为「时间 → 会话 ID → 描述」，但这个顺序是被渲染器的列约束
// 逼出来的，不是随意选的：
//   - Leading 列上限 24 格（ui.fullScreenListLeadingMaxWidth），时间串
//     "2026-10-28 09:30 (1天前)" 已经占满，塞不进 ID；
//   - Detail 列只有 min(32, 宽/3) 且排在行尾——把 ID 放那儿会被直接截掉，
//     用户看到的就是一行没有 ID 的列表（这正是之前的问题）；
//   - Title 是唯一的弹性宽列，所以 ID 落在 Title 首位、描述紧随其后。aicli 会话 ID
//     定长（session_<14 位时间>_<8 位随机>），因此各行描述仍天然对齐。
//
// Detail 里不再重复 ID，只带「目录 · provider/model · 条数 · 状态 · 启动参数」；
// ID、标题、摘要、目录、启动参数全部进 SearchText，搜索能力不受列宽影响。
func buildSessionsBrowserItems(items []sessionsCommandItem, selectedID string) ([]uiFullScreenListItemLike, sessionsBrowserRowIndex) {
	now := time.Now()
	rows := make([]uiFullScreenListItemLike, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "(untitled)"
		}
		brief := fitDisplayText(title, sessionsCommandBriefMaxWidth)
		rowTitle := strings.TrimSpace(item.SessionID) + " · " + brief
		details := make([]string, 0, 5)
		if workspace := strings.TrimSpace(item.Workspace); workspace != "" {
			details = append(details, workspace)
		}
		if provider := strings.TrimSpace(item.Provider); provider != "" {
			model := strings.TrimSpace(item.Model)
			if model != "" {
				provider += "/" + model
			}
			details = append(details, provider)
		}
		details = append(details, fmt.Sprintf("%d 条", item.MessageCount))
		if state := strings.TrimSpace(item.State); state != "" {
			details = append(details, state)
		}
		if len(item.LaunchArgs) > 0 {
			details = append(details, "启动 "+strings.Join(item.LaunchArgs, " "))
		}
		search := strings.Join([]string{
			item.SessionID, item.Title, item.Summary, item.Provider, item.Model,
			item.Workspace, item.State, strings.Join(item.LaunchArgs, " "), strings.Join(item.LaunchArgv, " "),
		}, " ")
		rows = append(rows, uiFullScreenListItemLike{
			Leading:    sessionsBrowserLeading(formatSessionUpdatedAt(item.UpdatedAt, now), item.SessionID, selectedID),
			Title:      rowTitle,
			Detail:     fitDisplayText(strings.Join(details, " · "), sessionsCommandBriefMaxWidth*2),
			SearchText: search,
			Preview:    strings.TrimSpace(item.Summary),
		})
	}
	return rows, sessionsBrowserRowIndex{items: items, rowOffset: 0}
}

// sessionsBrowserLeading 给「上一次刚操作过的会话」加一个可见标记：重新打开列表
// 后（搜索态会重置），用户需要一眼看到刚才动的是哪一行。
func sessionsBrowserLeading(leading, sessionID, selectedID string) string {
	if strings.TrimSpace(selectedID) == "" ||
		!strings.EqualFold(strings.TrimSpace(sessionID), strings.TrimSpace(selectedID)) {
		return leading
	}
	return "▸ " + leading
}

// sessionsBrowserRowForID 把「上一次动作落在哪个会话」换算成行下标。
//
// rows 与 items 是 1:1 同序的（buildSessionsBrowserItems 不增不减行），所以直接用
// items 下标。没选中过、或该会话已不在当前结果里（被筛选掉/已删）时返回 0，
// 即保持默认首行。
func sessionsBrowserRowForID(items []sessionsCommandItem, sessionID string) int {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0
	}
	for index, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.SessionID), sessionID) {
			return index
		}
	}
	return 0
}

// buildSessionsBrowserActionItems 渲染某个会话的动作菜单。不可用的动作直接不出现，
// 而不是让用户选中后报错。
func buildSessionsBrowserActionItems(item sessionsCommandItem, actions sessionsBrowserActions) ([]uiFullScreenListItemLike, []sessionsBrowserAction) {
	rows := make([]uiFullScreenListItemLike, 0, 8)
	ids := make([]sessionsBrowserAction, 0, 8)
	add := func(id sessionsBrowserAction, title, detail string) {
		ids = append(ids, id)
		rows = append(rows, uiFullScreenListItemLike{
			Title:  title,
			Detail: detail,
			// 动作行不参与目录编号（同 /model 的「刷新模型列表」约定）。
			NoIndex: true,
		})
	}
	if actions.OpenTerminal != nil {
		command := item.LaunchCommand
		if command == "" {
			command = formatLaunchCommand("", item.SessionID, item.LaunchArgs)
		}
		add(sessionsActionOpenTerminal, "在新终端里打开（分离窗口）", command)
	}
	if actions.ResumeHere != nil && actions.CanResumeHere {
		add(sessionsActionResumeHere, "在当前终端里恢复（退出后回到本列表）", "")
	}
	if actions.Detail != nil {
		add(sessionsActionDetail, "查看详情（含启动参数）", "")
	}
	if actions.Rename != nil {
		add(sessionsActionRename, "重命名会话", blankToDash(item.Title))
	}
	if actions.Archive != nil && !strings.EqualFold(item.State, string(runtimechat.StateArchived)) {
		add(sessionsActionArchive, "归档会话", "")
	}
	if actions.Activate != nil && strings.EqualFold(item.State, string(runtimechat.StateArchived)) {
		add(sessionsActionActivate, "取消归档（重新激活）", "")
	}
	if actions.Delete != nil {
		add(sessionsActionDelete, "删除会话", "")
	}
	add(sessionsActionBack, "返回会话列表", "")
	return rows, ids
}

// runSessionsBrowserActionFlow 执行一次动作菜单；返回 (结果, 结束信号)。
func runSessionsBrowserActionFlow(actions sessionsBrowserActions, runner sessionsBrowserListRunner, state sessionsBrowserState, item sessionsCommandItem) (sessionsBrowserActionResult, *sessionsBrowserOutcome) {
	rows, ids := buildSessionsBrowserActionItems(item, actions)
	pick, err := runner(sessionsBrowserListSpec{
		Title:        "会话动作 · " + blankToDash(item.Title),
		Subtitle:     item.SessionID + " · " + state.message,
		Items:        rows,
		ConfirmLabel: "执行选中动作",
	})
	if err != nil {
		return sessionsBrowserActionResult{Err: err}, nil
	}
	if pick.Cancelled {
		return sessionsBrowserActionResult{}, nil
	}
	if pick.Index < 0 || pick.Index >= len(ids) {
		return sessionsBrowserActionResult{}, nil
	}
	return dispatchSessionsBrowserAction(actions, runner, item, ids[pick.Index])
}

// dispatchSessionsBrowserAction 是动作菜单到具体动作的唯一映射（无 IO，可单测）。
func dispatchSessionsBrowserAction(actions sessionsBrowserActions, runner sessionsBrowserListRunner, item sessionsCommandItem, action sessionsBrowserAction) (sessionsBrowserActionResult, *sessionsBrowserOutcome) {
	switch action {
	case sessionsActionOpenTerminal:
		if actions.OpenTerminal == nil {
			return sessionsBrowserActionResult{Action: action, Err: fmt.Errorf("当前环境没有可用的终端程序（设置 %s=<后端名或终端程序路径>）", termopen.EnvTerminal)}, nil
		}
		message, err := actions.OpenTerminal(item)
		return sessionsBrowserActionResult{Action: action, Message: message, Err: err}, nil
	case sessionsActionResumeHere:
		if actions.ResumeHere == nil {
			return sessionsBrowserActionResult{Action: action, Err: fmt.Errorf("当前终端不支持原地恢复")}, nil
		}
		message, err := actions.ResumeHere(item)
		outcome := sessionsBrowserOutcome{ExitCode: sessionsExitOK, Message: message}
		if err != nil {
			outcome.ExitCode = sessionsExitFailure
			outcome.Message = err.Error()
		}
		// 原地恢复是「离开列表」的动作：子进程结束后浏览结束，避免用户
		// 在一个已经翻篇的会话上继续点操作。
		return sessionsBrowserActionResult{Action: action, Message: message, Err: err}, &outcome
	case sessionsActionDetail:
		if actions.Detail == nil {
			return sessionsBrowserActionResult{Action: action, Err: fmt.Errorf("会话详情不可用")}, nil
		}
		detail, err := actions.Detail(item.SessionID)
		if err != nil {
			return sessionsBrowserActionResult{Action: action, Err: err}, nil
		}
		rows := make([]uiFullScreenListItemLike, 0, len(detail.lines())+1)
		for _, line := range detail.lines() {
			rows = append(rows, uiFullScreenListItemLike{Title: line, NoIndex: true})
		}
		rows = append(rows, uiFullScreenListItemLike{Title: "返回", NoIndex: true})
		if _, runErr := runner(sessionsBrowserListSpec{
			Title:        "会话详情 · " + blankToDash(item.Title),
			Subtitle:     "只读信息 · Enter/Esc 返回",
			Items:        rows,
			ConfirmLabel: "返回",
		}); runErr != nil {
			return sessionsBrowserActionResult{Action: action, Err: runErr}, nil
		}
		return sessionsBrowserActionResult{Action: action}, nil
	case sessionsActionRename:
		if actions.Rename == nil {
			return sessionsBrowserActionResult{Action: action, Err: fmt.Errorf("重命名不可用")}, nil
		}
		title, cancelled, err := runSessionsBrowserTextPrompt(runner, "重命名会话 · "+item.SessionID,
			"输入新标题（Enter 确认 · Esc 取消）", item.Title, func(text string) error {
				if strings.TrimSpace(text) == "" {
					return fmt.Errorf("标题不能为空")
				}
				return nil
			})
		if cancelled || err != nil {
			return sessionsBrowserActionResult{Action: action, Err: err}, nil
		}
		if err := actions.Rename(item.SessionID, title); err != nil {
			return sessionsBrowserActionResult{Action: action, Err: err}, nil
		}
		return sessionsBrowserActionResult{Action: action, Message: "已重命名为 " + title}, nil
	case sessionsActionArchive:
		if actions.Archive == nil {
			return sessionsBrowserActionResult{Action: action, Err: fmt.Errorf("归档不可用")}, nil
		}
		if err := actions.Archive(item.SessionID); err != nil {
			return sessionsBrowserActionResult{Action: action, Err: err}, nil
		}
		return sessionsBrowserActionResult{Action: action, Message: "已归档 " + item.SessionID}, nil
	case sessionsActionActivate:
		if actions.Activate == nil {
			return sessionsBrowserActionResult{Action: action, Err: fmt.Errorf("激活不可用")}, nil
		}
		if err := actions.Activate(item.SessionID); err != nil {
			return sessionsBrowserActionResult{Action: action, Err: err}, nil
		}
		return sessionsBrowserActionResult{Action: action, Message: "已取消归档 " + item.SessionID}, nil
	case sessionsActionDelete:
		return runSessionsBrowserDeleteFlow(actions, runner, item), nil
	default:
		return sessionsBrowserActionResult{Action: sessionsActionBack}, nil
	}
}

// runSessionsBrowserDeleteFlow 是删除的两段确认（列表行 → 确认屏）。
func runSessionsBrowserDeleteFlow(actions sessionsBrowserActions, runner sessionsBrowserListRunner, item sessionsCommandItem) sessionsBrowserActionResult {
	if actions.Delete == nil {
		return sessionsBrowserActionResult{Action: sessionsActionDelete, Err: fmt.Errorf("删除不可用")}
	}
	brief := blankToDash(item.Title)
	rows := []uiFullScreenListItemLike{
		{Title: "确认删除该会话（不可恢复）", Detail: item.SessionID + " · " + brief, NoIndex: true},
		{Title: "取消", NoIndex: true},
	}
	pick, err := runner(sessionsBrowserListSpec{
		Title:        "删除会话确认",
		Subtitle:     "Enter 确认选择 · Esc 取消",
		Items:        rows,
		ConfirmLabel: "继续",
	})
	if err != nil {
		return sessionsBrowserActionResult{Action: sessionsActionDelete, Err: err}
	}
	if pick.Cancelled || pick.Index != 0 {
		return sessionsBrowserActionResult{Action: sessionsActionDelete, Message: "已取消删除"}
	}
	if err := actions.Delete(item.SessionID); err != nil {
		return sessionsBrowserActionResult{Action: sessionsActionDelete, Err: err}
	}
	return sessionsBrowserActionResult{Action: sessionsActionDelete, Message: "已删除 " + item.SessionID}
}

// runSessionsBrowserTextPrompt 渲染一个单值输入屏（重命名/标题编辑）。
func runSessionsBrowserTextPrompt(runner sessionsBrowserListRunner, title, hint, initial string, validate func(string) error) (string, bool, error) {
	pick, err := runner(sessionsBrowserListSpec{
		Title:    title,
		Subtitle: hint,
		FreeText: &sessionsBrowserFreeText{Hint: hint, Value: initial, Validate: validate},
	})
	if err != nil {
		return "", false, err
	}
	if pick.Cancelled {
		return "", true, nil
	}
	text := strings.TrimSpace(pick.Text)
	if text == "" {
		return "", true, nil
	}
	if validate != nil {
		if err := validate(text); err != nil {
			return "", false, err
		}
	}
	return text, false, nil
}

// openSessionInNewTerminal 用平台终端在新窗口里拉起会话（默认入口 resume）。
func openSessionInNewTerminal(item sessionsCommandItem, launcher *termopen.Launcher) (string, error) {
	if launcher == nil {
		launcher = termopen.Default()
	}
	binary := strings.TrimSpace(item.LaunchBinary)
	if binary == "" {
		self, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("无法确定 aicli 可执行文件路径: %w", err)
		}
		binary = self
	}
	binary = termopen.AbsolutePathForTerminal(binary)
	args := sessionRelaunchArgs(item.SessionID, item.LaunchArgs)
	if len(args) == 0 {
		return "", fmt.Errorf("会话 ID 为空，无法在新终端打开")
	}
	dir := firstNonEmptyChatValue(item.LaunchCwd, item.Workspace)
	title := "aicli · " + firstNonEmptyChatValue(item.Title, item.SessionID)
	result, err := launcher.Launch(termopen.Request{
		Title:  title,
		Dir:    dir,
		Binary: binary,
		Args:   args,
		Env:    []string{envSessionDetachedLauncher + "=sessions-ui"},
	})
	if err != nil {
		return "", err
	}
	note := fmt.Sprintf("已在新终端打开会话 %s（后端 %s，pid %d）", item.SessionID, result.Plan.Backend, result.PID)
	if mode := strings.TrimSpace(result.Mode); mode != "" {
		note += " · " + mode
	}
	return note, nil
}

// envSessionDetachedLauncher 标记「这个会话窗口是由分离启动拉起的」，供子进程与
// gateway/mesh 审计识别来源（与 mesh 的 AICLI_MESH_SPAWNED_BY 同一思路）。
const envSessionDetachedLauncher = "AICLI_SESSION_LAUNCHED_BY"

// resumeSessionInCurrentTerminal 在当前终端以前台子进程恢复会话：完整的 argv
// 语义（含 root 持久旗标）都由子进程自己解析，比在本进程里重建 cobra 命令更可靠。
func resumeSessionInCurrentTerminal(item sessionsCommandItem) (string, error) {
	binary := strings.TrimSpace(item.LaunchBinary)
	if binary == "" {
		if self, err := os.Executable(); err == nil {
			binary = self
		}
	}
	if binary == "" {
		binary = "aicli"
	}
	args := sessionRelaunchArgs(item.SessionID, item.LaunchArgs)
	command := exec.Command(termopen.AbsolutePathForTerminal(binary), args...)
	command.Dir = firstNonEmptyChatValue(item.LaunchCwd, item.Workspace)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if asExitError(err, &exitErr) {
			return fmt.Sprintf("会话 %s 的恢复进程退出码 %d", item.SessionID, exitErr.ExitCode()), nil
		}
		return "", fmt.Errorf("在当前终端恢复会话失败: %w", err)
	}
	return fmt.Sprintf("会话 %s 已结束", item.SessionID), nil
}

func asExitError(err error, target **exec.ExitError) bool {
	typed, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	*target = typed
	return true
}

// sessionsTerminalLauncher 按 --terminal 覆盖装配终端后端（不改动进程环境变量，
// 以免影响本进程其它判定）。
func sessionsTerminalLauncher(override string) *termopen.Launcher {
	launcher := termopen.Default()
	if strings.TrimSpace(override) == "" {
		return launcher
	}
	base := launcher.Getenv
	launcher.Getenv = func(key string) string {
		if key == termopen.EnvTerminal {
			return override
		}
		if base == nil {
			return ""
		}
		return base(key)
	}
	return launcher
}

// ============================================================================
// 生产装配：会话存储、动作表、全屏渲染器与命令入口。
// 纯逻辑的 runSessionsBrowser 只认 actions/runner，这一段负责把真东西接上。
// ============================================================================

// sessionsBrowserStore 是一次交互浏览期间的会话库句柄（打开一次、用到底、退出时
// 关闭）。文本/JSON 路径每次调用各自开合（collectSessionsCommandResult），但交互
// 界面会连续做很多次列表与写动作，反复开关管理器既慢又会打断 SQLite 的读快照。
type sessionsBrowserStore struct {
	manager    *runtimechat.SessionManager
	userID     string
	sessionDir string
	// workspace 非空表示只看该工作目录（与 chat --resume 同口径）。
	workspace string
	// filter 是命令上带来的固定筛选（--state/--provider/--model）；搜索词另加。
	filter ChatSessionListFilter
	limit  int
}

// openSessionsBrowserStore 解析范围并打开会话库。参数错误与存储错误都返回 error，
// 由调用方映射成退出码 2（确定性错误）。
func openSessionsBrowserStore(cfg *config.Config, req sessionsCommandRequest) (*sessionsBrowserStore, error) {
	workspace := ""
	if !req.All {
		resolved, err := sessionsCurrentWorkspace(cfg)
		if err != nil {
			return nil, err
		}
		workspace = resolved
	}
	runtimeConfig, runtimeConfigPath := loadChatPersistenceRuntimeConfig(cfg, nil)
	manager, userID, sessionDir, err := newChatSessionManagerWithRuntimeConfig(req.SessionDir, runtimeConfig, runtimeConfigPath, req.User)
	if err != nil {
		return nil, fmt.Errorf("初始化会话存储失败: %w", err)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = sessionsUnlimitedLimit
	}
	if req.Interactive && !req.LimitExplicit {
		// 交互界面默认按一页浏览，不被文本列表的 20 条上限截断。
		limit = sessionsBrowserPageSize
	}
	store := &sessionsBrowserStore{
		manager:    manager,
		userID:     userID,
		sessionDir: sessionDir,
		workspace:  workspace,
		limit:      limit,
		filter: ChatSessionListFilter{
			State:     runtimechat.SessionState(strings.ToLower(strings.TrimSpace(req.State))),
			Provider:  strings.TrimSpace(req.Provider),
			Model:     strings.TrimSpace(req.Model),
			Workspace: workspace,
			Limit:     limit,
		},
	}
	return store, nil
}

// Stop 释放管理器（与 collectSessionsCommandResult 的 defer 对称）。
func (s *sessionsBrowserStore) Stop() {
	if s != nil && s.manager != nil {
		s.manager.Stop()
	}
}

// listSessions 按范围 + 固定筛选 + 搜索词取会话（元数据口径，不加载历史）。
func (s *sessionsBrowserStore) listSessions(query string) ([]*runtimechat.Session, error) {
	if s == nil || s.manager == nil {
		return nil, fmt.Errorf("会话管理未启用")
	}
	filter := s.filter
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		// 命令上的 --query 与界面里的搜索框叠加时，以界面搜索为准（用户在
		// 屏幕上看到的就是搜索框内容，两条条件 AND 只会让他困惑）。
		filter.Query = trimmed
	}
	filter.Limit = s.limit
	sessions, err := listFilteredChatSessions(s.manager, s.userID, filter)
	if err != nil {
		return nil, fmt.Errorf("列出会话失败: %w", err)
	}
	return sessions, nil
}

// List 返回可浏览的会话行（与 --json 同源）。
func (s *sessionsBrowserStore) List(query string) ([]sessionsCommandItem, error) {
	sessions, err := s.listSessions(query)
	if err != nil {
		return nil, err
	}
	return buildSessionsCommandItems(sessions), nil
}

// Detail 读取单个会话的详情（只读元数据，不回放历史）。
func (s *sessionsBrowserStore) Detail(sessionID string) (sessionsSessionDetail, error) {
	if s == nil || s.manager == nil {
		return sessionsSessionDetail{}, fmt.Errorf("会话管理未启用")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sessionsSessionDetail{}, fmt.Errorf("会话 ID 为空")
	}
	session, err := s.manager.GetMetadata(context.Background(), sessionID)
	if err != nil {
		return sessionsSessionDetail{}, fmt.Errorf("读取会话 %s 失败: %w", sessionID, err)
	}
	if session == nil {
		return sessionsSessionDetail{}, fmt.Errorf("会话 %s 不存在", sessionID)
	}
	item := sessionsCommandItem{}
	if items := buildSessionsCommandItems([]*runtimechat.Session{session}); len(items) > 0 {
		item = items[0]
	}
	detail := sessionsSessionDetail{
		SessionID:     item.SessionID,
		Title:         item.Title,
		State:         item.State,
		Workspace:     item.Workspace,
		Provider:      item.Provider,
		Model:         item.Model,
		Protocol:      item.Protocol,
		MessageCount:  item.MessageCount,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
		Summary:       item.Summary,
		LaunchEntry:   item.LaunchEntry,
		LaunchArgs:    item.LaunchArgs,
		LaunchArgv:    item.LaunchArgv,
		LaunchCwd:     item.LaunchCwd,
		LaunchBinary:  item.LaunchBinary,
		LaunchCommand: item.LaunchCommand,
	}
	if strings.TrimSpace(detail.LaunchCommand) == "" {
		detail.LaunchCommand = formatLaunchCommand(detail.LaunchBinary, detail.SessionID, detail.LaunchArgs)
	}
	if !item.LaunchRecorded.IsZero() {
		detail.LaunchRecorded = item.LaunchRecorded.Local().Format(time.RFC3339)
	}
	detail.LaunchHistory = formatLaunchHistory(session.Metadata.Context)
	detail.Mesh = describeSessionMeshWindow(session)
	return detail, nil
}

// formatLaunchHistory 把历次启动记录渲染成可读行（最新在前）。
func formatLaunchHistory(ctx map[string]interface{}) []string {
	entries := launchHistoryEntriesFromContext(ctx)
	if len(entries) == 0 {
		return nil
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		at, _ := entry["at"].(string)
		entryName, _ := entry["entry"].(string)
		cwd, _ := entry["cwd"].(string)
		flags := launchStringList(entry["flags"])
		argv := launchStringList(entry["argv"])
		tokens := flags
		if len(tokens) == 0 {
			tokens = argv
		}
		line := strings.TrimSpace(fmt.Sprintf("%s %s %s [%s]", at, entryName, cwd, strings.Join(tokens, " ")))
		lines = append(lines, line)
	}
	return lines
}

// describeSessionMeshWindow 报告该会话是否已有 mesh 节点（gateway/mesh 接线点）。
// 这里只读会话上下文，不发起任何网络调用；mesh 未启用时返回空串。
func describeSessionMeshWindow(session *runtimechat.Session) string {
	if session == nil || session.Metadata.Context == nil {
		return ""
	}
	nodeID := strings.TrimSpace(sessionmeta.String(session.Metadata.Context, meshWindowNodeIDContextKey))
	if nodeID == "" {
		return ""
	}
	endpoint := strings.TrimSpace(sessionmeta.String(session.Metadata.Context, meshWindowEndpointContextKey))
	if endpoint == "" {
		return "mesh 节点 " + nodeID
	}
	return "mesh 节点 " + nodeID + " · " + endpoint
}

// mesh 窗口在会话上下文里的键（由 chat_mesh.go 的节点登记写入；这里只读）。
const (
	meshWindowNodeIDContextKey   = "aicli_mesh_node_id"
	meshWindowEndpointContextKey = "aicli_mesh_node_endpoint"
)

// Rename 持久化会话标题（与 /rename、web 侧重命名同一入口）。
func (s *sessionsBrowserStore) Rename(sessionID, title string) error {
	if s == nil || s.manager == nil {
		return fmt.Errorf("会话管理未启用")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("标题不能为空")
	}
	if err := s.manager.SetTitle(context.Background(), strings.TrimSpace(sessionID), title); err != nil {
		return fmt.Errorf("重命名会话失败: %w", err)
	}
	return nil
}

// Delete 删除会话。
func (s *sessionsBrowserStore) Delete(sessionID string) error {
	if s == nil || s.manager == nil {
		return fmt.Errorf("会话管理未启用")
	}
	if err := s.manager.Delete(context.Background(), strings.TrimSpace(sessionID)); err != nil {
		return fmt.Errorf("删除会话失败: %w", err)
	}
	return nil
}

// Archive 归档会话。
func (s *sessionsBrowserStore) Archive(sessionID string) error {
	if s == nil || s.manager == nil {
		return fmt.Errorf("会话管理未启用")
	}
	if err := s.manager.ArchiveSession(context.Background(), strings.TrimSpace(sessionID)); err != nil {
		return fmt.Errorf("归档会话失败: %w", err)
	}
	return nil
}

// Activate 取消归档（重新激活）。
func (s *sessionsBrowserStore) Activate(sessionID string) error {
	if s == nil || s.manager == nil {
		return fmt.Errorf("会话管理未启用")
	}
	if err := s.manager.Activate(context.Background(), strings.TrimSpace(sessionID)); err != nil {
		return fmt.Errorf("取消归档失败: %w", err)
	}
	return nil
}

// sessionsBrowserActionsFor 把存储、终端后端与原地恢复能力装配成动作表。
func sessionsBrowserActionsFor(store *sessionsBrowserStore, launcher *termopen.Launcher, canResumeHere bool) sessionsBrowserActions {
	actions := sessionsBrowserActions{
		List:     store.List,
		Detail:   store.Detail,
		Rename:   store.Rename,
		Delete:   store.Delete,
		Archive:  store.Archive,
		Activate: store.Activate,
		OpenTerminal: func(item sessionsCommandItem) (string, error) {
			return openSessionInNewTerminal(item, launcher)
		},
		ResumeHere:      resumeSessionInCurrentTerminal,
		CanResumeHere:   canResumeHere,
		TerminalSummary: "",
	}
	if launcher == nil {
		actions.OpenTerminal = nil
		return actions
	}
	if _, err := launcher.Detect(); err != nil {
		// 探测失败时不展示该动作：菜单里出现一个必然失败的动作，比不出现更糟。
		actions.OpenTerminal = nil
		actions.TerminalSummary = err.Error()
		return actions
	}
	actions.TerminalSummary = launcher.Describe()
	return actions
}

// toUISessionsBrowserItems 把渲染层无关的行描述映射成 ui 的全屏行。
func toUISessionsBrowserItems(rows []uiFullScreenListItemLike) []ui.FullScreenListItem {
	items := make([]ui.FullScreenListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, ui.FullScreenListItem{
			Leading:    row.Leading,
			Title:      row.Title,
			Detail:     row.Detail,
			Preview:    row.Preview,
			SearchText: row.SearchText,
			Disabled:   row.Disabled,
			NoIndex:    row.NoIndex,
		})
	}
	return items
}

// sessionsBrowserListOptions 把渲染请求翻译成全屏列表参数（与真终端隔离的纯函数，
// 便于断言 InitialRow 确实被透传到了 InitialIndex）。
func sessionsBrowserListOptions(spec sessionsBrowserListSpec) ui.FullScreenListOptions {
	options := ui.FullScreenListOptions{
		Title:        spec.Title,
		Subtitle:     spec.Subtitle,
		EmptyMessage: "没有匹配的会话",
		ConfirmLabel: spec.ConfirmLabel,
		Items:        toUISessionsBrowserItems(spec.Items),
	}
	if options.ConfirmLabel == "" {
		options.ConfirmLabel = "选择"
	}
	if spec.FreeText == nil {
		// 动作完成后的重开必须回到同一行：行下标→ InitialIndex 的透传只在不处于
		// 单值输入屏时生效（输入屏没有列表语义）。
		options.InitialIndex = spec.InitialRow
	}
	if spec.FreeText != nil {
		options.FreeTextMode = true
		options.FreeTextHint = spec.FreeText.Hint
		options.FreeTextValue = spec.FreeText.Value
		options.Items = nil
		options.EmptyMessage = ""
		validate := spec.FreeText.Validate
		options.OnConfirmText = func(text string) error {
			if validate == nil {
				return nil
			}
			return validate(text)
		}
	}
	return options
}

// newSessionsBrowserListRunner 返回生产渲染器：一屏一次全屏列表（自带 DEC 1049
// 租约的进入与退出，所以每次返回都已经在主屏上，前台恢复子进程才能安全接管终端）。
func newSessionsBrowserListRunner(terminal *ui.Terminal) sessionsBrowserListRunner {
	return func(spec sessionsBrowserListSpec) (sessionsBrowserListPick, error) {
		result, err := ui.RunStartupFullScreenList(context.Background(), terminal, sessionsBrowserListOptions(spec))
		if err != nil {
			return sessionsBrowserListPick{}, err
		}
		return sessionsBrowserListPick{
			Index:           result.Index,
			Cancelled:       result.Cancelled,
			DeleteRequested: result.DeleteRequested,
			Text:            result.Text,
		}, nil
	}
}

// shouldRunSessionsInteractive 决定是否进入交互界面：
//   - 显式 --interactive=false / --plain 一律走文本；
//   - JSON 输出永远走机器可读通道（脚本契约不能因为TTY存在而改变）；
//   - 其余情况要求一个能承载全屏列表的 ANSI 交互终端。
func shouldRunSessionsInteractive(req sessionsCommandRequest, terminal *ui.Terminal) bool {
	if req.Plain || !req.Interactive || isJSONOutputFormat(req.Output.Format) {
		return false
	}
	return ui.CanUseFullScreenList(terminal)
}

// runSessionsInteractiveCommand 是 `aicli sessions` 的交互入口。
func runSessionsInteractiveCommand(cfg *config.Config, req sessionsCommandRequest, terminal *ui.Terminal) int {
	store, err := openSessionsBrowserStore(cfg, req)
	if err != nil {
		printSessionsCommandError(err, req.Output)
		return sessionsExitFailure
	}
	defer store.Stop()

	launcher := sessionsTerminalLauncher(req.Terminal)
	actions := sessionsBrowserActionsFor(store, launcher, ui.CanUseFullScreenList(terminal))
	runner := newSessionsBrowserListRunner(terminal)
	outcome := runSessionsBrowser(actions, runner)
	if message := strings.TrimSpace(outcome.Message); message != "" {
		if outcome.ExitCode == sessionsExitOK {
			fmt.Println(message)
		} else {
			fmt.Fprintf(os.Stderr, "错误: %s\n", message)
		}
	}
	return outcome.ExitCode
}

// runSessionsOpenTerminal 是 `aicli sessions --open-terminal <id>` 的非交互入口：
// 脚本与 gateway 侧只需要「把这个会话的窗口拉起来」，不需要界面。
func runSessionsOpenTerminal(cfg *config.Config, req sessionsCommandRequest, sessionID string) int {
	store, err := openSessionsBrowserStore(cfg, req)
	if err != nil {
		printSessionsCommandError(err, req.Output)
		return sessionsExitFailure
	}
	defer store.Stop()

	item, err := store.itemByID(sessionID)
	if err != nil {
		printSessionsCommandError(err, req.Output)
		return sessionsExitFailure
	}
	message, err := openSessionInNewTerminal(item, sessionsTerminalLauncher(req.Terminal))
	if err != nil {
		printSessionsCommandError(err, req.Output)
		return sessionsExitFailure
	}
	if isJSONOutputFormat(req.Output.Format) {
		printCommandJSONOutput("sessions", req.Output.Envelope, map[string]interface{}{
			"status":         "opened",
			"session_id":     item.SessionID,
			"launch_command": formatLaunchCommand(item.LaunchBinary, item.SessionID, item.LaunchArgs),
			"message":        message,
		})
		return sessionsExitOK
	}
	fmt.Println(message)
	return sessionsExitOK
}

// itemByID 在同一个存储句柄上定位一个会话（不做工作目录过滤：显式 ID 优先）。
func (s *sessionsBrowserStore) itemByID(sessionID string) (sessionsCommandItem, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return sessionsCommandItem{}, fmt.Errorf("会话 ID 为空")
	}
	if s == nil || s.manager == nil {
		return sessionsCommandItem{}, fmt.Errorf("会话管理未启用")
	}
	if session, err := s.manager.GetMetadata(context.Background(), sessionID); err == nil && session != nil {
		if items := buildSessionsCommandItems([]*runtimechat.Session{session}); len(items) > 0 {
			return items[0], nil
		}
	}
	// 元数据口径拿不到时回到列表口径（例如筛选态下的可见性差异）。
	items, err := s.List("")
	if err != nil {
		return sessionsCommandItem{}, err
	}
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.SessionID), sessionID) {
			return item, nil
		}
	}
	return sessionsCommandItem{}, fmt.Errorf("会话 %s 不存在", sessionID)
}

// latestSessionItem 返回范围内最近更新的一条会话（供 `resume --detach` 无 ID 时用）。
func (s *sessionsBrowserStore) latestSessionItem() (sessionsCommandItem, error) {
	sessions, err := s.listSessions("")
	if err != nil {
		return sessionsCommandItem{}, err
	}
	for _, session := range sessions {
		if session == nil || session.MessageCount() <= 0 {
			// 只有 instructions 占位消息的空会话不可恢复（与 /resume 同口径）。
			continue
		}
		if items := buildSessionsCommandItems([]*runtimechat.Session{session}); len(items) > 0 {
			return items[0], nil
		}
	}
	return sessionsCommandItem{}, fmt.Errorf("没有可恢复的历史会话")
}
