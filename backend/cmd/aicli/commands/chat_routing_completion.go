package commands

import (
	"sort"
	"strings"
)

// 会话级路由（/routing）Tab 补全（方案 §10.2 I-9）。
//
// 逐段补全：作用域 → 键 → 值；非法值退化为最近似候选（补全只影响输入，
// 写回仍由命令层按 §3.5 同源校验，本文件不改变任何校验语义）。
//
// 同源纪律（§6.1/I-3）：档位来自 chatRoutingLevelSummaries（与状态栏、
// `/routing show`、面板第一级一致），provider/model/reasoning 值来自
// chatRoutingPanelValueOptions（与面板第三级建议值引擎一致），本文件不另建
// 一份候选表；`candidates` 为只读字段（§5.3），不参与补全。

const chatRoutingCompletionGroup = string(chatSlashCommandGroupSession)

// chatRoutingCompletionProfileFields 是键路径直达 `profiles.<level>.<field>` /
// `levels.<level>.<field>` 可补全的字段集合，与 chatRoutingApplyProfileOverride
// 的可写字段一致（provider/model/reasoning_effort/thinking_effort/prompt_cache）。
var chatRoutingCompletionProfileFields = []string{"provider", "model", "reasoning_effort", "thinking_effort", "prompt_cache"}

func completeChatRoutingSlashArgs(session *ChatSession, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	query := activeSlashArgumentQuery(ctx)
	current := strings.ToLower(strings.TrimSpace(ctx.Current.Text))
	previous := strings.ToLower(strings.TrimSpace(ctx.Previous.Text))

	// flag 可出现在任意位置，故先于位置解析判定 `--to <layer>`。
	if previous == "--to" {
		return chatRoutingMatchCandidates(chatRoutingLayerArgumentCandidates(), query)
	}
	if strings.HasPrefix(current, "--") {
		return chatRoutingMatchCandidates(chatRoutingFlagArgumentCandidates(), query)
	}

	index := chatRoutingCursorTokenIndex(ctx)
	if index <= 0 {
		return chatRoutingMatchCandidates(chatRoutingTopLevelArgumentCandidates(), query)
	}

	positional := chatRoutingCompletedPositionalTokens(ctx, index)
	if len(positional) == 0 {
		return chatRoutingMatchCandidates(chatRoutingTopLevelArgumentCandidates(), query)
	}

	switch strings.ToLower(positional[0]) {
	case "main", "sub":
		return completeChatRoutingWriteArgs(session, positional, query)
	case "reset":
		if len(positional) == 1 {
			return chatRoutingMatchCandidates(chatRoutingResetTargetArgumentCandidates(), query)
		}
		if len(positional) == 2 && chatRoutingIsScopeToken(positional[1]) {
			return chatRoutingMatchCandidates(chatRoutingLevelArgumentCandidates(session, positional[1]), query)
		}
		return chatRoutingMatchCandidates(chatRoutingFlagArgumentCandidates(), query)
	case "save":
		if len(positional) == 1 {
			return chatRoutingMatchCandidates(chatRoutingLayerArgumentCandidates(), query)
		}
		return nil
	case "show", "doctor", "on", "off":
		if len(positional) == 1 {
			return chatRoutingMatchCandidates(chatRoutingScopeArgumentCandidates(), query)
		}
		return chatRoutingMatchCandidates(chatRoutingFlagArgumentCandidates(), query)
	default:
		return chatRoutingMatchCandidates(chatRoutingTopLevelArgumentCandidates(), query)
	}
}

