// Command supervision-metrics 是 C0-E（设计稿 §7.3 度量基线）的一次性采集入口：
// 打开一个 supervision store（durable run 行 + 完成出件），打印与
// `/supervision/metrics` 完全相同的 JSON 快照。
//
// 为什么要有离线入口：宿主（runtime-server）在线采样走 HTTP 端点；而「上线后对比
// 记录」（§7.3 待办）需要在任意时刻对任意一份 supervision.db 复算同一份数字，
// 包括已经下线的实例。两个入口共用 supervision.CollectMetricsSnapshot 与
// supervision.ParseMetricsWindowValue，口径不可能各说各话。
//
// 用法（基线采集）：
//
//	go run ./cmd/supervision-metrics -store "$env:TEMP\ai-agent-runtime\supervision\supervision.db" -since 7d
//	go run ./cmd/supervision-metrics -store <path> -root <root_session_id> -max-candidates 50
//
// 只读：命令不写任何行（读路径全部是 SELECT）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "supervision-metrics: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("supervision-metrics", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	storePath := flags.String("store", "", "supervision.db 路径（必填）")
	rootSessionID := flags.String("root", "", "只读一个 root scope；省略 = 全库（部署基线）")
	sinceRaw := flags.String("since", "", `窗口下界：RFC3339 或回看时长（"24h" / "7d"）；省略 = 无界`)
	untilRaw := flags.String("until", "", "窗口上界：RFC3339 或回看时长；省略 = 无界")
	windowLimit := flags.Int("limit", 0, "窗口读取行数上限（默认 200）")
	maxCandidates := flags.Int("max-candidates", 0, "误杀复核清单条数上限（默认 20）")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if *storePath == "" {
		return fmt.Errorf("-store is required (path to supervision.db)")
	}

	now := time.Now().UTC()
	since, err := supervision.ParseMetricsWindowValue(*sinceRaw, now)
	if err != nil {
		return fmt.Errorf("since: %w", err)
	}
	until, err := supervision.ParseMetricsWindowValue(*untilRaw, now)
	if err != nil {
		return fmt.Errorf("until: %w", err)
	}

	store, err := supervision.NewSQLiteSupervisionStore(&supervision.StoreConfig{Path: *storePath})
	if err != nil {
		return fmt.Errorf("open store %s: %w", *storePath, err)
	}
	defer func() { _ = store.Close() }()

	snapshot, err := supervision.CollectMetricsSnapshot(context.Background(), store, supervision.MetricsSnapshotOptions{
		RootSessionID: *rootSessionID,
		Since:         since,
		Until:         until,
		WindowLimit:   *windowLimit,
		MaxCandidates: *maxCandidates,
	})
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(snapshot)
}
