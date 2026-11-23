package commands

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

const chatMemoryCoreUsage = "用法: /memory core [list [active|candidate|stale|archived]|rebuild|activate <id> --yes|archive <id> --reason <text> --yes]"

// executeChatMemoryCoreCommand is the shared Core command implementation for
// the structured and legacy /memory entry points. It only operates on the
// already-resolved project store and always returns a renderer-neutral result.
func executeChatMemoryCoreCommand(store *memorystore.Store, args string, session *ChatSession) CommandResult {
	if store == nil {
		return commandTextResult("错误: 项目记忆未配置")
	}
	if store.Mode() == "off" {
		return commandTextResult("错误: memory core 需要启用 v2 memory mode（shadow/on）")
	}

	action, rest := splitFirstToken(strings.TrimSpace(args))
	switch strings.ToLower(action) {
	case "":
		return executeChatMemoryCoreOverview(store)
	case "list":
		status, valid := parseChatMemoryCoreListArgs(rest)
		if !valid {
			return commandTextResult("用法: /memory core list [active|candidate|stale|archived]")
		}
		return executeChatMemoryCoreList(store, status)
	case "activate":
		id, valid := parseChatMemoryCoreActivateArgs(rest)
		if !valid {
			return commandTextResult("用法: /memory core activate <id> --yes")
		}
		core, err := store.ActivateCore(context.Background(), id, "local_cli")
		if err != nil {
			return chatMemoryCoreOperationError("激活", err)
		}
		return commandTextResult(fmt.Sprintf(
			"提示: 已激活 project Core id=%s version=%d",
			safeMemoryHistoryField(core.ID, 120), core.Version,
		))
	case "archive":
		id, reason, valid := parseChatMemoryCoreArchiveArgs(rest)
		if !valid {
			return commandTextResult("用法: /memory core archive <id> --reason <text> --yes")
		}
		if err := store.ArchiveCore(context.Background(), id, "local_cli", reason); err != nil {
			return chatMemoryCoreOperationError("归档", err)
		}
		return commandTextResult(fmt.Sprintf(
			"提示: 已归档 project Core id=%s",
			safeMemoryHistoryField(id, 120),
		))
	case "rebuild":
		if strings.TrimSpace(rest) != "" {
			return commandTextResult("用法: /memory core rebuild")
		}
		return executeChatMemoryCoreRebuild(store, chatMemoryCoreRebuildConfig(session, store))
	default:
		return commandTextResult(chatMemoryCoreUsage)
	}
}

func chatMemoryCoreRebuildConfig(session *ChatSession, store *memorystore.Store) memoryruntime.Config {
	cfg := memoryruntime.Config{}
	if store != nil {
		cfg.Mode = store.Mode()
		cfg.ProjectKey = store.ProjectKey()
	}
	if session != nil {
		if runtimeConfig := loadRuntimeToolConfig(session.Config, session); runtimeConfig != nil {
			rebuild := runtimeConfig.Memory.Core.Rebuild
			cfg.CoreRebuildMaxRecords = rebuild.MaxRecords
			cfg.CoreRebuildMaxUsageRows = rebuild.MaxUsageRows
			cfg.CoreRebuildTokenBudget = rebuild.TokenBudget
		}
	}
	return cfg
}

