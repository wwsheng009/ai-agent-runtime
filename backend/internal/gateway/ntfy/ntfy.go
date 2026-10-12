// Package ntfy delivers gateway notifications through an ntfy server
// (https://ntfy.sh or a self-hosted instance).
//
// ntfy is the secondary notification channel: it reaches phones in networks
// where Web Push (FCM/WNS) is unavailable, without any in-app browser code.
package ntfy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Priority is the ntfy priority scale (1 = min … 5 = max/urgent).
const (
	PriorityMin     = 1
	PriorityLow     = 2
	PriorityDefault = 3
	PriorityHigh    = 4
	PriorityMax     = 5
)

// Message is one notification published to a topic.
type Message struct {
	Title    string
	Body     string
	URL      string // click target opened by the ntfy client
	Priority int    // 0 → default
	Tags     []string
}

// Sender publishes messages to a single ntfy topic.
type Sender struct {
	baseURL string
	topic   string
	token   string
	client  *http.Client
}

// NewSender builds a Sender. An empty baseURL or topic disables it
// (Enabled reports false), so callers can wire it unconditionally.
func NewSender(baseURL, topic, token string) *Sender {
	return &Sender{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		topic:   strings.TrimSpace(topic),
		token:   strings.TrimSpace(token),
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// Enabled reports whether the sender has enough configuration to publish.
func (s *Sender) Enabled() bool {
	return s != nil && s.baseURL != "" && s.topic != ""
}

// Topic returns the configured topic (empty when disabled).
func (s *Sender) Topic() string {
	if s == nil {
		return ""
	}
	return s.topic
}

type publishRequest struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
}

// Send publishes one message using the JSON publishing API. JSON (rather than
// headers) is used so Chinese/non-ASCII titles need no header encoding.
func (s *Sender) Send(ctx context.Context, m Message) error {
	if !s.Enabled() {
		return fmt.Errorf("ntfy: not configured (set --ntfy-url and --ntfy-topic)")
	}
	priority := m.Priority
	if priority <= 0 {
		priority = PriorityDefault
	}
	payload, err := json.Marshal(publishRequest{
		Topic:    s.topic,
		Title:    m.Title,
		Message:  m.Body,
		Priority: priority,
		Tags:     m.Tags,
		Click:    m.URL,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ntfy: server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}
