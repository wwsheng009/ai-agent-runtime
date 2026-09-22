package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

// ============================================================================
// TUI 账户命令：两个语义不同、各自独占一块备用屏的顶层命令。
//
//   /account [provider] [show|detect] [--save] [--no-refresh] [--json] [--timeout 15s]
//       → 刷新「当前（或指定）provider」的账户，并显示在单账户屏幕；
//         show = 只读缓存快照（--no-refresh 的同义写法），detect = 只探测站点类型。
//
//   /accounts [refresh|display] [--wait] [--enabled-only] [--json] [--timeout 15s]
//       → 默认提交「全部 provider」的后台刷新并立刻显示缓存快照（见
//         chat_account_async.go）；refresh 只提交，display 只看快照，
//         --wait 才回到「拉完再显示」的阻塞语义。
//
// 两个命令共用 `aicli balance`（NewBalanceCommand）的抓取/换算实现
// （refreshProviderAccountBalance / accountViewFromProviderSnapshot），避免 TUI 与
// CLI 的站点换算逻辑漂移（plan/site-type-account-balance-shared-capability-plan §7.2）。
// 曾经的 `/balance` 与 `/account list` 已移除：刷新当前账户是 /account 的默认行为，
// 刷新全部账户是 /accounts 的默认行为。
//
// 刷新结果只写回「当前生效 provider」的会话快照，并把它作为周期性刷新的新目标，
// 防止后台周期刷新用旧快照覆盖刚拉到的实时余额；`--save` 才写 config.yaml。
// 屏幕渲染用的快照在进入备用屏之前捕获，备用屏只渲染、不再发起 I/O。
// ============================================================================

const (
	// chatAccountCommandUsage 是 /account 的稳定用法文本（子命令与参数错误共用）。
	chatAccountCommandUsage = "用法: /account [provider] [show|detect] [--save] [--no-refresh] [--json] [--timeout 15s]"
	// chatAccountsCommandUsage 是 /accounts 的稳定用法文本。
	chatAccountsCommandUsage = "用法: /accounts [refresh|display] [--wait] [--enabled-only] [--no-refresh] [--json] [--timeout 15s]"
	// chatAccountTimeoutFlagUsage 说明 --timeout 的取值。
	chatAccountTimeoutFlagUsage = "错误: --timeout 需要时长参数（如 15s）\n" + chatAccountCommandUsage
)

type chatAccountCommandMode string

const (
	// chatAccountModeRefresh 是 /account 的默认模式：实时拉取当前账户再显示。
	chatAccountModeRefresh chatAccountCommandMode = "refresh"
	// chatAccountModeShow 只读缓存快照，不发起任何网络请求。
	chatAccountModeShow chatAccountCommandMode = "show"
	// chatAccountModeDetect 只探测站点类型，不取余额。
	chatAccountModeDetect chatAccountCommandMode = "detect"
	// chatAccountModeList 是 /accounts 的默认模式：提交后台刷新 + 立刻渲染缓存快照。
	chatAccountModeList chatAccountCommandMode = "list"
	// chatAccountModeListRefresh 只提交后台刷新（/accounts refresh），不打开屏幕。
	chatAccountModeListRefresh chatAccountCommandMode = "list-refresh"
	// chatAccountModeListDisplay 只渲染缓存快照（/accounts display），零网络。
	chatAccountModeListDisplay chatAccountCommandMode = "list-display"
)

// chatAccountCommandVariant 区分两个顶层命令：/accounts 面向全部 provider，
// /account 面向单个（默认当前）provider。
type chatAccountCommandVariant string

const (
	chatAccountVariantCurrent chatAccountCommandVariant = "account"
	chatAccountVariantAll     chatAccountCommandVariant = "accounts"
)

type chatAccountCommandRequest struct {
	Variant     chatAccountCommandVariant
	Mode        chatAccountCommandMode
	Provider    string
	Save        bool
	NoRefresh   bool
	EnabledOnly bool
	JSON        bool
	Timeout     time.Duration
	// Wait 让 /accounts 回到阻塞语义（拉完再渲染）；--json 默认隐含 Wait，因为
	// 脚本要的是完整数据而不是任务句柄（display/--no-refresh 时只序列化缓存）。
	Wait bool
}

// chatAccountReport 是 /account show|refresh|detect 的结构化投影：同一份数据既
// 驱动人类可读文本，也驱动 --json（供脚本使用）。
type chatAccountReport struct {
	Provider    string                    `json:"provider"`
	Status      string                    `json:"status"`
	SiteType    string                    `json:"site_type,omitempty"`
	Confidence  string                    `json:"confidence,omitempty"`
	Protocol    string                    `json:"protocol,omitempty"`
	BaseURL     string                    `json:"base_url,omitempty"`
	Source      string                    `json:"source,omitempty"`
	FetchedAt   string                    `json:"fetched_at,omitempty"`
	BalanceLine string                    `json:"balance_line,omitempty"`
	Account     *siteaccount.AccountView  `json:"account,omitempty"`
	Hits        []siteaccount.EndpointHit `json:"detection_hits,omitempty"`
	Saved       bool                      `json:"saved,omitempty"`
	Warning     string                    `json:"warning,omitempty"`
	Error       string                    `json:"error,omitempty"`
}

// chatAccountListReport 是 /account list 的结构化投影。
type chatAccountListReport struct {
	Total       int  `json:"total"`
	WithAccount int  `json:"with_account"`
	Refreshed   bool `json:"refreshed,omitempty"`
	// RefreshState/RefreshDetail 是后台刷新状态行（进行中/已刷新/仅缓存），渲染
	// 在表格上方；空值表示不显示状态行。
	RefreshState  string              `json:"refresh_state,omitempty"`
	RefreshDetail string              `json:"refresh_detail,omitempty"`
	Providers     []chatAccountReport `json:"providers"`
}

// executeStructuredChatAccountCommand renders the account surface through the
// structured command channel: /accounts keeps the whole-configuration overview
// (live by default), /account keeps the single-provider surface (live by
// default). Every branch — including argument and provider-resolution errors —
// stays inside a CommandResult, so the unified command gate never observes a
// fall-through to a legacy writer.
func executeStructuredChatAccountCommand(session *ChatSession, command string) CommandResult {
	req, errText := parseChatAccountCommand(command)
	if errText != "" {
		return commandTextResult(errText)
	}
	switch req.Mode {
	case chatAccountModeList, chatAccountModeListRefresh, chatAccountModeListDisplay:
		return chatAccountsResult(session, req)
	case chatAccountModeDetect:
		return chatAccountDetectResult(session, req)
	case chatAccountModeShow:
		return chatAccountShowResult(session, req)
	default:
		return chatAccountRefreshResult(session, req)
	}
}

