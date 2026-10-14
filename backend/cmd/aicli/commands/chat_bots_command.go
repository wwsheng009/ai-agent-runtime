package commands

// /bots 副屏：IM 连接器（机器人）管理（docs/bots/07-config-ux.md §3.4 的增强版）。
//
// 交互流（主屏 → 副屏 → 主屏）：
//
//	/bots
//	  ├─ 「＋ 新增机器人」→ 通道选择副屏 →（微信等 kind=qr 通道）零输入扫码接入：
//	  │    自动生成 ID → 创建 → start_login → 打印二维码/登录链接 →
//	  │    后台轮询 login_status（2s）→ confirmed 后回填 settings/secrets，
//	  │    并把 owner 用户自动加入 allowed_users（aicli fail-closed 下立即可用）。
//	  └─ 已有机器人 → 动作副屏：扫码登录 / 启用·停用 / 查看日志 / 删除 / 返回。
//
// 通道差异全部来自 Schema（kind=qr 动作、标签）；非扫码通道提示改用 web 页
// 配置（TUI 不做完整表单）。副屏生命周期由 chat_screen_framework 统一承担
// （租约/降级/Esc 契约）；扫码轮询在后台 goroutine 中运行，进度经
// session.Interaction 的 ephemeral 补充行写回主屏，不阻塞输入循环。

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	ui "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

const (
	chatBotsScreenListID     = "bots.list"
	chatBotsScreenProviderID = "bots.provider"
	chatBotsScreenActionID   = "bots.action"
	chatBotsScreenDeleteID   = "bots.delete"
	chatBotsScreenLogsID     = "bots.logs"
	chatBotsScreenScanID     = "bots.scan"

	// botsQRActionKind 是 Schema 里扫码动作的 kind（与三端前端同一约定）。
	botsQRActionKind = "qr"
	// botsLoginStatusAction 是扫码动作配套的状态查询动作名（通道约定，微信实现）。
	botsLoginStatusAction = "login_status"

	// botsScanMaxRefresh 是二维码过期后的自动换码次数上限。
	botsScanMaxRefresh = 3
	// botsScanMaxErrors 是连续轮询错误达到该值时停止（避免无限重试）。
	botsScanMaxErrors = 5
	// botsQRBlockWidth 是终端二维码渲染的目标字符宽度。
	botsQRBlockWidth = 72
)

// 测试注入点：轮询间隔与总超时。
var (
	botsScanPollInterval = 2 * time.Second
	botsScanDeadline     = 3 * time.Minute
)

// botsActionKind 是动作副屏的行语义（行序与 kinds 切片一一对应）。
type botsActionKind uint8

// botsActionRunner 是 /bots 需要的动作执行面（connectors.ActionProvider 的
// 最小子集，便于测试注入替身；生产实现来自通道 Provider 的 RunAction）。
type botsActionRunner interface {
	RunAction(ctx context.Context, cfg connectors.Config, action string, payload map[string]any) (map[string]any, error)
}

const (
	botsActionScan botsActionKind = iota
	botsActionEnable
	botsActionDisable
	botsActionLogs
	botsActionDelete
	botsActionBack
)

// handleChatBotsCommand 是 /bots 命令入口：未装配连接器时给出明确文案，
// 否则打开机器人列表副屏（能力不足/嵌套时由框架降级为主屏内联文档）。
func handleChatBotsCommand(session *ChatSession, _ string) {
	if session == nil {
		// 新功能不得新增终端直写（direct-writer 迁移债基线）；无会话时只记日志。
		logpkg.Warnf("bots: /bots 需要活动会话，已忽略")
		return
	}
	if unifiedDirectInteractiveOutput(session) {
		// 与 /agents 同款：Screen 效应必须统一派发（含降级内联）。
		result := executeStructuredBotsCommand(session)
		renderErr := renderChatCommandResult(session, result, false)
		if renderErr == nil {
			dispatchChatScreenEffects(session, result)
		}
		return
	}
	host, reason := botsResolveHost(session)
	if host == nil {
		printChatCommandOutput(session, botsUnavailableText(reason))
		return
	}
	// legacy/plain/JSON 投影：内联文档，不进入副屏。
	printChatCommandOutput(session, botsInlineDoc(host))
}

// executeStructuredBotsCommand 把 /bots 暴露给统一命令管线：未装配连接器时
// 返回明确文案；否则请求机器人列表副屏（能力不足时由 DegradeDoc 内联）。
func executeStructuredBotsCommand(session *ChatSession) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	host, reason := botsResolveHost(session)
	if host == nil {
		return commandTextResult(botsUnavailableText(reason))
	}
	return CommandResult{Action: CommandContinue, Screen: chatScreenSpecRef(chatScreenBotsListSpec(host))}
}

// botsResolveHost 解析连接器宿主：已装配（或已按需装配过）直接用；
// 未装配时**按需自装配**——/bots 本身就是配置入口，不要求用户先手工写
// config.yaml（缺文件时创建 starter 配置后启动连接器宿主）。
func botsResolveHost(session *ChatSession) (*LocalConnectorHost, string) {
	if host := currentLocalConnectorHost(); host != nil && host.Service() != nil && host.Store() != nil {
		return host, ""
	}
	host, err := startLocalConnectorsOnDemand(session)
	if err != nil {
		if note := localConnectorsInitNote(); note != "" {
			return nil, note
		}
		return nil, err.Error()
	}
	return host, ""
}

