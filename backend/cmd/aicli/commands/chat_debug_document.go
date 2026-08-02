package commands

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type chatDebugDocumentBuilder struct {
	lines []render.Line
}

func buildChatDebugDisplayDocument(session *ChatSession) render.Document {
	if session == nil {
		return render.SingleLineDoc(render.RoleSpan("错误: 当前没有活动会话", string(style.RoleError)))
	}

	var builder chatDebugDocumentBuilder
	builder.appendDocument(ui.SessionInfoDocument(buildChatSessionInfo(session), chatDebugDocumentWidth(session)))
	builder.blank()
	appendChatDebugSessionDetails(&builder, session)

	builder.heading("会话文件与目录:")
	builder.meta("Session:", chatDebugSessionLabel(session))
	builder.meta("Session Store:", chatDebugValueOrNone(currentRuntimeSessionStoreSummary(session)))
	builder.meta("Session File:", chatDebugValueOrNone(currentRuntimeSessionPath(session)))
	builder.meta("Chat Log File:", chatDebugValueOrNone(currentChatLogFile(session)))
	builder.meta("Debug Log File:", chatDebugValueOrNone(currentDebugLogFile(session)))
	builder.meta("HTTP Artifact Dir:", chatDebugValueOrNone(currentRuntimeHTTPArtifactDir(session)))
	builder.meta("Shell Artifact Dir:", chatDebugValueOrNone(currentLocalShellArtifactDir(session)))
	builder.meta("Generated Image Artifact Dir:", chatDebugValueOrNone(currentGeneratedImageArtifactDir(session)))
	builder.meta("Last HTTP Req:", chatDebugValueOrNone(chatDebugLastHTTPArtifactPath(session, true)))
	builder.meta("Last HTTP Resp:", chatDebugValueOrNone(chatDebugLastHTTPArtifactPath(session, false)))
	builder.meta("Last Shell Out:", chatDebugValueOrNone(currentLastLocalShellArtifactPath(session)))
	if session.RuntimeSession != nil {
		preview := session.RuntimeSession.BuildPreview()
		if preview.Title != "" {
			builder.meta("Title:", preview.Title)
		}
		if preview.Summary != "" {
			builder.meta("Summary:", preview.Summary)
		}
		if preview.MessageCount > 0 {
			builder.meta("History:", fmt.Sprintf("%d messages", preview.MessageCount))
		}
	}

	builder.heading("运行时调试:")
	if session.Config != nil {
		builder.meta("AICLI Config Path:", chatDebugValueOrNone(resolveAbsoluteChatPath(session.Config.ConfigFilePath)))
	}
	builder.meta("Profile Root:", chatDebugValueOrNone(resolveAbsoluteChatPath(session.ProfileRoot)))
	builder.meta("Agent Source:", chatDebugValueOrNone(formatChatAgentSourceLine(session)))
	builder.meta("Runtime Config Path:", chatDebugValueOrNone(resolveAbsoluteChatPath(session.RuntimeConfigPath)))
	builder.meta("MCP Config Path:", chatDebugValueOrNone(resolveAbsoluteChatPath(session.MCPConfigPath)))
	builder.meta("Resolved Skill Dirs:", chatDebugJoinedPaths(session.ResolvedSkillDirs))
	builder.meta("Output Format:", chatDebugValueOrNone(session.OutputFormat))
	builder.meta("No Interactive:", chatDebugBool(session.NoInteractive))
	builder.meta("JSON Output:", chatDebugBool(session.JSONOutput))
	builder.meta("JSON Envelope:", chatDebugBool(session.JSONEnvelope))
	builder.meta("MCP Enabled:", chatDebugBool(session.MCPEnabled))
	builder.meta("Debug Mode:", chatDebugBool(session.DebugMode))
	builder.meta("Skills Debug:", chatDebugBool(session.SkillsDebug))
	if session.LocalRuntimeHost == nil && (strings.TrimSpace(string(session.PermissionMode)) != "" || strings.TrimSpace(string(session.ApprovalReuseMode)) != "") {
		builder.meta("Permission Mode:", chatDebugValueOrNone(string(session.PermissionMode)))
		builder.meta("Approval Reuse:", chatDebugValueOrNone(formatChatApprovalReuseMode(session.ApprovalReuseMode)))
	}
	appendChatDebugPermissionLines(&builder, session)
	if session.InputQueue != nil {
		queuedCount, draining := queuedInteractiveInputState(session)
		if queuedCount == 0 && !draining {
			builder.meta("Queued Input:", "0 pending")
		}
	}
	if session.queuedInputDrain && session.InputQueue == nil {
		builder.meta("Queued Input:", "0 pending (draining)")
	}
	if session.Interaction != nil {
		builder.meta("Interaction:", session.Interaction.DebugSummary())
	} else {
		builder.meta("Interaction:", "<none>")
	}
	builder.meta("Agent Target:", chatDebugValueOrNone(strings.TrimSpace(session.SelectedAgentTarget)))
	if session.Surface != nil {
		builder.meta("Surface:", chatDebugBool(session.Surface.Enabled()))
		if table := session.Surface.RowPlanDebugString(); table != "" {
			builder.heading("Row Ownership (stage C):")
			builder.plainLines(strings.Split(strings.TrimSuffix(table, "\n"), "\n"))
		}
		if trace := session.Surface.PaintTraceDebugString(); trace != "" {
			builder.heading("Render Paint Trace:")
			builder.plainLines(strings.Split(strings.TrimSuffix(trace, "\n"), "\n"))
		}
	} else {
		builder.meta("Surface:", "<none>")
	}

	appendChatDebugRoutingLines(&builder, session)
	appendChatDebugPprofLines(&builder)
	builder.heading("AgentControl Registry:")
	builder.plain(chatAgentPanelRegistryLine(session))
	builder.plainLines(chatAgentControlConsistencyLines(session))
	builder.heading("Agent Graph:")
	builder.plainLines(chatAgentGraphLines(session))
	builder.heading("Mailbox Pending:")
	builder.plainLines(chatDebugMailboxLines(session))
	return builder.document()
}

