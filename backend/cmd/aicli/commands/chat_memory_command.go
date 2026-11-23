package commands

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorypolicy"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

// handleMemoryCommand implements the /memory slash command.
//
// Usage:
//
//	/memory                     show status
//	/memory status              show status
//	/memory add <text>          append a durable project note
//	/memory note <text>         alias of add
//	/memory flush <text>        alias of add
//	/memory list [n] [--source-kind <kind>] list recent active memory records
//	/memory search <query> [--source-kind <kind>] keyword search
func handleMemoryCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}

	store, err := openChatProjectMemoryStore(session)
	if err != nil {
		fmt.Printf("错误: 无法打开项目记忆: %v\n", err)
		return false
	}
	defer store.Close()

	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" || strings.EqualFold(firstToken(arg), "status") {
		printProjectMemoryStatus(store)
		return false
	}

	verb, rest := splitFirstToken(arg)
	switch strings.ToLower(verb) {
	case "core":
		renderLegacyMemoryResult(session, executeChatMemoryCoreCommand(store, rest, session))
		return false
	case "add", "note", "flush", "remember":
		text := strings.TrimSpace(rest)
		if text == "" {
			fmt.Println("用法: /memory add <text>")
			return false
		}
		note, err := store.Append(memorystore.AppendNoteOptions{
			Text:      text,
			Source:    "manual",
			SessionID: chatSessionID(session),
		})
		if err != nil {
			fmt.Printf("错误: 写入项目记忆失败: %v\n", err)
			return false
		}
		fmt.Printf("提示: 已写入项目记忆 id=%s\n%s\n", note.ID, summarizeMemoryNote(note.Text, 160))
		return false

	case "list", "ls", "recent":
		limit, sourceKind, valid := parseChatMemoryListArgs(rest)
		if !valid {
			fmt.Println("用法: /memory list [n] [--source-kind <kind>]")
			return false
		}
		notes, err := store.ListWithOptions(memorystore.ListOptions{Limit: limit, SourceKind: sourceKind})
		if err != nil {
			fmt.Printf("错误: 读取项目记忆失败: %v\n", err)
			return false
		}
		if len(notes) == 0 {
			fmt.Println("项目记忆为空。使用 /memory add <text> 写入。")
			return false
		}
		header := fmt.Sprintf("最近 %d 条项目记忆（root=%s）:", len(notes), store.Root())
		if sourceKind != "" {
			header = fmt.Sprintf("最近 %d 条项目记忆（source_kind=%s root=%s）:",
				len(notes), safeMemorySourceKindLabel(sourceKind), store.Root())
		}
		fmt.Println(header)
		for i, note := range notes {
			tags := ""
			if len(note.Tags) > 0 {
				tags = " [" + strings.Join(note.Tags, ", ") + "]"
			}
			if source := safeMemorySourceKindLabel(note.Source); source != "" && !strings.EqualFold(source, "manual") {
				tags = " [source=" + source + "]" + tags
			}
			fmt.Printf("%d. %s%s\n   %s\n", i+1, note.ID, tags, summarizeMemoryNote(note.Text, 200))
		}
		return false

	case "search", "find", "query":
		query, sourceKind, valid := parseChatMemorySearchArgs(rest)
		if !valid {
			fmt.Println("用法: /memory search <query> [--source-kind <kind>]")
			return false
		}
		result, err := store.SearchRecords(context.Background(), memorystore.RecordSearchQuery{
			Text: query, SourceKind: sourceKind, Limit: 8,
		})
		if err != nil {
			fmt.Printf("错误: 搜索项目记忆失败: %v\n", err)
			return false
		}
		if len(result.Items) == 0 {
			fmt.Printf("未找到与 %q 匹配的项目记忆。\n", query)
			return false
		}
		if sourceKind == "" {
			fmt.Printf("项目记忆搜索 %q → %d 条:\n", query, len(result.Items))
		} else {
			fmt.Printf("项目记忆搜索 %q（source_kind=%s）→ %d 条:\n",
				query, safeMemorySourceKindLabel(sourceKind), len(result.Items))
		}
		for i, hit := range result.Items {
			record := hit.Record
			sourceLabel := ""
			if source := safeMemorySourceKindLabel(record.SourceKind); source != "" && !strings.EqualFold(source, "manual") {
				sourceLabel = " source=" + source
			}
			fmt.Printf("%d. id=%s%s match=%s\n   %s\n",
				i+1, record.ID, sourceLabel, safeMemorySourceKindLabel(hit.MatchSource),
				summarizeMemoryNote(record.Text, 200))
		}
		return false

	case "history", "versions":
		id, valid := parseChatMemoryHistoryArgs(rest)
		if !valid {
			renderLegacyMemoryResult(session, commandTextResult("用法: /memory history <id>"))
			return false
		}
		if store.Mode() == "off" {
			renderLegacyMemoryResult(session, commandTextResult("错误: memory history 需要启用 v2 memory mode"))
			return false
		}
		versions, historyMore, err := store.ListRecordVersionsPage(context.Background(), id, 100)
		if err != nil {
			renderLegacyMemoryResult(session, commandTextResult("错误: 无法读取 memory history（记录不存在或历史不可用；内部诊断已隐藏）"))
			return false
		}
		renderLegacyMemoryResult(session, CommandResult{
			Blocks: []RenderBlock{{Document: buildChatMemoryHistoryDocument(id, versions, historyMore)}},
			Action: CommandContinue,
		})
		return false

	case "rollback":
		args, valid := parseChatMemoryRollbackArgs(rest)
		if !valid {
			renderLegacyMemoryResult(session, commandTextResult("用法: /memory rollback <id> <version> --expected-revision <n> --reason <text> --yes [--revert-supersede]"))
			return false
		}
		if store.Mode() == "off" {
			renderLegacyMemoryResult(session, commandTextResult("错误: memory rollback 需要启用 v2 memory mode"))
			return false
		}
		record, err := store.RollbackRecord(context.Background(), args.ID, memorystore.RecordRollbackOptions{
			TargetVersion: args.TargetVersion, ExpectedRevision: args.ExpectedRevision,
			Reason: args.Reason, ChangedBy: "local_cli", RevertSupersede: args.RevertSupersede,
		})
		if err != nil {
			renderLegacyMemoryResult(session, commandTextResult("错误: "+chatMemoryRollbackErrorMessage(err)))
			return false
		}
		renderLegacyMemoryResult(session, CommandResult{
			Blocks: []RenderBlock{{Document: buildChatMemoryRollbackDocument(record)}},
			Action: CommandContinue,
		})
		return false

	default:
		fmt.Println("用法: /memory [status|add <text>|list [n] [--source-kind <kind>]|search <query> [--source-kind <kind>]|history <id>|rollback <id> <version> --expected-revision <n> --reason <text> --yes]")
		return false
	}
}

