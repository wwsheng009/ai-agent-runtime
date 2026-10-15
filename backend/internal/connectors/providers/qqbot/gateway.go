// gateway.go 是 QQ 机器人 WS gateway 的接收循环：op 10 hello → op 2 Identify /
// op 6 Resume → op 0 事件分发 → op 1 心跳 / op 11 ack，断线指数退避重连。
// 协议细节镜像上游 codex-server/backend/internal/bots/qqbot.go，语义适配本仓
// connectors.PollingProvider（游标走 PollingState，ctx 取消即退出返回 nil）。
package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// QQ 机器人 gateway 协议 op 码。
const (
	gatewayOpDispatch       = 0
	gatewayOpHeartbeat      = 1
	gatewayOpIdentify       = 2
	gatewayOpResume         = 6
	gatewayOpReconnect      = 7
	gatewayOpInvalidSession = 9
	gatewayOpHello          = 10
	gatewayOpHeartbeatACK   = 11

	gatewayHelloTimeout = 15 * time.Second
	gatewayReadyTimeout = 15 * time.Second
)

// gatewayPayload 是 gateway 上下行的统一信封。
type gatewayPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

type gatewayHelloData struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

type gatewayReadyData struct {
	SessionID string `json:"session_id"`
}

type gatewayReadResult struct {
	payload gatewayPayload
	err     error
}

// gatewaySessionState 是一次连接内的网关会话游标（对应 PollingState 两键）。
type gatewaySessionState struct {
	sessionID string
	seq       int64
}

// reconnectError 表示"断开需要重连"（可重试）；clearSession 表示平台拒绝
// Resume（op 9 d=false 等），必须丢弃 session_id/seq 重新 Identify。
type reconnectError struct {
	clearSession bool
}

func (e *reconnectError) Error() string {
	if e == nil {
		return ""
	}
	return "qqbot: gateway reconnect required"
}

// RunPolling 运行 gateway 长连接直至 ctx 取消（返回 nil）：
//   - 断线/协议错误按指数退避重连（上限 maxRetryDelay），优先 Resume，
//     op 9 不可续传时清空游标后 Identify；
//   - 游标（gateway_session_id / gateway_seq）每次事件后经 PollingState 落盘，
//     宿主重启后可用 Resume 续传；
//   - 单条入站消息处理失败不回滚游标、不中断连接（与 telegram 轮询一致）。
func (p *Provider) RunPolling(ctx context.Context, cfg connectors.Config, handle connectors.InboundHandler, state connectors.PollingState) error {
	if p.appID(cfg) == "" || p.appSecret(cfg) == "" {
		return errors.New("qqbot: app_id and app_secret are required")
	}
	if handle == nil {
		return errors.New("qqbot: inbound handler is required")
	}
	backoff := p.retryDelay
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := p.runGatewaySession(ctx, cfg, handle, state)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		var reconnect *reconnectError
		if !errors.As(err, &reconnect) {
			return err
		}
		if reconnect.clearSession {
			p.clearGatewayState(state)
		}
		p.logf("qqbot: gateway disconnected: %v (retry in %s)", err, backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxRetryDelay {
			backoff = maxRetryDelay
		}
	}
}

