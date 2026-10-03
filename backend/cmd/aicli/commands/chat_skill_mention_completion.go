package commands

import (
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// P2：TUI `$` 技能补全（plan §5 P2 / §4.2 绑定优先）。
//
// 本文件是 composer `$` 弹层的独立控制器：
//   - token 解析只认同光标左侧的 `$name`（名称字符集、env 名单、纯数字、
//     行内代码/围栏代码豁免），与 chat_skill_mentions.go 的词法口径一致；
//   - 候选只列文本类技能（handler/workflow 与 disabled 不出现），同名多路径
//     因无法唯一绑定而跳过，排序沿用 catalog 顺序 + 名称升序；
//   - 唯一命中 Tab 直接插入 `$name ` 并把本次选择的 path 记到会话，
//     skillMentionKnownNames 构造时用该绑定覆盖 fn.sourcePath（P0 解析路径零改动）。
//
// 弹层 owner 与 slash 补全独立，两者互不抢占：composer 依次驱动两个控制器，
// 各自在无有效 token/非 `/` 文本时清理自己的弹层。

const (
	skillMentionCompletionPopupOwner = "skill_mention_completion"
	// skillMentionCompletionKeyHintLine 与 slash 弹层保持同一交互语汇。
	skillMentionCompletionKeyHintLine = "↑↓ 选择 · Tab 补全 · Enter 确认 · Esc 关闭"
	// skillMentionCompletionNoMatchPrefix 是 0 候选项的弹层文案前缀；
	// query 非空时追加 `: $<query>`。
	skillMentionCompletionNoMatchPrefix = "未找到匹配技能"
	// skillMentionCompletionMaxCandidates 是弹层候选上限（超出只显示前 10）。
	skillMentionCompletionMaxCandidates = 10
)

// chatSkillMentionBindings 是会话级的 `$name` 补全绑定表（小写名 → 归一化 path）。
// 由 composer 补全写入、回合注入读取；值类型嵌入 ChatSession，零值可用。
type chatSkillMentionBindings struct {
	mu    sync.Mutex
	paths map[string]string
}

// setSkillMentionBoundPath 记录一次补全选择：name 对应的技能定义路径。
// 空 name/path 视为无绑定（不覆盖既有值）。
func (s *ChatSession) setSkillMentionBoundPath(name, path string) {
	if s == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(name))
	normalized := normalizeSkillMentionPath(path)
	if key == "" || normalized == "" {
		return
	}
	s.skillMentionBindings.mu.Lock()
	defer s.skillMentionBindings.mu.Unlock()
	if s.skillMentionBindings.paths == nil {
		s.skillMentionBindings.paths = make(map[string]string)
	}
	s.skillMentionBindings.paths[key] = normalized
}

// skillMentionBoundPath 返回补全时为 name 绑定的技能路径（归一化）；
// 无绑定返回空串。
func (s *ChatSession) skillMentionBoundPath(name string) string {
	if s == nil {
		return ""
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return ""
	}
	s.skillMentionBindings.mu.Lock()
	defer s.skillMentionBindings.mu.Unlock()
	return s.skillMentionBindings.paths[key]
}

// chatSkillMentionTokenContext 是光标处的 `$` token 词法结果；索引按 rune 计。
type chatSkillMentionTokenContext struct {
	Active     bool
	Query      string
	TokenStart int
	TokenEnd   int
}

// chatSkillMentionCompletionCandidate 是一条可补全的文本技能。
type chatSkillMentionCompletionCandidate struct {
	Name        string
	Path        string
	Description string
}

type chatSkillMentionCompletionState struct {
	Active       bool
	Query        string
	TokenStart   int
	TokenEnd     int
	Candidates   []chatSkillMentionCompletionCandidate
	Selected     int
	CommonPrefix string
}