// startLocalConnectorsOnDemand 按需装配连接器宿主（/bots 首次使用）：
//   - 只对交互式会话生效（与启动路径同口径）；
//   - 通过 ensureWritableAICLIConfigPath 解析共享 config.yaml：文件缺失时
//     创建 starter 配置（与其他写操作同口径），而不是继续“零配置跳过”；
//   - 成功后发布路由与宿主引用（本会话的 /bots 与 loopback API 均可复用）。
func startLocalConnectorsOnDemand(session *ChatSession) (*LocalConnectorHost, error) {
	if session == nil || session.Config == nil {
		setLocalConnectorInitNote("会话缺少配置句柄")
		return nil, fmt.Errorf("会话缺少配置句柄")
	}
	if !chatHostSessionInteractive(session) {
		setLocalConnectorInitNote("需要交互式 aicli chat 会话（/bots 与机器人页均不可用于非交互运行）")
		return nil, fmt.Errorf("需要交互式 aicli chat 会话")
	}
	// 斜杠命令与 web 路由都不经过 turn 入口的 ensureChatExecutor：首帧后立即
	// 使用时，能力面（含 LocalRuntimeHost 与连接器宿主）可能仍在后台装载。
	// 在这里统一 await（/bots 与 web 按需自举共享本函数）——与 /skill、/call
	// 同款；幂等，未安装门控的路径（runtime-server/测试）是 no-op。等待带上
	// 显式上限：web 请求不能无限期挂起，超时按能力面装载超时同款口径报错。
	if session.LocalRuntimeHost == nil {
		waitCtx, cancelWait := context.WithTimeout(context.Background(), chatCapabilitiesWaitLimit)
		waitErr := awaitChatCapabilitiesForTurn(waitCtx, session)
		cancelWait()
		if waitErr != nil {
			setLocalConnectorInitNote("能力面装载失败：" + waitErr.Error())
			return nil, fmt.Errorf("能力面装载失败：%w", waitErr)
		}
	}
	runtimeHost := session.LocalRuntimeHost
	if runtimeHost == nil {
		reason := botsLocalHostUnavailableReason(session)
		setLocalConnectorInitNote(reason)
		return nil, fmt.Errorf("%s", reason)
	}
	configPath, err := ensureWritableAICLIConfigPath(session.Config, "")
	if err != nil {
		setLocalConnectorInitNote("准备共享 config.yaml 失败：" + err.Error())
		return nil, err
	}
	ctx := context.Background()
	if runtimeHost.lifecycleCtx != nil {
		ctx = runtimeHost.lifecycleCtx
	}
	started, startErr := StartLocalConnectors(ctx, runtimeHost, configPath, logpkg.Infof)
	if startErr != nil {
		setLocalConnectorInitNote("连接器装配失败：" + startErr.Error())
		return nil, startErr
	}
	setLocalConnectorInitNote("")
	logpkg.Infof("aicli connectors: started on demand (config=%s)", configPath)
	return started, nil
}

// botsLocalHostUnavailableReason 解释「本地连接器宿主无法装配」的具体原因：
// runtime-server 模式的会话会刻意把 LocalRuntimeHost 置空（本地能力面整段
// 不挂载），此时必须报出接管方与替代路径，而不是笼统的"未就绪"。
func botsLocalHostUnavailableReason(session *ChatSession) string {
	if session != nil {
		if provider, ok := session.ChatExecutor.(runtimeServerURLProvider); ok {
			if url := strings.TrimSpace(provider.RuntimeServerURL()); url != "" {
				return "当前会话由 runtime-server 接管（" + url +
					"）：本地连接器未挂载——请在该服务器的连接器页管理机器人，或用 --runtime-mode local 重开会话后使用 /bots"
			}
		}
		if initErr := strings.TrimSpace(session.CapabilitiesInitError); initErr != "" {
			return "会话能力面未挂载：" + initErr
		}
	}
	return "本机 runtime 未就绪（会话能力面未挂载）"
}

// botsUnavailableText 组装不可用文案：优先用本次解析出的原因，其次用
// 启动期记录的原因（config.yaml 缺失 / 装配失败 / 路径解析失败）。
func botsUnavailableText(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = localConnectorsInitNote()
	}
	if reason == "" {
		reason = "连接器无法在本会话装配（需要交互式 aicli chat）"
	}
	return "机器人管理不可用：" + reason + "。"
}

// ── 机器人列表副屏 ─────────────────────────────────────────────────────────

func chatScreenBotsListSpec(host *LocalConnectorHost) chatScreenSpec {
	configs := botsSortedConfigs(host)
	rows := make([]chatScreenRow, 0, len(configs)+1)
	rows = append(rows, chatScreenRow{
		Title:      "＋ 新增机器人（扫码接入）",
		Detail:     "选择通道后扫码接入；微信零输入（自动生成 ID，无需填基址）",
		SearchText: "new add create 新增 扫码 saoma",
	})
	for _, cfg := range configs {
		rows = append(rows, chatScreenRow{
			Title:      botsConnectorTitle(cfg),
			Detail:     botsConnectorDetail(host, cfg),
			SearchText: strings.ToLower(cfg.ID + " " + cfg.Provider + " " + cfg.Name),
		})
	}
	return chatScreenSpec{
		ID:           chatBotsScreenListID,
		Title:        "机器人",
		Kind:         chatScreenList,
		Subtitle:     "↑↓ 选择，Enter 打开，Esc 返回；首行可新增",
		ConfirmLabel: "打开",
		Rows:         rows,
		Trigger:      "command",
		AfterClose:   chatBotsListAfterClose(host),
		DegradeDoc:   chatScreenTextDoc(botsInlineDoc(host)),
	}
}

// chatBotsListAfterClose 在列表副屏关闭后按选择推进：首行=新增（通道选择
// 副屏），其余行=已有机器人的动作副屏。AfterClose 在租约释放后执行（I3），
// 因此这里继续 openChatScreen 是安全的。
func chatBotsListAfterClose(host *LocalConnectorHost) func(*ChatSession, chatScreenOutcome) {
	return func(session *ChatSession, outcome chatScreenOutcome) {
		if outcome.Degraded || outcome.Result != chatScreenClosedConfirm || outcome.Index < 0 {
			return
		}
		if outcome.Index == 0 {
			chatScreenOpenAndApply(session, chatScreenBotsProviderSpec(host))
			return
		}
		configs := botsSortedConfigs(host)
		idx := outcome.Index - 1
		if idx < 0 || idx >= len(configs) {
			return
		}
		chatScreenOpenAndApply(session, chatScreenBotsActionSpec(host, configs[idx]))
	}
}

// ── 通道选择副屏（新增机器人） ─────────────────────────────────────────────