// renderLegacyMemoryResult keeps history/rollback compatibility calls on the
// same structured output adapter as the primary /memory path. These new
// commands must not grow the chat package's direct-terminal-writer inventory.
func renderLegacyMemoryResult(session *ChatSession, result CommandResult) {
	_ = renderChatCommandResult(session, result, true)
}

func parseChatMemoryListArgs(rest string) (limit int, sourceKind string, valid bool) {
	tokens, sourceKind, valid := parseChatMemorySourceKindOption(rest)
	if !valid {
		return 0, "", false
	}
	limit = 10
	if len(tokens) > 0 {
		if n, err := strconv.Atoi(tokens[0]); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	return limit, sourceKind, true
}

func parseChatMemorySearchArgs(rest string) (query, sourceKind string, valid bool) {
	tokens, sourceKind, valid := parseChatMemorySourceKindOption(rest)
	if !valid || len(tokens) == 0 {
		return "", "", false
	}
	query = strings.TrimSpace(strings.Join(tokens, " "))
	return query, sourceKind, query != ""
}

// parseChatMemorySourceKindOption removes the optional source-kind selector
// from tokenized slash-command arguments. It rejects a missing or repeated
// option rather than silently widening a filtered query.
func parseChatMemorySourceKindOption(rest string) (tokens []string, sourceKind string, valid bool) {
	fields := splitChatCommandFields(rest)
	tokens = make([]string, 0, len(fields))
	valid = true
	specified := false
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		value := ""
		if strings.EqualFold(field, "--source-kind") {
			if i+1 >= len(fields) {
				return nil, "", false
			}
			i++
			value = strings.TrimSpace(fields[i])
		} else if strings.HasPrefix(strings.ToLower(field), "--source-kind=") {
			value = strings.TrimSpace(field[len("--source-kind="):])
		} else {
			tokens = append(tokens, field)
			continue
		}
		if value == "" || specified {
			return nil, "", false
		}
		sourceKind = strings.ToLower(value)
		specified = true
	}
	return tokens, sourceKind, true
}

