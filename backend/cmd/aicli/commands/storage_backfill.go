package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// sessionHistoryDBPath 解析会话历史库路径，与 cmd/session-dedupe 的默认解析同源
// （<user home>/.aicli/sessions/session_history.sqlite；AICLI_SESSION_DB 覆盖）。
func sessionHistoryDBPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("AICLI_SESSION_DB")); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".aicli", "sessions", "session_history.sqlite"), nil
}

// newStorageBackfillCommand 创建 `aicli storage backfill-identities`：
// 会话历史身份回填（session-message-identity-protocol-20261017 §5 Backfill）。
//
// 为存量 session_messages 中无 message_id 的行铸造行级身份（P3 前置迁移）。
// 幂等：已带 id 的行原样保留，重跑安全。不伪造 turn_id——存量行的 turn 归属
// 无法从行本身可靠推导（见 internal/chat/identity_backfill.go）。
func newStorageBackfillCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backfill-identities",
		Short: "为会话历史中无 message_id 的存量行铸造身份（幂等）",
		Long: `为会话历史库（session_messages）中无 message_id 的存量行铸造行级身份。

这是会话消息身份协议（docs/plan/session-message-identity-protocol-20261017.md）
的 P3 前置迁移：协议落地后新行必有 id（创建时授予），存量行经本命令回填后，
canonical 对齐的 substance 启发式才能退役（P4）。

安全约束：
  - 只增 metadata（message_id），不删行、不改内容；
  - 幂等：已带 id 的行原样跳过，重跑安全；
  - 不伪造 turn_id：存量行的 turn 归属无法从行本身可靠推导，读取端按前驱继承展示；
  - 建议先退出正在使用该库的 aicli / runtime-server 进程（离线迁移）。

示例：
  aicli storage backfill-identities
  aicli storage backfill-identities --session <session-id>
  aicli storage backfill-identities --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStorageBackfillIdentities(cmd, storageBackfillOptions{
				session: strings.TrimSpace(stringFlag(cmd, "session")),
				dryRun:  boolFlag(cmd, "dry-run"),
			})
		},
	}
	cmd.Flags().String("session", "", "只回填指定会话（缺省 = 全部会话）")
	cmd.Flags().Bool("dry-run", false, "只统计，不写入")
	return cmd
}

type storageBackfillOptions struct {
	session string
	dryRun  bool
}

func runStorageBackfillIdentities(cmd *cobra.Command, opts storageBackfillOptions) error {
	dbPath, err := sessionHistoryDBPath()
	if err != nil {
		return err
	}
	store, err := runtimechat.NewSQLiteSessionStorage(runtimechat.PersistentSessionStorageConfig{
		Path: dbPath,
	})
	if err != nil {
		return fmt.Errorf("open session history storage: %w", err)
	}
	defer func() { _ = store.CloseStorage() }()

	ctx := context.Background()
	type backfillResult struct {
		sessionID string
		rows      int
		granted   int
		skipped   string
	}
	var results []backfillResult

	collect := func(sessionID string) {
		loaded, err := store.Load(ctx, sessionID)
		if err != nil {
			results = append(results, backfillResult{sessionID: sessionID, skipped: err.Error()})
			return
		}
		granted := 0
		if !opts.dryRun {
			granted = runtimechat.BackfillCanonicalIdentities(loaded.History)
			if granted > 0 {
				if err := store.Update(ctx, loaded); err != nil {
					results = append(results, backfillResult{sessionID: sessionID, skipped: err.Error()})
					return
				}
			}
		} else {
			for index := range loaded.History {
				if strings.TrimSpace(runtimetypes.MessageID(loaded.History[index])) == "" {
					granted++
				}
			}
		}
		results = append(results, backfillResult{sessionID: sessionID, rows: len(loaded.History), granted: granted})
	}

	if opts.session != "" {
		collect(opts.session)
	} else {
		const pageSize = 200
		for offset := 0; ; offset += pageSize {
			sessions, err := store.ListAll(ctx, pageSize, offset)
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}
			for _, session := range sessions {
				collect(session.ID)
			}
			if len(sessions) < pageSize {
				break
			}
		}
	}

	var (
		totalRows    int
		totalGranted int
	)
	for _, result := range results {
		if result.skipped != "" {
			cmd.Printf("跳过 %s（%s）\n", result.sessionID, result.skipped)
			continue
		}
		totalRows += result.rows
		totalGranted += result.granted
		if result.granted > 0 || opts.dryRun {
			cmd.Printf("%s rows=%d granted=%d\n", result.sessionID, result.rows, result.granted)
		}
	}
	verb := "已回填"
	if opts.dryRun {
		verb = "dry-run 统计"
	}
	cmd.Printf("%s：会话 %d，行 %d，授予身份 %d\n", verb, len(results), totalRows, totalGranted)
	return nil
}
