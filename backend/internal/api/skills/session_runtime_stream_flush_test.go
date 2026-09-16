package skills

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flushSink 是 pacedFlushWriter 的测试替身：记录写入字节与 flush 次数，
// 用于逐条验证「合并窗口」的不变量（顺序、首帧立即、空闲零代价、错误粘滞）。
type flushSink struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	flushes int
	// writeErr 非空时 Write 直接失败，用于验证错误粘滞。
	writeErr error
}

func (s *flushSink) Header() http.Header { return http.Header{} }

func (s *flushSink) WriteHeader(int) {}

func (s *flushSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.buf.Write(p)
}

func (s *flushSink) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushes++
}

func (s *flushSink) snapshot() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String(), s.flushes
}

// TestPacedFlushWriter_FirstFlushIsImmediate 锁定「首帧不受节流影响」：
// lastFlush 为零值时任何 Flush 都必须立即出站，否则建连握手/TTFB 会被
// 合并窗口推迟（正是本文件要避免的回归）。
func TestPacedFlushWriter_FirstFlushIsImmediate(t *testing.T) {
	sink := &flushSink{}
	paced := newPacedFlushWriter(sink, sink, 64, 200*time.Millisecond)
	defer func() { _ = paced.Close() }()

	_, err := paced.Write([]byte(": open\n\n"))
	require.NoError(t, err)
	paced.Flush()

	body, flushes := sink.snapshot()
	assert.Equal(t, ": open\n\n", body)
	assert.Equal(t, 1, flushes, "首个 Flush 应立即出站")
}

// TestPacedFlushWriter_MergesWithinWindowThenTickerDelivers 是建议 4 的核心用例：
// 窗口内的多次 Flush 只置脏位（不写 socket），由 ticker 在下一个 tick 合并出站，
// 且合并后字节顺序/内容与写入完全一致。
func TestPacedFlushWriter_MergesWithinWindowThenTickerDelivers(t *testing.T) {
	sink := &flushSink{}
	paced := newPacedFlushWriter(sink, sink, 1024, 30*time.Millisecond)
	defer func() { _ = paced.Close() }()

	_, err := paced.Write([]byte("a"))
	require.NoError(t, err)
	paced.Flush() // 首帧立即出站，作为「上一次 flush」基准

	// 窗口内连写 50 帧：全部应被合批，期间不产生新的 flush。
	for i := 0; i < 50; i++ {
		_, err = paced.Write([]byte("b"))
		require.NoError(t, err)
		paced.Flush()
	}
	body, flushes := sink.snapshot()
	assert.Equal(t, "a", body, "窗口内的帧应留在缓冲里，不逐帧出站")
	assert.Equal(t, 1, flushes, "合并窗口内不应产生额外 flush")

	// ticker 兜底：最迟一个窗口后，攒下的 50 帧一次性出站。
	waitForResumeCondition(t, 2*time.Second, func() bool {
		body, _ := sink.snapshot()
		return len(body) == 51
	}, "ticker 应在合并窗口内把攒下的帧推出")
	body, flushes = sink.snapshot()
	assert.Equal(t, "a"+strings.Repeat("b", 50), body, "合并不得改变字节顺序或内容")
	assert.Equal(t, 2, flushes, "50 帧应合并成一次 flush")
}

// TestPacedFlushWriter_IdleProducesNoFlush 锁定「空闲连接零代价」：
// 没有待出站字节时 ticker 不得产生任何 flush（否则空闲长连接会被周期 syscall 拖着）。
func TestPacedFlushWriter_IdleProducesNoFlush(t *testing.T) {
	sink := &flushSink{}
	paced := newPacedFlushWriter(sink, sink, 64, 10*time.Millisecond)
	defer func() { _ = paced.Close() }()

	time.Sleep(120 * time.Millisecond) // 覆盖 ≥10 个 tick
	body, flushes := sink.snapshot()
	assert.Empty(t, body)
	assert.Equal(t, 0, flushes, "空闲（无脏数据）时不应有任何 flush")
}

