package ui

import (
	"strings"
	"testing"
)

func TestBuildBridgeFlushFrame(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		cursorCol int
		prompt    string
		want      string
	}{
		{
			name:      "empty line",
			line:      "",
			cursorCol: 2,
			prompt:    "> ",
			want:      "\r> \x1b[K\n\x1b[1A\r\x1b[2C",
		},
		{
			name:      "cursor at end",
			line:      "abc",
			cursorCol: 5,
			prompt:    "> ",
			want:      "\r> abc\x1b[K\n\x1b[1A\r\x1b[5C",
		},
		{
			name:      "cursor in middle",
			line:      "abc",
			cursorCol: 3,
			prompt:    "> ",
			want:      "\r> abc\x1b[K\n\x1b[1A\r\x1b[3C",
		},
		{
			name:      "no prompt",
			line:      "x",
			cursorCol: 1,
			prompt:    "",
			want:      "\rx\x1b[K\n\x1b[1A\r\x1b[1C",
		},
		{
			name:      "column zero",
			line:      "",
			cursorCol: 0,
			prompt:    "",
			want:      "\r\x1b[K\n\x1b[1A\r",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildBridgeFlushFrame([]rune(tt.line), tt.cursorCol, tt.prompt)
			if got != tt.want {
				t.Fatalf("frame mismatch\n got: %q\nwant: %q", got, tt.want)
			}
			// 帧主体必须包含且仅包含一个 \n 来触发行转发；随后必须立即
			// 回到上一行，不能把当前按键状态留成一条新的终端历史。
			if strings.Count(got, "\n") != 1 {
				t.Fatalf("frame must contain exactly one \\n: %q", got)
			}
			if !strings.Contains(got, "\n\x1b[1A\r") {
				t.Fatalf("frame must restore the input row after \\n: %q", got)
			}
		})
	}
}
