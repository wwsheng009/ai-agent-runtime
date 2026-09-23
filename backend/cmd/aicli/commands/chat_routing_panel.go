package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// 会话级路由全屏面板（方案 §5.3/§5.3.1）：Level → 字段 → 值 三级导航。
//
// 复用既有 picker 基建（chatPickerOpen/chatPickerStage/chatPickerClose +
// ui.FullScreenList），不新增渲染器；三级导航是同一全屏界面内的 stage 切换，
// 不是多个独立屏幕。Esc 在任一 stage 取消都不落盘（I-1）。

// chatRoutingPanelBuiltinLevels 是无任何 levels 配置时的面板兜底档位
// （与 §3.5.3 开启推导的内置四档一致）。
var chatRoutingPanelBuiltinLevels = []string{"easy", "normal", "hard", "expert"}

// chatRoutingPanelField 是第二级（字段）导航的一项。
type chatRoutingPanelField struct {
	Key     string // enabled | provider | model | reasoning_effort
	Summary string
}

var chatRoutingPanelFields = []chatRoutingPanelField{
	{Key: "enabled", Summary: "该级路由开关（on/off）"},
	{Key: "provider", Summary: "该级 provider"},
	{Key: "model", Summary: "该级 model"},
	{Key: "reasoning_effort", Summary: "该级 reasoning effort（按已选 model 过滤）"},
	{Key: "thinking_effort", Summary: "该级 thinking effort（无目录候选时给写入提示）"},
	{Key: "max_tokens", Summary: "该级 max_tokens（数值，直接写入）"},
	{Key: "temperature", Summary: "该级 temperature（数值，直接写入）"},
	{Key: "prompt_cache", Summary: "该级 prompt cache 开关（on/off）"},
}

// chatRoutingPanelLeaseHooks 绑定面板的 barrier 动作（OpenRoutingPanel/CloseRoutingPanel）。
func chatRoutingPanelLeaseHooks() chatPickerLeaseHooks {
	return chatPickerLeaseHooks{
		Open: func(leaseID uint64) ui.UIAction {
			return ui.OpenRoutingPanel{LeaseID: leaseID}
		},
		Close: func(leaseID uint64) ui.UIAction {
			return ui.CloseRoutingPanel{LeaseID: leaseID}
		},
	}
}

// canOpenChatRoutingPanel 与模型选择器同口径：只有统一 presenter 空闲、拥有
// viewport 且没有竞争弹层/备用屏时才能开启全屏面板。
func canOpenChatRoutingPanel(session *ChatSession) bool {
	return chatPickerSurfaceReady(session)
}

func closeChatRoutingPanelLease(session *ChatSession, lease ui.ScreenLease) {
	_ = chatPickerClose(session, lease, chatRoutingPanelLeaseHooks())
}

// chatRoutingPanelLevelItems 产出第一级（Level）列表项（§5.3 建议值引擎）：
// 内置四档恒在（未配置档标注未配置，选中后按 §3.5.3 推导开启），其余档位按
// 解析结果追加，顺序与 orderedRoutingLevels 一致。
func chatRoutingPanelLevelItems(session *ChatSession, scope string) []ui.FullScreenListItem {
	rows := chatRoutingLevelSummaries(session, scope)
	byLevel := make(map[string]agentconfig.RoutingLevelSummary, len(rows))
	levels := make([]string, 0, len(rows)+len(chatRoutingPanelBuiltinLevels))
	levels = append(levels, chatRoutingPanelBuiltinLevels...)
	for _, row := range rows {
		level := strings.ToLower(strings.TrimSpace(row.Level))
		if level == "" {
			continue
		}
		byLevel[level] = row
		if !chatRoutingPanelContainsLevel(levels, level) {
			levels = append(levels, level)
		}
	}
	items := make([]ui.FullScreenListItem, 0, len(levels))
	for _, level := range levels {
		row, ok := byLevel[level]
		if !ok {
			items = append(items, ui.FullScreenListItem{
				Title:      level,
				Detail:     "未配置（选择后按 §3.5.3 推导开启）",
				SearchText: level,
			})
			continue
		}
		detail := joinNonEmptyStatusParts(row.Provider, row.Model, row.Reasoning)
		if detail == "" {
			detail = "未配置"
		}
		if row.Source != "" {
			detail = strings.TrimSpace(detail + " (" + row.Source + ")")
		}
		if !row.Enabled {
			detail = "关闭 · " + detail
		}
		if row.Expensive {
			detail += " [expensive]"
		}
		items = append(items, ui.FullScreenListItem{
			Title:      level,
			Detail:     detail,
			SearchText: level + " " + detail,
		})
	}
	return items
}