func executeChatMemoryCoreRebuild(store *memorystore.Store, cfg memoryruntime.Config) CommandResult {
	result, err := memoryruntime.NewCoreRebuilder(store, cfg).Run(context.Background())
	if err != nil {
		return commandTextResult("错误: Core 候选重建失败（内部诊断已隐藏；active Core 未更改）")
	}
	if result.Candidate != nil {
		candidate := result.Candidate
		return commandTextResult(fmt.Sprintf(
			"提示: 已创建待审核 project Core 候选 id=%s version=%d sources=%d tokens=%d；不会自动激活，active Core 未更改",
			safeMemoryHistoryField(candidate.ID, 120), candidate.Version,
			len(candidate.SourceIDs), candidate.TokenEstimate,
		))
	}
	if result.Skipped == "no_safe_sources" {
		return commandTextResult(fmt.Sprintf(
			"提示: 没有符合条件的安全来源，未创建候选（job=%s status=%s）；active Core 未更改",
			safeMemoryHistoryField(result.JobID, 120), safeMemoryHistoryField(string(result.JobStatus), 32),
		))
	}
	return commandTextResult(fmt.Sprintf(
		"提示: Core rebuild 未取得执行权（job=%s status=%s）；active Core 未更改",
		safeMemoryHistoryField(result.JobID, 120), safeMemoryHistoryField(string(result.JobStatus), 32),
	))
}

func executeChatMemoryCoreOverview(store *memorystore.Store) CommandResult {
	core, err := store.GetActiveCore(context.Background(), memorystore.ScopeProject, "")
	switch {
	case err == nil:
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatMemoryCoreDocument(core)}},
			Action: CommandContinue,
		}
	case errors.Is(err, sql.ErrNoRows):
		candidates, listErr := store.ListCore(context.Background(), memorystore.CoreMemoryFilter{
			ScopeKind: memorystore.ScopeProject,
			Status:    memorystore.CoreStatusCandidate,
			Limit:     5,
		})
		if listErr != nil {
			return commandTextResult("错误: 无法读取 Core 候选（内部诊断已隐藏）")
		}
		if len(candidates) == 0 {
			return commandTextResult("当前没有 active Core 或候选。可运行 /memory core rebuild 生成待审核候选。")
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatMemoryCoreListDocument(
				memorystore.CoreStatusCandidate, candidates, "当前没有 active Core；候选摘要",
			)}},
			Action: CommandContinue,
		}
	case errors.Is(err, memorystore.ErrCoreSourceIneligible):
		// An invalid active Core must not be shown as if it were still usable,
		// nor silently replaced by a candidate preview.
		return commandTextResult("错误: active Core 来源已失效；已拒绝显示该 Core（请重新审核候选）")
	default:
		return commandTextResult("错误: 无法读取 active Core（内部诊断已隐藏）")
	}
}

func executeChatMemoryCoreList(store *memorystore.Store, status string) CommandResult {
	if status == memorystore.CoreStatusActive {
		core, err := store.GetActiveCore(context.Background(), memorystore.ScopeProject, "")
		if errors.Is(err, sql.ErrNoRows) {
			return CommandResult{
				Blocks: []RenderBlock{{Document: buildChatMemoryCoreListDocument(status, nil, "Core")}},
				Action: CommandContinue,
			}
		}
		if err != nil {
			return chatMemoryCoreOperationError("读取", err)
		}
		return CommandResult{
			Blocks: []RenderBlock{{Document: buildChatMemoryCoreListDocument(status, []memorystore.CoreMemory{core}, "Core")}},
			Action: CommandContinue,
		}
	}

	cores, err := store.ListCore(context.Background(), memorystore.CoreMemoryFilter{
		ScopeKind: memorystore.ScopeProject,
		Status:    status,
		Limit:     20,
	})
	if err != nil {
		return commandTextResult("错误: 无法读取 Core 列表（内部诊断已隐藏）")
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildChatMemoryCoreListDocument(status, cores, "Core")}},
		Action: CommandContinue,
	}
}

func parseChatMemoryCoreListArgs(args string) (string, bool) {
	fields := splitChatCommandFields(args)
	if len(fields) > 1 {
		return "", false
	}
	if len(fields) == 0 {
		return memorystore.CoreStatusActive, true
	}
	status := strings.ToLower(strings.TrimSpace(fields[0]))
	switch status {
	case memorystore.CoreStatusActive, memorystore.CoreStatusCandidate,
		memorystore.CoreStatusStale, memorystore.CoreStatusArchived:
		return status, true
	default:
		return "", false
	}
}