func chatScreenBotsProviderSpec(host *LocalConnectorHost) chatScreenSpec {
	schemas := host.Service().Providers()
	rows := make([]chatScreenRow, 0, len(schemas))
	for _, schema := range schemas {
		detail := "字段配置（请在 web 页填写）"
		if botsQRActionKey(schema) != "" {
			detail = "扫码接入（零输入）"
		}
		rows = append(rows, chatScreenRow{
			Title:      firstNonEmptyChatValue(schema.Label, schema.Provider),
			Detail:     detail + " · " + schema.Provider,
			SearchText: strings.ToLower(schema.Provider + " " + schema.Label),
		})
	}
	if len(rows) == 0 {
		rows = append(rows, chatScreenRow{Title: "（没有已注册的通道）"})
	}
	return chatScreenSpec{
		ID:           chatBotsScreenProviderID,
		Title:        "选择机器人类型",
		Kind:         chatScreenList,
		Subtitle:     "↑↓ 选择，Enter 开始接入，Esc 返回",
		ConfirmLabel: "开始",
		Rows:         rows,
		Trigger:      "command",
		AfterClose:   chatBotsProviderAfterClose(host),
		DegradeDoc:   chatScreenTextDoc(botsProvidersInlineDoc(host)),
	}
}

func chatBotsProviderAfterClose(host *LocalConnectorHost) func(*ChatSession, chatScreenOutcome) {
	return func(session *ChatSession, outcome chatScreenOutcome) {
		if outcome.Degraded || outcome.Result != chatScreenClosedConfirm || outcome.Index < 0 {
			return
		}
		schemas := host.Service().Providers()
		if outcome.Index >= len(schemas) {
			return
		}
		schema := schemas[outcome.Index]
		if botsQRActionKey(schema) == "" {
			printChatCommandOutput(session, fmt.Sprintf(
				"通道 %s 需要在 web 页配置字段：运行 aicli chat --web 后打开「机器人」页签（或使用 runtime-server 连接器页）。",
				firstNonEmptyChatValue(schema.Label, schema.Provider)))
			return
		}
		botsCreateAndScan(session, host, schema)
	}
}

// ── 动作副屏（已有机器人） ─────────────────────────────────────────────────

func chatScreenBotsActionSpec(host *LocalConnectorHost, cfg connectors.Config) chatScreenSpec {
	schema, _ := botsProviderSchema(host, cfg.Provider)
	rows := make([]chatScreenRow, 0, 6)
	kinds := make([]botsActionKind, 0, 6)
	add := func(kind botsActionKind, title, detail, search string) {
		kinds = append(kinds, kind)
		rows = append(rows, chatScreenRow{Title: title, Detail: detail, SearchText: strings.ToLower(search)})
	}
	if botsQRActionKey(schema) != "" {
		add(botsActionScan, "扫码登录", "重新获取二维码（凭据过期 / 换账号时使用）", "scan qr login 扫码 登录")
	}
	if cfg.Enabled {
		add(botsActionDisable, "停用", "停止轮询/长连接（保留配置与凭据）", "disable stop 停用")
	} else {
		add(botsActionEnable, "启用", "开始轮询/长连接（需 allowed_users 非空）", "enable start 启用")
	}
	add(botsActionLogs, "查看日志", "最近 50 条运行日志 + 累计计数", "logs 日志")
	add(botsActionDelete, "删除", "从配置移除该机器人（不可恢复）", "delete remove 删除")
	add(botsActionBack, "返回", "回到机器人列表", "back return 返回")
	return chatScreenSpec{
		ID:           chatBotsScreenActionID,
		Title:        "机器人：" + botsConnectorTitle(cfg),
		Kind:         chatScreenList,
		Subtitle:     "↑↓ 选择，Enter 执行，Esc 返回",
		ConfirmLabel: "执行",
		Rows:         rows,
		Trigger:      "command",
		AfterClose:   chatBotsActionAfterClose(host, cfg, kinds),
		DegradeDoc:   chatScreenTextDoc(botsActionInlineDoc(host, cfg)),
	}
}

func chatBotsActionAfterClose(host *LocalConnectorHost, cfg connectors.Config, kinds []botsActionKind) func(*ChatSession, chatScreenOutcome) {
	return func(session *ChatSession, outcome chatScreenOutcome) {
		if outcome.Degraded || outcome.Result != chatScreenClosedConfirm || outcome.Index < 0 || outcome.Index >= len(kinds) {
			return
		}
		switch kinds[outcome.Index] {
		case botsActionScan:
			schema, _ := botsProviderSchema(host, cfg.Provider)
			botsStartScanFlow(session, host, cfg, schema)
		case botsActionEnable:
			botsSetConnectorEnabled(session, host, cfg, true)
		case botsActionDisable:
			botsSetConnectorEnabled(session, host, cfg, false)
		case botsActionLogs:
			chatScreenOpenAndApply(session, chatScreenBotsLogsSpec(host, cfg))
		case botsActionDelete:
			chatScreenOpenAndApply(session, chatScreenBotsDeleteSpec(host, cfg))
		case botsActionBack:
			chatScreenOpenAndApply(session, chatScreenBotsListSpec(host))
		}
	}
}

func botsSetConnectorEnabled(session *ChatSession, host *LocalConnectorHost, cfg connectors.Config, enabled bool) {
	cfg.Enabled = enabled
	if err := host.Store().PutConfig(cfg); err != nil {
		printChatCommandOutput(session, "保存失败："+err.Error())
		return
	}
	if enabled {
		if err := host.Service().ReloadConnector(context.Background(), cfg.ID); err != nil {
			// 例如白名单为空（fail-closed 跳过启动）：按事实报告，不谎报已运行。
			printChatCommandOutput(session, fmt.Sprintf("已启用 %s（重载告警：%v）", cfg.ID, err))
			return
		}
		printChatCommandOutput(session, "已启用："+cfg.ID)
		return
	}
	host.Service().StopConnector(cfg.ID)
	printChatCommandOutput(session, "已停用："+cfg.ID)
}

// ── 删除确认副屏（默认选「取消」） ─────────────────────────────────────────