// chatAccountVariantForCommand 判断这次调用来自 /accounts 还是 /account。
// commandMatches 要求命令名后紧跟分隔符，两个名字不互相前缀匹配，因此顺序无关。
func chatAccountVariantForCommand(command string) chatAccountCommandVariant {
	if commandMatches(command, "/accounts") {
		return chatAccountVariantAll
	}
	return chatAccountVariantCurrent
}

// parseChatAccountCommand 把两个命令之后的参数词解析为命令请求。错误文本为空
// 表示成功；文本投影与结构化投影共用同一解析，避免参数语义漂移。
func parseChatAccountCommand(command string) (chatAccountCommandRequest, string) {
	req := chatAccountCommandRequest{
		Variant: chatAccountVariantForCommand(command),
		Mode:    chatAccountModeRefresh,
		Timeout: chatAccountBalanceRefreshTimeout,
	}
	argsText := extractCommandArgument(command)
	if req.Variant == chatAccountVariantAll {
		req.Mode = chatAccountModeList
		return parseChatAccountsCommandArgs(req, argsText)
	}
	return parseChatAccountCommandArgs(req, argsText)
}

// parseChatAccountCommandArgs 解析 /account：可选的子命令（show/status、
// detect/site，以及默认行为的兼容别名 refresh/sync）或 provider 位置参数，加标志。
func parseChatAccountCommandArgs(req chatAccountCommandRequest, argsText string) (chatAccountCommandRequest, string) {
	tokens := splitChatCommandFields(argsText)
	if len(tokens) > 0 && !strings.HasPrefix(tokens[0], "-") {
		if hint := chatAccountRemovedSubcommandHint(strings.ToLower(tokens[0])); hint != "" {
			return req, hint
		}
		switch strings.ToLower(tokens[0]) {
		case "show", "status":
			req.Mode = chatAccountModeShow
			tokens = tokens[1:]
		case "detect", "site":
			req.Mode = chatAccountModeDetect
			tokens = tokens[1:]
		case "refresh", "sync":
			// 默认即刷新：保留为兼容别名，不再单独宣传。
			req.Mode = chatAccountModeRefresh
			tokens = tokens[1:]
		}
	}

	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		switch {
		case token == "--json":
			req.JSON = true
		case token == "--save":
			req.Save = true
		case token == "--no-refresh":
			req.NoRefresh = true
		case token == "--timeout":
			if i+1 >= len(tokens) {
				return req, chatAccountTimeoutFlagUsage
			}
			i++
			timeout, err := time.ParseDuration(tokens[i])
			if err != nil || timeout <= 0 {
				return req, fmt.Sprintf("错误: --timeout 非法: %q\n%s", tokens[i], chatAccountCommandUsage)
			}
			req.Timeout = timeout
		case strings.HasPrefix(token, "--timeout="):
			value := strings.TrimPrefix(token, "--timeout=")
			timeout, err := time.ParseDuration(value)
			if err != nil || timeout <= 0 {
				return req, fmt.Sprintf("错误: --timeout 非法: %q\n%s", value, chatAccountCommandUsage)
			}
			req.Timeout = timeout
		case strings.HasPrefix(token, "-"):
			return req, fmt.Sprintf("错误: 未知选项 %q\n%s", token, chatAccountCommandUsage)
		default:
			if req.Provider != "" {
				return req, fmt.Sprintf("错误: 多余参数 %q\n%s", token, chatAccountCommandUsage)
			}
			if hint := chatAccountRemovedSubcommandHint(strings.ToLower(token)); hint != "" {
				return req, hint
			}
			if _, ok := chatAccountSubcommandModeForToken(strings.ToLower(token)); ok {
				// 子命令必须写在标志之前，否则会被当成 provider 名。
				return req, fmt.Sprintf("错误: 子命令 %q 必须放在标志之前\n%s", token, chatAccountCommandUsage)
			}
			req.Provider = token
		}
	}
	return normalizeChatAccountRequest(req)
}

// parseChatAccountsCommandArgs 解析 /accounts：可选的子命令（refresh/sync/reload
// 只提交后台刷新；display/show/status/cache 只看缓存快照）加标志。位置参数里的
// provider 名一律拒绝并指向 /account，避免两个命令的语义再次重叠。
func parseChatAccountsCommandArgs(req chatAccountCommandRequest, argsText string) (chatAccountCommandRequest, string) {
	tokens := splitChatCommandFields(argsText)
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		switch {
		case token == "--json":
			req.JSON = true
		case token == "--wait":
			req.Wait = true
		case token == "--enabled-only":
			req.EnabledOnly = true
		case token == "--no-refresh":
			req.NoRefresh = true
		case token == "--save":
			return req, "错误: --save 只适用于 /account（逐个 provider 刷新后写回）\n" + chatAccountsCommandUsage
		case token == "--timeout":
			if i+1 >= len(tokens) {
				return req, "错误: --timeout 需要时长参数（如 15s）\n" + chatAccountsCommandUsage
			}
			i++
			timeout, err := time.ParseDuration(tokens[i])
			if err != nil || timeout <= 0 {
				return req, fmt.Sprintf("错误: --timeout 非法: %q\n%s", tokens[i], chatAccountsCommandUsage)
			}
			req.Timeout = timeout
		case strings.HasPrefix(token, "--timeout="):
			value := strings.TrimPrefix(token, "--timeout=")
			timeout, err := time.ParseDuration(value)
			if err != nil || timeout <= 0 {
				return req, fmt.Sprintf("错误: --timeout 非法: %q\n%s", value, chatAccountsCommandUsage)
			}
			req.Timeout = timeout
		case strings.HasPrefix(token, "-"):
			return req, fmt.Sprintf("错误: 未知选项 %q\n%s", token, chatAccountsCommandUsage)
		default:
			mode, ok := chatAccountsSubcommandModeForToken(strings.ToLower(token))
			if !ok {
				return req, fmt.Sprintf("错误: /accounts 不接受位置参数 %q（单个 provider 请用 /account %s）\n%s",
					token, token, chatAccountsCommandUsage)
			}
			if i != 0 {
				// 子命令必须写在标志之前，否则「先标志后子命令」会被静默当成标志
				// 解析，与 /account 的规则保持一致。
				return req, fmt.Sprintf("错误: 子命令 %q 必须放在标志之前\n%s", token, chatAccountsCommandUsage)
			}
			req.Mode = mode
		}
	}
	if req.JSON && !req.NoRefresh && req.Mode != chatAccountModeListDisplay {
		// JSON 是脚本契约：默认要等到刷新完成才有完整表格可序列化；但
		// display/--no-refresh 表示只序列化缓存快照，不能借 --json 拉网络。
		req.Wait = true
	}
	return normalizeChatAccountsRequest(req)
}