// runGatewaySession 建立一次 gateway 连接并接收事件，直至出错（返回
// reconnectError）或 ctx 取消（返回 nil）。
func (p *Provider) runGatewaySession(ctx context.Context, cfg connectors.Config, handle connectors.InboundHandler, state connectors.PollingState) error {
	token, err := p.accessToken(ctx, cfg, false)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		p.logf("qqbot: access token unavailable: %v", err)
		return &reconnectError{}
	}
	gatewayURL, err := p.gatewayURL(ctx, p.baseFor(cfg), token, p.appSecret(cfg))
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		p.logf("qqbot: fetch gateway url failed: %v", err)
		return &reconnectError{}
	}
	conn, _, err := p.dialer.DialContext(ctx, gatewayURL, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		p.logf("qqbot: dial gateway failed: %s", redact(err.Error(), token))
		return &reconnectError{}
	}
	defer conn.Close()

	hello, err := p.readHello(conn)
	if err != nil {
		p.logf("qqbot: read gateway hello failed: %s", redact(err.Error(), token))
		return &reconnectError{}
	}

	current := p.loadGatewayState(state)
	resumed := current.sessionID != "" && current.seq > 0
	if resumed {
		// 优先 Resume：session_id + 最新 seq 续传，避免漏事件。
		if err := conn.WriteJSON(map[string]any{
			"op": gatewayOpResume,
			"d": map[string]any{
				"token":      "QQBot " + token,
				"session_id": current.sessionID,
				"seq":        current.seq,
			},
		}); err != nil {
			return &reconnectError{}
		}
	} else {
		if err := conn.WriteJSON(map[string]any{
			"op": gatewayOpIdentify,
			"d": map[string]any{
				"token":   "QQBot " + token,
				"intents": p.intentsFor(cfg),
				"shard":   [2]int{0, 1},
			},
		}); err != nil {
			return &reconnectError{}
		}
	}

	established, err := p.waitForReady(conn, current)
	if err != nil {
		return err
	}
	current = established
	p.persistGatewayState(state, current)
	p.logf("qqbot: gateway connected (session_id=%s, seq=%d, resumed=%v)", current.sessionID, current.seq, resumed)

	// 读协程：唯一的并发读者（gorilla/websocket 允许一读一写）。
	// sessionDone 让读协程随本次连接会话结束而退出（重连不依赖 ctx 取消）。
	reads := make(chan gatewayReadResult, 1)
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go p.readLoop(conn, reads, sessionDone)

	ticker := time.NewTicker(time.Duration(hello.HeartbeatInterval) * time.Millisecond)
	defer ticker.Stop()

	var writeMu sync.Mutex
	heartbeatAcked := true
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if !heartbeatAcked {
				p.logf("qqbot: heartbeat not acknowledged in time; reconnecting")
				return &reconnectError{}
			}
			heartbeatAcked = false
			if err := p.sendHeartbeat(conn, &writeMu, current.seq); err != nil {
				return &reconnectError{}
			}
		case result := <-reads:
			if result.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return &reconnectError{}
			}
			payload := result.payload
			if payload.S != nil {
				current.seq = *payload.S
			}
			switch payload.Op {
			case gatewayOpDispatch:
				next, err := p.handleDispatch(ctx, cfg, payload, current, handle, state)
				if err != nil {
					return err
				}
				current = next
			case gatewayOpHeartbeat:
				// 平台主动要求心跳：立即用最新 seq 应答。
				if err := p.sendHeartbeat(conn, &writeMu, current.seq); err != nil {
					return &reconnectError{}
				}
			case gatewayOpHeartbeatACK:
				heartbeatAcked = true
			case gatewayOpReconnect:
				return &reconnectError{}
			case gatewayOpInvalidSession:
				var resumable bool
				_ = json.Unmarshal(payload.D, &resumable)
				return &reconnectError{clearSession: !resumable}
			}
		}
	}
}

// handleDispatch 处理 op 0 事件：READY/RESUMED 更新会话；消息事件解析为
// InboundMessage 并交给 handle；其余事件仅推进游标。
func (p *Provider) handleDispatch(ctx context.Context, cfg connectors.Config, payload gatewayPayload, current gatewaySessionState, handle connectors.InboundHandler, state connectors.PollingState) (gatewaySessionState, error) {
	switch strings.TrimSpace(payload.T) {
	case "READY":
		var ready gatewayReadyData
		if err := json.Unmarshal(payload.D, &ready); err != nil {
			return current, &reconnectError{}
		}
		current.sessionID = strings.TrimSpace(ready.SessionID)
		p.persistGatewayState(state, current)
	case "RESUMED":
		p.persistGatewayState(state, current)
	default:
		msg, ok := parseGatewayMessage(cfg, payload.T, payload.D)
		// 游标先落盘：消息一旦认领（进入 handle），重启后不应重复消费。
		p.persistGatewayState(state, current)
		if !ok {
			return current, nil
		}
		p.rememberInboundMsgID(msg.Chat, msg.MessageID)
		if err := handle(ctx, msg); err != nil {
			p.logf("qqbot: handle message %s failed: %v", msg.MessageID, err)
		}
	}
	return current, nil
}

// readLoop 是唯一的读协程：把下行信封送入 reads；连接关闭或会话结束
// （sessionDone 关闭）时退出，避免重连后残留阻塞的读协程。
func (p *Provider) readLoop(conn *websocket.Conn, reads chan<- gatewayReadResult, sessionDone <-chan struct{}) {
	for {
		var payload gatewayPayload
		if err := conn.ReadJSON(&payload); err != nil {
			select {
			case reads <- gatewayReadResult{err: err}:
			case <-sessionDone:
			}
			return
		}
		select {
		case reads <- gatewayReadResult{payload: payload}:
		case <-sessionDone:
			return
		}
	}
}

