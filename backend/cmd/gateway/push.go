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
type pushNotifier struct {
	mu      sync.Mutex
	seen    map[string]string // "nodeID|sessionID" -> last attention value
	manager *push.Manager
	baseURL string
}

func newPushNotifier(manager *push.Manager, baseURL string) *pushNotifier {
	return &pushNotifier{
		seen:    make(map[string]string),
		manager: manager,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

// observe diffs the current attention states against the previous snapshot and
// broadcasts a notification for each newly-attentive session.
func (p *pushNotifier) observe(nodes []*tunnel.Node) {
	if p == nil || p.manager == nil {
		return
	}
	p.mu.Lock()
	next := make(map[string]string)
	var pending []push.Notification
	for _, node := range nodes {
		for _, sess := range node.Sessions {
			if strings.TrimSpace(sess.Attention) == "" {
				continue
			}
			key := node.NodeID + "|" + sess.SessionID
			next[key] = sess.Attention
			if prev, ok := p.seen[key]; ok && prev == sess.Attention {
				continue
			}
			pending = append(pending, push.Notification{
				Title: attentionTitle(sess.Title),
				Body:  attentionBody(node, sess.Attention),
				Tag:   key,
				URL:   p.sessionURL(node.NodeID, sess.SessionID),
			})
		}
	}
	p.seen = next
	p.mu.Unlock()

	if len(pending) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, n := range pending {
			p.manager.Broadcast(ctx, n)
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