// detectChatSkillMentionToken 解析光标左侧的 `$name` token（plan §5 P2 行为 1）：
// 从 cursor 向左找最近 `$`，中间的字符必须全部是名称字节；`$` 前一字符是名称
// 字节或 `$`（`a$b`、`$$`）时拒绝；行内代码（该 token 前反引号计数为奇）或
// 围栏代码内拒绝；env 名单与纯数字 token 拒绝。右侧字符不参与。
func detectChatSkillMentionToken(text string, cursor int) chatSkillMentionTokenContext {
	runes := []rune(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if len(runes) == 0 || cursor == 0 {
		return chatSkillMentionTokenContext{}
	}

	dollar := -1
	for i := cursor - 1; i >= 0; i-- {
		switch {
		case runes[i] == '$':
			dollar = i
		case isSkillMentionNameRune(runes[i]):
			continue
		}
		break
	}
	if dollar < 0 {
		return chatSkillMentionTokenContext{}
	}
	if dollar > 0 {
		previous := runes[dollar-1]
		// `a$b`：`$` 前一个字符仍是名称字节 → 不是 token 起点。
		// `$$`：第二个 `$` 紧贴第一个 → 拒绝（常见误输入/占位）。
		if isSkillMentionNameRune(previous) || previous == '$' {
			return chatSkillMentionTokenContext{}
		}
	}

	query := string(runes[dollar+1 : cursor])
	if !isSkillMentionCompletionQueryValid(query) {
		return chatSkillMentionTokenContext{}
	}
	if skillMentionTokenInCode(runes, dollar) {
		return chatSkillMentionTokenContext{}
	}
	return chatSkillMentionTokenContext{
		Active:     true,
		Query:      query,
		TokenStart: dollar,
		TokenEnd:   cursor,
	}
}

func isSkillMentionNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '_' || r == '-':
		return true
	default:
		return false
	}
}

// isSkillMentionCompletionQueryValid 拒绝 env 名单与纯数字 token；其余（含空
// query）都视为有效 token，允许弹层给出候选或“未找到匹配技能”提示。
func isSkillMentionCompletionQueryValid(query string) bool {
	if query == "" {
		return true
	}
	if _, ignored := skillMentionIgnoredNames[strings.ToLower(query)]; ignored {
		return false
	}
	return !isASCIIDigits(query)
}

// skillMentionTokenInCode 报告 `$` 是否落在围栏代码块或行内代码中：
// 逐行统计当前行之前的 ``` 开闭（trim 后前缀判断，与 scanSkillMentionTokens
// 同口径）；当前行内 `$` 前的反引号计数为奇即行内代码。
func skillMentionTokenInCode(runes []rune, dollar int) bool {
	lineStart := dollar
	for lineStart > 0 && runes[lineStart-1] != '\n' {
		lineStart--
	}

	fence := false
	lineBegin := 0
	for lineBegin < lineStart {
		lineEnd := lineBegin
		for lineEnd < lineStart && runes[lineEnd] != '\n' {
			lineEnd++
		}
		if strings.HasPrefix(strings.TrimSpace(string(runes[lineBegin:lineEnd])), "```") {
			fence = !fence
		}
		lineBegin = lineEnd + 1
	}
	if fence {
		return true
	}

	backticks := 0
	for i := lineStart; i < dollar; i++ {
		if runes[i] == '`' {
			backticks++
		}
	}
	return backticks%2 == 1
}

// isSkillMentionCompletableName 判断技能名是否可被 `$` token 完整引用：
// 名称必须全部是 token 字符集，否则补全出的 `$name ` 无法被词法解析
// （例如带空格的显示名永远不会命中，因此不进入候选）。
func isSkillMentionCompletableName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !isSkillMentionNameRune(r) {
			return false
		}
	}
	return true
}

// skillMentionCandidateDescription 取候选弹层的描述：short description 优先，
// 退回 description；都没有时为空（弹层只显示 `$name`）。
func skillMentionCandidateDescription(fn *SkillFunction) string {
	if fn == nil {
		return ""
	}
	if fn.summary != nil {
		if short := strings.TrimSpace(fn.summary.ShortDescription); short != "" {
			return short
		}
		if desc := strings.TrimSpace(fn.summary.Description); desc != "" {
			return desc
		}
	}
	skillItem := fn.resolvedTurnSkill()
	if skillItem == nil {
		return ""
	}
	if short := strings.TrimSpace(skillItem.ShortDescription); short != "" {
		return short
	}
	return strings.TrimSpace(skillItem.Description)
}

// skillMentionCompletionCommonPrefix 计算候选名的公共前缀（大小写不敏感比较，
// 输出保留第一条候选的原始大小写）。空候选或首字符即不同返回空串。
func skillMentionCompletionCommonPrefix(candidates []chatSkillMentionCompletionCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	base := []rune(candidates[0].Name)
	length := len(base)
	for _, candidate := range candidates[1:] {
		other := []rune(candidate.Name)
		if len(other) < length {
			length = len(other)
		}
		for i := 0; i < length; i++ {
			if unicode.ToLower(base[i]) != unicode.ToLower(other[i]) {
				length = i
				break
			}
		}
	}
	if length <= 0 {
		return ""
	}
	return string(base[:length])
}

// ---------------------------------------------------------------------------
// 控制器

type chatSkillMentionCompletionController struct {
	session *ChatSession

	mu                sync.Mutex
	state             chatSkillMentionCompletionState
	text              string
	cursor            int
	selectedName      string
	renderedSignature string
	surfaceEnabled    bool
	editorPasteActive bool
}

