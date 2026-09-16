package skills

import (
	"bufio"
	"errors"
	"net/http"
	"sync"
	"time"
)

// errPacedFlushWriterClosed 表示写出器已 Close（余量已出站）。之后的写入一律
// 报错而不是静默接受：Close 之后 ticker 已停，静默接受会让调用方误以为帧已出站，
// 实际却再没有人会推它（数据丢失比报错更难查）。
var errPacedFlushWriterClosed = errors.New("sse flush writer is closed")

// 建议 4（服务端 flush 背压）：把「每帧一次 flush」降频为「每 tick 至多一次 flush」。
//
// 症状：轨迹帧由 writeSSEEventFrame 写出，而它在 w 实现 http.Flusher 时**逐帧**
// flush（writeSSEComment 同理）。provider 按 token 回调，实测稳态 400+ 帧/s，于是
// 每帧都要走一次 bufio flush + net/http 响应缓冲 flush + socket write：帧的内容与
// 顺序都没错，代价全在系统调用与包边界上 —— 慢客户端或代理还会把每一帧都变成一次
// 背压等待，连接越多越明显。长连接上绝大多数帧本来就可以合批出站：前端按帧渲染，
// 50ms 的合并窗口对 token 流不可感知（人眼/动画帧粒度是 ~16ms 量级，而这里的窗口
// 只影响「同一瞬间多帧」的出站时刻，不改变帧本身）。
//
// 做法：包装 ResponseWriter，写入先进 16KB bufio；Flush 变成「按 tick 放行」，由内部
// ticker 保证脏缓冲最多迟滞一个 interval 出站。为此本类型**实现** http.Flusher
// （与此前 bufferedSSEWriter「刻意隐藏 Flusher」的做法相反）：调用方原有的 flush
// 调用点不必改写，语义仍是「现在应该让字节出站」，只是被节流器按窗口合并。
//
// 不变量（测试逐条覆盖，见 session_runtime_stream_flush_test.go）：
//   - 顺序与内容不变：所有字节按写入顺序经过同一个 bufio 落到同一个 sink，
//     合并只影响出站时刻，不做任何重排/丢弃；
//   - 首帧立即出站：lastFlush 为零值时任何 Flush 都立即生效 —— 建连握手
//     （`: open`）与 TTFB 不受节流影响；
//   - 空闲零代价：没有待出站字节时 ticker 不做任何 flush（不产生周期 syscall，
//     也不会把空闲连接的 tick 变成写唤醒）；
//   - 错误粘滞可传播：底层写/flush 的错误被记住并交给后续 Write/Flush/Err，
//     调用方沿用「flush 出错即客户端断开」的判定；
//   - 生命周期有界：Close 停 ticker 并清空余量，handler 返回后不留 goroutine；
//   - interval <= 0 退化为旧行为（每次 Flush 立即出站），是显式的回滚面。
type pacedFlushWriter struct {
	http.ResponseWriter
	// flusher 显式传入而不是就地断言：sink 可能是计数/包装层
	// （streamCountingResponseWriter 就刻意不实现 http.Flusher），
	// 而真正能推 net/http 响应缓冲的还是原始 ResponseWriter。
	flusher  http.Flusher
	buf      *bufio.Writer
	interval time.Duration

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once

	mu        sync.Mutex
	dirty     bool
	closed    bool
	lastFlush time.Time
	err       error
}

// newPacedFlushWriter 构造按 tick 合并 flush 的 SSE 写出器。
//
// interval <= 0 表示不合并（退化为「写即 flush」的旧行为）：此时不开后台
// goroutine，Flush 一律立即出站，Close 只做收尾 flush。
func newPacedFlushWriter(sink http.ResponseWriter, flusher http.Flusher, bufferSize int, interval time.Duration) *pacedFlushWriter {
	if bufferSize <= 0 {
		bufferSize = streamWriteBufferSize
	}
	p := &pacedFlushWriter{
		ResponseWriter: sink,
		flusher:        flusher,
		buf:            bufio.NewWriterSize(sink, bufferSize),
		interval:       interval,
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
	}
	if interval > 0 {
		go p.run()
	} else {
		// 无后台 goroutine：done 保持已关闭，Close 的等待立即返回。
		close(p.done)
	}
	return p
}

