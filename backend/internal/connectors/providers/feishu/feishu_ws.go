// 飞书 WS 长连接（PollingProvider，切片 3g）。
//
// 协议（上游 codex-server feishu.go 的移植，官方「长连接」模式）：
//  1. POST {api_base}/callback/ws/endpoint（AppID/AppSecret 明文体，无需
//     tenant token）→ {data:{URL|url, ClientConfig:{PingInterval,...}}}；
//  2. gorilla/websocket 二进制帧 = 飞书私有 protobuf：
//     字段 1 seq_id(uvarint)、2 log_id(uvarint)、3 service(uvarint)、
//     4 method(uvarint)、5 headers(repeated {1:key,2:value} bytes)、
//     6 payload_encoding(bytes)、7 payload_type(bytes)、8 payload(bytes)、
//     9 log_id_new(bytes)；
//  3. 帧处理：header type=ping → 回 pong（control 帧）；type 空或 event →
//     payload 按 v2 事件 JSON 解析（复用 HandleWebhook 的入站解析），回 ack
//     （method=data、原 headers 去掉 biz_rt 后追加 biz_rt=当前毫秒、
//     payload={"code":200}）；
//  4. 重连：读超时 90s（每帧刷新）；断线按指数退避重取 endpoint 重连
//     （初始 1s、上限 15s，与 QQ gateway 同口径），ctx 取消即退出。
//
// 模式声明：Modes 增加 "polling"（即 WS 长连接；Settings["mode"] 默认
// webhook 保持不变，显式选 polling 才启动）。
package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// wsEndpointPath 是长连接接入点（POST 换取 WS URL）。
	wsEndpointPath = "/callback/ws/endpoint"
	// wsReadTimeout 是单帧读超时（ping 保活间隔大于该值即断线重连）。
	wsReadTimeout = 90 * time.Second
	// wsReconnectInitial / wsReconnectMax 是重连退避边界。
	wsReconnectInitial = time.Second
	wsReconnectMax     = 15 * time.Second
	// wsReadLimit 是单帧读取上限（事件 payload 防御超大帧）。
	wsReadLimit = 1 << 20

	// wsFrameMethodControl / wsFrameMethodData 是帧 method 取值。
	wsFrameMethodControl int32 = 0
	wsFrameMethodData    int32 = 1

	// wsHeaderType / wsHeaderPing / wsHeaderPong / wsHeaderEvent / wsHeaderBizRT
	// 是帧 header 约定键。
	wsHeaderType  = "type"
	wsHeaderPing  = "ping"
	wsHeaderPong  = "pong"
	wsHeaderEvent = "event"
	wsHeaderBizRT = "biz_rt"
)

// wsEndpointResponse 是 /callback/ws/endpoint 的响应体。
type wsEndpointResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		URL      string `json:"URL"`
		URLLower string `json:"url"`
	} `json:"data"`
}

// SupportsPolling 报告是否应启动 WS 长连接（mode=polling 且凭据齐备；
// 默认 webhook 模式不受影响）。
func (p *Provider) SupportsPolling(cfg connectors.Config) bool {
	return strings.EqualFold(strings.TrimSpace(cfg.Setting("mode", "")), "polling") &&
		strings.TrimSpace(cfg.Secret(secretAppSecret)) != ""
}

// RunPolling 实现 connectors.PollingProvider：WS 长连接直至 ctx 取消；
// 断线按指数退避重连（endpoint 每次重取）。
func (p *Provider) RunPolling(ctx context.Context, cfg connectors.Config, handle connectors.InboundHandler, state connectors.PollingState) error {
	if handle == nil {
		return errors.New("feishu: inbound handler is required")
	}
	if strings.TrimSpace(cfg.Secret(secretAppSecret)) == "" {
		return errors.New("feishu: app_secret is required for websocket mode")
	}
	backoff := wsReconnectInitial
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := p.runWSSession(ctx, cfg, handle)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		p.logf("feishu: websocket disconnected: %v (retry in %s)", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > wsReconnectMax {
			backoff = wsReconnectMax
		}
	}
}