// chatAccountsSubcommandModeForToken 把 /accounts 的子命令词（含别名）映射为模式。
func chatAccountsSubcommandModeForToken(token string) (chatAccountCommandMode, bool) {
	switch token {
	case "refresh", "sync", "reload":
		return chatAccountModeListRefresh, true
	case "display", "show", "status", "cache":
		return chatAccountModeListDisplay, true
	}
	return "", false
}

// normalizeChatAccountsRequest 归一化 /accounts 的子命令与标志组合，拒绝互相矛盾
// 的组合（静默忽略会让「我到底刷没刷新」变得不可推断）。
func normalizeChatAccountsRequest(req chatAccountCommandRequest) (chatAccountCommandRequest, string) {
	if req.NoRefresh && req.Wait {
		return req, "错误: --no-refresh 与 --wait 不能同时使用（前者不刷新，后者等待刷新）\n" + chatAccountsCommandUsage
	}
	if req.NoRefresh {
		if req.Mode == chatAccountModeListRefresh {
			return req, "错误: refresh 与 --no-refresh 不能同时使用\n" + chatAccountsCommandUsage
		}
		req.Mode = chatAccountModeListDisplay
	}
	if req.Wait && req.Mode == chatAccountModeListRefresh {
		// refresh --wait：等本次刷新结束并把结果画出来，等价于 --wait。
		req.Mode = chatAccountModeList
	}
	return req, ""
}

// chatAccountRemovedSubcommandHint 为已经移出 /account 的子命令词返回迁移提示：
// 全部账户视图独立成 /accounts 之后，`/account list` 不再有对应实现。
func chatAccountRemovedSubcommandHint(token string) string {
	switch token {
	case "list", "ls":
		return "错误: /account list 已移除（全部账户请用 /accounts）\n" + chatAccountsCommandUsage
	}
	return ""
}

// normalizeChatAccountRequest 归一化 /account 的标志组合并拒绝矛盾组合，避免
// 「静默忽略某个标志」的语义漂移：--no-refresh 与 show 是同一模式。
func normalizeChatAccountRequest(req chatAccountCommandRequest) (chatAccountCommandRequest, string) {
	if req.Save && req.NoRefresh {
		return req, "错误: --save 与 --no-refresh 不能同时使用\n" + chatAccountCommandUsage
	}
	if req.Save && req.Mode != chatAccountModeRefresh {
		return req, "错误: --save 只适用于实时刷新（/account 的默认行为）\n" + chatAccountCommandUsage
	}
	if req.NoRefresh {
		if req.Mode == chatAccountModeDetect {
			return req, "错误: --no-refresh 只适用于刷新（/account 的默认行为）\n" + chatAccountCommandUsage
		}
		req.Mode = chatAccountModeShow
	}
	return req, ""
}

// resolveChatAccountProvider 解析目标 provider：缺省用当前会话生效 provider（含
// 会话内实时快照），显式名称优先命中会话快照，其次 config。
func resolveChatAccountProvider(session *ChatSession, name string) (string, config.Provider, string) {
	if session == nil {
		return "", config.Provider{}, "错误: 当前没有活动会话"
	}
	requested := strings.TrimSpace(name)
	if requested == "" {
		snapshotName, provider, ok := session.accountBalanceSnapshot()
		if ok && strings.TrimSpace(snapshotName) != "" {
			return strings.TrimSpace(snapshotName), provider, ""
		}
		requested = strings.TrimSpace(session.ProviderName)
		if requested == "" {
			return "", config.Provider{}, "错误: 当前会话没有生效的 provider\n" + chatAccountCommandUsage
		}
		return requested, session.Provider, ""
	}

	if strings.EqualFold(requested, strings.TrimSpace(session.ProviderName)) {
		if snapshotName, provider, ok := session.accountBalanceSnapshot(); ok && strings.EqualFold(snapshotName, requested) {
			return strings.TrimSpace(snapshotName), provider, ""
		}
		return strings.TrimSpace(session.ProviderName), session.Provider, ""
	}
	if session.Config != nil {
		if provider, ok := session.Config.Providers.Items[requested]; ok {
			return requested, provider, ""
		}
		names := make([]string, 0, len(session.Config.Providers.Items))
		for candidate := range session.Config.Providers.Items {
			names = append(names, candidate)
		}
		sort.Strings(names)
		for _, candidate := range names {
			if strings.EqualFold(candidate, requested) {
				return candidate, session.Config.Providers.Items[candidate], ""
			}
		}
	}
	return "", config.Provider{}, fmt.Sprintf("错误: 未找到 provider %q\n提示: /accounts 可列出全部 provider", requested)
}

// buildChatAccountReport 由 provider 快照构造报告（不触发任何网络 I/O）。
func buildChatAccountReport(name string, provider config.Provider) chatAccountReport {
	siteType := strings.TrimSpace(provider.SiteType)
	confidence := strings.TrimSpace(provider.SiteTypeConfidence)
	report := chatAccountReport{
		Provider:   name,
		Status:     "no_account",
		SiteType:   siteType,
		Confidence: confidence,
		Protocol:   provider.GetProtocol(),
		BaseURL:    strings.TrimSpace(provider.BaseURL),
	}
	if provider.Account == nil {
		if siteType != "" && !strings.EqualFold(siteType, string(siteaccount.SiteTypeUnknown)) {
			report.Warning = "无缓存账户数据；运行 /account refresh 实时拉取"
		}
		return report
	}
	report.Status = "cached"
	report.Source = strings.TrimSpace(provider.Account.Source)
	report.FetchedAt = strings.TrimSpace(provider.Account.FetchedAt)
	report.BalanceLine = formatProviderAccountBalanceLine(provider.Account, siteType, confidence)
	if report.BalanceLine == "" {
		report.Status = "cached_empty"
	}
	if errText := strings.TrimSpace(provider.Account.LastError); errText != "" {
		report.Warning = errText
	}
	view := accountViewFromProviderSnapshot(provider.Account, siteType, confidence)
	report.Account = &view
	return report
}

// chatAccountViewResult routes one single-account report to the dedicated
// /account screen on a unified interactive TTY, and to the plain document cell
// in every other projection (JSON / noninteractive / degraded TTY). The report
// is captured before the screen is entered, so the viewer performs no I/O and
// reads no mutable session state.
func chatAccountViewResult(session *ChatSession, req chatAccountCommandRequest, report chatAccountReport) CommandResult {
	if req.JSON {
		return chatAccountJSONResult(report)
	}
	if unifiedDirectInteractiveOutput(session) {
		return CommandResult{Action: CommandContinue, OpenAccountScreen: &AccountScreenRequest{Report: report}}
	}
	return commandTextResult(strings.Join(formatChatAccountReportLines(report), "\n"))
}

