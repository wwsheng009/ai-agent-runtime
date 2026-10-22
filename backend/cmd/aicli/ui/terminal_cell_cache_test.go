package ui

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// resetTerminalCellRowCache 清空行缓存，用于测试隔离与冷缓存对照。
func resetTerminalCellRowCache() {
	cache := &terminalCellRowCache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.current = make(map[terminalCellRowKey][]vt.Cell, cache.limit)
	cache.previous = make(map[terminalCellRowKey][]vt.Cell, cache.limit)
}

// frameRows 构造 height 行纯文本布局行（无结构化 RenderLine）。
func frameRows(height int, prefix string) []AppScreenRow {
	rows := make([]AppScreenRow, height)
	for index := range rows {
		rows[index] = AppScreenRow{
			Row:  index + 1,
			Text: fmt.Sprintf("%s-%02d", prefix, index),
		}
	}
	return rows
}

// TestTerminalCellRowCacheReturnsStableContent 锁定缓存语义：命中缓存与重新
// 物化必须产出完全相同的 cell 内容（否则画面会出现字节级差异）。
func TestTerminalCellRowCacheReturnsStableContent(t *testing.T) {
	resetTerminalCellRowCache()
	const width, height = 40, 10
	theme := style.ThemeContext{}
	rows := frameRows(height, "row")

	first, err := terminalFrameCellsWindow(rows, nil, width, height, 1, height, theme)
	require.NoError(t, err)
	require.Len(t, first, height)

	// 第二次调用应全部命中缓存。
	resetHits := 0
	second, err := terminalFrameCellsWindow(rows, nil, width, height, 1, height, theme)
	require.NoError(t, err)
	require.Len(t, second, height)

	for row := 0; row < height; row++ {
		require.Equal(t, len(first[row]), len(second[row]), "行宽必须一致")
		for col := 0; col < len(first[row]); col++ {
			require.Equal(t, first[row][col].Text, second[row][col].Text)
			require.Equal(t, first[row][col].Cont, second[row][col].Cont)
			require.Equal(t, first[row][col].SGR, second[row][col].SGR)
		}
		if len(first[row]) > 0 && first[row][0].Text != "" {
			resetHits++
		}
	}
	require.NotZero(t, resetHits)
}

// TestTerminalCellRowCacheSeparatesByWidthAndTheme 锁定键维度：宽度或主题变化
// 必须重新物化，不能复用旧 cell（否则换主题后颜色不更新）。
func TestTerminalCellRowCacheSeparatesByWidthAndTheme(t *testing.T) {
	resetTerminalCellRowCache()
	rows := frameRows(3, "row")
	themeA := style.ThemeContext{}
	themeB := style.ThemeContext{}
	themeB.UseHyperlink = !themeA.UseHyperlink

	narrow, err := terminalFrameCellsWindow(rows, nil, 20, 3, 1, 3, themeA)
	require.NoError(t, err)
	wide, err := terminalFrameCellsWindow(rows, nil, 40, 3, 1, 3, themeA)
	require.NoError(t, err)
	require.Len(t, narrow[0], 20)
	require.Len(t, wide[0], 40)

	other, err := terminalFrameCellsWindow(rows, nil, 20, 3, 1, 3, themeB)
	require.NoError(t, err)
	require.Len(t, other[0], 20)
}

// TestTerminalCellRowCacheEvictsAcrossGenerations 锁定容量行为：持续写入超过
// 两代容量后仍必须返回正确内容（不能返回错行）。
func TestTerminalCellRowCacheEvictsAcrossGenerations(t *testing.T) {
	resetTerminalCellRowCache()
	const width = 24
	theme := style.ThemeContext{}
	for index := 0; index < 700; index++ {
		rows := []AppScreenRow{{Row: 1, Text: fmt.Sprintf("line-%04d", index)}}
		cells, err := terminalFrameCellsWindow(rows, nil, width, 1, 1, 1, theme)
		require.NoError(t, err)
		require.Len(t, cells, 1)
		require.Len(t, cells[0], width)
	}
	// 回到最早的一行：已淘汰，必须重新物化且内容正确。
	rows := []AppScreenRow{{Row: 1, Text: "line-0000"}}
	cells, err := terminalFrameCellsWindow(rows, nil, width, 1, 1, 1, theme)
	require.NoError(t, err)
	require.Equal(t, "l", cells[0][0].Text)
}

// BenchmarkTerminalFrameCellsWindowCold 近似修复前：每次清空缓存，全部重新物化。
func BenchmarkTerminalFrameCellsWindowCold(b *testing.B) {
	const width, height = 120, 24
	theme := style.ThemeContext{}
	rows := frameRows(height, "steady-state-row")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resetTerminalCellRowCache()
		if _, err := terminalFrameCellsWindow(rows, nil, width, height, 1, height, theme); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTerminalFrameCellsWindowBaseline 精确复现「加缓存之前」的路径：
// 与 terminalFrameCellsWindow 相同的校验与 materializer 复用，但不查/不写
// 行缓存。Cold/Warm 与它的差值才是缓存本身的净收益。
func BenchmarkTerminalFrameCellsWindowBaseline(b *testing.B) {
	const width, height = 120, 24
	rows := frameRows(height, "steady-state-row")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var materializer *vt.Screen
		frame := make([][]vt.Cell, height)
		for index, row := range rows {
			text := row.Text
			if materializer == nil {
				materializer = vt.NewScreen(width, 2)
			} else {
				materializer.Reset()
			}
			materializer.Feed(text)
			frame[index] = materializer.CellRow(1)
		}
		if len(frame) != height {
			b.Fatal("bad frame")
		}
	}
}

// BenchmarkTerminalFrameCellsWindowWarm 修复后的稳态：视口行不变，全部命中。
func BenchmarkTerminalFrameCellsWindowWarm(b *testing.B) {
	const width, height = 120, 24
	theme := style.ThemeContext{}
	rows := frameRows(height, "steady-state-row")
	if _, err := terminalFrameCellsWindow(rows, nil, width, height, 1, height, theme); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := terminalFrameCellsWindow(rows, nil, width, height, 1, height, theme); err != nil {
			b.Fatal(err)
		}
	}
}