// completeChatRoutingWriteArgs 处理 `main|sub <key> <value>` 与糖写法
// `main|sub level <level> <field> <value>` 的逐段补全。
func completeChatRoutingWriteArgs(session *ChatSession, positional []string, query string) []chatSlashCompletionCandidate {
	scope := strings.ToLower(positional[0])
	switch len(positional) {
	case 1:
		return chatRoutingMatchCandidates(chatRoutingKeyArgumentCandidates(session, scope), query)
	case 2:
		if strings.EqualFold(positional[1], "level") {
			return chatRoutingMatchCandidates(chatRoutingLevelArgumentCandidates(session, scope), query)
		}
		return chatRoutingMatchCandidates(chatRoutingValueArgumentCandidates(session, scope, chatRoutingLevelFromKey(scope, positional[1]), positional[1]), query)
	case 3:
		if strings.EqualFold(positional[1], "level") {
			return chatRoutingMatchCandidates(chatRoutingFieldArgumentCandidates(), query)
		}
		return nil
	case 4:
		if strings.EqualFold(positional[1], "level") {
			return chatRoutingMatchCandidates(chatRoutingValueArgumentCandidates(session, scope, positional[2], positional[3]), query)
		}
		return nil
	default:
		return nil
	}
}

func chatRoutingTopLevelArgumentCandidates() []chatSlashCompletionCandidate {
	return []chatSlashCompletionCandidate{
		{Command: "show", Summary: "只读摘要（可跟 main|sub，--json 输出投影 JSON）", Group: chatRoutingCompletionGroup},
		{Command: "doctor", Summary: "逐字段来源与回退阶梯诊断", Group: chatRoutingCompletionGroup},
		{Command: "on", Summary: "开启路由（可跟 main|sub）", Group: chatRoutingCompletionGroup, AcceptsArgs: true},
		{Command: "off", Summary: "关闭路由（可跟 main|sub）", Group: chatRoutingCompletionGroup, AcceptsArgs: true},
		{Command: "main", Summary: "主 Agent：<key> <value> 或 level <level> <field> <value>", Group: chatRoutingCompletionGroup, AcceptsArgs: true},
		{Command: "sub", Summary: "子 Agent：<key> <value> 或 level <level> <field> <value>", Group: chatRoutingCompletionGroup, AcceptsArgs: true},
		{Command: "reset", Summary: "清除覆盖（[main|sub] [<level>] [--to session|workspace|config]）", Group: chatRoutingCompletionGroup, AcceptsArgs: true},
		{Command: "save", Summary: "保存到目标层（[session|workspace|config]；session 层随会话持久化）", Group: chatRoutingCompletionGroup, AcceptsArgs: true},
		{Command: "--to", Summary: "写入层选择（session|workspace|config）", Group: chatRoutingCompletionGroup, AcceptsArgs: true},
		{Command: "--yes", Summary: "config 层写入/清除的二次确认", Group: chatRoutingCompletionGroup},
		{Command: "--json", Summary: "show/doctor 的 JSON 输出", Group: chatRoutingCompletionGroup},
	}
}

func chatRoutingScopeArgumentCandidates() []chatSlashCompletionCandidate {
	return []chatSlashCompletionCandidate{
		{Command: chatRoutingScopeMain, Summary: "主 Agent 路由", Group: chatRoutingCompletionGroup},
		{Command: chatRoutingScopeSub, Summary: "子 Agent 路由", Group: chatRoutingCompletionGroup},
	}
}

func chatRoutingLayerArgumentCandidates() []chatSlashCompletionCandidate {
	return []chatSlashCompletionCandidate{
		{Command: chatRoutingLayerSession, Summary: "会话层覆盖（随会话持久化）", Group: chatRoutingCompletionGroup},
		{Command: chatRoutingLayerWorkspace, Summary: "写入会话绑定 workspace 的配置（影响本 workspace 全部会话）", Group: chatRoutingCompletionGroup},
		{Command: chatRoutingLayerConfig, Summary: "写入用户配置（影响全部会话，需 --yes）", Group: chatRoutingCompletionGroup},
	}
}