// runWSSession 建立一次 WS 连接并处理帧，直至出错或 ctx 取消。
func (p *Provider) runWSSession(ctx context.Context, cfg connectors.Config, handle connectors.InboundHandler) error {
	endpoint, err := p.wsEndpoint(ctx, cfg)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("dial feishu websocket: %w", err)
	}
	defer conn.Close()
	conn.SetReadLimit(wsReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))

	// ctx 取消时强制关闭连接（解除 ReadMessage 阻塞）。
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		if messageType != websocket.BinaryMessage {
			continue
		}
		if err := p.handleWSFrame(ctx, cfg, conn, handle, data); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

// wsEndpoint 获取长连接接入 URL（AppID/AppSecret 明文体，无需 tenant token）。
func (p *Provider) wsEndpoint(ctx context.Context, cfg connectors.Config) (string, error) {
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	appSecret := cfg.Secret(secretAppSecret)
	var response wsEndpointResponse
	if err := p.doJSON(ctx, http.MethodPost, base, wsEndpointPath, "", map[string]string{
		"AppID":     strings.TrimSpace(cfg.Secret(settingAppID)),
		"AppSecret": appSecret,
	}, &response, appSecret); err != nil {
		return "", err
	}
	if response.Code != 0 {
		return "", newAPIError("callback/ws/endpoint", response.Code, response.Msg, appSecret)
	}
	endpoint := strings.TrimSpace(firstNonEmpty(response.Data.URL, response.Data.URLLower))
	if endpoint == "" {
		return "", errors.New("feishu: websocket endpoint response did not include a url")
	}
	return endpoint, nil
}

// handleWSFrame 处理一帧：ping → pong；event → 入站解析 + ack。
func (p *Provider) handleWSFrame(ctx context.Context, cfg connectors.Config, conn *websocket.Conn, handle connectors.InboundHandler, data []byte) error {
	frame, err := parseWSFrame(data)
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(frame.headerValue(wsHeaderType))) {
	case wsHeaderPing:
		return writeWSFrame(conn, wsPongFrame(frame))
	case "", wsHeaderEvent:
		return p.handleWSEventFrame(ctx, cfg, conn, handle, frame)
	default:
		return nil // 未知控制帧：忽略
	}
}

// handleWSEventFrame 解析事件 payload 并回 ack；payload 为 v2 事件 JSON
// （与 webhook 同构，复用 inboundMessage / cardActionMessage）。
func (p *Provider) handleWSEventFrame(ctx context.Context, cfg connectors.Config, conn *websocket.Conn, handle connectors.InboundHandler, frame wsFrame) error {
	var envelope webhookEnvelope
	if err := json.Unmarshal(frame.Payload, &envelope); err != nil {
		_ = writeWSFrame(conn, wsAckFrame(frame, p.now()))
		return fmt.Errorf("decode feishu ws event: %s", err.Error())
	}
	message, ok, parseErr := p.parseWSEvent(cfg, envelope.Header, envelope.Event)
	ackErr := writeWSFrame(conn, wsAckFrame(frame, p.now()))
	if parseErr != nil {
		if ackErr != nil {
			return errors.Join(parseErr, ackErr)
		}
		return parseErr
	}
	if !ok {
		return ackErr
	}
	if err := handle(ctx, message); err != nil {
		if ackErr != nil {
			return errors.Join(err, ackErr)
		}
		return err
	}
	return ackErr
}

// parseWSEvent 复用 webhook 入站解析（消息 + 卡片按钮）。
func (p *Provider) parseWSEvent(cfg connectors.Config, header eventHeader, raw json.RawMessage) (connectors.InboundMessage, bool, error) {
	switch strings.TrimSpace(header.EventType) {
	case eventMessageReceive:
		return p.inboundMessage(cfg, header, raw)
	case eventCardAction:
		return cardActionMessage(cfg, header, raw, p.now)
	default:
		return connectors.InboundMessage{}, false, nil
	}
}