func chatScreenBotsDeleteSpec(host *LocalConnectorHost, cfg connectors.Config) chatScreenSpec {
	rows := []chatScreenRow{
		{Title: "取消", Detail: "保留该机器人", SearchText: "cancel 取消"},
		{Title: "确认删除 " + cfg.ID, Detail: "移除配置与凭据（不可恢复）", SearchText: "delete confirm 删除"},
	}
	return chatScreenSpec{
		ID:           chatBotsScreenDeleteID,
		Title:        "删除机器人：" + cfg.ID,
		Kind:         chatScreenList,
		Subtitle:     "默认选中「取消」；Enter 确认选择，Esc 返回",
		ConfirmLabel: "确认",
		Rows:         rows,
		Trigger:      "command",
		AfterClose: func(session *ChatSession, outcome chatScreenOutcome) {
			if outcome.Degraded || outcome.Result != chatScreenClosedConfirm || outcome.Index != 1 {
				return
			}
			host.Service().StopConnector(cfg.ID)
			if err := host.Store().DeleteConfig(cfg.ID); err != nil {
				printChatCommandOutput(session, "删除失败："+err.Error())
				return
			}
			printChatCommandOutput(session, "已删除："+cfg.ID)
		},
		DegradeDoc: chatScreenTextDoc("删除机器人：" + cfg.ID +
			"\n请在 web 页（aicli chat --web → 机器人页签）执行删除；TUI 副屏不可用时不做破坏性操作。"),
	}
}

// ── 日志副屏 ───────────────────────────────────────────────────────────────

func chatScreenBotsLogsSpec(host *LocalConnectorHost, cfg connectors.Config) chatScreenSpec {
	entries, _ := host.Service().Logs(cfg.ID, 50)
	stats, _ := host.Service().Stats(cfg.ID)
	lines := []string{
		fmt.Sprintf("%s（%s）最近日志", botsConnectorTitle(cfg), cfg.ID),
		fmt.Sprintf("入 %d · 出 %d · 错 %d · 去重跳过 %d · 队列满 %d",
			stats.Inbound, stats.Outbound, stats.Errors, stats.DedupSkipped, stats.QueueFull),
	}
	if stats.LastError != "" {
		lines = append(lines, "最近错误："+stats.LastError)
	}
	lines = append(lines, "")
	if len(entries) == 0 {
		lines = append(lines, "（暂无日志；进程内环形缓冲，重启清零）")
	}
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("%s [%s/%s] %s",
			entry.Time.Format("15:04:05"), entry.Level, entry.Direction, entry.Summary))
	}
	return chatScreenDocumentSpec(chatBotsScreenLogsID, "运行日志："+cfg.ID, textLinesDocument(lines))
}

// ── 扫码接入（极简流：零输入 + 重复扫码识别） ─────────────────────────────

// botsCreateAndScan 为扫码通道创建连接器（自动 ID/默认基址/默认模式），
// 随后立即进入扫码登录。创建后 Reload 失败属预期（尚未取得 bot_token），
// 只记不报——扫码成功后回填并再次 Reload。
func botsCreateAndScan(session *ChatSession, host *LocalConnectorHost, schema api.ProviderSchema) {
	cfg := connectors.Config{
		ID:        botsGenerateConnectorID(schema.Provider),
		Provider:  schema.Provider,
		Name:      firstNonEmptyChatValue(schema.Label, schema.Provider),
		Enabled:   true,
		Mode:      botsDefaultMode(schema),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Settings:  map[string]string{},
		Secrets:   map[string]string{},
		Policy:    connectors.Policy{CommandPrefix: "/"},
	}
	if err := host.Store().PutConfig(cfg); err != nil {
		printChatCommandOutput(session, "创建机器人失败："+err.Error())
		return
	}
	_ = host.Service().ReloadConnector(context.Background(), cfg.ID)
	printChatCommandOutput(session, fmt.Sprintf("已创建机器人 %s（%s）。正在获取登录二维码…", cfg.ID, cfg.Name))
	botsStartScanFlow(session, host, cfg, schema)
}

// botsStartScanFlow 执行 start_login 并打开「扫码登录」实况副屏：屏内展示
// 二维码与实时状态（空闲 tick 自动刷新），confirmed 后自动回填并自动关屏，
// Esc 取消。全屏能力不足时降级为主屏二维码 + 后台轮询（botsPollLogin）。
func botsStartScanFlow(session *ChatSession, host *LocalConnectorHost, cfg connectors.Config, schema api.ProviderSchema) {
	runner := botsActionProvider(host, cfg.Provider)
	if runner == nil {
		printChatCommandOutput(session, "该通道不支持动作执行，无法扫码。")
		return
	}
	qrKey := botsQRActionKey(schema)
	if qrKey == "" {
		printChatCommandOutput(session, "该通道不支持扫码登录。")
		return
	}
	payload, err := runner.RunAction(context.Background(), cfg, qrKey, map[string]any{})
	if err != nil {
		printChatCommandOutput(session, "获取二维码失败："+err.Error())
		return
	}
	if botsPayloadString(payload, "login_id") == "" {
		printChatCommandOutput(session, "登录会话缺少 login_id，已停止轮询。")
		return
	}
	state := newBotsScanScreenState(host, runner, cfg, schema, qrKey, payload)
	chatScreenOpenAndApply(session, chatScreenBotsScanSpec(state))
}

// ── 扫码登录实况副屏（状态机 + debug overlay 渲染） ────────────────────────

// botsScanScreenRunner 是扫码副屏的渲染入口（RunDocument）：默认跑 ui 的
// debug overlay（空闲 tick 自动驱动 Refresh，~75ms；ctx 取消即退出）；
// 测试替换为无 TTY 的驱动桩。
var botsScanScreenRunner = func(session *ChatSession, lease ui.ScreenLease, ctx context.Context, options ui.DebugOverlayOptions) error {
	return ui.RunDebugOverlayWithLease(ctx, resumeFullScreenTerminal(session), options, lease)
}

