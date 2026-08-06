package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// CommandAction describes the lifecycle action requested by a structured chat
// command. The zero value keeps the current chat running.
type CommandAction uint8

const (
	CommandContinue CommandAction = iota
	CommandQuit
)

// RenderBlock is one structured output fragment produced by a chat command.
// A CommandResult may contain several fragments, but the command adapter merges
// them into one retained command cell before touching the terminal.
type RenderBlock struct {
	Document render.Document
}

// ResumePickerRequest is the immutable query carried by the typed /resume
// picker effect. It deliberately captures the parsed filter before dispatch:
// the alternate-screen interaction must not borrow mutable session filter state
// while it owns a ScreenLease.
type ResumePickerRequest struct {
	Filter ChatSessionListFilter
}

// BacktrackPickerRequest marks the destructive user-turn selection effect.
// It intentionally carries no mutable transcript data: turns are loaded after
// dispatch, and the eventual mutation is derived from the selected stable
// MessageID only after alternate-screen ownership has been released.
type BacktrackPickerRequest struct{}

// BacktrackApplyRequest is the immutable direct-mutation effect for
// `/backtrack <index> --apply|--submit`. The request is parsed before command
// dispatch and executed only after the unified command gate has claimed the
// command, so the legacy handler cannot regain ownership of the terminal.
// The domain mutation itself is followed by canonical Scene replacement and a
// single retained result cell in applyUnifiedBacktrackRequest.
type BacktrackApplyRequest struct {
	Request runtimechat.BacktrackRequest
}

// ModelPickerRequest is the immutable query carried by the typed /model
// picker effect. It captures the already-parsed explicit provider/model before
// dispatch: the alternate-screen interaction must not borrow mutable session
// state while it owns a ScreenLease. Empty Provider/Model mean the picker must
// ask for that stage; NeedReasoning requests the reasoning-effort stage when
// the interactive mutation did not pin a value and the model card supports it.
type ModelPickerRequest struct {
	Provider      string
	Model         string
	NeedReasoning bool
}

// ThemePickerRequest marks the live-preview theme selection effect. It carries
// no mutable state: the picker snapshots the current theme axes and only its
// confirmed result becomes a unified apply + result cell after lease release.
type ThemePickerRequest struct{}

// SkillPickerRequest marks the skill selection effect. It carries no mutable
// state: the picker lists the function catalog after dispatch, and the
// confirmed skill becomes a composer draft (via RestoreComposerDraft) only
// after alternate-screen ownership has been released.
type SkillPickerRequest struct{}

// ExportPickerRequest marks the export session/format selection effect. It
// carries no mutable state: the picker lists candidate sessions after dispatch,
// and the export runs only after alternate-screen ownership is released.
type ExportPickerRequest struct{}

// CommandResult is the renderer-neutral result of a local chat command.
type CommandResult struct {
	Blocks []RenderBlock
	Action CommandAction
	// ReplayHistory requests a transcript replay after the command cell is
	// committed. Only /load-style commands with a load side effect set it: the
	// confirmation document stays the atomic command cell, while the replayed
	// history is appended through the replay renderer as its own cells.
	// Plain/JSON/noninteractive projections ignore the flag; the replay
	// renderer falls back to plain output on its own.
	ReplayHistory bool
	// OpenTranscript opens the read-only semantic transcript pager after the
	// command has reconciled canonical history into Scene/AppState. It is used
	// only by the unified interactive /history command; replaying an already
	// mounted transcript would duplicate rows instead of giving the user a
	// navigable history view.
	OpenTranscript bool
	// OpenResumePicker requests the typed alternate-screen session picker. It
	// has no document payload: the picker borrows a ScreenLease, publishes its
	// lease-bound state through the UI actor, and only its final result becomes
	// a retained command/replay transaction. The request owns its parsed filter
	// so `/resume --cwd` cannot fall back to a legacy line-reader path.
	OpenResumePicker *ResumePickerRequest
	// OpenBacktrackPicker requests the lease-bound user-turn picker. It has no
	// rendered document because selection, cancellation and a destructive apply
	// are committed only after the alternate screen is released and the primary
	// presenter has recovered.
	OpenBacktrackPicker *BacktrackPickerRequest
	// OpenModelPicker requests the lease-bound provider→model→reasoning picker.
	// It has no document payload: each stage borrows the same alternate screen,
	// and the eventual mutation is applied only after lease release and primary
	// presenter recovery, mirroring the backtrack picker contract.
	OpenModelPicker *ModelPickerRequest
	// OpenThemePicker requests the lease-bound live-preview theme selector. It
	// has no document payload: browsing mutates only the working theme snapshot
	// inside the picker, and the confirmed result is applied after lease release
	// and primary presenter recovery.
	OpenThemePicker *ThemePickerRequest
	// OpenSkillPicker requests the lease-bound skill selector. It has no
	// document payload: the confirmed skill becomes a composer draft
	// (`/skill <name> `) only after lease release and primary presenter recovery.
	OpenSkillPicker *SkillPickerRequest
	// OpenExportPicker requests the lease-bound export session/format selector.
	// It has no document payload: the export runs only after lease release and
	// primary presenter recovery.
	OpenExportPicker *ExportPickerRequest
	// ApplyBacktrack requests the direct destructive transaction. It has no
	// document payload: the mutation must rebuild canonical history before its
	// result cell is committed, and submit/draft effects run only afterwards.
	ApplyBacktrack *BacktrackApplyRequest
	// SendObjective requests a chat send of the given objective after the
	// command cell is committed. Only /goal-set-style commands set it: the
	// confirmation document stays the atomic command cell, while the objective
	// request streams through the normal send pipeline as its own turn.
	// Plain/JSON/noninteractive projections ignore the flag; dispatch performs
	// the send for every projection that entered the structured path (JSON
	// mode still uses the legacy handler).
	SendObjective string
	// RestoreComposerDraft restores a failed-turn prompt after the command cell
	// is committed. This is a typed post-commit UI effect, not terminal output:
	// the actor remains the only composer renderer and refuses to overwrite a
	// draft that appeared while the command result was being committed.
	RestoreComposerDraft string
}

