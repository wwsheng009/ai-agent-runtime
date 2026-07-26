package commands

import (
	"fmt"
	"os"
	"strconv"
	"strings"

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
//	/memory list [n]            list recent notes (default 10)
//	/memory search <query>      keyword search
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

	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" || strings.EqualFold(firstToken(arg), "status") {
		printProjectMemoryStatus(store)
		return false
	}

	verb, rest := splitFirstToken(arg)
	switch strings.ToLower(verb) {
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
		limit := 10
		if token := strings.TrimSpace(rest); token != "" {
			if n, err := strconv.Atoi(firstToken(token)); err == nil && n > 0 {
				limit = n
			}
		}
		notes, err := store.List(limit)
		if err != nil {
			fmt.Printf("错误: 读取项目记忆失败: %v\n", err)
			return false
		}
		if len(notes) == 0 {
			fmt.Println("项目记忆为空。使用 /memory add <text> 写入。")
			return false
		}
		fmt.Printf("最近 %d 条项目记忆（root=%s）:\n", len(notes), store.Root())
		for i, note := range notes {
			tags := ""
			if len(note.Tags) > 0 {
				tags = " [" + strings.Join(note.Tags, ", ") + "]"
			}
			fmt.Printf("%d. %s%s\n   %s\n", i+1, note.ID, tags, summarizeMemoryNote(note.Text, 200))
		}
		return false

	case "search", "find", "query":
		query := strings.TrimSpace(rest)
		if query == "" {
			fmt.Println("用法: /memory search <query>")
			return false
		}
		hits, err := store.Search(memorystore.SearchOptions{Query: query, Limit: 8})
		if err != nil {
			fmt.Printf("错误: 搜索项目记忆失败: %v\n", err)
			return false
		}
		if len(hits) == 0 {
			fmt.Printf("未找到与 %q 匹配的项目记忆。\n", query)
			return false
		}
		fmt.Printf("项目记忆搜索 %q → %d 条:\n", query, len(hits))
		for i, hit := range hits {
			fmt.Printf("%d. score=%.2f id=%s\n   %s\n", i+1, hit.Score, hit.Note.ID, summarizeMemoryNote(hit.Note.Text, 200))
		}
		return false

	default:
		fmt.Println("用法: /memory [status|add <text>|list [n]|search <query>]")
		return false
	}
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
	fmt.Printf("notes=%s total=%d\n", store.Path(), len(all))
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
	return memorystore.New(memorystore.Config{Root: root})
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