// botsScanScreenState 是扫码副屏的状态机：
//   - Refresh 在 overlay 的空闲 tick 上被调用（必须非阻塞、非 actor goroutine）：
//     这里只做节流调度（每 botsScanPollInterval 发起一次后台轮询）并返回当前快照，
//     真正的网络轮询在 goroutine 里跑；
//   - confirmed → 回填凭据 → cancel ctx（overlay 每帧检查 ctx.Err() → 自动关屏）；
//   - Esc 由用户关闭 → AfterClose 调 abort() 停轮询并给出一行提示。
type botsScanScreenState struct {
	mu      sync.Mutex
	host    *LocalConnectorHost
	runner  botsActionRunner
	cfg     connectors.Config
	schema  api.ProviderSchema
	qrKey   string
	loginID string
	payload map[string]any

	ctx       context.Context
	cancel    context.CancelFunc
	polling   bool
	lastPoll  time.Time
	status    string
	refreshes int
	errors    int
	done      bool
	final     string

	body    string
	version int

	// lastSeenVersion 只由 overlay 渲染 goroutine 读写（Refresh 的调用方）。
	lastSeenVersion int
}

func newBotsScanScreenState(host *LocalConnectorHost, runner botsActionRunner, cfg connectors.Config, schema api.ProviderSchema, qrKey string, payload map[string]any) *botsScanScreenState {
	ctx, cancel := context.WithCancel(context.Background())
	state := &botsScanScreenState{
		host: host, runner: runner, cfg: cfg, schema: schema, qrKey: qrKey,
		loginID:         botsPayloadString(payload, "login_id"),
		payload:         payload,
		ctx:             ctx,
		cancel:          cancel,
		status:          "wait",
		lastSeenVersion: -1,
	}
	state.renderBodyLocked()
	return state
}

func (st *botsScanScreenState) title() string {
	return "扫码登录：" + botsConnectorTitle(st.cfg)
}

func (st *botsScanScreenState) snapshot() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.body
}

func (st *botsScanScreenState) isDone() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.done
}

func (st *botsScanScreenState) finalMessage() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.final
}

// renderBodyLocked 依据当前状态重建屏幕正文（调用方持锁）。
func (st *botsScanScreenState) renderBodyLocked() {
	var builder strings.Builder
	builder.WriteString("状态：" + botsScanStatusText(st.status) + "（每 2 秒自动刷新，Esc 取消）\n\n")
	builder.WriteString(botsQRBody(st.cfg, st.payload))
	if st.final != "" {
		builder.WriteString("\n\n" + st.final)
	}
	st.body = builder.String()
	st.version++
}

// refresh 是 overlay 的刷新回调（空闲 tick + r 键）：非阻塞，只做节流与快照。
func (st *botsScanScreenState) refresh(_ ui.DebugOverlayRefreshTrigger) (string, string, bool) {
	st.mu.Lock()
	schedule := !st.done && !st.polling && time.Since(st.lastPoll) >= botsScanPollInterval
	if schedule {
		st.polling = true
		st.lastPoll = time.Now()
	}
	body, version := st.body, st.version
	st.mu.Unlock()
	if schedule {
		go st.pollOnce()
	}
	changed := version != st.lastSeenVersion
	st.lastSeenVersion = version
	return st.title(), body, changed
}

// pollOnce 在一次后台轮询里查询 login_status 并按结果推进状态机。
func (st *botsScanScreenState) pollOnce() {
	defer func() {
		st.mu.Lock()
		st.polling = false
		st.mu.Unlock()
	}()
	payload, err := st.runner.RunAction(st.ctx, st.cfg, botsLoginStatusAction, map[string]any{"login_id": st.loginID})
	if err != nil {
		if st.ctx.Err() != nil { // 用户取消/成功自动关屏：静默退出。
			return
		}
		st.mu.Lock()
		st.errors++
		fail := st.errors >= botsScanMaxErrors
		attempts := st.errors
		st.mu.Unlock()
		if fail {
			st.finish(fmt.Sprintf("扫码登录失败（连续 %d 次轮询错误）：%v", attempts, err))
		}
		return
	}
	status := botsPayloadString(payload, "status")
	st.mu.Lock()
	st.errors = 0
	st.status = status
	switch status {
	case "confirmed":
		credentialReady := botsPayloadBool(payload, "credential_ready")
		st.mu.Unlock()
		if !credentialReady {
			st.finish("已确认登录，等待凭据就绪（可稍后重新运行 /bots）。")
			return
		}
		adopted, patchErr := botsApplyCredentialPatch(st.host, st.cfg, payload)
		if patchErr != nil {
			st.finish("凭据回填失败：" + patchErr.Error())
			return
		}
		if adopted != st.cfg.ID {
			st.finish(fmt.Sprintf("扫码登录成功：识别为既有机器人 %s（同一 owner），已更新其连接为新 bot；原连接由微信解除，临时连接器 %s 已清理。", adopted, st.cfg.ID))
			return
		}
		st.finish(fmt.Sprintf("扫码登录成功：%s 凭据已回填（owner 已加入 allowed_users）。", st.cfg.ID))
		return
	case "expired":
		st.refreshes++
		tooMany := st.refreshes > botsScanMaxRefresh
		st.mu.Unlock()
		if tooMany {
			st.finish("二维码多次过期，已停止（可重新运行 /bots）。")
			return
		}
		refreshed, refreshErr := st.runner.RunAction(st.ctx, st.cfg, st.qrKey, map[string]any{})
		if refreshErr != nil {
			return // 下一拍再试
		}
		st.mu.Lock()
		if newID := botsPayloadString(refreshed, "login_id"); newID != "" {
			st.loginID = newID
		}
		st.payload = refreshed
		st.status = "wait"
		st.renderBodyLocked()
		st.mu.Unlock()
	default:
		st.renderBodyLocked()
		st.mu.Unlock()
	}
}