// Document merges all command fragments without rendering them. This is the
// atomic command-to-retained adapter boundary: one result becomes one cell.
func (r CommandResult) Document() render.Document {
	var doc render.Document
	for _, block := range r.Blocks {
		doc.Blocks = append(doc.Blocks, block.Document.Blocks...)
	}
	return doc
}

// tryExecuteStructuredChatCommand recognizes commands that have completed the
// structured-output migration. A matched command must never fall through to its
// legacy stdout handler.
func tryExecuteStructuredChatCommand(session *ChatSession, command string) (CommandResult, bool, error) {
	cmdLower := strings.ToLower(strings.TrimSpace(command))
	// /model is fully migrated: status is a finite read-only report, bare /model
	// opens the typed provider→model→reasoning picker, and explicit mutations
	// apply through the unified command cell. It must be recognized before the
	// broad /model legacy fence so no variant can revive the terminal writer.
	if commandMatches(cmdLower, "/model") && unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredModelCommand(session, command); handled {
			return result, true, nil
		}
	}
	// /theme is fully migrated: read-only queries stay finite documents, select
	// opens the typed live-preview picker, and explicit set variants apply
	// through the unified command cell. It must be recognized before the broad
	// /theme legacy fence so no variant can revive the terminal writer.
	if commandMatches(cmdLower, "/theme") && unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredThemeCommand(session, command); handled {
			return result, true, nil
		}
	}
	// /skills is fully migrated: explicit list queries stay finite documents,
	// bare /skills and /skills select open the typed picker (whose confirmed
	// selection becomes a composer draft), and /skill <name> <prompt> executes
	// through the unified command cell. Both must be recognized before the broad
	// legacy fence so no variant can revive the terminal writer.
	if commandMatches(cmdLower, "/skills") && unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredSkillsMenuCommand(session, command); handled {
			return result, true, nil
		}
	}
	if commandMatches(cmdLower, "/skill") && unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredSkillCommand(session, command); handled {
			return result, true, nil
		}
	}
	// /export is fully migrated: explicit targets/formats apply through the
	// unified command cell, and bare /export opens the typed session/format
	// picker. It must be recognized before the broad /export legacy fence so no
	// variant can revive the terminal writer.
	if commandMatches(cmdLower, "/export") && unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredExportCommand(session, command); handled {
			return result, true, nil
		}
	}
	// /login is fully migrated: the prompter already routes through the unified
	// composer, and the result is rendered as one unified command cell. It must
	// be recognized before the broad /login legacy fence so no variant can
	// revive the terminal writer.
	if commandMatches(cmdLower, "/login") && unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredLoginCommand(session, command); handled {
			return result, true, nil
		}
	}
	// The backtrack picker and apply path are effects, but list/audit/preview
	// requests are finite read-only reports. Claim only those reports here; a
	// bare command, selection request, or any mutation keeps the unified fence.
	if (commandMatches(cmdLower, "/backtrack") || commandMatches(cmdLower, "/rewind")) && unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredBacktrackQueryCommand(session, command); handled {
			return result, true, nil
		}
	}
	// A direct /resume target has no picker interaction: it can restore the
	// canonical session, commit one semantic confirmation cell, then replay
	// history through the existing CommandResult post-commit boundary. Bare
	// /resume and its workspace-filtered form are typed picker effects; direct
	// target restore remains a finite CommandResult + history replay.
	if commandMatches(cmdLower, "/resume") && unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredResumeCommand(session, command); handled {
			return result, true, nil
		}
	}
	// The unified interactive session has no legacy terminal writer. Commands
	// whose old implementation still owns a raw prompt, a fullscreen picker, or
	// Direct terminal output must be claimed here before dispatch clears the prompt or
	// reaches handleCommand. Plain, JSON and --no-interactive projections keep
	// their established command implementations until those commands receive a
	// native semantic interaction model.
	if result, fenced := unifiedInteractiveLegacyCommandFence(session, cmdLower); fenced {
		return result, true, nil
	}
	if commandMatches(cmdLower, "/trust") && unifiedDirectInteractiveOutput(session) {
		return executeStructuredTrustCommand(session, command), true, nil
	}
	if commandMatches(cmdLower, "/agents") && unifiedDirectInteractiveOutput(session) {
		return executeStructuredAgentsCommand(session, command), true, nil
	}
	if commandMatches(cmdLower, "/compact") && unifiedDirectInteractiveOutput(session) {
		return executeStructuredCompactCommand(session, command), true, nil
	}
	if commandMatches(cmdLower, "/retry") && unifiedDirectInteractiveOutput(session) {
		return executeStructuredRetryCommand(session, command), true, nil
	}
	if !commandMatches(cmdLower, "/debug") && !commandMatches(cmdLower, "/status") && !commandMatches(cmdLower, "/load") &&
		!commandMatches(cmdLower, "/goal") && !commandMatches(cmdLower, "/memory") && !commandMatches(cmdLower, "/stream") &&
		cmdLower != "/s" && cmdLower != "/n" && !commandMatches(cmdLower, "/fast") && !commandMatches(cmdLower, "/reasoning") &&
		!commandMatches(cmdLower, "/reasoning_effort") && !commandMatches(cmdLower, "/reasoning-effort") &&
		!commandMatches(cmdLower, "/title") && !commandMatches(cmdLower, "/rename") && !commandMatches(cmdLower, "/function") &&
		!commandMatches(cmdLower, "/describe") && !commandMatches(cmdLower, "/functions") && !commandMatches(cmdLower, "/catalog") &&
		!commandMatches(cmdLower, "/sessions") && !commandMatches(cmdLower, "/help") && !commandMatches(cmdLower, "/?") &&
		!commandMatches(cmdLower, "/new") && cmdLower != "/session" && !commandMatches(cmdLower, "/history") && !commandMatches(cmdLower, "/h") &&
		!commandMatches(cmdLower, "/queue") && !commandMatches(cmdLower, "/attach") &&
		!commandMatches(cmdLower, "/permission-mode") && !commandMatches(cmdLower, "/mode") &&
		!commandMatches(cmdLower, "/approval-reuse") && !commandMatches(cmdLower, "/plan") &&
		!commandMatches(cmdLower, "/timeline") && !commandMatches(cmdLower, "/collab") {
		return CommandResult{}, false, nil
	}

	// Pure slash-command documents are deliberately recognized before the
	// legacy handler calls beginDirectInteractiveOutput. This keeps finite
	// output on the semantic command-cell path and prevents legacy terminal-write
	// helpers (session rows in particular) from reaching a unified terminal.
	if commandMatches(cmdLower, "/help") || commandMatches(cmdLower, "/?") {
		if strings.TrimSpace(extractCommandArgument(command)) != "" {
			return CommandResult{}, false, nil
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatSlashHelpDocument()}},
			Action: CommandContinue,
		}, true, nil
	}

	if commandMatches(cmdLower, "/queue") {
		return executeStructuredQueueCommand(session, command), true, nil
	}

	if commandMatches(cmdLower, "/attach") {
		return executeStructuredAttachmentCommand(session, command), true, nil
	}

	if commandMatches(cmdLower, "/permission-mode") || commandMatches(cmdLower, "/mode") {
		return executeStructuredPermissionModeCommand(session, command), true, nil
	}

	if commandMatches(cmdLower, "/approval-reuse") {
		return executeStructuredApprovalReuseCommand(session, command), true, nil
	}

	if commandMatches(cmdLower, "/plan") {
		return executeStructuredPlanCommand(session, command), true, nil
	}

	if commandMatches(cmdLower, "/timeline") {
		return executeStructuredTimelineCommand(session, command), true, nil
	}

	if commandMatches(cmdLower, "/collab") {
		return executeStructuredCollabCommand(session, command), true, nil
	}

	if commandMatches(cmdLower, "/function") || commandMatches(cmdLower, "/describe") {
		name, jsonOutput := extractCommandArgumentOptions(command)
		if name == "" {
			return CommandResult{
				Blocks: []RenderBlock{{Document: buildChatPlainTextCommandDocument("错误: 需要指定 function 名称\n用法: /function <name> [--json] 或 /describe <name> [--json]")}},
				Action: CommandContinue,
			}, true, nil
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatFunctionDescriptorDocument(session, name, jsonOutput)}},
			Action: CommandContinue,
		}, true, nil
	}

	if commandMatches(cmdLower, "/functions") || commandMatches(cmdLower, "/catalog") {
		prompt, jsonOutput := extractCommandArgumentOptions(command)
		if prompt == "" && jsonOutput {
			return CommandResult{
				Blocks: []RenderBlock{{Document: buildChatFunctionCatalogDocument(session, true)}},
				Action: CommandContinue,
			}, true, nil
		}
		if prompt == "" {
			return CommandResult{
				Blocks: []RenderBlock{{Document: buildChatPlainTextCommandDocument("错误: 需要提供 prompt 预览最终暴露集合\n用法: /functions <prompt> [--json] 或 /catalog <prompt> [--json]")}},
				Action: CommandContinue,
			}, true, nil
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatFunctionExposureDocument(session, prompt, jsonOutput)}},
			Action: CommandContinue,
		}, true, nil
	}

	if commandMatches(cmdLower, "/sessions") {
		if session == nil {
			return CommandResult{}, true, fmt.Errorf("会话管理未启用")
		}
		filter := session.SessionFilter
		filter.Query = strings.TrimSpace(extractCommandArgument(command))
		doc, err := buildChatSessionSummariesDocument(session.SessionManager, session.SessionUserID, currentRuntimeSessionID(session), filter)
		if err != nil {
			return CommandResult{}, true, err
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: doc}},
			Action: CommandContinue,
		}, true, nil
	}

	if cmdLower == "/session" {
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatCurrentSessionDocument(session)}},
			Action: CommandContinue,
		}, true, nil
	}

	if commandMatches(cmdLower, "/new") {
		if strings.TrimSpace(extractCommandArgument(command)) != "" {
			return CommandResult{}, false, nil
		}
		if session == nil {
			return CommandResult{}, true, fmt.Errorf("会话管理未启用")
		}
		if err := createNewRuntimeConversation(session, ""); err != nil {
			return CommandResult{}, true, err
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatNewSessionDocument(session)}},
			Action: CommandContinue,
		}, true, nil
	}

	if commandMatches(cmdLower, "/history") || commandMatches(cmdLower, "/h") {
		if strings.TrimSpace(extractCommandArgument(command)) != "" {
			return CommandResult{}, false, nil
		}
		// A unified session already owns one canonical Scene transcript. Replaying
		// those same cells into the primary viewport would either duplicate them
		// or be a no-op after idempotent reconcile. Seed any just-restored history
		// and open the semantic pager instead, so /history is an actual history
		// reader rather than a one-time startup side effect.
		if unifiedDirectInteractiveOutput(session) && hasVisibleChatHistory(session) {
			printVisibleChatHistory(session, "")
			return CommandResult{Action: CommandContinue, OpenTranscript: true}, true, nil
		}
		// Plain/noninteractive projections retain the established replay behavior.
		// Claim it here so the unified command gate never sends /history to a
		// legacy handler merely to show its empty-state message.
		if printVisibleChatHistory(session, "对话历史") != 0 {
			return CommandResult{}, true, nil
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: render.SingleLineDoc(render.TextSpan("当前会话暂无历史消息"))}},
			Action: CommandContinue,
		}, true, nil
	}

	if commandMatches(cmdLower, "/status") {
		// /status accepts no arguments; the legacy handler reports the
		// parameter error so the message stays visible in every mode.
		if strings.TrimSpace(extractCommandArgument(command)) != "" {
			return CommandResult{}, false, nil
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatStatusDocument(session)}},
			Action: CommandContinue,
		}, true, nil
	}

	if commandMatches(cmdLower, "/load") {
		// /load 参数错误与加载失败保持 legacy：错误消息需在所有模式下可见，
		// 与 /status 带参错误同一设计决策。成功路径结构化：加载副作用在此
		// 执行，确认文档为原子命令 cell；历史回放（逐消息 cell）由 dispatch
		// 在提交确认 cell 后通过 ReplayHistory 触发。
		sessionID := strings.TrimSpace(extractCommandArgument(command))
		if sessionID == "" || session == nil {
			return CommandResult{}, false, nil
		}
		if err := loadRuntimeConversation(session, sessionID); err != nil {
			return CommandResult{}, false, nil
		}
		return CommandResult{
			Blocks:        []RenderBlock{{Document: buildChatLoadDocument(session)}},
			Action:        CommandContinue,
			ReplayHistory: hasVisibleChatHistory(session),
		}, true, nil
	}

	if commandMatches(cmdLower, "/title") || commandMatches(cmdLower, "/rename") {
		// A successful title mutation has no interactive prompt/modal behavior,
		// so it can commit its confirmation as one retained command cell. Keep
		// missing-argument and no-session errors on the legacy path.
		title := strings.TrimSpace(extractCommandArgument(command))
		if title == "" || session == nil || session.RuntimeSession == nil {
			return CommandResult{}, false, nil
		}
		if err := updateChatSessionTitle(session, title); err != nil {
			return CommandResult{}, true, err
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatTitleUpdatedDocument()}},
			Action: CommandContinue,
		}, true, nil
	}

	if commandMatches(cmdLower, "/goal") {
		// 成功路径结构化：状态视图与 clear/pause/resume/complete/设置目标。
		// --json 变体、持久化错误与校验错误保持 legacy（错误消息需在所有
		// 模式可见，JSON 投影不在文档契约内）。设置目标成功后经
		// SendObjective 在确认 cell 提交后触发 AI 目标请求（与 ReplayHistory
		// 同款 post-commit 声明）。
		if session == nil {
			return CommandResult{}, false, nil
		}
		arg := strings.TrimSpace(extractCommandArgument(command))
		switch strings.ToLower(arg) {
		case "", "status":
			goal, ok, err := currentSessionGoal(session)
			if err != nil {
				return CommandResult{}, false, nil
			}
			var goalPtr *runtimegoal.SessionGoal
			if ok {
				goalPtr = goal
			}
			return CommandResult{
				Blocks: []RenderBlock{{Document: buildChatGoalStatusDocument(goalPtr)}},
				Action: CommandContinue,
			}, true, nil
		case "clear":
			result, handled := executeStructuredGoalClear(session)
			return result, handled, nil
		case "pause", "resume", "complete":
			result, handled := executeStructuredGoalStatusChange(session, strings.ToLower(arg))
			return result, handled, nil
		}
		objective, jsonOutput := stripJSONOption(arg)
		if jsonOutput {
			return CommandResult{}, false, nil
		}
		result, handled := executeStructuredGoalSet(session, objective)
		return result, handled, nil
	}

	if commandMatches(cmdLower, "/memory") {
		// 成功路径结构化：status/add/list/search 均为文档输出；参数用法错误、
		// store 打开/读写错误与未知动词保持 legacy（消息需在所有模式可见）。
		if session == nil {
			return CommandResult{}, false, nil
		}
		store, err := openChatProjectMemoryStore(session)
		if err != nil {
			return CommandResult{}, false, nil
		}
		arg := strings.TrimSpace(extractCommandArgument(command))
		if arg == "" || strings.EqualFold(firstToken(arg), "status") {
			doc, ready := buildChatMemoryStatusDocument(store)
			if !ready {
				return CommandResult{}, false, nil
			}
			return CommandResult{
				Blocks: []RenderBlock{{Document: doc}},
				Action: CommandContinue,
			}, true, nil
		}
		verb, rest := splitFirstToken(arg)
		switch strings.ToLower(verb) {
		case "add", "note", "flush", "remember":
			text := strings.TrimSpace(rest)
			if text == "" {
				return CommandResult{}, false, nil
			}
			note, err := store.Append(memorystore.AppendNoteOptions{
				Text:      text,
				Source:    "manual",
				SessionID: chatSessionID(session),
			})
			if err != nil {
				return CommandResult{}, false, nil
			}
			return CommandResult{
				Blocks: []RenderBlock{{Document: buildChatMemoryAddDocument(note)}},
				Action: CommandContinue,
			}, true, nil
		case "list", "ls", "recent":
			limit := 10
			if token := strings.TrimSpace(rest); token != "" {
				if n, err := strconv.Atoi(firstToken(token)); err == nil && n > 0 {
					limit = n
				}
			}
			notes, err := store.List(limit)
			if err != nil {
				return CommandResult{}, false, nil
			}
			return CommandResult{
				Blocks: []RenderBlock{{Document: buildChatMemoryListDocument(notes, limit, store.Root())}},
				Action: CommandContinue,
			}, true, nil
		case "search", "find", "query":
			query := strings.TrimSpace(rest)
			if query == "" {
				return CommandResult{}, false, nil
			}
			hits, err := store.Search(memorystore.SearchOptions{Query: query, Limit: 8})
			if err != nil {
				return CommandResult{}, false, nil
			}
			return CommandResult{
				Blocks: []RenderBlock{{Document: buildChatMemorySearchDocument(hits, query)}},
				Action: CommandContinue,
			}, true, nil
		default:
			return CommandResult{}, false, nil
		}
	}

	if commandMatches(cmdLower, "/stream") {
		return executeStructuredStreamCommand(session, command, nil), true, nil
	}

	if cmdLower == "/s" || cmdLower == "/n" {
		stream := cmdLower == "/s"
		return executeStructuredStreamCommand(session, command, &stream), true, nil
	}

	if commandMatches(cmdLower, "/fast") {
		return executeStructuredFastCommand(session, command), true, nil
	}

	if commandMatches(cmdLower, "/reasoning") {
		return executeStructuredReasoningCommand(session, command), true, nil
	}

	if commandMatches(cmdLower, "/reasoning_effort") || commandMatches(cmdLower, "/reasoning-effort") {
		return executeStructuredReasoningEffortCommand(session, command), true, nil
	}

	if commandMatches(cmdLower, "/debug") {
		if unifiedDirectInteractiveOutput(session) {
			result, handled := tryExecuteStructuredDebugCommand(session, command)
			return result, handled, nil
		}
		fields := strings.Fields(strings.TrimSpace(extractCommandArgument(command)))
		if len(fields) == 1 {
			switch strings.ToLower(fields[0]) {
			case "display", "show", "info":
				// Keep the existing non-unified projection compatible. The
				// unified branch above handles all read-only variants through
				// the CommandResult/Scene pipeline.
				if session != nil && session.RuntimeEventBridge != nil {
					session.RuntimeEventBridge.recordInteractionAnchor("debug")
				}
				return CommandResult{
					Blocks: []RenderBlock{{Document: buildChatDebugDisplayDocument(session)}},
					Action: CommandContinue,
				}, true, nil
			}
		}
	}

	return CommandResult{}, false, nil
}