// ── 帧编解码（飞书私有 protobuf） ───────────────────────────────────────────

// wsHeader 是帧 header（key/value 对）。
type wsHeader struct {
	Key   string
	Value string
}

// wsFrame 是一帧（protobuf 字段见文件头注释）。
type wsFrame struct {
	SeqID           uint64
	LogID           uint64
	Service         int32
	Method          int32
	Headers         []wsHeader
	PayloadEncoding string
	PayloadType     string
	Payload         []byte
	LogIDNew        string
}

// headerValue 取 header 值（大小写不敏感）。
func (f wsFrame) headerValue(key string) string {
	key = strings.TrimSpace(key)
	for _, header := range f.Headers {
		if strings.EqualFold(strings.TrimSpace(header.Key), key) {
			return strings.TrimSpace(header.Value)
		}
	}
	return ""
}

// wsAckFrame 构造 ack 帧：原 headers 去掉 biz_rt 后追加 biz_rt=当前毫秒，
// payload={"code":200}（官方协议应答）。
func wsAckFrame(frame wsFrame, now time.Time) wsFrame {
	headers := make([]wsHeader, 0, len(frame.Headers)+1)
	for _, header := range frame.Headers {
		if strings.EqualFold(strings.TrimSpace(header.Key), wsHeaderBizRT) {
			continue
		}
		headers = append(headers, header)
	}
	headers = append(headers, wsHeader{Key: wsHeaderBizRT, Value: strconv.FormatInt(now.UnixMilli(), 10)})
	return wsFrame{
		SeqID:           frame.SeqID,
		LogID:           frame.LogID,
		Service:         frame.Service,
		Method:          frame.Method,
		Headers:         headers,
		PayloadEncoding: "json",
		PayloadType:     "string",
		Payload:         []byte(`{"code":200}`),
		LogIDNew:        frame.LogIDNew,
	}
}

// wsPongFrame 构造 pong 帧（control 方法 + type=pong）。
func wsPongFrame(frame wsFrame) wsFrame {
	return wsFrame{
		SeqID:   frame.SeqID,
		LogID:   frame.LogID,
		Service: frame.Service,
		Method:  wsFrameMethodControl,
		Headers: []wsHeader{{Key: wsHeaderType, Value: wsHeaderPong}},
	}
}

// writeWSFrame 编码并写一帧（二进制）。
func writeWSFrame(conn *websocket.Conn, frame wsFrame) error {
	if conn == nil {
		return nil
	}
	data, err := marshalWSFrame(frame)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.BinaryMessage, data)
}

// marshalWSFrame 编码帧为飞书私有 protobuf。
func marshalWSFrame(frame wsFrame) ([]byte, error) {
	buffer := make([]byte, 0, len(frame.Payload)+128)
	buffer = appendProtoVarintField(buffer, 1, frame.SeqID)
	buffer = appendProtoVarintField(buffer, 2, frame.LogID)
	buffer = appendProtoVarintField(buffer, 3, uint64(frame.Service))
	buffer = appendProtoVarintField(buffer, 4, uint64(frame.Method))
	for _, header := range frame.Headers {
		headerBytes := make([]byte, 0, len(header.Key)+len(header.Value)+8)
		headerBytes = appendProtoBytesField(headerBytes, 1, []byte(header.Key))
		headerBytes = appendProtoBytesField(headerBytes, 2, []byte(header.Value))
		buffer = appendProtoBytesField(buffer, 5, headerBytes)
	}
	if frame.PayloadEncoding != "" {
		buffer = appendProtoBytesField(buffer, 6, []byte(frame.PayloadEncoding))
	}
	if frame.PayloadType != "" {
		buffer = appendProtoBytesField(buffer, 7, []byte(frame.PayloadType))
	}
	if len(frame.Payload) > 0 {
		buffer = appendProtoBytesField(buffer, 8, frame.Payload)
	}
	if frame.LogIDNew != "" {
		buffer = appendProtoBytesField(buffer, 9, []byte(frame.LogIDNew))
	}
	return buffer, nil
}