func chatRoutingPanelContainsLevel(levels []string, level string) bool {
	for _, existing := range levels {
		if existing == level {
			return true
		}
	}
	return false
}

// chatRoutingPanelFieldItems 产出第二级（字段）列表项（§5.3）。
func chatRoutingPanelFieldItems() []ui.FullScreenListItem {
	items := make([]ui.FullScreenListItem, 0, len(chatRoutingPanelFields))
	for _, field := range chatRoutingPanelFields {
		items = append(items, ui.FullScreenListItem{
			Title:      field.Key,
			Detail:     field.Summary,
			SearchText: field.Key + " " + field.Summary,
		})
	}
	return items
}

// chatRoutingPanelValueItems 把候选值渲染为第三级（值）列表（§5.3 建议值引擎）：
// options 必须是已归一化的候选序列（与写回值一一对应），current 命中时加 (当前)
// 标记，Preview 显示写入后的键与生效时机。
func chatRoutingPanelValueItems(options []string, scope, level, field, current string) []ui.FullScreenListItem {
	key := chatRoutingPanelWriteKey(scope, level, field)
	items := make([]ui.FullScreenListItem, 0, len(options))
	for _, option := range options {
		title := option
		if current != "" && strings.EqualFold(strings.TrimSpace(option), current) {
			title += "  (当前)"
		}
		items = append(items, ui.FullScreenListItem{
			Title:      title,
			Detail:     field,
			Preview:    fmt.Sprintf("写入 %s = %s（下一 turn 生效）", key, option),
			SearchText: field + " " + option,
		})
	}
	return items
}

// chatRoutingPanelCurrentValue 取该级字段的当前生效值（来源可能是 session/
// workspace/config/default/derived），仅用于列表里的 (当前) 标记；无投影数据
// 的字段（thinking_effort/max_tokens/temperature/prompt_cache）返回空。
func chatRoutingPanelCurrentValue(session *ChatSession, scope, level, field string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	for _, row := range chatRoutingLevelSummaries(session, scope) {
		if !strings.EqualFold(strings.TrimSpace(row.Level), level) {
			continue
		}
		switch field {
		case "provider":
			return row.Provider
		case "model":
			return row.Model
		case "reasoning_effort":
			return row.Reasoning
		case "enabled":
			if row.Enabled {
				return "on"
			}
			return "off"
		default:
			return ""
		}
	}
	return ""
}

// chatRoutingPanelValueOptions 返回某字段的建议值（去重、保序）。
func chatRoutingPanelValueOptions(session *ChatSession, field string) []string {
	switch field {
	case "provider":
		return chatRoutingCandidateValues(providerNameArgumentCandidates(session))
	case "model":
		return chatRoutingCandidateValues(runtimeModelArgumentCandidates(session))
	case "reasoning_effort":
		return chatRoutingCandidateValues(reasoningEffortArgumentCandidates(session))
	case "enabled", "prompt_cache":
		return []string{"on", "off"}
	default:
		return nil
	}
}