// tryExecuteStructuredDebugCommand retains finite diagnostics, debug toggles,
// and archive export as one semantic command transaction. None of these
// variants owns a prompt, a background stream, or an alternate screen.
func tryExecuteStructuredDebugCommand(session *ChatSession, command string) (CommandResult, bool) {
	action, opts, err := parseChatDebugCommand(extractCommandArgument(command))
	if err != nil {
		if unifiedDirectInteractiveOutput(session) {
			return commandTextResult("错误: " + err.Error() + "\n" + chatDebugUsageText()), true
		}
		return CommandResult{}, false
	}

	switch action {
	case "on", "off":
		return executeStructuredDebugModeCommand(session, action == "on"), true
	case "status":
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatDebugModeStatusDocument(session)}},
			Action: CommandContinue,
		}, true
	case "routing":
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatDebugRoutingDocument(session)}},
			Action: CommandContinue,
		}, true
	case "display":
		// §5.5 debug display captures the model-tail interaction anchor. The
		// command submission point uses it for tail-anchored insertion rather
		// than appending an unrelated ordinary command cell.
		if session != nil && session.RuntimeEventBridge != nil {
			session.RuntimeEventBridge.recordInteractionAnchor("debug")
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatDebugDisplayDocument(session)}},
			Action: CommandContinue,
		}, true
	case "export":
		result, err := exportChatDebugArchive(session, opts)
		if err != nil {
			return commandErrorResult(err), true
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatDebugArchiveDocument(result)}},
			Action: CommandContinue,
		}, true
	default:
		return CommandResult{}, false
	}
}