// parseWSFrame 解码飞书私有 protobuf 帧。
func parseWSFrame(data []byte) (wsFrame, error) {
	frame := wsFrame{}
	for len(data) > 0 {
		tag, rest, err := readProtoUvarint(data)
		if err != nil {
			return wsFrame{}, err
		}
		data = rest
		fieldNumber := int(tag >> 3)
		wireType := int(tag & 0x7)
		switch wireType {
		case 0:
			value, next, err := readProtoUvarint(data)
			if err != nil {
				return wsFrame{}, err
			}
			switch fieldNumber {
			case 1:
				frame.SeqID = value
			case 2:
				frame.LogID = value
			case 3:
				frame.Service = int32(value)
			case 4:
				frame.Method = int32(value)
			}
			data = next
		case 2:
			value, next, err := readProtoBytes(data)
			if err != nil {
				return wsFrame{}, err
			}
			switch fieldNumber {
			case 5:
				header, err := parseWSFrameHeader(value)
				if err != nil {
					return wsFrame{}, err
				}
				frame.Headers = append(frame.Headers, header)
			case 6:
				frame.PayloadEncoding = string(value)
			case 7:
				frame.PayloadType = string(value)
			case 8:
				frame.Payload = append([]byte(nil), value...)
			case 9:
				frame.LogIDNew = string(value)
			}
			data = next
		default:
			return wsFrame{}, fmt.Errorf("unsupported feishu protobuf wire type %d", wireType)
		}
	}
	return frame, nil
}

// parseWSFrameHeader 解码 header（重复 {key,value} bytes）。
func parseWSFrameHeader(data []byte) (wsHeader, error) {
	header := wsHeader{}
	for len(data) > 0 {
		tag, rest, err := readProtoUvarint(data)
		if err != nil {
			return wsHeader{}, err
		}
		data = rest
		fieldNumber := int(tag >> 3)
		wireType := int(tag & 0x7)
		if wireType != 2 {
			return wsHeader{}, fmt.Errorf("unsupported feishu header wire type %d", wireType)
		}
		value, next, err := readProtoBytes(data)
		if err != nil {
			return wsHeader{}, err
		}
		switch fieldNumber {
		case 1:
			header.Key = string(value)
		case 2:
			header.Value = string(value)
		}
		data = next
	}
	return header, nil
}

// ── protobuf 原语 ───────────────────────────────────────────────────────────

func appendProtoVarintField(buffer []byte, fieldNumber int, value uint64) []byte {
	buffer = appendProtoUvarint(buffer, uint64(fieldNumber<<3))
	return appendProtoUvarint(buffer, value)
}

func appendProtoBytesField(buffer []byte, fieldNumber int, value []byte) []byte {
	buffer = appendProtoUvarint(buffer, uint64(fieldNumber<<3|2))
	buffer = appendProtoUvarint(buffer, uint64(len(value)))
	return append(buffer, value...)
}

func appendProtoUvarint(buffer []byte, value uint64) []byte {
	for value >= 0x80 {
		buffer = append(buffer, byte(value)|0x80)
		value >>= 7
	}
	return append(buffer, byte(value))
}

func readProtoUvarint(data []byte) (uint64, []byte, error) {
	var (
		value uint64
		shift uint
	)
	for index, item := range data {
		value |= uint64(item&0x7f) << shift
		if item < 0x80 {
			return value, data[index+1:], nil
		}
		shift += 7
		if shift > 63 {
			return 0, nil, errors.New("invalid feishu protobuf varint")
		}
	}
	return 0, nil, io.ErrUnexpectedEOF
}

func readProtoBytes(data []byte) ([]byte, []byte, error) {
	length, rest, err := readProtoUvarint(data)
	if err != nil {
		return nil, nil, err
	}
	if uint64(len(rest)) < length {
		return nil, nil, io.ErrUnexpectedEOF
	}
	return rest[:length], rest[length:], nil
}