// chatRoutingPanelValueOptionsForLevel 是 §10.2 I-3 的档位感知取值候选：
// reasoning_effort 按「该级生效 model」取目录（该级未写 model 时回落会话模型），
// 其余字段与 chatRoutingPanelValueOptions 完全同源。
func chatRoutingPanelValueOptionsForLevel(session *ChatSession, scope, level, field string) []string {
	if strings.EqualFold(strings.TrimSpace(field), "reasoning_effort") {
		return chatRoutingReasoningValueOptionsForLevel(session, scope, level)
	}
	return chatRoutingPanelValueOptions(session, field)
}

// chatRoutingReasoningValueOptionsForLevel 用该级 provider/model 取 reasoning 目录；
// 目录为空（模型无能力卡片 / 未识别）时返回 nil——**不**回落会话模型候选，避免给出
// 该级模型不支持的假建议（I-3「不支持值不出现」；面板按空候选给「直接写入」提示）。
func chatRoutingReasoningValueOptionsForLevel(session *ChatSession, scope, level string) []string {
	if session == nil {
		return nil
	}
	provider := session.Provider
	model := strings.TrimSpace(effectiveRuntimeModel(session))
	if row, ok := chatRoutingLevelSummaryFor(session, scope, level); ok {
		if name := strings.TrimSpace(row.Provider); name != "" {
			// 该级 provider 尚未在本机注册（或会话无配置）时无法确定模型目录：
			// 给空候选而不是会话候选，避免给出该级并不支持的假建议（I-3）。
			named, exists := agentconfig.Provider{}, false
			if session.Config != nil {
				named, exists = session.Config.Providers.Items[name]
			}
			if !exists {
				return nil
			}
			provider = named
			if strings.TrimSpace(row.Model) == "" {
				// 只覆盖 provider 时，该级生效 model 取 provider 的默认模型（§3.5 逐字段继承）。
				model = strings.TrimSpace(named.DefaultModel)
			}
		}
		if rowModel := strings.TrimSpace(row.Model); rowModel != "" {
			model = rowModel
		}
	}
	if model == "" {
		return nil
	}
	catalog := reasoningEffortCatalogForModel(provider, model)
	if len(catalog.options) == 0 {
		return nil
	}
	return chatRoutingCandidateValues(reasoningEffortArgumentCandidatesFromOptions(catalog.options))
}

