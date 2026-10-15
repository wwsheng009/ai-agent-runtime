package feishu

// 飞书 WS 长连接端到端测试（feishu_ws.go）：
//   - 帧编解码往返（protobuf uvarint/bytes/headers/payload）；
//   - ping → pong（control 帧，method=0，type=pong）；
//   - 事件帧 → 入站解析 + ack（method 不变、biz_rt 刷新、payload={"code":200}）；
//   - 端到端：endpoint 接口 → WS 升级 → 下发消息事件 → handler 收到入站；
//   - SupportsPolling 门控（mode=polling 且 app_secret 齐备）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestWSFrameRoundTrip(t *testing.T) {
	frame := wsFrame{
		SeqID:           42,
		LogID:           7,
		Service:         1,
		Method:          wsFrameMethodData,
		Headers:         []wsHeader{{Key: "type", Value: "event"}, {Key: wsHeaderBizRT, Value: "123"}},
		PayloadEncoding: "json",
		PayloadType:     "string",
		Payload:         []byte(`{"hello":"world"}`),
		LogIDNew:        "log-new",
	}
	data, err := marshalWSFrame(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := parseWSFrame(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if decoded.SeqID != frame.SeqID || decoded.LogID != frame.LogID ||
		decoded.Service != frame.Service || decoded.Method != frame.Method ||
		decoded.PayloadEncoding != frame.PayloadEncoding || decoded.PayloadType != frame.PayloadType ||
		string(decoded.Payload) != string(frame.Payload) || decoded.LogIDNew != frame.LogIDNew ||
		len(decoded.Headers) != len(frame.Headers) {
		t.Fatalf("round trip mismatch: %+v vs %+v", decoded, frame)
	}
	for index, header := range frame.Headers {
		if decoded.Headers[index].Key != header.Key || decoded.Headers[index].Value != header.Value {
			t.Fatalf("header[%d] = %+v, want %+v", index, decoded.Headers[index], header)
		}
	}
}

func TestWSPongAndAckFrames(t *testing.T) {
	incoming := wsFrame{
		SeqID:   5,
		Service: 2,
		Method:  wsFrameMethodControl,
		Headers: []wsHeader{{Key: "type", Value: wsHeaderPing}, {Key: wsHeaderBizRT, Value: "9"}},
	}
	pong := wsPongFrame(incoming)
	if pong.Method != wsFrameMethodControl || pong.headerValue(wsHeaderType) != wsHeaderPong {
		t.Fatalf("pong frame = %+v", pong)
	}
	event := wsFrame{
		SeqID:   6,
		Service: 2,
		Method:  wsFrameMethodData,
		Headers: []wsHeader{{Key: "type", Value: "event"}, {Key: wsHeaderBizRT, Value: "9"}},
		Payload: []byte(`{}`),
	}
	ack := wsAckFrame(event, time.UnixMilli(1700000000000))
	if ack.Method != wsFrameMethodData || ack.headerValue(wsHeaderBizRT) != "1700000000000" {
		t.Fatalf("ack frame = %+v", ack)
	}
	if string(ack.Payload) != `{"code":200}` || ack.PayloadEncoding != "json" {
		t.Fatalf("ack payload = %q encoding %q", ack.Payload, ack.PayloadEncoding)
	}
}

// newFakeFeishuWS 启动假飞书 WS 服务（供帧级测试；端到端用例自建服务器）。
func newFakeFeishuWS(t *testing.T) (*httptest.Server, func(frame wsFrame) error, <-chan []byte) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	var (
		mu        sync.Mutex
		writeConn *websocket.Conn
		received  = make(chan []byte, 16)
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		writeConn = conn
		mu.Unlock()
		go func() {
			for {
				messageType, data, err := conn.ReadMessage()
				if err != nil {
					return
				}
				select {
				case received <- append([]byte(nil), append([]byte{byte(messageType)}, data...)...):
				default:
				}
			}
		}()
	}))
	t.Cleanup(server.Close)

	writeFrame := func(frame wsFrame) error {
		mu.Lock()
		conn := writeConn
		mu.Unlock()
		if conn == nil {
			return errors.New("ws conn not ready")
		}
		data, err := marshalWSFrame(frame)
		if err != nil {
			return err
		}
		return conn.WriteMessage(websocket.BinaryMessage, data)
	}
	return server, writeFrame, received
}