// chatAccountsViewResult is the /accounts counterpart: the provider table goes
// to the dedicated all-accounts screen, or to the document cell when the
// alternate screen cannot be hosted.
func chatAccountsViewResult(session *ChatSession, req chatAccountCommandRequest, list chatAccountListReport) CommandResult {
	if req.JSON {
		return chatAccountJSONResult(list)
	}
	if unifiedDirectInteractiveOutput(session) {
		return CommandResult{Action: CommandContinue, OpenAccountsScreen: &AccountListScreenRequest{
			List: list,
			// 冻结「打开屏幕那一刻」的刷新参数：屏内 r 重新提交后台刷新时复用
			// 它们（含 --enabled-only 过滤与单 provider 超时），屏幕因此不需要
			// 读取任何可变的请求状态。
			Refresh: chatAccountRefreshParams{EnabledOnly: req.EnabledOnly, Timeout: req.Timeout},
		}}
	}
	return commandTextResult(strings.Join(formatChatAccountListLines(list), "\n"))
}

// chatAccountShowResult renders the cached snapshot for one provider (no
// network I/O) on the /account screen.
func chatAccountShowResult(session *ChatSession, req chatAccountCommandRequest) CommandResult {
	name, provider, errText := resolveChatAccountProvider(session, req.Provider)
	if errText != "" {
		return commandTextResult(errText)
	}
	return chatAccountViewResult(session, req, buildChatAccountReport(name, provider))
}

// chatAccountRefreshResult performs one live fetch and shows the fresh snapshot
// on the /account screen. The snapshot is applied to the session (status line +
// periodic-refresh target) whenever the provider is the active one; only --save
// writes config.yaml. A failed fetch stays in the transcript as a document cell
// (with the cached snapshot) instead of taking over the screen.
func chatAccountRefreshResult(session *ChatSession, req chatAccountCommandRequest) CommandResult {
	name, provider, errText := resolveChatAccountProvider(session, req.Provider)
	if errText != "" {
		return commandTextResult(errText)
	}

	client := siteaccount.NewClient(nil)
	outcome, refreshErr := refreshProviderAccountBalance(context.Background(), client, name, &provider, req.Timeout)
	report := chatAccountReport{
		Provider:   name,
		Status:     firstNonEmptyText(outcome.Status, "refresh_error"),
		SiteType:   firstNonEmptyText(strings.TrimSpace(outcome.SiteType), strings.TrimSpace(provider.SiteType)),
		Confidence: firstNonEmptyText(strings.TrimSpace(outcome.SiteTypeConfidence), strings.TrimSpace(provider.SiteTypeConfidence)),
		Protocol:   provider.GetProtocol(),
		BaseURL:    strings.TrimSpace(provider.BaseURL),
		Warning:    strings.TrimSpace(outcome.Warning),
	}
	if refreshErr != nil {
		report.Status = firstNonEmptyText(strings.TrimSpace(outcome.Status), "refresh_error")
		report.Error = refreshErr.Error()
		if report.Warning == "" {
			report.Warning = refreshErr.Error()
		}
		// Fall back to the cached snapshot so the command still reports what is
		// currently used by /status.
		if cached := buildChatAccountReport(name, provider); cached.Account != nil {
			report.Account = cached.Account
			report.BalanceLine = cached.BalanceLine
			report.Source = cached.Source
			report.FetchedAt = cached.FetchedAt
		}
		if req.JSON {
			return chatAccountJSONResult(report)
		}
		lines := formatChatAccountReportLines(report)
		lines = append(lines, "提示: 本次实时刷新失败，以上为缓存快照（/account --no-refresh 可跳过刷新）")
		return commandTextResult(strings.Join(lines, "\n"))
	}

	applyLiveBalanceOutcome(&provider, outcome)
	if outcome.Account != nil {
		report.Account = outcome.AccountView
		report.BalanceLine = formatProviderAccountBalanceLine(provider.Account, provider.SiteType, provider.SiteTypeConfidence)
		report.Source = strings.TrimSpace(provider.Account.Source)
		report.FetchedAt = strings.TrimSpace(provider.Account.FetchedAt)
	}
	applyChatAccountProviderSnapshot(session, name, provider, true)

	if req.Save {
		if saveErr := saveChatAccountProviderSnapshot(session, name, provider); saveErr != nil {
			report.Warning = firstNonEmptyText(report.Warning, saveErr.Error())
		} else {
			report.Saved = true
		}
	}

	return chatAccountViewResult(session, req, report)
}

// chatAccountDetectResult probes only the site type (no balance fetch) and
// records the detection metadata on the session snapshot, so a later
// /account refresh or the periodic refresher can skip a redundant probe.
func chatAccountDetectResult(session *ChatSession, req chatAccountCommandRequest) CommandResult {
	name, provider, errText := resolveChatAccountProvider(session, req.Provider)
	if errText != "" {
		return commandTextResult(errText)
	}
	report := chatAccountReport{
		Provider: name,
		Status:   "detect_error",
		Protocol: provider.GetProtocol(),
		BaseURL:  strings.TrimSpace(provider.BaseURL),
	}
	client := siteaccount.NewClient(nil)
	result, detectErr := client.DetectSiteType(context.Background(), siteaccount.DetectInput{
		BaseURL: provider.BaseURL,
		Timeout: req.Timeout,
	})
	if detectErr != nil {
		report.Error = detectErr.Error()
		report.Warning = detectErr.Error()
		if req.JSON {
			return chatAccountJSONResult(report)
		}
		return commandTextResult(strings.Join(formatChatAccountReportLines(report), "\n"))
	}

	report.Status = "detected"
	report.SiteType = string(result.SiteType)
	report.Confidence = string(result.Confidence)
	report.Hits = result.Hits
	if !result.DetectedAt.IsZero() {
		report.FetchedAt = result.DetectedAt.UTC().Format(time.RFC3339)
	}

	provider.SiteType = report.SiteType
	provider.SiteTypeConfidence = report.Confidence
	if report.FetchedAt != "" {
		provider.SiteTypeDetectedAt = report.FetchedAt
	}
	// Detection alone must not wake the periodic refresher: it only records the
	// site type, so a provider that is known to be unsupported stays untouched
	// until the user explicitly refreshes.
	applyChatAccountProviderSnapshot(session, name, provider, false)

	return chatAccountViewResult(session, req, report)
}

// chatAccountsResult renders the all-provider surface on the /accounts screen.
//
// The default path is asynchronous: it submits one background refresh and renders
// the cached snapshot immediately, so N providers no longer block the REPL for up
// to N × --timeout (see chat_account_async.go). `display` (alias --no-refresh)
// never touches the network, `refresh` only submits, and `--wait` restores the
// old "fetch everything, then render" semantics. Per-provider failures stay in
// the table as warning/error rows instead of failing the whole command.
func chatAccountsResult(session *ChatSession, req chatAccountCommandRequest) CommandResult {
	if session == nil {
		return commandTextResult("错误: 当前没有活动会话")
	}
	if session.Config == nil || len(session.Config.Providers.Items) == 0 {
		return commandTextResult("错误: 当前配置没有可用的 provider")
	}

	switch req.Mode {
	case chatAccountModeListRefresh:
		return chatAccountsSubmitResult(session, req)
	case chatAccountModeListDisplay:
		return chatAccountsCachedResult(session, req)
	}
	if !req.Wait && (session.NoInteractive || session.JSONOutput) {
		// 非交互调用（脚本、管道、JSON 输出）没有「稍后再 display」的机会：
		// 保持旧的阻塞语义，避免脚本读到「刚提交刷新、值还是旧快照」的数据。
		req.Wait = true
	}
	if req.Wait {
		return chatAccountsBlockingResult(session, req)
	}
	return chatAccountsAsyncResult(session, req)
}