// TestPacedFlushWriter_FlushNowBypassesWindow 锁定强制出站路径：握手帧、错误留痕
// 这类必须立刻可见的帧不能被合并窗口推迟。
func TestPacedFlushWriter_FlushNowBypassesWindow(t *testing.T) {
	sink := &flushSink{}
	paced := newPacedFlushWriter(sink, sink, 1024, time.Minute)
	defer func() { _ = paced.Close() }()

	_, err := paced.Write([]byte("first"))
	require.NoError(t, err)
	paced.Flush()
	_, err = paced.Write([]byte("second"))
	require.NoError(t, err)

	require.NoError(t, paced.FlushNow())
	body, flushes := sink.snapshot()
	assert.Equal(t, "firstsecond", body)
	assert.Equal(t, 2, flushes)
}

// TestPacedFlushWriter_WriteThroughBufferStillFlushes 覆盖「bufio 写满即透传」
// 的场景：字节虽然绕过了缓冲，net/http 的响应缓冲里可能仍有尾巴，因此
// Flush 依然必须调用底层 Flusher（否则会出现「_buffer 满了反而没 flush」的假成功）。
func TestPacedFlushWriter_WriteThroughBufferStillFlushes(t *testing.T) {
	sink := &flushSink{}
	paced := newPacedFlushWriter(sink, sink, 8, time.Minute)
	defer func() { _ = paced.Close() }()

	payload := strings.Repeat("x", 32) // 一次写入 > bufferSize，bufio 直接透传
	_, err := paced.Write([]byte(payload))
	require.NoError(t, err)
	paced.Flush()

	body, flushes := sink.snapshot()
	assert.Equal(t, payload, body)
	assert.Equal(t, 1, flushes, "写透缓冲后仍须显式 flush")
}

// TestPacedFlushWriter_DisabledIntervalKeepsLegacySemantics 锁定回滚面：
// flush_ms=0 时退化为旧行为（每次 Flush 立即出站），不引入 ticker 延迟。
func TestPacedFlushWriter_DisabledIntervalKeepsLegacySemantics(t *testing.T) {
	sink := &flushSink{}
	paced := newPacedFlushWriter(sink, sink, 1024, 0)
	defer func() { _ = paced.Close() }()

	for i := 0; i < 3; i++ {
		_, err := paced.Write([]byte("f"))
		require.NoError(t, err)
		paced.Flush()
		body, _ := sink.snapshot()
		assert.Equalf(t, strings.Repeat("f", i+1), body, "第 %d 次写入应立即可见", i+1)
	}
	_, flushes := sink.snapshot()
	assert.Equal(t, 3, flushes)
}

// TestPacedFlushWriter_ErrorIsSticky 覆盖「flush 出错即客户端断开」的判定：
// 底层错误被记住并交给后续 Write/Flush/Err，不再反复写坏连接。
func TestPacedFlushWriter_ErrorIsSticky(t *testing.T) {
	sink := &flushSink{writeErr: errors.New("client disconnected")}
	paced := newPacedFlushWriter(sink, sink, 64, time.Minute)
	defer func() { _ = paced.Close() }()

	// 帧先进 bufio，不会立刻触底：错误在出站时暴露，而不是在 Write 时。
	_, err := paced.Write([]byte("frame"))
	require.NoError(t, err)
	require.ErrorIs(t, paced.FlushNow(), sink.writeErr)
	assert.ErrorIs(t, paced.Err(), sink.writeErr)

	// 粘滞：后续写入不再尝试，直接返回同一个错误。
	_, err = paced.Write([]byte("frame-2"))
	require.Error(t, err)
	assert.ErrorIs(t, err, sink.writeErr)
}

// TestPacedFlushWriter_CloseFlushesTailAndIsIdempotent 覆盖收尾：
// handler 返回前必须把余量推出（否则「流结束了但最后几帧没到客户端」），
// 且 Close 幂等、不 panic。
func TestPacedFlushWriter_CloseFlushesTailAndIsIdempotent(t *testing.T) {
	sink := &flushSink{}
	// interval 取 1h：本用例只能靠 Close 的收尾 flush 把数据推出去。
	paced := newPacedFlushWriter(sink, sink, 1024, time.Hour)

	_, err := paced.Write([]byte("tail-frame"))
	require.NoError(t, err)
	require.NoError(t, paced.Close())

	body, flushes := sink.snapshot()
	assert.Equal(t, "tail-frame", body, "Close 必须清空余量")
	assert.Equal(t, 1, flushes)
	require.NoError(t, paced.Close(), "Close 应幂等")
	_, err = paced.Write([]byte("after-close"))
	assert.ErrorIs(t, err, errPacedFlushWriterClosed, "Close 之后的写入必须报错，不能静默丢失")
}
