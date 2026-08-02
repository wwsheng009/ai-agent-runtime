package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
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
	// SendObjective requests a chat send of the given objective after the
	// command cell is committed. Only /goal-set-style commands set it: the
	// confirmation document stays the atomic command cell, while the objective
	// request streams through the normal send pipeline as its own turn.
	// Plain/JSON/noninteractive projections ignore the flag; dispatch performs
	// the send for every projection that entered the structured path (JSON
	// mode still uses the legacy handler).
	SendObjective string
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
	if !commandMatches(cmdLower, "/debug") && !commandMatches(cmdLower, "/status") && !commandMatches(cmdLower, "/load") &&
		!commandMatches(cmdLower, "/goal") && !commandMatches(cmdLower, "/memory") && !commandMatches(cmdLower, "/stream") &&
		!commandMatches(cmdLower, "/title") && !commandMatches(cmdLower, "/rename") {
		return CommandResult{}, false, nil
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
		// 成功路径结构化：toggle/set/status 均为文档输出；参数解析错误与
		// nil session 保持 legacy（错误消息需在所有模式可见）。/s /n 快捷
		// 别名仍走 legacy（独立 handler）。
		if session == nil {
			return CommandResult{}, false, nil
		}
		req, err := parseStreamCommandRequest(command)
		if err != nil {
			return CommandResult{}, false, nil
		}
		if req.Action == streamCommandStatus {
			return CommandResult{
				Blocks: []RenderBlock{{Document: buildChatStreamStatusDocument(session)}},
				Action: CommandContinue,
			}, true, nil
		}
		previous := session.Stream
		switch req.Action {
		case streamCommandToggle:
			session.Stream = !session.Stream
		case streamCommandSet:
			session.Stream = req.Value
		}
		warnIfChatSessionSyncFails(session, "toggle stream", syncRuntimeSessionFromChat(session))
		if session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
		if previous != session.Stream {
			persistStreamCommandPreference(session)
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatStreamToggleDocument(session)}},
			Action: CommandContinue,
		}, true, nil
	}

	fields := strings.Fields(strings.TrimSpace(extractCommandArgument(command)))
	if len(fields) != 1 {
		return CommandResult{}, false, nil
	}
	switch strings.ToLower(fields[0]) {
	case "display", "show", "info":
		// §5.5 用户交互例外：/debug 输出捕获触发时刻模型尾部锚点（不进入
		// 编码器因果链）；提交点（submitCommandResult）据此做 Tail 锚定
		// 插入而非普通命令追加。
		if session != nil && session.RuntimeEventBridge != nil {
			session.RuntimeEventBridge.recordInteractionAnchor("debug")
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatDebugDisplayDocument(session)}},
			Action: CommandContinue,
		}, true, nil
	default:
		return CommandResult{}, false, nil
	}
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
