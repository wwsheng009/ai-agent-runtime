package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

// pushNotifier watches the registry and delivers a Web Push notification when a
// session newly enters an attention state (docs/gateway/08-notifications.md).
//
// Notification policy (§5.5-5.6): approval/question/error are high priority and
// are delivered immediately, piercing quiet hours; completed/waiting are
// coalesced within coalesceWindow and muted during quietHours.
type pushNotifier struct {
	mu      sync.Mutex
	seen    map[string]string // "nodeID|sessionID" -> last attention value
	manager *push.Manager
	baseURL string
	// presence suppresses pushes for sessions a console client is watching.
	presence *presenceRegistry

	// coalesceWindow merges low-priority notifications arriving close together
	// (0 disables coalescing). quietHours mutes low-priority notifications.
	coalesceWindow time.Duration
	quietHours     []quietRange
	now            func() time.Time

	batch map[string][]push.Notification
	timer *time.Timer
}

func newPushNotifier(manager *push.Manager, baseURL string, presence *presenceRegistry) *pushNotifier {
	return &pushNotifier{
		seen:     make(map[string]string),
		manager:  manager,
		baseURL:  strings.TrimRight(baseURL, "/"),
		presence: presence,
		now:      time.Now,
	}
}

// pendingNotification pairs a notification with the attention kind that
// produced it so coalescing can group by kind.
type pendingNotification struct {
	attention string
	note      push.Notification
}

// observe diffs the current attention states against the previous snapshot and
// notifies for each newly-attentive session.
func (p *pushNotifier) observe(nodes []*tunnel.Node) {
	if p == nil || p.manager == nil {
		return
	}
	p.mu.Lock()
	next := make(map[string]string)
	var immediate []push.Notification
	var batched []pendingNotification
	for _, node := range nodes {
		for _, sess := range node.Sessions {
			attention := strings.TrimSpace(sess.Attention)
			if attention == "" {
				continue
			}
			key := node.NodeID + "|" + sess.SessionID
			// Suppress while a console client is viewing this session, and leave
			// it unrecorded so the notification still fires once they look away.
			if p.presence != nil && p.presence.Active(node.NodeID, sess.SessionID) {
				continue
			}
			next[key] = attention
			if prev, ok := p.seen[key]; ok && prev == attention {
				continue
			}
			note := push.Notification{
				Title: attentionTitle(sess.Title),
				Body:  attentionBody(node, attention),
				Tag:   key,
				URL:   p.sessionURL(node.NodeID, sess.SessionID),
			}
			if isHighPriorityAttention(attention) {
				immediate = append(immediate, note)
			} else {
				batched = append(batched, pendingNotification{attention: attention, note: note})
			}
		}
	}
	p.seen = next
	if p.coalesceWindow > 0 {
		for _, item := range batched {
			if p.batch == nil {
				p.batch = make(map[string][]push.Notification)
			}
			p.batch[item.attention] = append(p.batch[item.attention], item.note)
		}
		if len(batched) > 0 && p.timer == nil {
			p.timer = time.AfterFunc(p.coalesceWindow, p.flush)
		}
	} else {
		for _, item := range batched {
			immediate = append(immediate, item.note)
		}
	}
	p.mu.Unlock()

	p.dispatch(immediate)
}

// flush emits the coalesced low-priority batch unless quiet hours are active.
func (p *pushNotifier) flush() {
	p.mu.Lock()
	batch := p.batch
	p.batch = nil
	p.timer = nil
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	quiet := inQuietHours(p.quietHours, now())
	p.mu.Unlock()

	if len(batch) == 0 || quiet {
		return
	}
	out := make([]push.Notification, 0, len(batch))
	for attention, items := range batch {
		note := coalesceNotifications(attention, items)
		if len(items) > 1 && p.baseURL != "" {
			note.URL = p.baseURL + "/"
		}
		out = append(out, note)
	}
	p.dispatch(out)
}

// dispatch delivers notifications asynchronously so the registry callback never
// blocks on the push services.
func (p *pushNotifier) dispatch(notes []push.Notification) {
	if len(notes) == 0 {
		return
	}
	manager := p.manager
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, n := range notes {
			manager.Broadcast(ctx, n)
		}
	}()
}

func (p *pushNotifier) sessionURL(nodeID, sessionID string) string {
	if p.baseURL == "" {
		return ""
	}
	u := p.baseURL + "/n/" + nodeID + "/"
	if sessionID != "" {
		u += "?session=" + sessionID
	}
	return u
}

func attentionTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return "Session needs attention"
	}
	return title
}

func attentionBody(node *tunnel.Node, attention string) string {
	name := node.Name
	if strings.TrimSpace(name) == "" {
		name = node.NodeID
	}
	return fmt.Sprintf("%s: %s", name, attention)
}

// handlePushConfig returns the VAPID public key so the service worker can
// subscribe.
func handlePushConfig(manager *push.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":    true,
			"public_key": manager.PublicKey(),
		})
	}
}

// handlePushSubscriptions lists or adds push subscriptions.
func handlePushSubscriptions(manager *push.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "push disabled"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"subscriptions": manager.Store().List()})
		case http.MethodPost:
			var req struct {
				Endpoint string `json:"endpoint"`
				Keys     struct {
					P256dh string `json:"p256dh"`
					Auth   string `json:"auth"`
				} `json:"keys"`
				Label string `json:"label"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
				return
			}
			if strings.TrimSpace(req.Endpoint) == "" || strings.TrimSpace(req.Keys.P256dh) == "" || strings.TrimSpace(req.Keys.Auth) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endpoint and keys are required"})
				return
			}
			sub := push.Subscription{
				Endpoint: strings.TrimSpace(req.Endpoint),
				P256dh:   strings.TrimSpace(req.Keys.P256dh),
				Auth:     strings.TrimSpace(req.Keys.Auth),
				Label:    strings.TrimSpace(req.Label),
			}
			if err := manager.Store().Add(sub); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to store subscription"})
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"subscribed": true})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// handlePushUnsubscribe removes a subscription by endpoint.
func handlePushUnsubscribe(manager *push.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "push disabled"})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Endpoint) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endpoint is required"})
			return
		}
		if !manager.Store().Remove(strings.TrimSpace(req.Endpoint)) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "subscription not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"unsubscribed": true})
	}
}

// handlePushTest sends a test notification to every subscription.
func handlePushTest(manager *push.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "push disabled"})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		delivered := manager.Broadcast(ctx, push.Notification{
			Title: "aicli-gateway",
			Body:  "Test notification",
			Tag:   "test",
		})
		writeJSON(w, http.StatusOK, map[string]any{"delivered": delivered, "subscriptions": manager.Store().Count()})
	}
}