// finish 结束流程：写入终态、重绘并 cancel ctx（overlay 自动关屏）。
func (st *botsScanScreenState) finish(message string) {
	st.mu.Lock()
	if st.done {
		st.mu.Unlock()
		return
	}
	st.done = true
	st.final = strings.TrimSpace(message)
	st.renderBodyLocked()
	cancel := st.cancel
	st.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// abort 停止后台轮询（用户 Esc 取消时调用）。
func (st *botsScanScreenState) abort() {
	if st.cancel != nil {
		st.cancel()
	}
}

func chatScreenBotsScanSpec(state *botsScanScreenState) chatScreenSpec {
	const hint = "r 刷新状态 · Esc 取消"
	initial := state.snapshot()
	return chatScreenSpec{
		ID:          chatBotsScreenScanID,
		Title:       state.title(),
		Kind:        chatScreenDocument,
		Doc:         textLinesDocument(strings.Split(initial, "\n")),
		RefreshHint: hint,
		Trigger:     "command",
		RunDocument: func(session *ChatSession, lease ui.ScreenLease, _ chatScreenSpec) error {
			err := botsScanScreenRunner(session, lease, state.ctx, ui.DebugOverlayOptions{
				Title:       state.title(),
				Body:        initial,
				Refresh:     state.refresh,
				RefreshHint: hint,
			})
			if state.isDone() {
				// 成功/失败/超时触发的自动关屏（ctx cancel）：不是渲染错误。
				return nil
			}
			return err
		},
		AfterClose: func(session *ChatSession, outcome chatScreenOutcome) {
			if outcome.Degraded {
				// 全屏能力不足：降级为主屏二维码 + 后台轮询 + 补充行。
				state.abort()
				payload := state.payloadSnapshot()
				botsPrintQR(session, state.cfg, payload)
				go botsPollLogin(session, state.host, state.runner, state.cfg, state.schema, state.loginIDSnapshot())
				return
			}
			if message := state.finalMessage(); message != "" {
				printChatCommandOutput(session, message)
				return
			}
			state.abort()
			printChatCommandOutput(session, "已取消扫码（可重新运行 /bots）。")
		},
	}
}

func (st *botsScanScreenState) payloadSnapshot() map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.payload
}

func (st *botsScanScreenState) loginIDSnapshot() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.loginID
}

// botsPollLogin 后台轮询 login_status：状态变化经 ephemeral 补充行写回主屏；
// confirmed 且 credential_ready 时回填 settings/secrets（并把 owner 用户自动
// 加入 allowed_users）；二维码过期自动换码（上限 botsScanMaxRefresh 次）。
// 独立成同步函数便于测试直接调用（生产调用点包在 goroutine 里）。
func botsPollLogin(session *ChatSession, host *LocalConnectorHost, runner botsActionRunner, cfg connectors.Config, schema api.ProviderSchema, loginID string) {
	deadline := time.Now().Add(botsScanDeadline)
	lastStatus := ""
	refreshes := 0
	consecutiveErrors := 0
	for time.Now().Before(deadline) {
		time.Sleep(botsScanPollInterval)
		payload, err := runner.RunAction(context.Background(), cfg, botsLoginStatusAction, map[string]any{"login_id": loginID})
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= botsScanMaxErrors {
				botsSupplement(session, fmt.Sprintf("机器人 %s 登录轮询失败：%v", cfg.ID, err))
				return
			}
			continue
		}
		consecutiveErrors = 0
		status := botsPayloadString(payload, "status")
		if status != lastStatus {
			botsSupplement(session, botsScanStatusLine(cfg.ID, status))
			lastStatus = status
		}
		switch status {
		case "confirmed":
			if !botsPayloadBool(payload, "credential_ready") {
				botsSupplement(session, fmt.Sprintf("机器人 %s 已确认登录，等待凭据就绪（可稍后重新运行 /bots）。", cfg.ID))
				return
			}
			adopted, err := botsApplyCredentialPatch(host, cfg, payload)
			if err != nil {
				botsSupplement(session, fmt.Sprintf("机器人 %s 凭据回填失败：%v（请在 web 页手动填写）。", cfg.ID, err))
				return
			}
			if adopted != cfg.ID {
				botsSupplement(session, fmt.Sprintf("扫码登录成功：识别为既有机器人 %s（同一 owner），已更新其连接为新 bot；原连接由微信解除，临时连接器 %s 已清理。", adopted, cfg.ID))
				return
			}
			botsSupplement(session, fmt.Sprintf("机器人 %s 扫码登录成功：凭据已回填，owner 用户已加入 allowed_users。", cfg.ID))
			return
		case "expired":
			if refreshes >= botsScanMaxRefresh {
				botsSupplement(session, fmt.Sprintf("机器人 %s 二维码多次过期，已停止轮询（可重新运行 /bots）。", cfg.ID))
				return
			}
			refreshes++
			qrKey := botsQRActionKey(schema)
			if qrKey == "" {
				return
			}
			refreshed, rerr := runner.RunAction(context.Background(), cfg, qrKey, map[string]any{})
			if rerr != nil {
				continue
			}
			if newID := botsPayloadString(refreshed, "login_id"); newID != "" {
				loginID = newID
				lastStatus = ""
				botsSupplement(session, botsQRRefreshLine(cfg, refreshed))
			}
		}
	}
	botsSupplement(session, fmt.Sprintf("机器人 %s 扫码登录未完成（超时）。可重新运行 /bots 继续。", cfg.ID))
}