func chatRoutingFlagArgumentCandidates() []chatSlashCompletionCandidate {
	return []chatSlashCompletionCandidate{
		{Command: "--to", Summary: "写入层选择（session|workspace|config）", Group: chatRoutingCompletionGroup, AcceptsArgs: true},
		{Command: "--yes", Summary: "config 层写入/清除的二次确认", Group: chatRoutingCompletionGroup},
		{Command: "--json", Summary: "show/doctor 的 JSON 输出", Group: chatRoutingCompletionGroup},
	}
}

// chatRoutingResetTargetArgumentCandidates 是 `reset` 第 1 段（目标）候选：
// 作用域、写入层、--yes 三族都可能合法（§5.5）。
func chatRoutingResetTargetArgumentCandidates() []chatSlashCompletionCandidate {
	candidates := chatRoutingScopeArgumentCandidates()
	candidates = append(candidates, chatRoutingLayerArgumentCandidates()...)
	candidates = append(candidates, chatRoutingFlagArgumentCandidates()...)
	return dedupeSlashArgumentCandidates(candidates)
}

func chatRoutingIsScopeToken(token string) bool {
	switch strings.ToLower(strings.TrimSpace(token)) {
	case chatRoutingScopeMain, chatRoutingScopeSub:
		return true
	default:
		return false
	}
}

// chatRoutingKeyArgumentCandidates 给出 `<key>` 段候选（§3.5 键空间，与
// chatRoutingApplyMainKey/ApplySubKey 的 switch 一一对应）。
func chatRoutingKeyArgumentCandidates(session *ChatSession, scope string) []chatSlashCompletionCandidate {
	scope = chatRoutingNormalizeScope(scope)
	candidates := make([]chatSlashCompletionCandidate, 0, 24)
	add := func(key, summary string) {
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command:     key,
			Summary:     summary,
			Group:       chatRoutingCompletionGroup,
			AcceptsArgs: true,
		})
	}

	if scope == chatRoutingScopeSub {
		add("enabled", "开启/关闭子 Agent 路由")
		add("default_difficulty", "默认难度档位")
	} else {
		add("enabled", "开启/关闭主 Agent 路由")
		add("allow_expert", "是否允许 expert 档位")
		add("respect_provider_health", "是否尊重 provider 健康状态")
		add("levels", "允许的难度档位列表（逗号分隔）")
		add("expensive_levels", "昂贵档位列表（逗号分隔）")
		add("default_difficulty", "默认难度档位")
		add("cost_guard_mode", "成本护栏模式（soft|hard）")
		add("max_consecutive_expensive_steps", "连续昂贵步数上限（整数）")
		add("max_invalid_reports_per_turn", "单轮无效上报上限（整数）")
		add("downgrade_confirm_steps", "降档确认步数（整数）")
		add("min_dwell_steps", "最小停留步数（整数）")
	}
	add("level", "糖写法：level <level> <field> <value>")

	prefix := chatRoutingLevelKeyPrefix(scope)
	for _, level := range chatRoutingLevelNames(session, scope) {
		for _, field := range chatRoutingCompletionProfileFields {
			add(prefix+level+"."+field, "键路径直达（"+field+"）")
		}
	}
	return dedupeSlashArgumentCandidates(candidates)
}

// chatRoutingFieldArgumentCandidates 给出糖写法 `level <level> <field>` 的字段候选
// （可写字段集合，与 panel/profile 写回一致；`candidates` 只读不列）。
func chatRoutingFieldArgumentCandidates() []chatSlashCompletionCandidate {
	out := make([]chatSlashCompletionCandidate, 0, len(chatRoutingCompletionProfileFields))
	for _, field := range chatRoutingCompletionProfileFields {
		out = append(out, chatSlashCompletionCandidate{
			Command:     field,
			Summary:     "字段",
			Group:       chatRoutingCompletionGroup,
			AcceptsArgs: true,
		})
	}
	return out
}

