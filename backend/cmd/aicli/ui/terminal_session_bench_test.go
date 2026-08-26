package ui

import (
	"fmt"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

const benchmarkTerminalHistoryRowCount = 15000

var benchmarkTerminalHistoryRows []string

// BenchmarkTerminalHistoryPrepare15K measures one complete presentation pass
// for the production-sized Unicode history batch that motivated this fix.
// Input construction is outside the timed region; one operation is one batch.
func BenchmarkTerminalHistoryPrepare15K(b *testing.B) {
	commits := benchmarkTerminalHistoryCommits(benchmarkTerminalHistoryRowCount)
	theme := ThemeContextForProfile(style.ColorProfile{ColorProfile: render.NoColorProfile()})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := terminalHistoryCommitRows(commits, theme, 160)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkTerminalHistoryRows = rows
	}
	b.ReportMetric(benchmarkTerminalHistoryRowCount, "rows/batch")
}

// BenchmarkTerminalHistoryPreparedRetry15K measures the exact-validation path
// used after a proven zero-byte transport failure. It still validates immutable
// commit presentation, but reuses measured/resolved/ANSI rows.
func BenchmarkTerminalHistoryPreparedRetry15K(b *testing.B) {
	commits := benchmarkTerminalHistoryCommits(benchmarkTerminalHistoryRowCount)
	theme := ThemeContextForProfile(style.ColorProfile{ColorProfile: render.NoColorProfile()})
	session := NewTerminalSession(nil)
	session.transactionMu.Lock()
	defer session.transactionMu.Unlock()
	if _, err := session.prepareTerminalHistoryRows(commits, theme, 160); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := session.prepareTerminalHistoryRows(commits, theme, 160)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkTerminalHistoryRows = rows
	}
	b.ReportMetric(benchmarkTerminalHistoryRowCount, "rows/batch")
}

func benchmarkTerminalHistoryCommits(rowCount int) []HistoryCommit {
	lines := make([]render.Line, rowCount)
	for index := range lines {
		lines[index] = render.Line{Spans: []render.Span{
			{Text: fmt.Sprintf("第 %05d 行", index)},
			{Text: " · "},
			{Text: fmt.Sprintf("中文历史内容 %05d", index)},
		}}
	}
	commit := terminalSessionCommit(1, lines...)
	return []HistoryCommit{commit}
}
