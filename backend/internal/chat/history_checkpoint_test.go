package chat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 长 turn 中途落库的节流窗口是 actor 与 runtime HTTP agent-chat 共用的策略，
// 这里覆盖它的三条契约：窗口内只放行一次、写失败可回退重试、以及显式禁用。

func TestCheckpointWindowThrottlesAndUndoRestoresWindow(t *testing.T) {
	window := NewCheckpointWindow(time.Minute)
	require.True(t, window.Enabled())

	undo, ok := window.Reserve()
	require.True(t, ok, "空窗口的第一个提交点必须放行")
	require.NotNil(t, undo)

	if _, ok := window.Reserve(); ok {
		t.Fatal("同一窗口内的第二个提交点必须被节流")
	}

	// 写入失败：回退时间戳后，下一个提交点必须立刻重试，而不是再等一个窗口。
	undo()
	undo, ok = window.Reserve()
	require.True(t, ok, "undo 必须让失败的中途落库立即重试")
	require.NotNil(t, undo)

	// 回退到占用前时间戳后，窗口内仍然只放行一次。
	if _, ok := window.Reserve(); ok {
		t.Fatal("回退后已再次占用窗口，不应放行第二次写入")
	}

	window.Reset()
	if _, ok := window.Reserve(); !ok {
		t.Fatal("Reset 后必须重新放行一个窗口")
	}
}

func TestCheckpointWindowDisabledForNonPositiveInterval(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		window := NewCheckpointWindow(interval)
		require.False(t, window.Enabled(), "interval=%s 必须禁用中途落库", interval)
		if _, ok := window.Reserve(); ok {
			t.Fatalf("interval=%s 时 Reserve 不得放行写入", interval)
		}
		undo, ok := window.Reserve()
		require.False(t, ok)
		require.Nil(t, undo, "禁用窗口不返回 undo")
	}

	var nilWindow *CheckpointWindow
	require.False(t, nilWindow.Enabled())
	require.Zero(t, nilWindow.Interval())
	if _, ok := nilWindow.Reserve(); ok {
		t.Fatal("nil 窗口不得放行写入")
	}
	nilWindow.Reset() // 不得 panic
}

func TestResolveSessionCheckpointIntervalDefaults(t *testing.T) {
	require.Equal(t, DefaultSessionCheckpointInterval, resolveSessionCheckpointInterval(0))
	require.Equal(t, DefaultSessionCheckpointInterval, NewCheckpointWindow(resolveSessionCheckpointInterval(0)).Interval())
	require.Equal(t, 5*time.Second, resolveSessionCheckpointInterval(5*time.Second))
	require.Equal(t, -time.Minute, resolveSessionCheckpointInterval(-time.Minute))
}

func TestReserveCheckpointWindowIgnoresNilTimestamp(t *testing.T) {
	undo, ok := reserveCheckpointWindow(nil, time.Minute)
	require.False(t, ok)
	require.Nil(t, undo)
}