func appendChatDebugSessionDetails(builder *chatDebugDocumentBuilder, session *ChatSession) {
	if descriptor, ok := chatRuntimeExecutorDescriptor(session.ChatExecutor); ok {
		builder.meta("Runtime Core:", fmt.Sprintf("%s contract=v%d", descriptor.Core.Name, descriptor.Core.ContractVersion))
		builder.meta("Runtime Transport:", descriptor.Transport)
	}
	if session.MCPEnabled && session.MCPStatus != nil {
		builder.meta("MCP:", fmt.Sprintf("已启用 (%d 个工具, %d 个 MCP 服务器)",
			session.MCPStatus.ToolCount, session.MCPStatus.MCPCount))
	}
	if session.ProfileName != "" {
		profileValue := session.ProfileName
		if session.ProfileAgent != "" {
			profileValue += fmt.Sprintf(" (agent=%s)", session.ProfileAgent)
		}
		builder.meta("Profile:", profileValue)
	}
	if line := formatChatAgentSourceLine(session); line != "" {
		builder.meta("Agent Source:", line)
	}
	if reasoningEffort := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort); reasoningEffort != "" {
		builder.meta("Reasoning Effort:", reasoningEffort)
	}
	if !chatReasoningOutputEnabled(session) {
		builder.meta("Reasoning Output:", "off")
	}
	if session.LocalRuntimeHost != nil {
		builder.meta("Permission Mode:", string(session.PermissionMode))
		builder.meta("Approval Reuse:", formatChatApprovalReuseMode(session.ApprovalReuseMode))
	}
	if queuedCount, draining := queuedInteractiveInputState(session); queuedCount > 0 || draining {
		value := fmt.Sprintf("%d pending", queuedCount)
		if draining {
			value += " (draining)"
		}
		builder.meta("Queued Input:", value)
	}
	if session.DisableTools {
		builder.meta("Tools:", "disabled")
	} else if session.ToolPolicy != nil {
		if names := session.ToolPolicy.AllowedToolNames(); len(names) > 0 {
			builder.meta("Tools Allowlist:", strings.Join(names, ", "))
		}
	}
	if session.HTTPDebug {
		builder.meta("HTTP Debug:", "on")
	}
	if session.RetryConfig.DisableRetries {
		builder.meta("Retry Mode:", "fail-fast")
	}
	if session.RuntimeSession == nil {
		return
	}
	if generation := runtimeSessionCompactGeneration(session.RuntimeSession); generation > 0 {
		builder.meta("Compact Gen:", fmt.Sprintf("#%d", generation))
		if rootTitle := strings.TrimSpace(runtimeSessionContextString(session.RuntimeSession, runtimechat.ContextCompactRootTitle)); rootTitle != "" {
			builder.meta("Compact Root:", rootTitle)
		}
		if rootID := strings.TrimSpace(runtimeSessionContextString(session.RuntimeSession, runtimechat.ContextCompactRootSessionID)); rootID != "" {
			builder.meta("Compact Root ID:", rootID)
		}
	}
}