func printProjectMemoryStatus(store *memorystore.Store) {
	if store == nil {
		fmt.Println("项目记忆: 未配置")
		return
	}
	notes, err := store.List(5)
	if err != nil {
		fmt.Printf("项目记忆 root=%s path=%s\n错误: %v\n", store.Root(), store.Path(), err)
		return
	}
	all, _ := store.List(0)
	fmt.Printf("项目记忆 root=%s\n", store.Root())
	suffix := ""
	if store.Mode() != "off" {
		suffix = fmt.Sprintf(" db=%s mode=%s", store.DBPath(), store.Mode())
	}
	fmt.Printf("notes=%s total=%d%s\n", store.Path(), len(all), suffix)
	if report, statusErr := store.Status(context.Background()); statusErr == nil {
		for _, line := range memoryStoreStatusLines(report) {
			fmt.Println(line)
		}
	}
	if len(notes) == 0 {
		fmt.Println("最近笔记: （空）")
		fmt.Println("提示: /memory add <text> 写入跨会话笔记")
		return
	}
	fmt.Println("最近笔记:")
	for i, note := range notes {
		fmt.Printf("  %d. %s — %s\n", i+1, note.ID, summarizeMemoryNote(note.Text, 120))
	}
}

func openChatProjectMemoryStore(session *ChatSession) (*memorystore.Store, error) {
	root := resolveChatProjectMemoryRoot(session)
	if root == "" {
		return nil, fmt.Errorf("无法解析项目根目录")
	}
	cfg := memorystore.Config{Root: root}
	runtimeConfig := loadRuntimeToolConfig(session.Config, session)
	if runtimeConfig != nil {
		cfg.Mode = runtimeConfig.Memory.Mode
		cfg.StorePath = runtimeConfig.Memory.StorePath
		cfg.InjectionMode = runtimeConfig.Memory.Injection.Mode
		cfg.PinnedBudgetRatio = runtimeConfig.Memory.Injection.PinnedBudgetRatio
		cfg.CategoryBudgetsEnabled = true
		cfg.CategoryBudgets = memorystore.InjectionCategoryBudgets{
			Core:      runtimeConfig.Memory.Injection.Budgets.Core,
			Task:      runtimeConfig.Memory.Injection.Budgets.Task,
			Retrieved: runtimeConfig.Memory.Injection.Budgets.Retrieved,
			Procedure: runtimeConfig.Memory.Injection.Budgets.Procedure,
			Reference: runtimeConfig.Memory.Injection.Budgets.Reference,
		}
		cfg.DisableInjection = !runtimeConfig.Memory.Injection.Enabled
		cfg.DisableManual = !runtimeConfig.Memory.Manual.Enabled
		cfg.RecallTraceEnabled = runtimeConfig.Memory.Injection.RecallTrace.Enabled
		cfg.RecallTraceSampleRate = runtimeConfig.Memory.Injection.RecallTrace.SampleRate
		cfg.Retention = memorypolicy.StoreRetention(runtimeConfig.Memory)
		cfg.StaleBehavior = memorypolicy.StaleBehavior(runtimeConfig.Memory)
		cfg.DisableSecretFilter = memorypolicy.DisableSecretFilter(runtimeConfig.Memory)
		cfg.TurnDigestBatchSize = runtimeConfig.Memory.Generation.TurnDigest.MaxTurnsPerBatch
	}
	return memorystore.New(cfg)
}

func chatMemoryInjectionDiagnosticsBudget(session *ChatSession) (limit, tokenBudget int) {
	limit = 8
	tokenBudget = memorystore.DefaultTokenBudget
	if session == nil {
		return limit, tokenBudget
	}
	runtimeConfig := loadRuntimeToolConfig(session.Config, session)
	if runtimeConfig == nil {
		return limit, tokenBudget
	}
	if configured := runtimeConfig.Memory.Injection.MaxItems; configured > 0 {
		limit = configured
	}
	if configured := runtimeConfig.Memory.Injection.EffectiveTokenBudget(); configured > 0 {
		tokenBudget = configured
	}
	// Diagnostics are intentionally bounded even if a local runtime config
	// requests a larger production injection count.
	if limit > 50 {
		limit = 50
	}
	return limit, tokenBudget
}

func resolveChatProjectMemoryRoot(session *ChatSession) string {
	var runtimeConfig = loadRuntimeToolConfig(nil, session)
	if session != nil {
		runtimeConfig = loadRuntimeToolConfig(session.Config, session)
	}
	projectRoot := strings.TrimSpace(resolveLocalWorkspacePath(runtimeConfig, session))
	if projectRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			projectRoot = cwd
		}
	}
	if projectRoot == "" {
		return ""
	}
	return memorystore.ResolveRoot(projectRoot, "")
}

func chatSessionID(session *ChatSession) string {
	if session == nil {
		return ""
	}
	if session.RuntimeSession != nil {
		return strings.TrimSpace(session.RuntimeSession.ID)
	}
	return ""
}

func summarizeMemoryNote(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if maxRunes <= 0 || text == "" {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "…"
}