func parseChatMemoryCoreActivateArgs(args string) (string, bool) {
	fields := splitChatCommandFields(args)
	if len(fields) != 2 || !strings.EqualFold(fields[1], "--yes") {
		return "", false
	}
	id := strings.TrimSpace(fields[0])
	if id == "" || strings.HasPrefix(id, "--") {
		return "", false
	}
	return id, true
}

func parseChatMemoryCoreArchiveArgs(args string) (id, reason string, valid bool) {
	fields := splitChatCommandFields(args)
	if len(fields) < 4 {
		return "", "", false
	}
	id = strings.TrimSpace(fields[0])
	if id == "" || strings.HasPrefix(id, "--") {
		return "", "", false
	}

	seenReason, seenYes := false, false
	reasonParts := make([]string, 0, 3)
	for index := 1; index < len(fields); index++ {
		switch {
		case strings.EqualFold(fields[index], "--yes"):
			if seenYes {
				return "", "", false
			}
			seenYes = true
		case strings.EqualFold(fields[index], "--reason"):
			if seenReason || index+1 >= len(fields) ||
				strings.HasPrefix(fields[index+1], "--") {
				return "", "", false
			}
			seenReason = true
			index++
			for index < len(fields) && !strings.EqualFold(fields[index], "--yes") {
				if strings.HasPrefix(fields[index], "--") {
					return "", "", false
				}
				reasonParts = append(reasonParts, fields[index])
				index++
			}
			index--
		default:
			return "", "", false
		}
	}
	reason = strings.TrimSpace(strings.Join(reasonParts, " "))
	if !seenReason || !seenYes || reason == "" || utf8.RuneCountInString(reason) > 500 {
		return "", "", false
	}
	return id, reason, true
}

func chatMemoryCoreOperationError(operation string, err error) CommandResult {
	switch {
	case errors.Is(err, memorystore.ErrCoreSourceIneligible):
		return commandTextResult("错误: Core 来源已失效或不再符合要求；" + operation + "已拒绝")
	case errors.Is(err, memorystore.ErrCoreNotCandidate):
		return commandTextResult("错误: 该 Core 不是可激活的候选")
	case errors.Is(err, sql.ErrNoRows):
		return commandTextResult("错误: Core 不存在")
	default:
		return commandTextResult("错误: Core " + operation + "失败（内部诊断已隐藏）")
	}
}

func buildChatMemoryCoreDocument(core memorystore.CoreMemory) render.Document {
	return textLinesDocument([]string{
		fmt.Sprintf("project Core status=%s id=%s version=%d tokens=%d",
			safeMemoryHistoryField(core.Status, 32), safeMemoryHistoryField(core.ID, 120),
			core.Version, core.TokenEstimate),
		fmt.Sprintf("approved_by=%s sources=%d",
			safeMemoryHistoryField(core.ApprovedBy, 80), len(core.SourceIDs)),
		"content: " + safeMemoryHistoryField(core.Content, 600),
	})
}

func buildChatMemoryCoreListDocument(status string, cores []memorystore.CoreMemory, title string) render.Document {
	if len(cores) == 0 {
		return textLinesDocument([]string{fmt.Sprintf("%s status=%s: （无）",
			safeMemoryHistoryField(title, 80), safeMemoryHistoryField(status, 32))})
	}
	lines := []string{fmt.Sprintf("%s status=%s count=%d:",
		safeMemoryHistoryField(title, 80), safeMemoryHistoryField(status, 32), len(cores))}
	for index, core := range cores {
		lines = append(lines,
			fmt.Sprintf("%d. id=%s version=%d tokens=%d generated_by=%s sources=%d",
				index+1, safeMemoryHistoryField(core.ID, 120), core.Version, core.TokenEstimate,
				safeMemoryHistoryField(core.GeneratedBy, 80), len(core.SourceIDs)),
			"   "+safeMemoryHistoryField(core.Content, 320),
		)
	}
	return textLinesDocument(lines)
}