func appendChatDebugPermissionLines(builder *chatDebugDocumentBuilder, session *ChatSession) {
	overlay := session.PermissionsOverlay
	builder.meta("Permission Rules:", chatDebugValueOrNone(runtimepolicy.FormatPermissionsOverlaySummary(overlay)))
	if path := strings.TrimSpace(overlay.SourcePath); path != "" {
		builder.meta("Permissions File:", chatDebugValueOrNone(resolveAbsoluteChatPath(path)))
	}
	if len(session.CLIAllowTools) > 0 {
		builder.meta("CLI Allow Tools:", strings.Join(session.CLIAllowTools, ", "))
	}
	if len(session.CLIDenyTools) > 0 {
		builder.meta("CLI Deny Tools:", strings.Join(session.CLIDenyTools, ", "))
	}
	var trust foldertrust.Resolution
	if session.FolderTrust.WorkspaceKey != "" || session.FolderTrust.FeatureEnabled || session.FolderTrust.Source != "" {
		trust = session.FolderTrust
	} else {
		trust = currentFolderTrust()
	}
	builder.meta("Folder Trust:", chatDebugValueOrNone(foldertrust.FormatSummary(trust)))
	if len(overlay.Rules) == 0 {
		return
	}
	names := make([]string, 0, len(overlay.Rules))
	for _, rule := range overlay.Rules {
		name := strings.TrimSpace(rule.Name)
		if name == "" {
			name = string(rule.Decision)
		}
		names = append(names, name)
	}
	builder.meta("Permission Rule Names:", strings.Join(names, ", "))
}

func appendChatDebugRoutingLines(builder *chatDebugDocumentBuilder, session *ChatSession) {
	appendChatRoutingConfigDocument(builder, "Subagent Routing", localChatSubagentRoutingConfig(session), "subagent")
	teamSource := "subagent_inherited"
	if session.Config != nil && session.Config.AICLI != nil && session.Config.AICLI.Teams != nil && session.Config.AICLI.Teams.Routing != nil {
		teamSource = "team_independent"
	}
	appendChatRoutingConfigDocument(builder, "Team Routing", localChatTeamRoutingConfig(session), teamSource)
}

func appendChatRoutingConfigDocument(builder *chatDebugDocumentBuilder, title string, routing *config.AICLISubagentRoutingConfig, source string) {
	builder.heading(title + ":")
	if strings.TrimSpace(source) != "" {
		builder.meta("Routing Source:", source)
	}
	builder.meta("Routing Enabled:", chatDebugBool(modelrouting.RoutingEnabled(routing)))
	builder.meta("Compatibility:", modelrouting.CompatibilityMode(routing))
	builder.meta("Default Difficulty:", modelrouting.DefaultDifficulty(routing))
	builder.meta("Inherit Parent:", chatDebugBool(modelrouting.InheritParentWhenMissing(routing)))
	builder.meta("Validate Models:", chatDebugBool(modelrouting.ValidateModelCapabilities(routing)))
	builder.meta("Reasoning Policy:", modelrouting.UnsupportedReasoningPolicy(routing))
	if routing == nil {
		builder.meta("Levels:", "<none>")
		builder.meta("Roles:", "<none>")
		return
	}
	builder.meta("Provider Override:", chatDebugBool(routing.AllowExplicitProviderOverride))
	builder.meta("Model Override:", chatDebugBool(routing.AllowExplicitModelOverride))
	builder.meta("Reasoning Override:", chatDebugBool(routing.AllowExplicitReasoningOverride))
	builder.meta("Expert Limit:", strconv.Itoa(routing.MaxExpertConcurrency))
	if len(routing.AllowedProviderOverrides) > 0 {
		builder.meta("Allowed Providers:", strings.Join(routing.AllowedProviderOverrides, ", "))
	}
	if len(routing.AllowedModelOverrides) > 0 {
		builder.meta("Allowed Models:", strings.Join(routing.AllowedModelOverrides, ", "))
	}

	levelNames := sortedChatRouteProfileNames(routing.Levels)
	if len(levelNames) == 0 {
		builder.meta("Levels:", "<none>")
	} else {
		builder.meta("Levels:", fmt.Sprintf("%d configured", len(levelNames)))
		for _, level := range levelNames {
			builder.plain(fmt.Sprintf("  - %s: %s", level, chatRouteProfileSummary(routing.Levels[level])))
		}
	}

	if len(routing.Roles) == 0 {
		builder.meta("Roles:", "<none>")
		return
	}
	roleNames := make([]string, 0, len(routing.Roles))
	for role := range routing.Roles {
		roleNames = append(roleNames, role)
	}
	sort.Strings(roleNames)
	builder.meta("Roles:", fmt.Sprintf("%d configured", len(roleNames)))
	for _, role := range roleNames {
		levels := routing.Roles[role]
		for _, level := range sortedChatRouteProfileNames(levels) {
			builder.plain(fmt.Sprintf("  - %s.%s: %s", role, level, chatRouteProfileSummary(levels[level])))
		}
	}
}