func chatRoutingCandidateValues(candidates []chatSlashCompletionCandidate) []string {
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		value := strings.TrimSpace(candidate.Command)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// chatRoutingPanelEntry 打开面板；无全屏能力时退化为只读摘要（§5.3.1 边界）。
// scope=main|sub；level 非空时直达该级字段列表；keyPath 非空时直达值选择器
// （接受 "profiles.<level>.<field>" 与 "<field>" 两种写法）。
func chatRoutingPanelEntry(session *ChatSession, scope, level, keyPath string) string {
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope != "sub" {
		scope = "main"
	}
	level = strings.TrimSpace(level)
	keyPath = strings.TrimSpace(keyPath)
	if keyPath == "" && strings.Contains(level, ".") {
		// `/routing main profiles.hard.model`：单 token 键路径直达值选择器（§5.3）。
		level, keyPath = chatRoutingPanelParseKeyPath("", level)
	}
	if keyPath != "" {
		level, keyPath = chatRoutingPanelParseKeyPath(level, keyPath)
	}
	// §5.6/I-8/INV-A3（M16）：子会话只读打开——宿主接线只把主 Agent 路由接主会话
	// （chat_actor_host.go 的 isBaseSession 分支），子会话里的写入不会生效，因此
	// 不提供可写选择器，只回只读摘要 + 明确提示。
	if chatRoutingSessionIsChildAgent(session) {
		return chatRoutingPanelEntryText(session, scope, level) + "\n" + chatRoutingChildSessionReadOnlyNote
	}
	if session == nil || !canOpenChatRoutingPanel(session) {
		return chatRoutingPanelEntryText(session, scope, level)
	}

	lease, err := chatPickerOpen(session, "会话路由", chatRoutingPanelLeaseHooks())
	if err != nil {
		return "错误: 打开路由面板失败: " + err.Error()
	}

	if level == "" {
		picked, cancelled, stageErr := chatPickerStage(context.Background(), session, lease, ui.FullScreenListOptions{
			Title:        "选择难度档位",
			Subtitle:     fmt.Sprintf("scope: %s · Enter 进入字段列表，Esc 取消", scope),
			EmptyMessage: "没有可用的档位",
			ConfirmLabel: "选择该档位",
			Items:        chatRoutingPanelLevelItems(session, scope),
		})
		if stageErr != nil {
			closeChatRoutingPanelLease(session, lease)
			return "错误: 选择档位失败: " + stageErr.Error()
		}
		if cancelled {
			closeChatRoutingPanelLease(session, lease)
			return "已取消（未写入）"
		}
		items := chatRoutingPanelLevelItems(session, scope)
		if picked < 0 || picked >= len(items) {
			closeChatRoutingPanelLease(session, lease)
			return "已取消（未写入）"
		}
		level = items[picked].Title
	}

	field := keyPath
	if field == "" {
		picked, cancelled, stageErr := chatPickerStage(context.Background(), session, lease, ui.FullScreenListOptions{
			Title:        "选择字段",
			Subtitle:     fmt.Sprintf("scope: %s · level: %s · Enter 进入值列表，Esc 取消", scope, level),
			EmptyMessage: "没有可编辑字段",
			ConfirmLabel: "选择该字段",
			Items:        chatRoutingPanelFieldItems(),
		})
		if stageErr != nil {
			closeChatRoutingPanelLease(session, lease)
			return "错误: 选择字段失败: " + stageErr.Error()
		}
		if cancelled {
			closeChatRoutingPanelLease(session, lease)
			return "已取消（未写入）"
		}
		field = chatRoutingPanelFields[picked].Key
	}

	valueOptions := normalizeChatPickerOptions(chatRoutingPanelValueOptionsForLevel(session, scope, level, field))
	if len(valueOptions) == 0 {
		closeChatRoutingPanelLease(session, lease)
		return fmt.Sprintf("没有可用的 %s 候选值；用 /routing %s %s <value> 直接写入", field, scope, chatRoutingPanelWriteKey(scope, level, field))
	}
	valueItems := chatRoutingPanelValueItems(valueOptions, scope, level, field, chatRoutingPanelCurrentValue(session, scope, level, field))
	picked, cancelled, stageErr := chatPickerStage(context.Background(), session, lease, ui.FullScreenListOptions{
		Title:        "选择值",
		Subtitle:     fmt.Sprintf("scope: %s · level: %s · field: %s · Enter 写入 session 层，Esc 取消", scope, level, field),
		EmptyMessage: "没有匹配的值",
		ConfirmLabel: "写入并生效（下一 turn）",
		Items:        valueItems,
	})
	if stageErr != nil {
		closeChatRoutingPanelLease(session, lease)
		return "错误: 选择值失败: " + stageErr.Error()
	}
	if cancelled {
		closeChatRoutingPanelLease(session, lease)
		return "已取消（未写入）"
	}
	value := valueOptions[picked]

	// §5.4/I-2：写入层选择器（session/workspace/config）。选择 config 即二次确认，
	// 目标文件路径在选择项与结果文本中回显。
	layerItems := chatRoutingPanelLayerItems(session)
	layerPicked, layerCancelled, layerErr := chatPickerStage(context.Background(), session, lease, ui.FullScreenListOptions{
		Title:        "选择写入层",
		Subtitle:     fmt.Sprintf("scope: %s · level: %s · field: %s · 值: %s", scope, level, field, value),
		EmptyMessage: "没有可写层",
		ConfirmLabel: "写入该层（下一 turn 生效）",
		Items:        layerItems,
	})
	if layerErr != nil {
		closeChatRoutingPanelLease(session, lease)
		return "错误: 选择写入层失败: " + layerErr.Error()
	}
	if layerCancelled || layerPicked < 0 || layerPicked >= len(chatRoutingPanelLayerNames) {
		closeChatRoutingPanelLease(session, lease)
		return "已取消（未写入）"
	}
	layer := chatRoutingPanelLayerNames[layerPicked]

	text, writeErr := chatRoutingWriteKey(session, scope, chatRoutingPanelWriteKey(scope, level, field), value, layer, layer == chatRoutingLayerConfig)
	closeChatRoutingPanelLease(session, lease)
	if writeErr != nil {
		return "错误: " + writeErr.Error()
	}
	return text
}

// chatRoutingPanelLayerNames 与 chatRoutingPanelLayerItems 一一对应（§5.4）。
var chatRoutingPanelLayerNames = []string{
	chatRoutingLayerSession,
	chatRoutingLayerWorkspace,
	chatRoutingLayerConfig,
}

// chatRoutingPanelLayerItems 产出写入层选择项：workspace/config 回显目标文件路径，
// config 项显式标注「影响所有会话」（选中即二次确认）。
func chatRoutingPanelLayerItems(session *ChatSession) []ui.FullScreenListItem {
	items := []ui.FullScreenListItem{
		{Title: chatRoutingLayerSession, Detail: "仅本会话（随会话持久化）", SearchText: chatRoutingLayerSession},
	}
	if _, target, err := chatRoutingWorkspaceWriteTarget(session); err == nil {
		items = append(items, ui.FullScreenListItem{Title: chatRoutingLayerWorkspace, Detail: "工作区偏好 · " + target, SearchText: chatRoutingLayerWorkspace})
	} else {
		items = append(items, ui.FullScreenListItem{Title: chatRoutingLayerWorkspace, Detail: "工作区偏好不可用（" + err.Error() + "）", SearchText: chatRoutingLayerWorkspace})
	}
	if path, kind := agentconfig.AICLIConfigWriteTargetForRouting(); strings.TrimSpace(path) != "" {
		items = append(items, ui.FullScreenListItem{Title: chatRoutingLayerConfig, Detail: "影响所有会话 · " + path + "（" + kind + "）", SearchText: chatRoutingLayerConfig})
	} else {
		items = append(items, ui.FullScreenListItem{Title: chatRoutingLayerConfig, Detail: "影响所有会话 · 配置写入目标不可用", SearchText: chatRoutingLayerConfig})
	}
	return items
}

// chatRoutingPanelWriteKey 把面板选择映射为写入键（§5.3/§5.4）：
// enabled 是分节开关（不带档位前缀），provider/model/reasoning_effort 落在
// profiles.<level>.<field>。
func chatRoutingPanelWriteKey(scope, level, field string) string {
	field = strings.TrimSpace(field)
	if field == "" {
		return ""
	}
	if field == "enabled" {
		return "enabled"
	}
	if strings.HasPrefix(field, "profiles.") || strings.HasPrefix(field, "levels.") {
		return field
	}
	return chatRoutingLevelKeyPrefix(scope) + strings.TrimSpace(level) + "." + field
}

// chatRoutingPanelParseKeyPath 解析键路径直达参数（§5.3 直达）：
// "profiles.hard.model" → level=hard, field=model；"model" → level 不变。
func chatRoutingPanelParseKeyPath(level, keyPath string) (string, string) {
	parts := strings.Split(strings.TrimSpace(keyPath), ".")
	switch {
	case len(parts) == 3 && (parts[0] == "profiles" || parts[0] == "levels"):
		return parts[1], parts[2]
	case len(parts) == 1:
		return level, parts[0]
	default:
		return level, strings.Join(parts, ".")
	}
}

// joinNonEmptyStatusParts 用 " · " 连接非空片段（状态栏/列表共用口径）。
func joinNonEmptyStatusParts(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, " · ")
}