// wsURLFor 把 endpoint 响应的占位 URL 替换为真实 WS 地址（用 http 中转包装：
// 直接在 endpoint handler 里替换 server.URL）。这里用包装层实现。
func TestRunPollingReceivesMessageOverWS(t *testing.T) {
	var (
		mu        sync.Mutex
		writeConn *websocket.Conn
	)
	ready := make(chan struct{})
	upgrader := websocket.Upgrader{}
	var wsURL string
	endpointServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "msg": "ok",
			"data": map[string]any{"URL": wsURL, "ClientConfig": map[string]any{"PingInterval": 60}},
		})
	}))
	defer endpointServer.Close()

	eventServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		writeConn = conn
		mu.Unlock()
		close(ready)
		// 保持连接读取（避免客户端写阻塞）。
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer eventServer.Close()
	wsURL = "ws" + strings.TrimPrefix(eventServer.URL, "http")

	fake := newFakeFeishu(t)
	// endpoint 请求由 eventServer 承担；api_base 指向 endpointServer。
	provider := New(WithAPIBase(endpointServer.URL), WithLogger(func(string, ...any) {}))
	cfg := testConfig()
	cfg.Settings["mode"] = "polling"
	cfg.Settings[settingAPIBase] = endpointServer.URL

	messages := make(chan connectors.InboundMessage, 4)
	handler := func(_ context.Context, message connectors.InboundMessage) error {
		messages <- message
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = provider.RunPolling(ctx, cfg, handler, nil) }()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("event server connection not established in time")
	}
	// 下发一条消息事件帧（与 webhook 同构的 v2 事件）。
	eventPayload := messageEventPayload("evt-ws-1", "text", `{"text":"ws 你好"}`)
	raw, _ := json.Marshal(map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_type": eventMessageReceive,
			"event_id":   "evt-ws-1",
			"token":      "vt-123",
			"app_id":     "cli_test",
		},
		"event": eventPayload["event"],
	})
	mu.Lock()
	conn := writeConn
	mu.Unlock()
	if conn == nil {
		t.Fatal("ws conn missing")
	}
	frameData, _ := marshalWSFrame(wsFrame{
		SeqID:   1,
		Service: 2,
		Method:  wsFrameMethodData,
		Headers: []wsHeader{{Key: "type", Value: "event"}},
		Payload: raw,
	})
	if err := conn.WriteMessage(websocket.BinaryMessage, frameData); err != nil {
		t.Fatalf("write event frame: %v", err)
	}

	select {
	case message := <-messages:
		if message.Text != "ws 你好" || message.Chat != "oc_chat_1" {
			t.Fatalf("unexpected inbound: %+v", message)
		}
		if message.MessageID != "evt-ws-1" {
			t.Fatalf("message id = %q, want evt-ws-1", message.MessageID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inbound message not received in time")
	}
	cancel()
	_ = fake
}

func TestSupportsPollingGating(t *testing.T) {
	provider := New()
	webhookCfg := testConfig()
	if provider.SupportsPolling(webhookCfg) {
		t.Fatal("webhook mode must not start polling")
	}
	pollingCfg := testConfig()
	pollingCfg.Settings["mode"] = "polling"
	if !provider.SupportsPolling(pollingCfg) {
		t.Fatal("polling mode with secret should start polling")
	}
	noSecret := connectors.Config{Settings: map[string]string{"mode": "polling"}}
	if provider.SupportsPolling(noSecret) {
		t.Fatal("polling without app_secret must not start polling")
	}
}