func executeStructuredDebugModeCommand(session *ChatSession, enabled bool) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	session.DebugMode = enabled
	if session.Surface != nil {
		session.Surface.SetPaintTraceEnabled(enabled)
	}
	var warnings []error
	if err := syncRuntimeSessionFromChat(session); err != nil {
		warnings = append(warnings, fmt.Errorf("切换 debug mode 后同步会话失败: %w", err))
	}
	return commandResultWithWarnings(buildChatDebugModeMutationDocument(enabled), warnings...)
}

// unifiedInteractiveLegacyCommandFence turns an unported interactive command
// into one retained semantic command cell. It is deliberately a deny-list for
// the remaining complex flows, rather than an environment toggle: once a
// TerminalSession owns the TTY, execution may not revive a legacy prompt,
// alternate-screen writer, or raw stdout output.
func unifiedInteractiveLegacyCommandFence(session *ChatSession, command string) (CommandResult, bool) {
	if !unifiedDirectInteractiveOutput(session) {
		return CommandResult{}, false
	}

	commandName := ""
	switch {
	case commandMatches(command, "/backtrack"), commandMatches(command, "/rewind"):
		commandName = "/backtrack"
	case commandMatches(command, "/resume"):
		commandName = "/resume"
	default:
		return CommandResult{}, false
	}

	return CommandResult{
		Blocks: []RenderBlock{{Document: render.SingleLineDoc(render.TextSpan(
			fmt.Sprintf("错误: %s 正在迁移到统一渲染器，已拒绝旧终端直写", commandName),
		))}},
		Action: CommandContinue,
	}, true
}

