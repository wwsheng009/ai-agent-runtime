//go:build windows

package ui

import (
	"os"
	"testing"
)

// 需求：经典 conhost 的 CUP 是 screen-buffer 坐标。只有窗口起点在 buffer
// 第 0 行、且 buffer 宽度等于窗口宽度时，内联渲染的绝对行号才与可见行一致。
// 其余情形（用户滚动过、buffer 比窗口宽）必须报告为不可靠，避免把内容画到
// 看不见的 buffer 行上——这是缩放后“内容跳位/重复”的最后一类根因。
func TestScreenGeometryAbsoluteRowsReliable(t *testing.T) {
	tests := []struct {
		name string
		geo  ScreenGeometry
		want bool
	}{
		{
			name: "non-windows terminal reports no buffer geometry",
			geo:  ScreenGeometry{},
			want: true,
		},
		{
			name: "conhost window at buffer origin with matching width",
			geo:  ScreenGeometry{Supported: true, WindowOriginRow: 0, WindowWidth: 120, WindowHeight: 40, BufferWidth: 120, BufferHeight: 9001},
			want: true,
		},
		{
			name: "user scrolled the console",
			geo:  ScreenGeometry{Supported: true, WindowOriginRow: 37, WindowWidth: 120, WindowHeight: 40, BufferWidth: 120, BufferHeight: 9001},
			want: false,
		},
		{
			name: "buffer wider than window",
			geo:  ScreenGeometry{Supported: true, WindowOriginRow: 0, WindowWidth: 100, WindowHeight: 40, BufferWidth: 120, BufferHeight: 9001},
			want: false,
		},
		{
			name: "degenerate buffer width",
			geo:  ScreenGeometry{Supported: true, WindowOriginRow: 0, WindowWidth: 100, WindowHeight: 40, BufferWidth: 0},
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.geo.AbsoluteRowsReliable(); got != test.want {
				t.Fatalf("AbsoluteRowsReliable() = %t, want %t (%+v)", got, test.want, test.geo)
			}
		})
	}
}

// 需求：真实控制台（ConPTY 下测试自身就是 console 客户端）必须能取到
// screen buffer 几何；普通管道则返回 Supported=false，不误报不可靠。
func TestPlatformScreenGeometryForConsoleAndPipe(t *testing.T) {
	console := platformScreenGeometry(int(os.Stdout.Fd()))
	if !console.Supported {
		t.Skip("test stdout is not a console; buffer geometry unavailable")
	}
	if console.BufferWidth <= 0 || console.WindowWidth <= 0 || console.WindowHeight <= 0 {
		t.Fatalf("console geometry incomplete: %+v", console)
	}
	if console.BufferWidth < console.WindowWidth {
		t.Fatalf("buffer narrower than window: %+v", console)
	}

	file, err := os.CreateTemp(t.TempDir(), "screen-geometry-*.txt")
	if err != nil {
		t.Fatalf("open temp file: %v", err)
	}
	defer file.Close()
	if geo := platformScreenGeometry(int(file.Fd())); geo.Supported {
		t.Fatalf("non-console handle reported buffer geometry: %+v", geo)
	}
}
