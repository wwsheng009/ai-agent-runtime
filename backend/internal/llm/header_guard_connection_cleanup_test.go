package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// §13.5 疑点复核：agent 级装置曾观察到 guard 超时后连接似未清理
// （httptest.Server.Close 悬挂 ≥1 分钟、goroutine dump 中有挂起 handler）。
// 本探针在 provider 层单请求复现，用裸 TCP 监听器直接观察客户端是否拆除连接：
// 挂起上游收下请求后仅保持连接、永不返回响应头；guard 超时后客户端必须断开，
// 监听端 io.Copy 以 EOF/错误终结即为证据。若 3s 内未观察到断开，则"半开连接
// 泄漏"成立。

type connOutcome struct {
	bytes int64
	err   error
}

// disconnectingUpstreamListener accepts TCP connections, drains whatever the
// client sends, then keeps reading until the client tears the connection down.
// Each terminal outcome (bytes read + error) is reported on the channel; stop()
// force-closes leftovers so the test can never hang on a non-disconnecting
// client.
func disconnectingUpstreamListener(t *testing.T) (addr string, outcomes <-chan connOutcome, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ch := make(chan connOutcome, 4)
	var mu sync.Mutex
	var conns []net.Conn
	var wg sync.WaitGroup

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				n, err := io.Copy(io.Discard, c)
				ch <- connOutcome{bytes: n, err: err}
			}(conn)
		}
	}()

	stop = func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	}
	return ln.Addr().String(), ch, stop
}

func TestProviderHeaderGuardTimeoutDisconnectsHungUpstream(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "non-streaming"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			addr, outcomes, stop := disconnectingUpstreamListener(t)
			defer stop()

			provider, err := NewProvider(&ProviderConfig{
				Type:                  "openai",
				BaseURL:               "http://" + addr,
				MaxRetries:            0,
				ResponseHeaderTimeout: 100 * time.Millisecond,
			})
			require.NoError(t, err)

			runtime := NewLLMRuntime(&RuntimeConfig{DefaultModel: "guard-model", MaxRetries: 0})
			require.NoError(t, runtime.RegisterProvider("guard-model", provider))

			// 5s 上限仅作安全网：guard 必须在它之前自行失败。
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			_, err = runtime.Call(ctx, &LLMRequest{
				Model:    "guard-model",
				Stream:   streaming,
				Messages: []types.Message{{Role: "user", Content: "hang please"}},
			})
			require.Error(t, err)
			require.NotErrorIs(t, err, context.DeadlineExceeded,
				"guard must fail on its own, before the test context deadline")
			require.Contains(t, err.Error(), "timeout awaiting response headers")

			select {
			case out := <-outcomes:
				if out.err != nil && errors.Is(out.err, net.ErrClosed) {
					t.Fatal("only the test's own force-close was observed; the client never disconnected")
				}
				require.Positive(t, out.bytes, "the request never reached the upstream")
				// EOF (nil error from io.Copy) or a transport error: either way
				// the client closed the hung connection instead of leaking it.
			case <-time.After(3 * time.Second):
				t.Fatal("upstream never observed the client disconnecting the hung connection (half-open leak)")
			}
		})
	}
}

// TestProviderHeaderGuardDisconnectAppliesToHTTP2 把断开观察补到 HTTP/2：
// 传输层 ResponseHeaderTimeout 仅作用于 HTTP/1.1（见 provider.go 的 doRequest
// 注释），h2 挂起场景完全依赖自研 guard；因此这里必须同样证明 guard 超时会
// 拆除 h2 流（客户端 RST_STREAM → 服务端 request context 取消），而非留下
// 一个永不收口的流。
func TestProviderHeaderGuardDisconnectAppliesToHTTP2(t *testing.T) {
	protoCh := make(chan string, 1)
	canceled := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case protoCh <- fmt.Sprintf("%d.%d", r.ProtoMajor, r.ProtoMinor):
		default:
		}
		<-r.Context().Done() // 等客户端拆除 h2 流（RST_STREAM）
		select {
		case canceled <- struct{}{}:
		default:
		}
	})
	ts := httptest.NewUnstartedServer(handler)
	ts.EnableHTTP2 = true
	ts.StartTLS()
	// 兜底清理：先强制断开，再 Close（Close 只强关 idle/new 连接，
	// 在途请求会一直等——这正是 §13.5 观察到的悬挂机理，见 Go 源码
	// net/http/httptest.Server.Close 对 StateActive 的处理）。
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})

	p := &ProviderWrapper{
		config: &ProviderConfig{ResponseHeaderTimeout: 100 * time.Millisecond},
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL,
		strings.NewReader(`{"model":"guard-model","messages":[]}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	_, err = p.doRequest(ts.Client(), req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout awaiting response headers")

	select {
	case proto := <-protoCh:
		require.Equal(t, "2.0", proto, "test must exercise HTTP/2 to cover the guard")
	case <-time.After(3 * time.Second):
		t.Fatal("request never reached the h2 handler")
	}

	select {
	case <-canceled:
		// 服务端 h2 请求上下文被取消：guard 超时拆除了流，未泄漏。
	case <-time.After(3 * time.Second):
		t.Fatal("h2: upstream request context was never cancelled after the guard timeout (stream leak)")
	}
}