// chatRoutingValueArgumentCandidates 给出 `<key> <value>` / `level <level> <field> <value>`
// 两种写法下的取值候选：键路径与字段名都归一为「字段」再取同源建议值（§I-3）。
func chatRoutingValueArgumentCandidates(session *ChatSession, scope, level, keyOrField string) []chatSlashCompletionCandidate {
	switch strings.ToLower(strings.TrimSpace(keyOrField)) {
	case "enabled", "allow_expert", "respect_provider_health":
		return chatRoutingOnOffValueCandidates()
	case "cost_guard_mode":
		return chatRoutingValueCandidatesFromStrings([]string{"soft", "hard"})
	case "default_difficulty":
		return chatRoutingLevelArgumentCandidates(session, scope)
	case "levels", "expensive_levels":
		return chatRoutingLevelArgumentCandidates(session, scope)
	default:
		field := chatRoutingCompletionFieldFor(session, scope, keyOrField)
		if field == "" {
			return nil
		}
		return chatRoutingValueCandidatesFromStrings(chatRoutingPanelValueOptionsForLevel(session, scope, level, field))
	}
}

// chatRoutingLevelFromKey 从键路径里取档位（`profiles.hard.model` → hard；
// 非键路径/无档位返回空串）。
func chatRoutingLevelFromKey(scope, key string) string {
	scope = chatRoutingNormalizeScope(scope)
	if level, _, ok := chatRoutingSplitLevelKey(strings.ToLower(strings.TrimSpace(key)), chatRoutingLevelKeyPrefix(scope)); ok {
		return level
	}
	return ""
}

// chatRoutingCompletionFieldFor 把「键」或「字段」归一为字段名（无匹配返回空）。
func chatRoutingCompletionFieldFor(session *ChatSession, scope, keyOrField string) string {
	value := strings.ToLower(strings.TrimSpace(keyOrField))
	if value == "" {
		return ""
	}
	scope = chatRoutingNormalizeScope(scope)
	if _, field, ok := chatRoutingSplitLevelKey(value, chatRoutingLevelKeyPrefix(scope)); ok {
		return field
	}
	for _, field := range chatRoutingCompletionProfileFields {
		if field == value {
			return field
		}
	}
	return ""
}

func chatRoutingOnOffValueCandidates() []chatSlashCompletionCandidate {
	return chatRoutingValueCandidatesFromStrings([]string{"on", "off"})
}

func chatRoutingValueCandidatesFromStrings(values []string) []chatSlashCompletionCandidate {
	out := make([]chatSlashCompletionCandidate, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, chatSlashCompletionCandidate{
			Command: value,
			Summary: "路由取值",
			Group:   chatRoutingCompletionGroup,
		})
	}
	return dedupeSlashArgumentCandidates(out)
}

// chatRoutingLevelArgumentCandidates 给出档位候选：生效档位（同源投影）优先，
// 再补齐内置四档（与面板第一级一致）。
func chatRoutingLevelArgumentCandidates(session *ChatSession, scope string) []chatSlashCompletionCandidate {
	scope = chatRoutingNormalizeScope(scope)
	out := make([]chatSlashCompletionCandidate, 0, len(chatRoutingPanelBuiltinLevels)+4)
	seen := make(map[string]struct{})
	add := func(level, summary string) {
		level = strings.ToLower(strings.TrimSpace(level))
		if level == "" {
			return
		}
		if _, exists := seen[level]; exists {
			return
		}
		seen[level] = struct{}{}
		out = append(out, chatSlashCompletionCandidate{
			Command:     level,
			Summary:     summary,
			Group:       chatRoutingCompletionGroup,
			AcceptsArgs: true,
		})
	}
	for _, row := range chatRoutingLevelSummaries(session, scope) {
		summary := "难度档位"
		if model := strings.TrimSpace(row.Model); model != "" {
			summary = "难度档位 · " + model
		}
		add(row.Level, summary)
	}
	for _, level := range chatRoutingPanelBuiltinLevels {
		add(level, "难度档位（内置）")
	}
	return out
}