// chatAccountsSubmitResult 只提交后台刷新（/accounts refresh）：不打开备用屏，
// 因为提交动作本身没有新数据可看。
func chatAccountsSubmitResult(session *ChatSession, req chatAccountCommandRequest) CommandResult {
	job, started, errText := submitChatAccountsRefresh(session, req)
	if errText != "" {
		return commandTextResult(errText)
	}
	state := job.snapshotState()
	if !started {
		return commandTextResult(fmt.Sprintf("刷新已在进行中（%s，%d provider）\n用 /accounts display 查看当前快照",
			formatChatAccountsDuration(time.Since(state.SubmittedAt)), state.Total))
	}
	return commandTextResult(strings.Join([]string{
		fmt.Sprintf("已提交全部 provider 的后台刷新（%d provider，单个超时 %s）", state.Total, job.timeout),
		"用 /accounts display 查看快照；/accounts --wait 可阻塞等待本次结果",
	}, "\n"))
}

// chatAccountsAsyncResult 是 /accounts 的默认路径：提交后台刷新后立刻渲染缓存
// 快照（含进行中状态行），命令不再等待网络。
func chatAccountsAsyncResult(session *ChatSession, req chatAccountCommandRequest) CommandResult {
	_, _, errText := submitChatAccountsRefresh(session, req)
	if errText != "" {
		return commandTextResult(errText)
	}
	return chatAccountsCachedResult(session, req)
}

// chatAccountsCachedResult 渲染缓存快照（零网络）：/accounts 默认路径与
// /accounts display 共用同一渲染，只在状态行上区分「后台刷新中」与否。
func chatAccountsCachedResult(session *ChatSession, req chatAccountCommandRequest) CommandResult {
	return chatAccountsViewResult(session, req, chatAccountsCachedReport(session, chatAccountProviderItems(session), req))
}

// chatAccountsCachedReport 组装 /accounts 的「缓存快照」报告（零网络）：表格 +
// 刷新状态行。命令路径、/accounts display 与屏内刷新共用这一条组装，因此三处的
// 状态行永远同源。
//
// items 是 provider 的值快照：命令路径传 config 的即时副本；备用屏传开屏前冻结的
// 副本，屏内刷新因此不再读取可变的 Providers map（见 chatAccountsScreenRefresher）。
func chatAccountsCachedReport(session *ChatSession, items map[string]config.Provider, req chatAccountCommandRequest) chatAccountListReport {
	list := buildChatAccountsListReport(session, items, req, false)
	_, state := session.accountListSnapshot()
	list.RefreshState, list.RefreshDetail = chatAccountsRefreshStateLabel(state, time.Now())
	return list
}

// chatAccountsBlockingResult 是 /accounts --wait 的阻塞语义（也是 --json 的隐含
// 行为）：逐个 provider 拉完再渲染，结果同样写入会话缓存供后续 display 复用。
func chatAccountsBlockingResult(session *ChatSession, req chatAccountCommandRequest) CommandResult {
	targets := chatAccountsRefreshTargets(session, req.EnabledOnly)
	if len(targets) == 0 {
		return commandTextResult(chatAccountsNoRefreshTargetsError)
	}
	client := siteaccount.NewClient(nil)
	refresh := session.accountListRefresh
	if refresh == nil {
		refresh = refreshProviderAccountBalance
	}
	reports := make([]chatAccountReport, 0, len(targets))
	results := make(map[string]config.Provider, len(targets))
	ok, failed := 0, 0
	for _, target := range targets {
		provider := target.Provider
		outcome, refreshErr := refresh(context.Background(), client, target.Name, &provider, req.Timeout)
		applyDetectedSiteTypeOutcome(&provider, outcome)
		report := buildChatAccountReport(target.Name, provider)
		if refreshErr != nil {
			failed++
			report.Status = firstNonEmptyText(strings.TrimSpace(outcome.Status), "refresh_error")
			report.Error = refreshErr.Error()
			report.Warning = firstNonEmptyText(strings.TrimSpace(outcome.Warning), refreshErr.Error())
			reports = append(reports, report)
			results[target.Name] = provider
			continue
		}
		ok++
		applyLiveBalanceOutcome(&provider, outcome)
		report = buildChatAccountReport(target.Name, provider)
		report.Status = firstNonEmptyText(strings.TrimSpace(outcome.Status), report.Status)
		report.Warning = firstNonEmptyText(strings.TrimSpace(outcome.Warning), report.Warning)
		reports = append(reports, report)
		results[target.Name] = provider
	}
	// 阻塞路径的结果与异步路径同源：写入会话缓存并（对当前生效 provider）刷新
	// 状态行，这样紧接着的 /accounts display 不会回退到旧快照。
	session.publishChatAccountsRefreshResults(results)

	list := chatAccountListReport{Total: len(reports), Refreshed: true, Providers: reports}
	for _, report := range reports {
		if report.Account != nil || report.BalanceLine != "" {
			list.WithAccount++
		}
	}
	list.RefreshState = "已刷新"
	list.RefreshDetail = fmt.Sprintf("成功 %d，失败 %d", ok, failed)
	return chatAccountsViewResult(session, req, list)
}

// chatAccountProviderItems 拷出 config 里的 provider 值副本，供需要在 actor 之外
// 读取的调用方使用：命令路径读完即用，备用屏把它冻结到开屏那一刻（渲染循环因此
// 不再触碰可变的 Providers map）。
func chatAccountProviderItems(session *ChatSession) map[string]config.Provider {
	if session == nil || session.Config == nil {
		return nil
	}
	items := make(map[string]config.Provider, len(session.Config.Providers.Items))
	for name, provider := range session.Config.Providers.Items {
		items[name] = provider
	}
	return items
}