func newChatSkillMentionCompletionController(session *ChatSession) *chatSkillMentionCompletionController {
	return &chatSkillMentionCompletionController{
		session: session,
		state: chatSkillMentionCompletionState{
			Selected: -1,
		},
	}
}

// shouldEnableSkillMentionCompletion 是 composer 挂载门控：Surface 可用且
// gate==enabled（off/auto 非交互/auto 未信任项目均不创建控制器、不弹层）。
func shouldEnableSkillMentionCompletion(session *ChatSession) bool {
	if session == nil || session.Surface == nil || !session.Surface.Enabled() {
		return false
	}
	return skillMentionResolveGate(session, true) == skillMentionGateEnabled
}

func (c *chatSkillMentionCompletionController) UpdateSnapshot(snapshot ui.LineEditorSnapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.editorPasteActive = snapshot.PasteActive
	c.applyTextUpdateLocked(snapshot.Text, snapshot.Cursor)
	c.renderLocked()
}

// Clear 重置控制器并清掉本控制器拥有的弹层（composer Close 时调用）。
func (c *chatSkillMentionCompletionController) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetStateLocked()
	c.clearPopupLocked()
}

// Cancel 关闭 active 弹层；无弹层返回 false，交回编辑器其它 Esc 语义。
func (c *chatSkillMentionCompletionController) Cancel() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Active {
		return false
	}
	c.resetStateLocked()
	c.clearPopupLocked()
	return true
}

// Navigate 在 active 弹层内循环移动选中；无候选项时不消费按键。
func (c *chatSkillMentionCompletionController) Navigate(delta int) bool {
	if c == nil || delta == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Active || len(c.state.Candidates) == 0 {
		return false
	}
	selected := c.state.Selected
	if selected < 0 || selected >= len(c.state.Candidates) {
		selected = 0
	}
	selected = (selected + delta) % len(c.state.Candidates)
	if selected < 0 {
		selected += len(c.state.Candidates)
	}
	c.state.Selected = selected
	c.selectedName = c.state.Candidates[selected].Name
	c.renderLocked()
	return true
}