// chatRoutingLevelNames 返回档位名序列（键路径直达补全用，复用同一档位来源）。
func chatRoutingLevelNames(session *ChatSession, scope string) []string {
	rows := chatRoutingLevelArgumentCandidates(session, scope)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Command)
	}
	return out
}

// chatRoutingCursorTokenIndex 返回光标所在 token 下标；光标位于新增空 token 时
// 返回该 token 下标，位置解析时它不参与「已完成 token」计数。
func chatRoutingCursorTokenIndex(ctx slashArgumentContext) int {
	for index := range ctx.Tokens {
		if slashArgumentCursorInToken(ctx, index) {
			return index
		}
	}
	return len(ctx.Tokens)
}

// chatRoutingCompletedPositionalTokens 返回光标之前已完结的位置参数（跳过 flag），
// 用于判定当前处于「作用域 → 键 → 值」的哪一段。
func chatRoutingCompletedPositionalTokens(ctx slashArgumentContext, index int) []string {
	out := make([]string, 0, index)
	for position := 0; position < index && position < len(ctx.Tokens); position++ {
		text := strings.TrimSpace(ctx.Tokens[position].Text)
		if text == "" || strings.HasPrefix(text, "--") {
			continue
		}
		out = append(out, text)
	}
	return out
}

// chatRoutingMatchCandidates 常规匹配 + 非法值最近似兜底（I-9）。
func chatRoutingMatchCandidates(candidates []chatSlashCompletionCandidate, query string) []chatSlashCompletionCandidate {
	matched := matchSlashArgumentCandidates(candidates, query)
	query = strings.ToLower(strings.TrimSpace(query))
	if len(matched) > 0 || query == "" {
		return matched
	}
	return chatRoutingNearestCandidates(candidates, query)
}

// chatRoutingNearestCandidates 按编辑距离给最近似候选（最多 5 个）。阈值随输入
// 长度放宽，但始终要求「差得不多」，避免把毫不相关的候选塞给用户。
func chatRoutingNearestCandidates(candidates []chatSlashCompletionCandidate, query string) []chatSlashCompletionCandidate {
	type scored struct {
		index    int
		distance int
	}
	threshold := len([]rune(query)) / 2
	if threshold < 2 {
		threshold = 2
	}
	ranked := make([]scored, 0, len(candidates))
	for index, candidate := range candidates {
		name := strings.ToLower(strings.TrimSpace(candidate.Command))
		if name == "" {
			continue
		}
		distance := chatRoutingEditDistance([]rune(query), []rune(name))
		if distance > threshold {
			continue
		}
		ranked = append(ranked, scored{index: index, distance: distance})
	}
	if len(ranked) == 0 {
		return nil
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].distance == ranked[j].distance {
			return ranked[i].index < ranked[j].index
		}
		return ranked[i].distance < ranked[j].distance
	})
	if len(ranked) > 5 {
		ranked = ranked[:5]
	}
	out := make([]chatSlashCompletionCandidate, 0, len(ranked))
	for _, item := range ranked {
		out = append(out, candidates[item.index])
	}
	return out
}

// chatRoutingEditDistance 是标准 Levenshtein 距离（补全兜底用，规模极小）。
func chatRoutingEditDistance(left, right []rune) int {
	if len(left) == 0 {
		return len(right)
	}
	if len(right) == 0 {
		return len(left)
	}
	previous := make([]int, len(right)+1)
	current := make([]int, len(right)+1)
	for index := range previous {
		previous[index] = index
	}
	for i := 1; i <= len(left); i++ {
		current[0] = i
		for j := 1; j <= len(right); j++ {
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			best := previous[j-1] + cost
			if candidate := previous[j] + 1; candidate < best {
				best = candidate
			}
			if candidate := current[j-1] + 1; candidate < best {
				best = candidate
			}
			current[j] = best
		}
		previous, current = current, previous
	}
	return previous[len(right)]
}