// rejectUnifiedInteractiveLegacyCommand protects direct handler invocations
// outside dispatch (for example Esc-triggered backtrack selection). A missing
// coordinator after TerminalSession ownership is intentionally treated as
// handled: renderChatCommandResult then fails closed instead of using stdout.
func rejectUnifiedInteractiveLegacyCommand(session *ChatSession, command string) bool {
	result, fenced := unifiedInteractiveLegacyCommandFence(session, command)
	if !fenced {
		return false
	}
	_ = renderChatCommandResult(session, result, false)
	return true
}

func executeStructuredQueueCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	switch strings.ToLower(strings.TrimSpace(extractCommandArgument(command))) {
	case "", "status":
		count, draining := queuedInteractiveInputState(session)
		state := fmt.Sprintf("%d pending", count)
		if draining {
			state += " (draining)"
		}
		return commandTextResult("当前 queued input: " + state)
	case "clear":
		discarded := discardPendingInteractiveInput(session)
		refreshChatComposerContext(session)
		return commandTextResult(fmt.Sprintf("已清空 queued input: %d", discarded))
	default:
		return commandTextResult("错误: /queue 仅支持空参数或 clear\n用法: /queue 或 /queue clear")
	}
}

func executeStructuredAttachmentCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		if len(session.ImagePaths) == 0 {
			return commandTextResult("当前无待发送图片附件")
		}
		lines := []string{fmt.Sprintf("待发送图片附件 (%d):", len(session.ImagePaths))}
		for index, path := range session.ImagePaths {
			lines = append(lines, fmt.Sprintf("  [%d] %s", index+1, path))
		}
		return commandTextResult(strings.Join(lines, "\n"))
	}
	if strings.EqualFold(arg, "clear") {
		count := len(session.ImagePaths)
		session.ImagePaths = nil
		refreshChatComposerContext(session)
		return commandTextResult(fmt.Sprintf("已清空 %d 个待发送图片附件", count))
	}
	if strings.HasPrefix(strings.ToLower(arg), "remove ") {
		if len(session.ImagePaths) == 0 {
			return commandTextResult("错误: 当前没有可移除的图片附件")
		}
		index, err := strconv.Atoi(strings.TrimSpace(arg[len("remove "):]))
		if err != nil || index < 1 || index > len(session.ImagePaths) {
			return commandTextResult(fmt.Sprintf("错误: 附件序号无效，可选范围为 1-%d", len(session.ImagePaths)))
		}
		removed := session.ImagePaths[index-1]
		session.ImagePaths = append(session.ImagePaths[:index-1], session.ImagePaths[index:]...)
		refreshChatComposerContext(session)
		return commandTextResult(fmt.Sprintf("已移除图片附件: %s (当前剩余 %d 个)", removed, len(session.ImagePaths)))
	}
	path := strings.Trim(arg, `"'`)
	if warnings := llm.ValidateLocalInputImagePaths([]string{path}); len(warnings) > 0 {
		return commandTextResult(fmt.Sprintf("错误: 无法添加图片附件 %q；请确认文件存在、可读且为支持的非 SVG 图片", path))
	}
	for _, existing := range session.ImagePaths {
		if strings.EqualFold(strings.TrimSpace(existing), path) {
			return commandTextResult(fmt.Sprintf("提示: 图片附件已存在: %s", path))
		}
	}
	session.ImagePaths = append(session.ImagePaths, path)
	refreshChatComposerContext(session)
	return commandTextResult(fmt.Sprintf("已添加图片附件: %s (当前共 %d 个)", path, len(session.ImagePaths)))
}