// botsApplyCredentialPatch 把 login_status 返回的 settings/secrets 合并进
// 连接器配置并重载；白名单为空时自动加入 owner 用户（极简且安全：默认只
// 允许扫码者本人与机器人对话）。
//
// 重复扫码识别：confirmed 后 payload.settings.account_id（微信 ilink_bot_id，
// 平台侧稳定机器人标识）若与**另一个既有连接器**（同 provider）的 account_id
// 相同 → 本次扫码是在为一台已接入的机器人取新凭据：凭据合并进既有连接器、
// 自动壳连接器被删除，返回替换后的实际连接器 id。壳转正时补打 CreatedAt。
// 返回值 adoptedID 是凭据最终所在的连接器 id（= cfg.ID 或既有 id）。
func botsApplyCredentialPatch(host *LocalConnectorHost, cfg connectors.Config, payload map[string]any) (string, error) {
	if cfg.Settings == nil {
		cfg.Settings = map[string]string{}
	}
	if cfg.Secrets == nil {
		cfg.Secrets = map[string]string{}
	}
	owner := ""
	if settings, ok := payload["settings"].(map[string]any); ok {
		for key, value := range settings {
			text := botsAnyString(value)
			if text == "" {
				continue
			}
			cfg.Settings[key] = text
			if key == "owner_user_id" {
				owner = text
			}
		}
	}
	if secrets, ok := payload["secrets"].(map[string]any); ok {
		for key, value := range secrets {
			if text := botsAnyString(value); text != "" {
				cfg.Secrets[key] = text
			}
		}
	}
	if owner != "" && len(cfg.Policy.AllowedUsers) == 0 {
		cfg.Policy.AllowedUsers = []string{owner}
	}
	cfg.Enabled = true
	// 重复扫码收敛（共用 connectors.FindSameBotConfig，与 restapi 同语义）：
	// payload.settings.account_id（平台稳定标识）命中另一个既有连接器
	// = 同一台机器人重新扫码取凭据 → 合并进既有、删除壳。
	adoptedID := cfg.ID
	if cfg.BotIdentity() != "" {
		if existing, found := connectors.FindSameBotConfig(host.Store().ListConfigs(), cfg); found {
			existing.Settings = cfg.Settings
			existing.Secrets = cfg.Secrets
			existing.Enabled = true
			if owner != "" && len(existing.Policy.AllowedUsers) == 0 {
				existing.Policy.AllowedUsers = []string{owner}
			}
			existing.UpdatedAt = time.Now().UTC()
			if err := host.Store().PutConfig(existing); err != nil {
				return "", err
			}
			if service := host.Service(); service != nil {
				_ = service.ReloadConnector(context.Background(), existing.ID)
				service.StopConnector(cfg.ID)
			}
			if err := host.Store().DeleteConfig(cfg.ID); err != nil {
				return "", fmt.Errorf("清理壳连接器 %s 失败: %w", cfg.ID, err)
			}
			return existing.ID, nil
		}
	}
	if cfg.CreatedAt.IsZero() {
		cfg.CreatedAt = time.Now().UTC() // 壳转正补戳（旧调用方未打戳时）
	}
	cfg.UpdatedAt = time.Now().UTC()
	if err := host.Store().PutConfig(cfg); err != nil {
		return "", err
	}
	if service := host.Service(); service != nil {
		_ = service.ReloadConnector(context.Background(), cfg.ID)
	}
	return adoptedID, nil
}

// botsPrintQR 把登录载荷渲染到主屏（降级路径：全屏副屏不可用时使用）。
func botsPrintQR(session *ChatSession, cfg connectors.Config, payload map[string]any) {
	printChatCommandOutput(session, "机器人 "+cfg.ID+" 扫码登录：\n"+botsQRBody(cfg, payload))
}

// botsQRBody 渲染二维码正文（不含标题行）：data:image PNG 尽力渲染为终端块
// 字符（近似采样，识别困难时以 web 页兜底）；URL/文本做本地 QR 编码渲染，
// 失败才退回链接原文。
func botsQRBody(cfg connectors.Config, payload map[string]any) string {
	content := botsPayloadString(payload, "qr_code_content")
	var builder strings.Builder
	switch {
	case content == "":
		builder.WriteString("二维码内容为空；可稍后重新运行 /bots。")
	case strings.HasPrefix(content, "data:image/"):
		if blocks, ok := botsRenderQRBlocks(content); ok {
			builder.WriteString(blocks)
			builder.WriteString("\n用手机扫码完成登录（终端为近似渲染；识别困难时请改用 web 页）。")
		} else {
			builder.WriteString("二维码为图片格式，终端无法渲染：请在 aicli chat --web 的「机器人」页扫码，或使用 runtime-server 连接器页。")
		}
	default:
		// 平台通常返回登录链接/短码文本：本地做 QR 编码并在终端渲染
		// （不依赖平台给图）。渲染失败才退回纯链接。
		if blocks, ok := botsRenderQRText(content); ok {
			builder.WriteString(blocks)
			builder.WriteString("\n用手机扫码完成登录；也可打开链接：" + content)
		} else {
			builder.WriteString(content)
			builder.WriteString("\n用手机打开链接完成登录。")
		}
	}
	if expires := botsPayloadString(payload, "expires_at"); expires != "" {
		fmt.Fprintf(&builder, "\n有效期至 %s；过期会自动换新码。", expires)
	}
	return builder.String()
}

// ── 终端二维码渲染（data:image/png;base64 → 半块字符） ─────────────────────

// botsRenderQRBlocks 尽力把 data:image/png;base64 的二维码渲染为终端块字符
// （█/▀/▄/空格）。不做模块识别，只做等分采样——识别困难时调用方回退为
// 链接/指引文案；非 PNG data URI 或解码失败返回 ok=false。
func botsRenderQRBlocks(content string) (string, bool) {
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(content, prefix) {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(content, prefix)))
	if err != nil {
		return "", false
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", false
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < 8 || height < 8 || width > 4096 || height > 4096 {
		return "", false
	}
	step := (width + botsQRBlockWidth - 1) / botsQRBlockWidth
	if step < 1 {
		step = 1
	}
	var builder strings.Builder
	for y := 0; y+step <= height; y += step * 2 { // 每个字符行 = 两个像素行（半块）
		for x := 0; x+step <= width; x += step {
			top := botsSampleDark(img, bounds, x, y, step)
			bottom := y+step < height && botsSampleDark(img, bounds, x, y+step, step)
			builder.WriteString(botsBlockChar(top, bottom))
		}
		builder.WriteString("\n")
	}
	out := builder.String()
	if strings.TrimSpace(out) == "" {
		return "", false
	}
	return out, true
}

// botsRenderQRText 把任意文本（登录链接/短码）编码为 QR 并在终端渲染。
// 使用 skip2/go-qrcode（MIT）的 ToSmallString：半块字符（█▀▄ 空格）、无
// ANSI 码；深色终端下 inverseColor=false 即正确极性（暗模块=终端背景色）。
// 内容过长/编码失败时返回 ok=false，由调用方回退为打印原文链接。
func botsRenderQRText(content string) (string, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", false
	}
	code, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return "", false
	}
	blocks := code.ToSmallString(false)
	if strings.TrimSpace(blocks) == "" {
		return "", false
	}
	return blocks, true
}

// botsSampleDark 判断采样块是否为「暗」（二维码模块）。块内均匀取最多 16 个
// 采样点求平均亮度，比单点采样抗缩放噪声。
func botsSampleDark(img image.Image, bounds image.Rectangle, x, y, step int) bool {
	const samples = 4
	cell := step / samples
	if cell < 1 {
		cell = 1
	}
	var total, count uint64
	for sy := 0; sy < samples; sy++ {
		for sx := 0; sx < samples; sx++ {
			px := x + sx*cell
			py := y + sy*cell
			if px >= bounds.Max.X || py >= bounds.Max.Y {
				continue
			}
			r, g, b, _ := img.At(px, py).RGBA()
			total += uint64(299*r + 587*g + 114*b)
			count++
		}
	}
	if count == 0 {
		return false
	}
	return total/count < 0x8000
}

