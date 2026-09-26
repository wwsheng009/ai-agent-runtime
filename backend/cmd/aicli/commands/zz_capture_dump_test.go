package commands

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// TestZZCaptureDump 是临时诊断入口：用仓库自带 VT 回放 --render-output-file 产物，
// 在指定字节偏移处 dump 屏幕，直接看用户看到的画面。
// 仅在设置 AICLI_CAPTURE_DUMP 时运行（不影响 CI）。
func TestZZCaptureDump(t *testing.T) {
	path := os.Getenv("AICLI_CAPTURE_DUMP")
	if path == "" {
		t.Skip("AICLI_CAPTURE_DUMP not set")
	}
	width, height := 120, 44
	if v := os.Getenv("AICLI_CAPTURE_WIDTH"); v != "" {
		width, _ = strconv.Atoi(v)
	}
	if v := os.Getenv("AICLI_CAPTURE_HEIGHT"); v != "" {
		height, _ = strconv.Atoi(v)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	offsets := []int{}
	for _, part := range strings.Split(os.Getenv("AICLI_CAPTURE_OFFSETS"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value, _ := strconv.Atoi(part)
		offsets = append(offsets, value)
	}
	if len(offsets) == 0 {
		offsets = []int{len(data)}
	}

	screen := vt.NewScreen(width, height)
	prev := 0
	for _, offset := range offsets {
		if offset > len(data) {
			offset = len(data)
		}
		if offset < prev {
			t.Fatalf("offsets must be ascending: %d after %d", offset, prev)
		}
		screen.Feed(string(data[prev:offset]))
		prev = offset
		var b strings.Builder
		b.WriteString("\n=== screen @" + strconv.Itoa(offset) + " ===\n")
		b.WriteString("cursor=" + strconv.Itoa(screen.CursorRow()) + "x" + strconv.Itoa(screen.CursorCol()) +
			" scrollback=" + strconv.Itoa(len(screen.ScrollbackLines())) + "\n")
		from := height - 12
		if from < 1 {
			from = 1
		}
		for row, line := range screen.Lines(from, height) {
			b.WriteString("row" + strconv.Itoa(from+row) + " |" + line + "|\n")
		}
		if os.Getenv("AICLI_CAPTURE_SCROLLBACK") != "" {
			scrollback := screen.ScrollbackLines()
			b.WriteString("scrollback=" + strconv.Itoa(len(scrollback)) + " 采样:\n")
			samples := []int{0, len(scrollback) / 4, len(scrollback) / 2, len(scrollback) * 3 / 4}
			seen := map[int]bool{}
			for _, index := range samples {
				if index < 0 || index >= len(scrollback) || seen[index] {
					continue
				}
				seen[index] = true
				b.WriteString("  sb[" + strconv.Itoa(index) + "] |" + trimLine(scrollback[index]) + "|\n")
			}
			for index := len(scrollback) - 6; index < len(scrollback); index++ {
				if index >= 0 {
					b.WriteString("  sb尾部[" + strconv.Itoa(index) + "] |" + trimLine(scrollback[index]) + "|\n")
				}
			}
		}
		t.Log(b.String())
	}
}

func trimLine(line string) string {
	if len(line) > 120 {
		return line[:120]
	}
	return line
}