// executeStructuredCompactCommand keeps the synchronous compact mutation and
// its outcome in one Scene-backed command transaction. Compact has no prompt,
// picker, or long-lived terminal effect; the report is therefore safe to
// commit after runtime state and title metadata have been reconciled.
func executeStructuredCompactCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	mode, err := normalizeChatCompactMode(extractCommandArgument(command))
	if err != nil {
		return commandErrorResult(err)
	}
	report, compactErr := runManualChatCompact(session, mode)
	if report != nil {
		applyChatCompactContextUsage(session, report.Result, report.Status, true)
		if report.Result != nil {
			refreshChatTitleMetadata(session)
		}
	}
	if compactErr != nil {
		if report == nil {
			return commandErrorResult(compactErr)
		}
		return commandTextResult(formatChatCompactReport(report) + "\n错误: " + compactErr.Error())
	}
	return commandTextResult(formatChatCompactReport(report))
}

func executeStructuredPermissionModeCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	value := strings.TrimSpace(extractCommandArgument(command))
	if value == "" {
		return commandTextResult(fmt.Sprintf("当前 permission-mode: %s", session.PermissionMode))
	}
	mode, err := parseChatPermissionMode(value, false)
	if err != nil {
		return commandErrorResult(err)
	}
	if mode == "bypass_permissions" {
		return commandTextResult("错误: /permission-mode bypass_permissions 需要确认交互，尚未迁移到统一渲染命令通道。")
	}
	session.PermissionMode = mode
	session.RequestedPermissionMode = string(mode)
	session.EffectivePermissionMode = string(mode)
	if session.ActiveTeam != nil {
		session.ActiveTeam.PermissionMode = mode
	}
	message := fmt.Sprintf("提示: 已切换到 permission-mode=%s", mode)
	if err := syncRuntimeSessionFromChat(session); err != nil {
		message += fmt.Sprintf("\n警告: 切换 permission mode 后同步会话失败: %v", err)
	}
	return commandTextResult(message)
}

func executeStructuredApprovalReuseCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	value := strings.ToLower(strings.TrimSpace(extractCommandArgument(command)))
	if value == "" || value == "status" || value == "list" {
		lines := []string{fmt.Sprintf("当前 approval-reuse: %s", formatChatApprovalReuseMode(session.ApprovalReuseMode))}
		if session.RuntimeEventBridge != nil {
			grants := session.RuntimeEventBridge.approvalGrantStatusLines(time.Now())
			if len(grants) > 0 {
				lines = append(lines, fmt.Sprintf("生效中的审批复用授权: %d", len(grants)))
				lines = append(lines, grants...)
				lines = append(lines, "使用 /approval-reuse clear 可立即撤销全部授权")
				return commandTextResult(strings.Join(lines, "\n"))
			}
		}
		lines = append(lines, "当前没有生效的审批复用授权")
		return commandTextResult(strings.Join(lines, "\n"))
	}
	if value == "clear" || value == "revoke" {
		cleared := 0
		if session.RuntimeEventBridge != nil {
			cleared = session.RuntimeEventBridge.clearApprovalGrants()
		}
		return commandTextResult(fmt.Sprintf("已撤销 approval-reuse 授权: %d", cleared))
	}
	mode, err := parseChatApprovalReuseMode(value)
	if err != nil {
		return commandErrorResult(err)
	}
	session.ApprovalReuseMode = mode
	cleared := 0
	if session.RuntimeEventBridge != nil {
		cleared = session.RuntimeEventBridge.clearApprovalGrants()
	}
	message := fmt.Sprintf("提示: 已切换到 approval-reuse=%s；已撤销旧作用域授权 %d 条", formatChatApprovalReuseMode(mode), cleared)
	if err := syncRuntimeSessionFromChat(session); err != nil {
		message += fmt.Sprintf("\n警告: 切换 approval reuse 后同步会话失败: %v", err)
	}
	return commandTextResult(message)
}

func executeStructuredStreamCommand(session *ChatSession, command string, forced *bool) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	req := streamCommandRequest{}
	if forced != nil {
		req = streamCommandRequest{Action: streamCommandSet, Value: *forced}
	} else {
		parsed, err := parseStreamCommandRequest(command)
		if err != nil {
			return commandTextResult(fmt.Sprintf("错误: %v\n用法: /stream [on|off|toggle|status]", err))
		}
		req = parsed
	}
	if req.Action == streamCommandStatus {
		return commandResultWithWarnings(buildChatStreamStatusDocument(session))
	}

	previous := session.Stream
	if req.Action == streamCommandToggle {
		session.Stream = !session.Stream
	} else {
		session.Stream = req.Value
	}
	var warnings []error
	if err := syncRuntimeSessionFromChat(session); err != nil {
		warnings = append(warnings, fmt.Errorf("切换 stream 后同步会话失败: %w", err))
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	if previous != session.Stream {
		if err := saveStreamCommandPreference(session); err != nil {
			warnings = append(warnings, err)
		}
	}
	return commandResultWithWarnings(buildChatStreamToggleDocument(session), warnings...)
}

func executeStructuredFastCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	req, err := parseFastCommandRequest(command)
	if err != nil {
		return commandTextResult(fmt.Sprintf("错误: %v\n用法: /fast [on|off|toggle|status]", err))
	}
	if req.Action == fastCommandStatus {
		return commandResultWithWarnings(buildChatFastStatusDocument(session))
	}
	if !chatSessionSupportsFastMode(session) {
		protocol := strings.TrimSpace(session.Provider.GetProtocol())
		if protocol == "" {
			protocol = "(unknown)"
		}
		return commandTextResult(fmt.Sprintf("错误: Fast 模式仅支持 codex 协议（当前: %s）", protocol))
	}

	previous := session.FastMode
	if req.Action == fastCommandToggle {
		session.FastMode = !session.FastMode
	} else {
		session.FastMode = req.Value
	}
	var warnings []error
	if err := syncRuntimeSessionFromChat(session); err != nil {
		warnings = append(warnings, fmt.Errorf("切换 fast 后同步会话失败: %w", err))
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	if previous != session.FastMode {
		if err := saveFastCommandPreference(session); err != nil {
			warnings = append(warnings, err)
		}
	}
	return commandResultWithWarnings(buildChatFastToggleDocument(session), warnings...)
}

func executeStructuredReasoningCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	req, err := parseReasoningCommandRequest(command)
	if err != nil {
		return commandTextResult(fmt.Sprintf("错误: %v\n用法: /reasoning [on|off|status]", err))
	}
	if req.Action == reasoningCommandSet {
		session.SuppressReasoningOutput = !req.Value
		if session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
	}
	return commandResultWithWarnings(buildChatReasoningStatusDocument(session))
}

func executeStructuredReasoningEffortCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	req, err := parseReasoningEffortCommandRequest(command)
	if err != nil {
		return commandTextResult(fmt.Sprintf("错误: %v\n用法: /reasoning_effort [status|select|clear|<value>]", err))
	}
	switch req.Action {
	case reasoningEffortCommandStatus:
		return commandResultWithWarnings(buildChatReasoningEffortStatusDocument(session))
	case reasoningEffortCommandSelect:
		return commandTextResult("错误: /reasoning_effort select 需要选择器交互，尚未迁移到统一渲染命令通道。")
	case reasoningEffortCommandClear, reasoningEffortCommandSet:
		raw := req.Value
		explicit := req.Action == reasoningEffortCommandSet
		warnings, err := applyStructuredReasoningEffortSelection(session, raw, explicit)
		if err != nil {
			return commandErrorResult(err)
		}
		return commandResultWithWarnings(buildChatReasoningEffortStatusDocument(session), warnings...)
	default:
		return commandTextResult("错误: 无法识别的 reasoning_effort 操作")
	}
}