// run 是唯一的后台 goroutine：按 tick 把脏缓冲推出去。
//
// 只在有脏数据时 flush，因此空闲连接没有任何周期 syscall；节奏由 ticker
// 自身保证（每 interval 至多一次），不再叠加 lastFlush 判定 —— 免得时钟
// 抖动让「刚被节流的 flush」和「这次的 tick」互相谦让，把数据饿在缓冲里。
func (p *pacedFlushWriter) run() {
	defer close(p.done)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.mu.Lock()
			if p.err == nil && p.dirty {
				_ = p.flushLocked()
			}
			p.mu.Unlock()
		}
	}
}

// Write 把帧写进缓冲区，并标记「有待出站字节」。
//
// 即便 bufio 因写满而把字节透传到底层，net/http 自己的 2048B 响应缓冲里仍可能
// 留着尾巴，所以只要有写入就必须置脏，交给 flush 兜底（这正是本文件注释所说
// 「bufio 与 socket 之间还隔着一层」的那个坑）。
func (p *pacedFlushWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, errPacedFlushWriterClosed
	}
	if p.err != nil {
		return 0, p.err
	}
	n, err := p.buf.Write(b)
	p.dirty = true
	if err != nil {
		p.err = err
	}
	return n, err
}

// Flush 实现 http.Flusher：窗口内只置脏位，由 ticker 出站；窗口外立即出站。
func (p *pacedFlushWriter) Flush() {
	_ = p.flushIfDue()
}

// FlushNow 无视合并窗口立即出站。用于必须立刻可见的帧（握手、错误留痕、
// retry 提示），以及 handler 收尾前的最后一次 flush。
func (p *pacedFlushWriter) FlushNow() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errPacedFlushWriterClosed
	}
	return p.flushLocked()
}

// Err 返回粘滞的底层错误（nil 表示连接仍健康）。
func (p *pacedFlushWriter) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Close 停止后台 flush 并把余量推出去。
//
// handler 返回前必须调用（defer）：否则最后一帧可能还停在 bufio 里，而 ticker
// 一停就再没人推它 —— 表现就是「流结束了但最后几帧没到客户端」。幂等。
func (p *pacedFlushWriter) Close() error {
	p.stopOnce.Do(func() { close(p.stop) })
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.err
	}
	p.closed = true
	return p.flushLocked()
}

func (p *pacedFlushWriter) flushIfDue() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if p.err != nil {
		return p.err
	}
	if !p.dirty {
		// 没有待出站字节：不做任何 flush（空闲连接零 syscall）。
		return nil
	}
	if p.interval > 0 && !p.lastFlush.IsZero() && time.Since(p.lastFlush) < p.interval {
		// 窗口未到：留给 ticker，最多迟滞一个 interval。
		return nil
	}
	return p.flushLocked()
}

// flushLocked 做两级 flush（调用方须持有 p.mu）：
//  1. bufio → ResponseWriter：把 SSE 帧从 16KB 缓冲写进 net/http；
//  2. Flusher → socket：把 net/http 的 2048B 响应缓冲推到客户端。
//
// 缺 (2) 时字节仍停在 response.w 里，对客户端等价于「什么都没发出去」。
func (p *pacedFlushWriter) flushLocked() error {
	if p.err != nil {
		return p.err
	}
	if err := p.buf.Flush(); err != nil {
		p.err = err
		return err
	}
	if p.flusher != nil {
		p.flusher.Flush()
	}
	p.dirty = false
	p.lastFlush = time.Now()
	return nil
}