func sortedChatRouteProfileNames(profiles map[string]config.AICLISubagentRouteProfile) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func chatDebugDocumentWidth(session *ChatSession) int {
	if session != nil && session.Surface != nil && session.Surface.Enabled() {
		if width, _ := session.Surface.ActiveBandViewportSize(); width > 0 {
			return width
		}
	}
	if width := ui.GetTerminalWidth(); width > 0 {
		return width
	}
	return 80
}

func (b *chatDebugDocumentBuilder) appendDocument(doc render.Document) {
	lines := renderDocumentLines(doc)
	for len(lines) > 0 && chatDebugLineEmpty(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && chatDebugLineEmpty(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	b.lines = append(b.lines, lines...)
}

func (b *chatDebugDocumentBuilder) heading(text string) {
	b.lines = append(b.lines, render.Line{Spans: []render.Span{{
		Text:  ui.SanitizeTerminalText(text),
		Style: render.Style{Role: string(style.RoleInfo), Bold: true},
	}}})
}

func (b *chatDebugDocumentBuilder) meta(label, value string) {
	label = strings.Join(strings.Fields(ui.SanitizeTerminalText(label)), " ")
	if strings.TrimSpace(label) == "" {
		return
	}
	pad := chatSessionMetaLabelWidth - ui.DisplayWidth(label)
	if pad < 0 {
		pad = 0
	}
	b.lines = append(b.lines, render.Line{Spans: []render.Span{
		{Text: label + strings.Repeat(" ", pad), Style: render.Style{Role: string(style.RoleMetaLabel), Bold: true}},
		{Text: " " + value, Style: render.Style{Role: string(style.RoleTextSecondary)}},
	}})
}

func (b *chatDebugDocumentBuilder) plain(text string) {
	b.lines = append(b.lines, render.Line{Spans: []render.Span{{
		Text:  ui.SanitizeTerminalText(text),
		Style: render.Style{Role: string(style.RoleTextSecondary)},
	}}})
}

func (b *chatDebugDocumentBuilder) plainLines(lines []string) {
	for _, line := range lines {
		b.plain(line)
	}
}

func (b *chatDebugDocumentBuilder) blank() {
	if len(b.lines) == 0 || chatDebugLineEmpty(b.lines[len(b.lines)-1]) {
		return
	}
	b.lines = append(b.lines, render.Line{})
}

func (b *chatDebugDocumentBuilder) document() render.Document {
	lines := append([]render.Line(nil), b.lines...)
	for len(lines) > 0 && chatDebugLineEmpty(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && chatDebugLineEmpty(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return render.Document{}
	}
	return render.LinesDoc(lines...)
}

func renderDocumentLines(doc render.Document) []render.Line {
	var lines []render.Line
	for _, block := range doc.Blocks {
		lines = append(lines, block.Lines...)
	}
	return lines
}

func chatDebugLineEmpty(line render.Line) bool {
	for _, span := range line.Spans {
		if strings.TrimSpace(span.Text) != "" {
			return false
		}
	}
	return true
}