// buildChatAccountsListReport 用给定的 provider 快照组装 /accounts 表格（零网络）：
// 优先使用会话内的后台刷新缓存，其次当前生效 provider 的实时快照，最后回落到
// items 里的 config 快照。
func buildChatAccountsListReport(session *ChatSession, items map[string]config.Provider, req chatAccountCommandRequest, refreshed bool) chatAccountListReport {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)

	cached, _ := session.accountListSnapshot()
	snapshotName, live, hasLive := session.accountBalanceSnapshot()
	reports := make([]chatAccountReport, 0, len(names))
	for _, name := range names {
		provider := items[name]
		if req.EnabledOnly && !provider.Enabled {
			continue
		}
		if cachedProvider, ok := cached[name]; ok {
			provider = cachedProvider
		} else if hasLive && strings.EqualFold(strings.TrimSpace(snapshotName), name) {
			// The active provider may hold a newer live snapshot than config.
			provider = live
		}
		reports = append(reports, buildChatAccountReport(name, provider))
	}

	list := chatAccountListReport{Total: len(reports), Refreshed: refreshed, Providers: reports}
	for _, report := range reports {
		if report.Account != nil || report.BalanceLine != "" {
			list.WithAccount++
		}
	}
	return list
}

// applyLiveBalanceOutcome copies the live fetch result onto the provider copy
// (same field set as `aicli balance --refresh`).
func applyLiveBalanceOutcome(provider *config.Provider, outcome liveBalanceOutcome) {
	if provider == nil || outcome.Account == nil {
		return
	}
	provider.Account = cloneProviderAccountSnapshot(outcome.Account)
	if outcome.SiteType != "" {
		provider.SiteType = outcome.SiteType
	}
	if outcome.SiteTypeConfidence != "" {
		provider.SiteTypeConfidence = outcome.SiteTypeConfidence
	}
	if outcome.SiteTypeDetectedAt != "" {
		provider.SiteTypeDetectedAt = outcome.SiteTypeDetectedAt
	}
	if outcome.AccountAuthRef != "" {
		provider.AccountAuthRef = outcome.AccountAuthRef
	}
}

// applyChatAccountProviderSnapshot publishes a provider snapshot to the session
// when it is the active provider, then optionally wakes the periodic refresher
// so it continues from the fresh state instead of overwriting it.
func applyChatAccountProviderSnapshot(session *ChatSession, name string, provider config.Provider, wake bool) {
	if session == nil {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(session.ProviderName), strings.TrimSpace(name)) {
		// 只有当前生效 provider 才写入会话快照：其它 provider 的结果只留在
		// 命令输出里，避免状态行与 /model 显示错位。
		return
	}
	if wake {
		// 复用 /model 切换同款发布路径：它保留同 provider 已有的实时快照，并把
		// 新目标交给周期刷新器，这样后台刷新不会用旧快照覆盖刚拉到的余额。
		updateChatAccountBalanceProvider(session, name, provider)
		return
	}
	// detect-only：只记录站点类型元数据，刻意不唤醒周期刷新器，未支持的站点
	// 不会被每轮刷新反复重试。
	session.setAccountBalanceProvider(name, provider)
	if session.Interaction != nil {
		session.Interaction.RefreshAccountBalanceStatus()
	}
}

// saveChatAccountProviderSnapshot persists a freshly refreshed account to
// config.yaml (the /account refresh --save path).
func saveChatAccountProviderSnapshot(session *ChatSession, name string, provider config.Provider) error {
	if session == nil || session.Config == nil {
		return fmt.Errorf("配置不可用，无法保存账户快照")
	}
	configPath := strings.TrimSpace(session.Config.ConfigFilePath)
	if configPath == "" {
		return fmt.Errorf("未找到 config 路径，无法保存账户快照")
	}
	update := config.ProviderConfigUpdate{
		Name:    name,
		Account: cloneProviderAccountSnapshot(provider.Account),
	}
	if siteType := strings.TrimSpace(provider.SiteType); siteType != "" {
		update.SiteType = providerLoginStringValuePtr(siteType)
	}
	if confidence := strings.TrimSpace(provider.SiteTypeConfidence); confidence != "" {
		update.SiteTypeConfidence = providerLoginStringValuePtr(confidence)
	}
	if detectedAt := strings.TrimSpace(provider.SiteTypeDetectedAt); detectedAt != "" {
		update.SiteTypeDetectedAt = providerLoginStringValuePtr(detectedAt)
	}
	if authRef := strings.TrimSpace(provider.AccountAuthRef); authRef != "" {
		update.AccountAuthRef = providerLoginStringValuePtr(authRef)
	}
	if _, err := config.UpdateProviderConfig(configPath, update); err != nil {
		return fmt.Errorf("保存 provider %s 账户快照失败: %w", name, err)
	}
	if providerItem, ok := session.Config.Providers.Items[name]; ok {
		providerItem.Account = cloneProviderAccountSnapshot(provider.Account)
		providerItem.SiteType = provider.SiteType
		providerItem.SiteTypeConfidence = provider.SiteTypeConfidence
		providerItem.SiteTypeDetectedAt = provider.SiteTypeDetectedAt
		providerItem.AccountAuthRef = provider.AccountAuthRef
		session.Config.Providers.Items[name] = providerItem
	}
	return nil
}

// formatChatAccountReportLines renders one report as human-readable lines.
func formatChatAccountReportLines(report chatAccountReport) []string {
	lines := []string{fmt.Sprintf("Provider: %s", report.Provider)}
	if report.Protocol != "" {
		lines = append(lines, fmt.Sprintf("Protocol: %s", report.Protocol))
	}
	if report.BaseURL != "" {
		lines = append(lines, fmt.Sprintf("Base URL: %s", report.BaseURL))
	}
	siteType := report.SiteType
	if siteType == "" {
		siteType = string(siteaccount.SiteTypeUnknown)
	}
	if report.Confidence != "" {
		lines = append(lines, fmt.Sprintf("Site type: %s (%s)", siteType, report.Confidence))
	} else {
		lines = append(lines, fmt.Sprintf("Site type: %s", siteType))
	}
	lines = append(lines, fmt.Sprintf("Status: %s", report.Status))
	if report.BalanceLine != "" {
		lines = append(lines, fmt.Sprintf("Balance: %s", report.BalanceLine))
	}
	if report.Account != nil {
		lines = append(lines, formatChatAccountViewLines(*report.Account)...)
	}
	if report.Source != "" {
		lines = append(lines, fmt.Sprintf("Source: %s", report.Source))
	}
	if report.FetchedAt != "" {
		lines = append(lines, fmt.Sprintf("Fetched: %s", formatChatStatusLocalTime(report.FetchedAt)))
	}
	if report.Saved {
		lines = append(lines, "Saved: true（已写回 config.yaml）")
	}
	if len(report.Hits) > 0 {
		lines = append(lines, fmt.Sprintf("Detection: %d probe(s)", len(report.Hits)))
		for _, hit := range report.Hits {
			state := "miss"
			if hit.Matched {
				state = "match"
			}
			detail := strings.TrimSpace(hit.Detail)
			if detail != "" {
				detail = " " + detail
			}
			lines = append(lines, fmt.Sprintf("  - %s %s %d %s%s",
				strings.TrimSpace(hit.Path),
				strings.TrimSpace(string(hit.SiteType)),
				hit.StatusCode,
				state,
				detail))
		}
	}
	if report.Warning != "" {
		lines = append(lines, fmt.Sprintf("Warning: %s", report.Warning))
	}
	if report.Error != "" {
		lines = append(lines, fmt.Sprintf("Error: %s", report.Error))
	}
	return lines
}

