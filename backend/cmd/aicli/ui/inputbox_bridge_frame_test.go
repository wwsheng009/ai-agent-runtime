package ui

import (
	"strings"
	"testing"
)

func TestBuildBridgeFlushFrame(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		cursor      int
		promptWidth int
		cursorCol   int
		want        string
	}{
		{
			name:        "empty line",
			line:        "",
			cursor:      0,
			promptWidth: 2,
			cursorCol:   2,
			want:        "\r\x1b[2C\x1b[K\n\x1b[1A\x1b[2G",
		},
		{
			name:        "cursor at end",
			line:        "abc",
			cursor:      3,
			promptWidth: 2,
			cursorCol:   5,
			want:        "\r\x1b[2Cabc\x1b[K\n\x1b[1A\x1b[5G",
		},
		{
			name:        "cursor in middle",
			line:        "abc",
			cursor:      1,
			promptWidth: 2,
			cursorCol:   3,
			want:        "\r\x1b[2Cabc\x1b[K\x1b[2D\n\x1b[1A\x1b[3G",
		},
		{
			name:        "no prompt",
			line:        "x",
			cursor:      1,
			promptWidth: 0,
			cursorCol:   1,
			want:        "\rx\x1b[K\n\x1b[1A\x1b[1G",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildBridgeFlushFrame([]rune(tt.line), tt.cursor, tt.promptWidth, tt.cursorCol)
			if got != tt.want {
				t.Fatalf("frame mismatch\n got: %q\nwant: %q", got, tt.want)
			}
			// 帧主体（内容重绘部分）必须以 \n 收尾，桥接才会实时转发；
			// \n 之后仅允许「收回输入行 + 绝对列定位」——它们与下一帧内容
			// 同批到达，逐帧执行后光标位置依然正确。
			nl := strings.LastIndex(got, "\n")
			if nl <= 0 {
				t.Fatalf("frame body must contain \\n: %q", got)
			}
			tail := got[nl+1:]
			if !strings.HasPrefix(tail, "\x1b[1A") {
				t.Fatalf("after \\n must come \\x1b[1A, got %q (frame %q)", tail, got)
			}
			rest := strings.TrimPrefix(tail, "\x1b[1A")
			if rest == "" {
				return
			}
			digits := strings.TrimPrefix(rest, "\x1b[")
			digits = strings.TrimSuffix(digits, "G")
			if len(digits) == 0 {
				t.Fatalf("bad cursor-placement tail %q (frame %q)", tail, got)
			}
			for _, c := range digits {
				if c < '0' || c > '9' {
					t.Fatalf("bad cursor-placement tail %q (frame %q)", tail, got)
				}
			}
		})
	}
}