// readHello 读取 op 10 hello（限时 gatewayHelloTimeout），返回心跳间隔。
func (p *Provider) readHello(conn *websocket.Conn) (gatewayHelloData, error) {
	if err := conn.SetReadDeadline(p.now().Add(gatewayHelloTimeout)); err != nil {
		return gatewayHelloData{}, err
	}
	defer conn.SetReadDeadline(time.Time{})
	var payload gatewayPayload
	if err := conn.ReadJSON(&payload); err != nil {
		return gatewayHelloData{}, err
	}
	if payload.Op != gatewayOpHello {
		return gatewayHelloData{}, fmt.Errorf("qqbot: unexpected gateway op %d, want hello", payload.Op)
	}
	var hello gatewayHelloData
	if err := json.Unmarshal(payload.D, &hello); err != nil {
		return gatewayHelloData{}, err
	}
	if hello.HeartbeatInterval <= 0 {
		return gatewayHelloData{}, errors.New("qqbot: gateway hello missing heartbeat_interval")
	}
	return hello, nil
}

// waitForReady 等待握手结果（READY/RESUMED），期间用 payload.s 推进 seq；
// op 9 不可续传时要求清空游标重连。
func (p *Provider) waitForReady(conn *websocket.Conn, current gatewaySessionState) (gatewaySessionState, error) {
	if err := conn.SetReadDeadline(p.now().Add(gatewayReadyTimeout)); err != nil {
		return current, &reconnectError{}
	}
	defer conn.SetReadDeadline(time.Time{})
	for {
		var payload gatewayPayload
		if err := conn.ReadJSON(&payload); err != nil {
			return current, &reconnectError{}
		}
		if payload.S != nil {
			current.seq = *payload.S
		}
		switch payload.Op {
		case gatewayOpDispatch:
			switch strings.TrimSpace(payload.T) {
			case "READY":
				var ready gatewayReadyData
				if err := json.Unmarshal(payload.D, &ready); err != nil {
					return current, &reconnectError{}
				}
				current.sessionID = strings.TrimSpace(ready.SessionID)
				return current, nil
			case "RESUMED":
				return current, nil
			}
		case gatewayOpInvalidSession:
			var resumable bool
			_ = json.Unmarshal(payload.D, &resumable)
			return current, &reconnectError{clearSession: !resumable}
		case gatewayOpReconnect:
			return current, &reconnectError{}
		}
	}
}