// applyStructuredReasoningEffortSelection shares the legacy mutation semantics
// while returning every non-fatal diagnostic to the CommandResult path. It
// must not invoke the legacy warning helpers because unified sessions have no
// stderr writer.
func applyStructuredReasoningEffortSelection(session *ChatSession, raw string, explicit bool) ([]error, error) {
	if session == nil {
		return nil, fmt.Errorf("当前没有活动会话")
	}
	reasoning := runtimetypes.NormalizeReasoningEffort(raw)
	var warnings []error
	if reasoning != "" {
		resolved, warning, err := resolveChatReasoningEffort(session.Provider, effectiveRuntimeModel(session), reasoning, explicit)
		if err != nil {
			return nil, err
		}
		if warning != "" {
			warnings = append(warnings, fmt.Errorf("%s", warning))
		}
		reasoning = resolved
	}

	session.ReasoningEffort = reasoning
	if err := syncRuntimeSessionFromChat(session); err != nil {
		warnings = append(warnings, fmt.Errorf("切换 reasoning_effort 后同步会话失败: %w", err))
	}
	if err := refreshLocalRuntimeAfterModelSelection(session); err != nil {
		warnings = append(warnings, fmt.Errorf("切换 reasoning_effort 后刷新本地 runtime 失败: %w", err))
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	if err := saveReasoningEffortCommandPreference(session); err != nil {
		warnings = append(warnings, err)
	}
	return warnings, nil
}

func commandTextResult(text string) CommandResult {
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildChatPlainTextCommandDocument(text)}},
		Action: CommandContinue,
	}
}

func commandResultWithWarnings(doc render.Document, warnings ...error) CommandResult {
	blocks := []RenderBlock{{Document: doc}}
	for _, warning := range warnings {
		if warning == nil {
			continue
		}
		blocks = append(blocks, RenderBlock{Document: buildChatPlainTextCommandDocument("警告: " + warning.Error())})
	}
	return CommandResult{Blocks: blocks, Action: CommandContinue}
}

func executeStructuredGoalClear(session *ChatSession) (CommandResult, bool) {
	if err := requireGoalPersistence(session); err != nil {
		return CommandResult{}, false
	}
	updated, err := runtimegoal.NewMetadataStore().ClearPersistent(
		context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session))
	if err != nil {
		return CommandResult{}, false
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		return CommandResult{}, false
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildChatGoalClearDocument()}},
		Action: CommandContinue,
	}, true
}

func executeStructuredGoalStatusChange(session *ChatSession, verb string) (CommandResult, bool) {
	var status runtimegoal.Status
	var message string
	switch verb {
	case "pause":
		status, message = runtimegoal.StatusPaused, "Goal 已暂停"
	case "resume":
		status, message = runtimegoal.StatusActive, "Goal 已恢复"
	case "complete":
		status, message = runtimegoal.StatusComplete, "Goal 已标记完成"
	default:
		return CommandResult{}, false
	}
	if err := requireGoalPersistence(session); err != nil {
		return CommandResult{}, false
	}
	store := runtimegoal.NewMetadataStore()
	goal, ok, err := store.Get(session.RuntimeSession)
	if err != nil {
		return CommandResult{}, false
	}
	if !ok || goal == nil {
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatGoalNoneDocument()}},
			Action: CommandContinue,
		}, true
	}
	if err := validateGoalStatusTransition(goal.Status, status); err != nil {
		return CommandResult{}, false
	}
	now := time.Now()
	goal.Status = status
	goal.UpdatedAt = now
	if status == runtimegoal.StatusComplete {
		goal.CompletedAt = &now
		goal.CompletedBy = "user"
		goal.CompletionSummary = ""
	} else {
		goal.CompletedAt = nil
		goal.CompletedBy = ""
		goal.CompletionSummary = ""
	}
	updated, err := store.PutPersistent(
		context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), *goal, runtimegoal.MutationUser)
	if err != nil {
		return CommandResult{}, false
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		return CommandResult{}, false
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildChatGoalMutationDocument(message, *goal)}},
		Action: CommandContinue,
	}, true
}

func executeStructuredGoalSet(session *ChatSession, objective string) (CommandResult, bool) {
	if err := requireGoalPersistence(session); err != nil {
		return CommandResult{}, false
	}
	goal, err := runtimegoal.NewSessionGoal(currentRuntimeSessionID(session), objective, time.Now())
	if err != nil {
		return CommandResult{}, false
	}
	store := runtimegoal.NewMetadataStore()
	replaced := ""
	if existing, ok, err := store.Get(session.RuntimeSession); err != nil {
		return CommandResult{}, false
	} else if ok && existing != nil {
		replaced = fmt.Sprintf("（已替换原 %s goal）", existing.Status)
	}
	updated, err := store.PutPersistent(
		context.Background(), session.SessionManager.GetStorage(), currentRuntimeSessionID(session), goal, runtimegoal.MutationUser)
	if err != nil {
		return CommandResult{}, false
	}
	if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
		return CommandResult{}, false
	}
	return CommandResult{
		Blocks:        []RenderBlock{{Document: buildChatGoalSetDocument(replaced, &goal)}},
		Action:        CommandContinue,
		SendObjective: objective,
	}, true
}

func commandErrorResult(err error) CommandResult {
	message := "错误: structured command failed"
	if err != nil {
		message = "错误: " + err.Error()
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: render.SingleLineDoc(render.TextSpan(message))}},
		Action: CommandContinue,
	}
}
