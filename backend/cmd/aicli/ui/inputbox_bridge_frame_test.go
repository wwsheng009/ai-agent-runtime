package ui

import (
	"strings"
	"testing"
)

func TestBuildBridgeFlushFrame(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		cursor    int
		cursorCol int
		prompt    string
		want      string
	}{
		{
			name:   "empty line",
			line:   "",
			cursor: 0,
			prompt: "> ",
			want:   "\r> \x1b[K\n",
		},
		{
			name:   "cursor at end",
			line:   "abc",
			cursor: 3,
			prompt: "> ",
			want:   "\r> abc\x1b[K\n",
		},
		{
			name:   "cursor in middle (same frame, cursor col unused)",
			line:   "abc",
			cursor: 1,
			prompt: "> ",
			want:   "\r> abc\x1b[K\n",
		},
		{
			name:   "no prompt",
			line:   "x",
			cursor: 1,
			prompt: "",
			want:   "\rx\x1b[K\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildBridgeFlushFrame([]rune(tt.line), tt.cursor, tt.cursorCol, tt.prompt)
			if got != tt.want {
				t.Fatalf("frame mismatch\n got: %q\nwant: %q", got, tt.want)
			}
			// 每帧必须以 \n 结尾（帧是完整一行，任何行缓冲/合并下都可见）。
			if !strings.HasSuffix(got, "\n") {
				t.Fatalf("frame must end with \\n: %q", got)
			}
		})
	}
}