func botsBlockChar(top, bottom bool) string {
	switch {
	case top && bottom:
		return "█"
	case top:
		return "▀"
	case bottom:
		return "▄"
	default:
		return " "
	}
}

// ── 小工具 ─────────────────────────────────────────────────────────────────

func botsSupplement(session *ChatSession, line string) {
	if session == nil || session.Interaction == nil || strings.TrimSpace(line) == "" {
		return
	}
	session.Interaction.RenderEphemeralSupplement(line)
}

// botsScanStatusText 把通道状态归一化为界面文案（屏幕/补充行共用）。
func botsScanStatusText(status string) string {
	text := map[string]string{
		"wait":      "等待扫码…",
		"scanned":   "已扫码，请在手机上确认…",
		"confirmed": "已确认",
		"expired":   "二维码已过期，正在换新…",
	}[status]
	if text == "" {
		text = status
	}
	return text
}

func botsScanStatusLine(connectorID, status string) string {
	return fmt.Sprintf("机器人 %s：%s", connectorID, botsScanStatusText(status))
}

func botsQRRefreshLine(cfg connectors.Config, payload map[string]any) string {
	content := botsPayloadString(payload, "qr_code_content")
	if content == "" || strings.HasPrefix(content, "data:image/") {
		return fmt.Sprintf("机器人 %s 已换新二维码：请在 web 页查看（终端不渲染图片）。", cfg.ID)
	}
	return fmt.Sprintf("机器人 %s 已换新登录链接：%s", cfg.ID, content)
}

// botsGenerateConnectorID 生成连接器 ID：<provider>-<base36 毫秒时间戳><4 位随机>。
// 满足 restapi 的 id 约束（^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$）。
func botsGenerateConnectorID(provider string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(provider) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
		}
	}
	prefix := builder.String()
	if prefix == "" {
		prefix = "conn"
	}
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	suffix := strconv.FormatInt(time.Now().UnixNano()/int64(time.Millisecond), 36) +
		fmt.Sprintf("%04x", rand.Intn(0x10000))
	return prefix + "-" + suffix
}

func botsQRActionKey(schema api.ProviderSchema) string {
	for _, action := range schema.Actions {
		if action.Kind == botsQRActionKind {
			return action.Key
		}
	}
	return ""
}

func botsProviderSchema(host *LocalConnectorHost, provider string) (api.ProviderSchema, bool) {
	for _, schema := range host.Service().Providers() {
		if schema.Provider == provider {
			return schema, true
		}
	}
	return api.ProviderSchema{}, false
}

func botsActionProvider(host *LocalConnectorHost, providerName string) botsActionRunner {
	provider, ok := host.Service().Provider(providerName)
	if !ok {
		return nil
	}
	runner, ok := provider.(botsActionRunner)
	if !ok {
		return nil
	}
	return runner
}

func botsDefaultMode(schema api.ProviderSchema) string {
	if len(schema.Modes) > 0 {
		return schema.Modes[0]
	}
	return "polling"
}

func botsSortedConfigs(host *LocalConnectorHost) []connectors.Config {
	configs := host.Store().ListConfigs()
	sort.Slice(configs, func(i, j int) bool { return configs[i].ID < configs[j].ID })
	return configs
}

func botsConnectorTitle(cfg connectors.Config) string {
	return firstNonEmptyChatValue(cfg.Name, cfg.ID)
}

func botsConnectorDetail(host *LocalConnectorHost, cfg connectors.Config) string {
	parts := []string{cfg.ID, cfg.Provider}
	if cfg.Mode != "" {
		parts = append(parts, cfg.Mode)
	}
	if cfg.Enabled {
		parts = append(parts, "已启用")
	} else {
		parts = append(parts, "已停用")
	}
	if service := host.Service(); service != nil {
		if stats, ok := service.Stats(cfg.ID); ok {
			parts = append(parts, fmt.Sprintf("入%d/出%d/错%d", stats.Inbound, stats.Outbound, stats.Errors))
		}
	}
	if !cfg.CreatedAt.IsZero() {
		parts = append(parts, "建于 "+cfg.CreatedAt.Local().Format("01-02 15:04"))
	}
	return strings.Join(parts, " · ")
}

func botsInlineDoc(host *LocalConnectorHost) string {
	configs := botsSortedConfigs(host)
	lines := []string{"机器人（连接器）:"}
	if len(configs) == 0 {
		lines = append(lines, "  （暂无；输入 /bots 打开副屏新增）")
	}
	for _, cfg := range configs {
		lines = append(lines, "  - "+botsConnectorTitle(cfg)+" ["+cfg.ID+"] "+botsConnectorDetail(host, cfg))
	}
	return strings.Join(lines, "\n")
}

func botsProvidersInlineDoc(host *LocalConnectorHost) string {
	lines := []string{"选择机器人类型（副屏不可用时请改用 web 页）:"}
	for _, schema := range host.Service().Providers() {
		mark := "字段配置"
		if botsQRActionKey(schema) != "" {
			mark = "扫码接入"
		}
		lines = append(lines, fmt.Sprintf("  - %s [%s] %s", firstNonEmptyChatValue(schema.Label, schema.Provider), schema.Provider, mark))
	}
	return strings.Join(lines, "\n")
}

func botsActionInlineDoc(host *LocalConnectorHost, cfg connectors.Config) string {
	return "机器人 " + botsConnectorTitle(cfg) + " [" + cfg.ID + "]\n" + botsConnectorDetail(host, cfg) +
		"\n（副屏不可用；管理操作请使用 web 页或稍后重试）"
}

func botsPayloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if value, ok := payload[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func botsPayloadBool(payload map[string]any, key string) bool {
	if payload == nil {
		return false
	}
	value, _ := payload[key].(bool)
	return value
}

func botsAnyString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}