// ApplyCompletion 处理 Tab（plan §5 P2 行为 3）：
//   - 无有效 token → (text, cursor, false)，Tab 回落到 @/plan-mode；
//   - 唯一候选 / 多候选且公共前缀不再加深 → 接受当前选中，插入 `$name ` 并记绑定；
//   - 多候选且 query 可加深到公共前缀 → 只延伸公共前缀（不插空格）；
//   - 0 候选 → handled=true、文本不变（弹层显示未找到匹配技能）。
func (c *chatSkillMentionCompletionController) ApplyCompletion(text string, cursor int) (string, int, bool) {
	if c == nil {
		return text, cursor, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.applyTextUpdateLocked(text, cursor)
	if !c.state.Active {
		c.renderLocked()
		return text, cursor, false
	}
	if len(c.state.Candidates) == 0 {
		c.renderLocked()
		return text, cursor, true
	}
	if len(c.state.Candidates) > 1 &&
		len([]rune(c.state.CommonPrefix)) > len([]rune(c.state.Query)) {
		nextText, nextCursor := applySkillMentionTokenCompletion(text, c.state.TokenStart, c.state.TokenEnd, c.state.CommonPrefix, false)
		c.applyTextUpdateLocked(nextText, nextCursor)
		c.renderLocked()
		return nextText, nextCursor, true
	}
	return c.acceptSelectedLocked(text)
}

// ApplySubmission 处理 Enter：弹层 active 且候选非空时接受当前选中（与 Tab
// 的多候选接受分支一致）；否则交回普通提交（图片令牌裁剪等逻辑保持不变）。
func (c *chatSkillMentionCompletionController) ApplySubmission(text string, cursor int) (string, int, bool) {
	if c == nil {
		return text, cursor, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.applyTextUpdateLocked(text, cursor)
	if !c.state.Active || len(c.state.Candidates) == 0 {
		if !c.state.Active {
			c.renderLocked()
		}
		return text, cursor, false
	}
	return c.acceptSelectedLocked(text)
}

// acceptSelectedLocked 应用当前选中候选：替换 token 为 `$name `（含尾随空格）、
// 记录绑定路径、清理弹层。
func (c *chatSkillMentionCompletionController) acceptSelectedLocked(text string) (string, int, bool) {
	selected := c.selectedCandidateLocked()
	if selected == nil {
		c.renderLocked()
		return text, c.cursor, false
	}
	nextText, nextCursor := applySkillMentionTokenCompletion(text, c.state.TokenStart, c.state.TokenEnd, selected.Name, true)
	c.session.setSkillMentionBoundPath(selected.Name, selected.Path)
	c.applyTextUpdateLocked(nextText, nextCursor)
	c.renderLocked()
	return nextText, nextCursor, true
}

func (c *chatSkillMentionCompletionController) selectedCandidateLocked() *chatSkillMentionCompletionCandidate {
	if c == nil || c.state.Selected < 0 || c.state.Selected >= len(c.state.Candidates) {
		return nil
	}
	return &c.state.Candidates[c.state.Selected]
}

func (c *chatSkillMentionCompletionController) applyTextUpdateLocked(text string, cursor int) {
	c.text = text
	c.cursor = cursor
	c.state = c.buildStateLocked(text, cursor, c.selectedName)
	if c.state.Selected >= 0 && c.state.Selected < len(c.state.Candidates) {
		c.selectedName = c.state.Candidates[c.state.Selected].Name
	} else {
		c.selectedName = ""
	}
}

func (c *chatSkillMentionCompletionController) buildStateLocked(text string, cursor int, previousName string) chatSkillMentionCompletionState {
	context := detectChatSkillMentionToken(text, cursor)
	state := chatSkillMentionCompletionState{
		Query:      context.Query,
		TokenStart: context.TokenStart,
		TokenEnd:   context.TokenEnd,
		Selected:   -1,
	}
	if !context.Active {
		return state
	}
	state.Active = true
	state.Candidates = c.collectCandidatesLocked(context.Query)
	state.CommonPrefix = skillMentionCompletionCommonPrefix(state.Candidates)
	if len(state.Candidates) == 0 {
		return state
	}
	if previousName != "" {
		for i, candidate := range state.Candidates {
			if strings.EqualFold(candidate.Name, previousName) {
				state.Selected = i
				return state
			}
		}
	}
	state.Selected = 0
	return state
}

// collectCandidatesLocked 枚举文本类技能候选（plan §5 P2 行为 2）：
// disabled 过滤、handler/workflow 过滤、同名多路径跳过、query 前缀过滤、
// catalog 顺序 + 名称升序排序、上限 10。
func (c *chatSkillMentionCompletionController) collectCandidatesLocked(query string) []chatSkillMentionCompletionCandidate {
	if c == nil || c.session == nil {
		return nil
	}
	binding := skillMentionBinding(c.session)
	if binding == nil {
		return nil
	}
	disabled := make(map[string]struct{})
	for _, name := range disabledSkillNames(c.session.Config) {
		if key := strings.ToLower(strings.TrimSpace(name)); key != "" {
			disabled[key] = struct{}{}
		}
	}

	byName := make(map[string][]skillMentionCandidate, len(binding.skillFunctions))
	for _, fn := range binding.skillFunctions {
		if fn == nil {
			continue
		}
		name := skillFunctionDisplayName(fn)
		if !isSkillMentionCompletableName(name) {
			continue
		}
		key := strings.ToLower(name)
		if _, isDisabled := disabled[key]; isDisabled {
			continue
		}
		// handler/workflow 技能不进入 `$` 补全（与 resolveMentionedTextSkills
		// 同一文本类口径），避免补出永远不会注入正文的提及。
		if !skillUsesDefaultExecution(fn.resolvedTurnSkill()) {
			continue
		}
		byName[key] = append(byName[key], skillMentionCandidate{
			fn:   fn,
			name: name,
			path: normalizeSkillMentionPath(fn.sourcePath),
		})
	}

	query = strings.ToLower(strings.TrimSpace(query))
	ordered := make([]skillMentionCandidate, 0, len(byName))
	for key, group := range byName {
		if query != "" && !strings.HasPrefix(key, query) {
			continue
		}
		unique := dedupeSkillMentionCandidatesByPath(group)
		if len(unique) != 1 {
			// 同名多路径（去重后 >1）无法唯一绑定：跳过。
			continue
		}
		ordered = append(ordered, unique[0])
	}
	sortSkillMentionCandidates(ordered, skillMentionCatalogOrder(binding))
	if len(ordered) > skillMentionCompletionMaxCandidates {
		ordered = ordered[:skillMentionCompletionMaxCandidates]
	}

	candidates := make([]chatSkillMentionCompletionCandidate, 0, len(ordered))
	for _, entry := range ordered {
		candidates = append(candidates, chatSkillMentionCompletionCandidate{
			Name:        entry.name,
			Path:        entry.path,
			Description: skillMentionCandidateDescription(entry.fn),
		})
	}
	return candidates
}

func (c *chatSkillMentionCompletionController) resetStateLocked() {
	c.state = chatSkillMentionCompletionState{Selected: -1}
	c.text = ""
	c.cursor = 0
	c.selectedName = ""
	c.renderedSignature = ""
	c.surfaceEnabled = false
	c.editorPasteActive = false
}

// applySkillMentionTokenCompletion 用 name 替换 [tokenStart, tokenEnd) 区间的
// `$...` token；withSpace 为 true 时插入 `$name ` 并把光标落在空格之后。
func applySkillMentionTokenCompletion(text string, tokenStart, tokenEnd int, name string, withSpace bool) (string, int) {
	runes := []rune(text)
	if tokenStart < 0 {
		tokenStart = 0
	}
	if tokenStart > len(runes) {
		tokenStart = len(runes)
	}
	if tokenEnd < tokenStart {
		tokenEnd = tokenStart
	}
	if tokenEnd > len(runes) {
		tokenEnd = len(runes)
	}
	insert := "$" + name
	if withSpace {
		insert += " "
	}
	nextText := string(runes[:tokenStart]) + insert + string(runes[tokenEnd:])
	nextCursor := tokenStart + len([]rune(insert))
	return nextText, nextCursor
}

// ---------------------------------------------------------------------------
// 弹层渲染

// renderLocked 与 slash 控制器同构：surface 不可用时不渲染；paste/队列草稿
// 阻塞时清理；签名去重避免每键重发弹层；无行时清理旧弹层。
func (c *chatSkillMentionCompletionController) renderLocked() {
	lines := renderSkillMentionCompletionPopup(c.state, ui.GetTerminalWidth())
	signature := strings.Join(lines, "\n")

	enabled := c.isSurfaceEnabledLocked()
	if !enabled {
		c.surfaceEnabled = false
		return
	}
	if c.isPopupBlockedLocked() {
		if c.renderedSignature != "" {
			c.clearPopupLocked()
		}
		c.renderedSignature = ""
		c.surfaceEnabled = false
		return
	}
	if !c.surfaceEnabled {
		c.renderedSignature = ""
	}
	c.surfaceEnabled = true

	if len(lines) == 0 {
		if c.renderedSignature != "" {
			c.clearPopupLocked()
		}
		c.renderedSignature = ""
		return
	}
	if signature == c.renderedSignature {
		return
	}

	newChatPromptOverlay(c.session).showOwnedPopupBelowPrompt(lines, skillMentionCompletionPopupOwner)
	c.renderedSignature = signature
}

func (c *chatSkillMentionCompletionController) clearPopupLocked() {
	newChatPromptOverlay(c.session).clearOwnedPopup(skillMentionCompletionPopupOwner)
}

func (c *chatSkillMentionCompletionController) isSurfaceEnabledLocked() bool {
	if c == nil || c.session == nil || c.session.Surface == nil {
		return false
	}
	return c.session.Surface.Enabled()
}

func (c *chatSkillMentionCompletionController) isPopupBlockedLocked() bool {
	if c == nil {
		return true
	}
	if c.editorPasteActive {
		return true
	}
	if c.session == nil {
		return false
	}
	if c.session.Interaction != nil && c.session.Interaction.IsPromptPasteActive() {
		return true
	}
	if c.session.InputQueue != nil && c.session.InputQueue.hasDraft() {
		return true
	}
	return false
}

// renderSkillMentionCompletionPopup 渲染弹层行：候选行 `$name — <desc>`
// （无描述只显示 `$name`，选中行以 `>` 标记），末行固定为按键提示；
// 0 候选时显示“未找到匹配技能[: $query]”。
func renderSkillMentionCompletionPopup(state chatSkillMentionCompletionState, width int) []string {
	if !state.Active {
		return nil
	}
	if width <= 0 {
		width = 80
	}
	if len(state.Candidates) == 0 {
		label := skillMentionCompletionNoMatchPrefix
		if query := strings.TrimSpace(state.Query); query != "" {
			label = fmt.Sprintf("%s: $%s", label, query)
		}
		return clampSlashCompletionPopupLines([]string{label}, width)
	}

	lines := make([]string, 0, len(state.Candidates)+1)
	for index, candidate := range state.Candidates {
		marker := " "
		if index == state.Selected {
			marker = ">"
		}
		line := fmt.Sprintf("%s $%s", marker, candidate.Name)
		if description := strings.TrimSpace(candidate.Description); description != "" {
			line += " — " + description
		}
		lines = append(lines, strings.TrimRight(line, " "))
	}
	lines = append(lines, skillMentionCompletionKeyHintLine)
	return clampSlashCompletionPopupLines(lines, width)
}