// sendHeartbeat 发送 op 1 心跳（d 为最新 seq；无事件时为 null）。
func (p *Provider) sendHeartbeat(conn *websocket.Conn, writeMu *sync.Mutex, seq int64) error {
	payload := gatewayPayload{Op: gatewayOpHeartbeat, D: json.RawMessage("null")}
	if seq > 0 {
		data, err := json.Marshal(seq)
		if err != nil {
			return err
		}
		payload.D = data
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	return conn.WriteJSON(payload)
}

// gatewayURL 获取 WSS 接入点：优先 GET {base}/gateway（通用接入点），
// 404/405 时回退 GET {base}/gateway/bot（带分片接入点，上游实现使用）。
func (p *Provider) gatewayURL(ctx context.Context, base, token, appSecret string) (string, error) {
	var lastErr error
	for _, endpoint := range []string{base + "/gateway", base + "/gateway/bot"} {
		var result struct {
			URL string `json:"url"`
		}
		err := p.doJSON(ctx, http.MethodGet, endpoint, token, []string{appSecret}, nil, &result)
		if err == nil {
			if gatewayURL := strings.TrimSpace(result.URL); gatewayURL != "" {
				return gatewayURL, nil
			}
			lastErr = errors.New("qqbot: gateway response missing url")
			continue
		}
		lastErr = err
		var apiErr *apiError
		if errors.As(err, &apiErr) && (apiErr.statusCode == http.StatusNotFound || apiErr.statusCode == http.StatusMethodNotAllowed) {
			continue
		}
		return "", err
	}
	return "", lastErr
}

// ── 游标（PollingState） ─────────────────────────────────────────────────────

// loadGatewayState 从 PollingState 恢复 session_id/seq（seq 需 > 0 才可 Resume）。
func (p *Provider) loadGatewayState(state connectors.PollingState) gatewaySessionState {
	var current gatewaySessionState
	if state == nil {
		return current
	}
	if sessionID, ok := state.Get(gatewaySessionKey); ok {
		current.sessionID = strings.TrimSpace(sessionID)
	}
	if raw, ok := state.Get(gatewaySeqKey); ok {
		if parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && parsed > 0 {
			current.seq = parsed
		}
	}
	return current
}

// persistGatewayState 把 session_id/seq 写入 PollingState，保证宿主重启后可
// Resume 续传。写失败仅记日志：连接继续，最坏情况重启后重新 Identify
// （平台重投由入站去重账本兜底）。
func (p *Provider) persistGatewayState(state connectors.PollingState, current gatewaySessionState) {
	if state == nil {
		return
	}
	if err := state.Set(gatewaySessionKey, current.sessionID); err != nil {
		p.logf("qqbot: persist gateway session_id failed: %v", err)
	}
	if err := state.Set(gatewaySeqKey, strconv.FormatInt(current.seq, 10)); err != nil {
		p.logf("qqbot: persist gateway seq failed: %v", err)
	}
}

// clearGatewayState 丢弃游标（op 9 判定不可续传后，下次连接走 Identify）。
func (p *Provider) clearGatewayState(state connectors.PollingState) {
	if state == nil {
		return
	}
	if err := state.Set(gatewaySessionKey, ""); err != nil {
		p.logf("qqbot: clear gateway session_id failed: %v", err)
	}
	if err := state.Set(gatewaySeqKey, "0"); err != nil {
		p.logf("qqbot: clear gateway seq failed: %v", err)
	}
}

// ── 事件解析 ────────────────────────────────────────────────────────────────

// messageReference 是入站消息的引用上下文（被引用消息摘要，事件可选字段）。
type messageReference struct {
	MessageID         string         `json:"message_id"`
	Content           string         `json:"content"`
	Title             string         `json:"title"`
	Message           *quotedMessage `json:"message"`
	ReferencedMessage *quotedMessage `json:"referenced_message"`
	SourceMessage     *quotedMessage `json:"source_message"`
}

// quotedMessage 是引用消息的文本载体（content/title 二选一）。
type quotedMessage struct {
	Content string `json:"content"`
	Title   string `json:"title"`
}

// gatewayMsgElement 是消息元素（官方 msg_elements：引用消息/合并转发/嵌套内容的
// 载体；message_type 0=文本 3=卡片 101=并行 102=聊天记录 103=引用）。
type gatewayMsgElement struct {
	MsgIdx      string              `json:"msg_idx"`
	MessageType int                 `json:"message_type"`
	Content     string              `json:"content"`
	Attachments []gatewayAttachment `json:"attachments"`
	ArkData     *gatewayArkData     `json:"ark_data"`
	MsgElements []gatewayMsgElement `json:"msg_elements"`
}

// gatewayArkData 是结构化卡片数据（message_type=3 的 ark_data 字段）。
type gatewayArkData struct {
	Prompt  string         `json:"prompt"`
	ArkType string         `json:"ark_type"`
	ArkName string         `json:"ark_name"`
	Fields  map[string]any `json:"fields"`
}

// inboundTextInput 汇总文本降级所需的全部来源（内容/引用/附件/消息元素/卡片）。
type inboundTextInput struct {
	Content      string
	StripMention bool
	Reference    *messageReference
	Attachments  []gatewayAttachment
	Elements     []gatewayMsgElement
	Ark          *gatewayArkData
}

// parseGatewayMessage 把 gateway 事件解析为 InboundMessage：
//   - Chat 用稳定键：群 "group:<group_openid>"（share_session_in_channel=false
//     时追加 ":user:<member_openid>" 按人隔离）、C2C "user:<openid>"、
//     频道 "channel:<channel_id>"；
//   - GROUP_AT_MESSAGE_CREATE（群@）与 GROUP_MESSAGE_CREATE（全量群消息，
//     按 group_message_mode 过滤）都走同一组装；
//   - MessageID 用事件 id（同时是 QQ 被动回复的 msg_id）；
//   - UserID 用 author.member_openid / author.user_openid / author.id；
//   - UserName 优先 author.username 昵称，缺省回退 openid/id；
//   - Text 去空白（群/频道消息额外剥掉开头的 @机器人 标记；引用消息拼接
//     "Quoted: …" 前缀）；媒体-only 消息不丢弃——语音优先取 asr_refer_text
//     转写，其余按附件生成 "[图片/语音/视频/文件: 名称]" 占位；
//   - Media 携带 attachments 元数据（url/文件名/类型/大小，下载留待媒体管线）。
func parseGatewayMessage(cfg connectors.Config, eventType string, data json.RawMessage) (connectors.InboundMessage, bool) {
	connectorID := cfg.ID
	switch strings.TrimSpace(eventType) {
	case "GROUP_AT_MESSAGE_CREATE":
		var event struct {
			ID               string              `json:"id"`
			MsgID            string              `json:"msg_id"`
			GroupOpenID      string              `json:"group_openid"`
			Content          string              `json:"content"`
			MessageReference *messageReference   `json:"message_reference"`
			Attachments      []gatewayAttachment `json:"attachments"`
			MsgElements      []gatewayMsgElement `json:"msg_elements"`
			ArkData          *gatewayArkData     `json:"ark_data"`
			Author           struct {
				MemberOpenID string `json:"member_openid"`
				Username     string `json:"username"`
			} `json:"author"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return connectors.InboundMessage{}, false
		}
		return buildGroupInboundMessage(cfg, connectorID,
			firstNonEmpty(event.ID, event.MsgID),
			strings.TrimSpace(event.GroupOpenID),
			strings.TrimSpace(event.Author.MemberOpenID),
			event.Author.Username,
			inboundTextInput{
				Content:     event.Content,
				Reference:   event.MessageReference,
				Attachments: event.Attachments,
				Elements:    event.MsgElements,
				Ark:         event.ArkData,
			})
	case "GROUP_MESSAGE_CREATE":
		// 全量群消息（平台开启「接收所有消息」后推送；intent 同为 1<<25）。
		// 触发策略（group_message_mode，缺省 mention）：mention=仅@机器人的
		// 消息进入 agent（mentions 含 bot:true 或原始 content 带 <@ 标记）；
		// all=每条群消息都参与（需运维显式开启，避免刷屏）。
		var event struct {
			ID               string              `json:"id"`
			MsgID            string              `json:"msg_id"`
			GroupOpenID      string              `json:"group_openid"`
			Content          string              `json:"content"`
			MessageReference *messageReference   `json:"message_reference"`
			Attachments      []gatewayAttachment `json:"attachments"`
			Mentions         []gatewayMention    `json:"mentions"`
			MsgElements      []gatewayMsgElement `json:"msg_elements"`
			ArkData          *gatewayArkData     `json:"ark_data"`
			Author           struct {
				MemberOpenID string `json:"member_openid"`
				Username     string `json:"username"`
			} `json:"author"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return connectors.InboundMessage{}, false
		}
		if groupMessageModeSetting(cfg) != "all" && !groupMentionsBot(event.Mentions, event.Content) {
			return connectors.InboundMessage{}, false
		}
		return buildGroupInboundMessage(cfg, connectorID,
			firstNonEmpty(event.ID, event.MsgID),
			strings.TrimSpace(event.GroupOpenID),
			strings.TrimSpace(event.Author.MemberOpenID),
			event.Author.Username,
			inboundTextInput{
				Content:     event.Content,
				Reference:   event.MessageReference,
				Attachments: event.Attachments,
				Elements:    event.MsgElements,
				Ark:         event.ArkData,
			})
	case "C2C_MESSAGE_CREATE":
		var event struct {
			ID               string              `json:"id"`
			MsgID            string              `json:"msg_id"`
			Content          string              `json:"content"`
			MessageReference *messageReference   `json:"message_reference"`
			Attachments      []gatewayAttachment `json:"attachments"`
			MsgElements      []gatewayMsgElement `json:"msg_elements"`
			ArkData          *gatewayArkData     `json:"ark_data"`
			Author           struct {
				UserOpenID string `json:"user_openid"`
				Username   string `json:"username"`
			} `json:"author"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return connectors.InboundMessage{}, false
		}
		messageID := firstNonEmpty(event.ID, event.MsgID)
		userOpenID := strings.TrimSpace(event.Author.UserOpenID)
		if messageID == "" || userOpenID == "" {
			return connectors.InboundMessage{}, false
		}
		text := normalizeInboundText(inboundTextInput{
			Content:     event.Content,
			Reference:   event.MessageReference,
			Attachments: event.Attachments,
			Elements:    event.MsgElements,
			Ark:         event.ArkData,
		})
		if text == "" {
			return connectors.InboundMessage{}, false
		}
		return connectors.InboundMessage{
			ConnectorID: connectorID,
			Provider:    Name,
			Chat:        chatKindUser + ":" + userOpenID,
			MessageID:   messageID,
			UserID:      userOpenID,
			UserName:    firstNonEmpty(event.Author.Username, userOpenID),
			Text:        text,
			Media:       attachmentsToMedia(event.Attachments),
			ProviderData: map[string]any{
				"qqbot_message_type": chatKindUser,
				"qqbot_user_openid":  userOpenID,
				"qqbot_event_msg_id": messageID,
			},
			ReceivedAt: time.Now().UTC(),
		}, true
	case "AT_MESSAGE_CREATE":
		// 频道（guild）@机器人消息：防御性解析（默认 intents 未订阅频道事件，
		// 后续切片放开 intents 后即可用）；发送路径未实现，见 Send。
		var event struct {
			ID               string              `json:"id"`
			Content          string              `json:"content"`
			ChannelID        string              `json:"channel_id"`
			GuildID          string              `json:"guild_id"`
			MessageReference *messageReference   `json:"message_reference"`
			Attachments      []gatewayAttachment `json:"attachments"`
			MsgElements      []gatewayMsgElement `json:"msg_elements"`
			ArkData          *gatewayArkData     `json:"ark_data"`
			Author           struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"author"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return connectors.InboundMessage{}, false
		}
		messageID := strings.TrimSpace(event.ID)
		channelID := strings.TrimSpace(event.ChannelID)
		userID := strings.TrimSpace(event.Author.ID)
		if messageID == "" || channelID == "" || userID == "" {
			return connectors.InboundMessage{}, false
		}
		text := normalizeInboundText(inboundTextInput{
			Content:      event.Content,
			Reference:    event.MessageReference,
			Attachments:  event.Attachments,
			Elements:     event.MsgElements,
			Ark:          event.ArkData,
			StripMention: true,
		})
		if text == "" {
			return connectors.InboundMessage{}, false
		}
		return connectors.InboundMessage{
			ConnectorID: connectorID,
			Provider:    Name,
			Chat:        chatKindChannel + ":" + channelID,
			ChatTitle:   channelID,
			MessageID:   messageID,
			UserID:      userID,
			UserName:    firstNonEmpty(event.Author.Username, userID),
			Text:        text,
			Media:       attachmentsToMedia(event.Attachments),
			ProviderData: map[string]any{
				"qqbot_message_type": chatKindChannel,
				"qqbot_channel_id":   channelID,
				"qqbot_guild_id":     strings.TrimSpace(event.GuildID),
				"qqbot_event_msg_id": messageID,
			},
			ReceivedAt: time.Now().UTC(),
		}, true
	default:
		return connectors.InboundMessage{}, false
	}
}

// gatewayMention 是群消息 @ 列表项（官方 mentions 字段子集）。
type gatewayMention struct {
	ID  string `json:"id"`
	Bot bool   `json:"bot"`
}

// groupMentionsBot 判断全量群消息是否@了机器人：mentions 含 bot:true 的
// 用户，或原始 content 保留 <@ 标记（防御：不同版本清洗时机不一）。
func groupMentionsBot(mentions []gatewayMention, content string) bool {
	for _, mention := range mentions {
		if mention.Bot {
			return true
		}
	}
	return strings.Contains(content, "<@")
}

// groupMessageModeSetting 读取群消息接收模式（group_message_mode）：
// mention（缺省）= 仅@机器人的消息；all = 全部群消息。非法值回落 mention。
func groupMessageModeSetting(cfg connectors.Config) string {
	if strings.EqualFold(strings.TrimSpace(cfg.Setting("group_message_mode", "")), "all") {
		return "all"
	}
	return "mention"
}

// buildGroupInboundMessage 组装群聊入站消息（GROUP_AT / GROUP_MESSAGE 共用）：
// chat 键含按人隔离后缀（share_session_in_channel=false），昵称回退 openid。
func buildGroupInboundMessage(cfg connectors.Config, connectorID, messageID, groupOpenID, memberOpenID, username string, in inboundTextInput) (connectors.InboundMessage, bool) {
	if messageID == "" || groupOpenID == "" || memberOpenID == "" {
		return connectors.InboundMessage{}, false
	}
	in.StripMention = true
	text := normalizeInboundText(in)
	if text == "" {
		return connectors.InboundMessage{}, false
	}
	chat := chatKindGroup + ":" + groupOpenID
	if !shareSessionInChannelSetting(cfg) {
		chat += ":user:" + memberOpenID
	}
	return connectors.InboundMessage{
		ConnectorID: connectorID,
		Provider:    Name,
		Chat:        chat,
		ChatTitle:   groupOpenID,
		MessageID:   messageID,
		UserID:      memberOpenID,
		UserName:    firstNonEmpty(username, memberOpenID),
		Text:        text,
		Media:       attachmentsToMedia(in.Attachments),
		ProviderData: map[string]any{
			"qqbot_message_type":  chatKindGroup,
			"qqbot_group_openid":  groupOpenID,
			"qqbot_member_openid": memberOpenID,
			"qqbot_event_msg_id":  messageID,
		},
		ReceivedAt: time.Now().UTC(),
	}, true
}

// gatewayAttachment 是入站消息附件（官方 attachments 字段：图片/视频/语音/文件）。
// 语音另带 voice_wav_url（SILK 转换后 WAV）与 asr_refer_text（ASR 参考转写）。
type gatewayAttachment struct {
	URL          string `json:"url"`
	FileName     string `json:"filename"`
	ContentType  string `json:"content_type"`
	Size         int64  `json:"size"`
	VoiceWavURL  string `json:"voice_wav_url"`
	ASRReferText string `json:"asr_refer_text"`
}

// attachmentsToMedia 把官方 attachments 映射为核心 Media 元数据（additive）：
// 下载/转存留给后续媒体管线；本层保证元数据（含带签名的 url）不丢。
func attachmentsToMedia(attachments []gatewayAttachment) []connectors.Media {
	if len(attachments) == 0 {
		return nil
	}
	medias := make([]connectors.Media, 0, len(attachments))
	for _, item := range attachments {
		url := strings.TrimSpace(item.URL)
		fileName := strings.TrimSpace(item.FileName)
		if url == "" && fileName == "" {
			continue
		}
		kind := attachmentKind(item.ContentType)
		if kind == "voice" {
			// 语音优先用官方转换后的 WAV（agent 可直接读取；SILK 需专用解码），
			// 无转换结果时回退原始地址。
			if wav := strings.TrimSpace(item.VoiceWavURL); wav != "" {
				url = wav
			}
		}
		medias = append(medias, connectors.Media{
			Kind:        kind,
			FileName:    fileName,
			ContentType: strings.TrimSpace(item.ContentType),
			Size:        item.Size,
			URL:         url,
		})
	}
	if len(medias) == 0 {
		return nil
	}
	return medias
}

// attachmentKind 映射 content_type → 核心媒体类别（image|voice|video|file）。
func attachmentKind(contentType string) string {
	lower := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case lower == "voice":
		return "voice"
	case strings.HasPrefix(lower, "image/"):
		return "image"
	case strings.HasPrefix(lower, "video/"):
		return "video"
	default:
		return "file"
	}
}

// voiceASRText 返回首个语音附件的 ASR 参考转写（无则空串）。
func voiceASRText(attachments []gatewayAttachment) string {
	for _, item := range attachments {
		if strings.ToLower(strings.TrimSpace(item.ContentType)) != "voice" {
			continue
		}
		if asr := strings.TrimSpace(item.ASRReferText); asr != "" {
			return asr
		}
	}
	return ""
}

// attachmentPlaceholderText 按附件生成可读占位（"[图片: photo.jpg] [语音: a.silk]"），
// 保证媒体-only 消息不被静默丢弃、agent 至少能感知附件存在与种类。
func attachmentPlaceholderText(attachments []gatewayAttachment) string {
	parts := make([]string, 0, len(attachments))
	for _, item := range attachments {
		var label string
		switch attachmentKind(item.ContentType) {
		case "image":
			label = "图片"
		case "voice":
			label = "语音"
		case "video":
			label = "视频"
		default:
			label = "文件"
		}
		name := strings.TrimSpace(item.FileName)
		if name == "" {
			parts = append(parts, "["+label+"]")
			continue
		}
		parts = append(parts, "["+label+": "+name+"]")
	}
	return strings.Join(parts, " ")
}

// normalizeInboundText 归一化入站文本：去掉首尾空白；StripMention 时剥掉
// QQ 群/频道消息内容开头的 @机器人 标记（<@!bot_id> / <@bot_id>）。
//
// 降级链（富内容/媒体-only 消息不丢弃）：正文 → msg_elements 展开（引用消息
// message_type=103 / 合并转发 102 / 并行 101，递归含嵌套卡片）→ 卡片占位
// （ark_data）→ 语音 ASR 转写 → 附件占位（"[图片/语音/视频/文件: 名称]"）；
// 纯文本带附件时追加占位行。legacy message_reference 摘要仍拼 "Quoted: " 前缀。
func normalizeInboundText(in inboundTextInput) string {
	trimmed := strings.TrimSpace(in.Content)
	if in.StripMention {
		for strings.HasPrefix(trimmed, "<@") {
			index := strings.Index(trimmed, ">")
			if index < 0 {
				break
			}
			trimmed = strings.TrimSpace(trimmed[index+1:])
		}
	}
	// msg_elements 是消息的**附加内容**（官方语义：引用/合并转发/并行消息
	// 的载体）：有正文时作为独立段追加；无正文时作为替代正文。
	if elements := flattenMsgElements(in.Elements, 0); elements != "" {
		if trimmed == "" {
			trimmed = elements
		} else {
			trimmed = trimmed + "\n\n" + elements
		}
	}
	if trimmed == "" {
		trimmed = arkPlaceholder(in.Ark)
	}
	switch {
	case trimmed == "":
		if asr := voiceASRText(in.Attachments); asr != "" {
			trimmed = asr
		} else {
			trimmed = attachmentPlaceholderText(in.Attachments)
		}
	case len(in.Attachments) > 0:
		if placeholder := attachmentPlaceholderText(in.Attachments); placeholder != "" {
			trimmed += "\n" + placeholder
		}
	}
	quoted := extractQuotedText(in.Reference)
	if quoted == "" {
		return trimmed
	}
	if trimmed == "" {
		return "Quoted: " + quoted
	}
	return "Quoted: " + quoted + "\n" + trimmed
}

// flattenMsgElements 递归展开消息元素为可读文本（引用/合并转发/嵌套内容），
// 深度上限 3 防御异常嵌套；元素无文本时降级为卡片占位。
func flattenMsgElements(elements []gatewayMsgElement, depth int) string {
	if len(elements) == 0 || depth > 3 {
		return ""
	}
	parts := make([]string, 0, len(elements))
	for _, element := range elements {
		if content := strings.TrimSpace(element.Content); content != "" {
			parts = append(parts, content)
			continue
		}
		if nested := flattenMsgElements(element.MsgElements, depth+1); nested != "" {
			parts = append(parts, nested)
			continue
		}
		if placeholder := arkPlaceholder(element.ArkData); placeholder != "" {
			parts = append(parts, placeholder)
		}
	}
	return strings.Join(parts, "\n\n")
}

// arkPlaceholder 把结构化卡片降级为一行占位（prompt > title > ark_name > ark_type）。
func arkPlaceholder(ark *gatewayArkData) string {
	if ark == nil {
		return ""
	}
	label := firstNonEmpty(
		strings.TrimSpace(ark.Prompt),
		arkFieldString(ark.Fields, "title"),
		strings.TrimSpace(ark.ArkName),
		strings.TrimSpace(ark.ArkType),
	)
	if label == "" {
		return "[卡片消息]"
	}
	return "[卡片消息: " + label + "]"
}

// arkFieldString 读取卡片 fields 中的字符串字段（非字符串返回空）。
func arkFieldString(fields map[string]any, key string) string {
	if fields == nil {
		return ""
	}
	value, ok := fields[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

// extractQuotedText 从 message_reference 提取被引用消息摘要：
// content/title 与三种嵌套形态（message/referenced_message/source_message）依次探测。
func extractQuotedText(ref *messageReference) string {
	if ref == nil {
		return ""
	}
	for _, value := range []string{
		strings.TrimSpace(ref.Content),
		quotedText(ref.Message),
		quotedText(ref.ReferencedMessage),
		quotedText(ref.SourceMessage),
		strings.TrimSpace(ref.Title),
	} {
		if value != "" {
			return value
		}
	}
	return ""
}

func quotedText(message *quotedMessage) string {
	if message == nil {
		return ""
	}
	if content := strings.TrimSpace(message.Content); content != "" {
		return content
	}
	return strings.TrimSpace(message.Title)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