// formatChatAccountViewLines renders the normalized account DTO details.
func formatChatAccountViewLines(view siteaccount.AccountView) []string {
	lines := make([]string, 0, 4)
	if view.PlanName != "" {
		lines = append(lines, fmt.Sprintf("Plan: %s", view.PlanName))
	}
	for _, detail := range view.BalanceDetails {
		label := firstNonEmptyText(strings.TrimSpace(detail.Currency), "balance")
		lines = append(lines, fmt.Sprintf("  - %s: total %s", label, formatChatAccountAmount(detail.TotalBalance, detail.Currency)))
		if detail.GrantedBalance != 0 || detail.ToppedUpBalance != 0 {
			lines = append(lines, fmt.Sprintf("    granted %s / topped up %s",
				formatChatAccountAmount(detail.GrantedBalance, detail.Currency),
				formatChatAccountAmount(detail.ToppedUpBalance, detail.Currency)))
		}
	}
	if view.QuotaRemaining != nil {
		lines = append(lines, fmt.Sprintf("Quota remaining: %s", formatChatAccountAmount(*view.QuotaRemaining, view.Currency)))
	}
	if view.QuotaUsed != nil {
		lines = append(lines, fmt.Sprintf("Quota used: %s", formatChatAccountAmount(*view.QuotaUsed, view.Currency)))
	}
	for _, sub := range view.Subscriptions {
		name := firstNonEmptyText(strings.TrimSpace(sub.Name), "subscription")
		state := strings.TrimSpace(sub.Status)
		if sub.Remaining != nil {
			lines = append(lines, fmt.Sprintf("Subscription: %s remaining %s (%s)", name, formatChatAccountAmount(*sub.Remaining, view.Currency), state))
			continue
		}
		lines = append(lines, fmt.Sprintf("Subscription: %s (%s)", name, state))
	}
	if view.Partial {
		lines = append(lines, "Partial: true（部分字段缺失）")
	}
	for _, item := range view.Errors {
		if text := strings.TrimSpace(item); text != "" {
			lines = append(lines, fmt.Sprintf("Detail: %s", text))
		}
	}
	return lines
}

func formatChatAccountAmount(value float64, unit string) string {
	text := strings.TrimSpace(fmt.Sprintf("%.2f", value))
	if unit = strings.TrimSpace(unit); unit != "" {
		return text + " " + unit
	}
	return text
}

// formatChatAccountListLines renders the provider table, prefixed by the
// background-refresh state line when the caller projected one.
func formatChatAccountListLines(list chatAccountListReport) []string {
	lines := make([]string, 0, len(list.Providers)+4)
	if state := strings.TrimSpace(list.RefreshState); state != "" {
		line := "刷新: " + state
		if detail := strings.TrimSpace(list.RefreshDetail); detail != "" {
			line += " · " + detail
		}
		lines = append(lines, line)
	}
	for _, report := range list.Providers {
		balance := report.BalanceLine
		if balance == "" {
			balance = "-"
		}
		siteType := firstNonEmptyText(report.SiteType, string(siteaccount.SiteTypeUnknown))
		lines = append(lines, fmt.Sprintf("%-20s %-12s %-14s %s", report.Provider, siteType, report.Status, balance))
		if report.Warning != "" {
			lines = append(lines, fmt.Sprintf("%-20s %-12s %s", "", "", "warning: "+report.Warning))
		}
		if report.Error != "" {
			lines = append(lines, fmt.Sprintf("%-20s %-12s %s", "", "", "error: "+report.Error))
		}
	}
	header := fmt.Sprintf("%-20s %-12s %-14s %s", "provider", "site_type", "status", "balance")
	lines = append([]string{header}, lines...)
	summary := fmt.Sprintf("%d provider(s), %d with account", list.Total, list.WithAccount)
	if list.Refreshed {
		summary += " · refreshed"
	}
	lines = append(lines, summary)
	lines = append(lines, "提示: /accounts refresh 提交后台刷新；/accounts display 只看缓存；/account [provider] 刷新单个账户")
	return lines
}

// chatAccountJSONResult renders any account projection as a JSON command cell.
func chatAccountJSONResult(payload interface{}) CommandResult {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return commandTextResult(fmt.Sprintf("错误: 账户数据序列化失败: %v", err))
	}
	return commandTextResult(string(encoded))
}

// ============================================================================
// /account 参数补全
// ============================================================================

type chatAccountCompletionSlot int

const (
	chatAccountSlotSubcommand chatAccountCompletionSlot = iota
	chatAccountSlotProvider
	chatAccountSlotFlags
)

// completeChatAccountSlashArgs 为 /account 提供子命令、provider、标志与 --timeout
// 取值的补全。词表与 parseChatAccountCommand 的解析保持一致，避免出现「可补全但
// 解析报错」的漂移。
func completeChatAccountSlashArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	query := activeSlashArgumentQuery(ctx)

	if chatAccountTimeoutValueFocus(ctx) {
		return matchSlashArgumentCandidates(chatAccountTimeoutValueCandidates(), query)
	}

	mode, slot := chatAccountCompletionSlotFor(ctx)
	var candidates []chatSlashCompletionCandidate
	switch slot {
	case chatAccountSlotProvider:
		candidates = append(candidates, chatAccountFlagCandidates(mode)...)
		candidates = append(candidates, providerNameArgumentCandidates(session)...)
	case chatAccountSlotFlags:
		candidates = chatAccountFlagCandidates(mode)
	default:
		candidates = chatAccountSubcommandCandidates()
		if strings.TrimSpace(query) != "" {
			// 第一个参数也可以直接写 provider 名：查询非空时把 provider 一并纳入，
			// 避免「provider 前缀无候选」的死角。
			candidates = append(candidates, providerNameArgumentCandidates(session)...)
		}
	}
	return matchSlashArgumentCandidates(dedupeSlashArgumentCandidates(candidates), query)
}

// completeChatAccountsSlashArgs 为 /accounts 提供子命令、标志与 --timeout 取值
// 补全。该命令不接受 provider 位置参数（单个 provider 属于 /account）。
func completeChatAccountsSlashArgs(_ *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	query := activeSlashArgumentQuery(ctx)
	if chatAccountTimeoutValueFocus(ctx) {
		return matchSlashArgumentCandidates(chatAccountTimeoutValueCandidates(), query)
	}
	// 第一个参数位同时给出子命令与标志；之后的参数位只给标志（子命令必须写在标志
	// 之前，补全不提供必然报错的组合）。
	index := len(ctx.Tokens)
	if ctx.CurrentOK {
		for i, token := range ctx.Tokens {
			if token.Start == ctx.Current.Start {
				index = i
				break
			}
		}
	}
	candidates := chatAccountsFlagCandidates()
	if index == 0 && !strings.HasPrefix(strings.TrimSpace(ctx.Current.Text), "-") {
		candidates = append(chatAccountsSubcommandCandidates(), candidates...)
	}
	return matchSlashArgumentCandidates(dedupeSlashArgumentCandidates(candidates), query)
}

// chatAccountSubcommandModeForToken 把子命令词（含别名）映射为模式。
func chatAccountSubcommandModeForToken(token string) (chatAccountCommandMode, bool) {
	switch token {
	case "show", "status":
		return chatAccountModeShow, true
	case "refresh", "sync":
		return chatAccountModeRefresh, true
	case "detect", "site":
		return chatAccountModeDetect, true
	}
	return "", false
}

// chatAccountTimeoutValueFocus 判断光标是否落在 --timeout 的取值上（"=取值" 或
// 空格后的下一个 token）。
func chatAccountTimeoutValueFocus(ctx slashArgumentContext) bool {
	if strings.HasPrefix(strings.TrimSpace(ctx.Current.Text), "--timeout=") {
		return true
	}
	return strings.TrimSpace(ctx.Previous.Text) == "--timeout"
}

// chatAccountCompletionSlotFor 推断当前光标所处的参数位（子命令/provider/标志）
// 以及所属模式；只有 show/refresh/detect 接受 provider 位置参数。
func chatAccountCompletionSlotFor(ctx slashArgumentContext) (chatAccountCommandMode, chatAccountCompletionSlot) {
	subIndex := -1
	mode := chatAccountModeRefresh
	for i, token := range ctx.Tokens {
		if i > 3 {
			break
		}
		if candidate, ok := chatAccountSubcommandModeForToken(strings.ToLower(strings.TrimSpace(token.Text))); ok {
			subIndex, mode = i, candidate
			break
		}
	}

	index := len(ctx.Tokens)
	if ctx.CurrentOK {
		for i, token := range ctx.Tokens {
			if token.Start == ctx.Current.Start {
				index = i
				break
			}
		}
	}
	switch {
	case subIndex < 0 && index == 0:
		return mode, chatAccountSlotSubcommand
	case subIndex >= 0 && index == subIndex+1:
		return mode, chatAccountSlotProvider
	default:
		return mode, chatAccountSlotFlags
	}
}

func chatAccountSubcommandCandidates() []chatSlashCompletionCandidate {
	const summary = "账户/余额"
	return []chatSlashCompletionCandidate{
		{Command: "refresh", Summary: summary + "：实时刷新（默认行为）", Group: string(chatSlashCommandGroupModel)},
		{Command: "show", Summary: summary + "：只读缓存快照（等价 --no-refresh）", Group: string(chatSlashCommandGroupModel)},
		{Command: "detect", Summary: "站点类型探测（不取余额）", Group: string(chatSlashCommandGroupModel)},
		{Command: "--json", Summary: "以 JSON 输出", Group: string(chatSlashCommandGroupModel)},
		{Command: "--timeout", Summary: "实时拉取超时", Group: string(chatSlashCommandGroupModel), AcceptsArgs: true},
	}
}

// chatAccountFlagCandidates 返回指定模式下合法的标志，避免补全给出必然报错的组合。
func chatAccountFlagCandidates(mode chatAccountCommandMode) []chatSlashCompletionCandidate {
	const group = string(chatSlashCommandGroupModel)
	candidates := []chatSlashCompletionCandidate{
		{Command: "--json", Summary: "以 JSON 输出", Group: group},
		{Command: "--timeout", Summary: "实时拉取超时", Group: group, AcceptsArgs: true},
		{Command: "--no-refresh", Summary: "只显示缓存快照，不刷新", Group: group},
	}
	if mode == chatAccountModeRefresh {
		candidates = append(candidates, chatSlashCompletionCandidate{Command: "--save", Summary: "刷新成功后写回 config.yaml", Group: group})
	}
	return candidates
}

// chatAccountsSubcommandCandidates 返回 /accounts 的子命令词表，与
// chatAccountsSubcommandModeForToken 的解析保持一致。
func chatAccountsSubcommandCandidates() []chatSlashCompletionCandidate {
	const summary = "全部账户"
	return []chatSlashCompletionCandidate{
		{Command: "refresh", Summary: summary + "：提交后台刷新（立即返回）", Group: string(chatSlashCommandGroupModel)},
		{Command: "display", Summary: summary + "：只显示缓存快照（零网络）", Group: string(chatSlashCommandGroupModel)},
	}
}

// chatAccountsFlagCandidates 返回 /accounts 的合法标志。
func chatAccountsFlagCandidates() []chatSlashCompletionCandidate {
	const group = string(chatSlashCommandGroupModel)
	return []chatSlashCompletionCandidate{
		{Command: "--wait", Summary: "阻塞等待刷新完成后再显示", Group: group},
		{Command: "--enabled-only", Summary: "只显示已启用 provider", Group: group},
		{Command: "--no-refresh", Summary: "等价 display：只显示缓存快照", Group: group},
		{Command: "--json", Summary: "以 JSON 输出（默认隐含 --wait；display 时只输出缓存）", Group: group},
		{Command: "--timeout", Summary: "实时拉取超时", Group: group, AcceptsArgs: true},
	}
}
func chatAccountTimeoutValueCandidates() []chatSlashCompletionCandidate {
	const group = string(chatSlashCommandGroupModel)
	values := []string{"5s", "10s", "15s", "30s", "1m", "2m"}
	candidates := make([]chatSlashCompletionCandidate, 0, len(values))
	for _, value := range values {
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command: value,
			Summary: "超时 " + value,
			Group:   group,
		})
	}
	return candidates
}

// handleChatAccountCommand 是 legacy（JSON 输出 / 非 unified 交互）入口。它与结构化
// 路径共用同一解析与报告构造，只把结果文档的可见文本写到 stdout，因此两条路径不会
// 产生语义漂移；返回值对齐既有命令处理器（false = 不退出 REPL）。
func handleChatAccountCommand(session *ChatSession, command string) bool {
	result := executeStructuredChatAccountCommand(session, command)
	texts := make([]string, 0, len(result.Blocks))
	for _, block := range result.Blocks {
		if text := strings.TrimRight(block.Document.PlainText(), "\n"); strings.TrimSpace(text) != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		printChatCommandOutput(session, "错误: /account 没有可显示的内容\n"+chatAccountCommandUsage)
		return false
	}
	printChatCommandOutput(session, strings.Join(texts, "\n"))
	return false
}
