package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/appdevice"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

// ErrDeviceGone marks a provider response that proves the device token is no
// longer valid (APNs/FCM 410/404, relay equivalent). The channel removes it.
var ErrDeviceGone = errors.New("apppush: device token is gone")

// AppDeviceStore is the subset of appdevice.Store used by the channel.
type AppDeviceStore interface {
	List() []appdevice.Device
	Delete(id string) bool
}

// AppPushSender delivers one event to one app installation. Implementations
// can target APNs, FCM, UnifiedPush or a trusted relay webhook.
type AppPushSender interface {
	Send(ctx context.Context, device appdevice.Device, ev notify.Event) error
}

// AppPush fans a notification out to every registered app installation.
type AppPush struct {
	Store  AppDeviceStore
	Sender AppPushSender
}

// Name implements notify.Channel.
func (a *AppPush) Name() string { return "apppush" }

// Enabled reports whether both storage and a provider are configured.
func (a *AppPush) Enabled() bool {
	return a != nil && a.Store != nil && a.Sender != nil
}

// Deliver sends to all registered devices. Invalid tokens are pruned; other
// failures are aggregated so one bad device cannot block the rest.
func (a *AppPush) Deliver(ctx context.Context, ev notify.Event) error {
	if !a.Enabled() {
		return nil
	}
	var errs []error
	for _, device := range a.Store.List() {
		err := a.Sender.Send(ctx, device, ev)
		if errors.Is(err, ErrDeviceGone) {
			a.Store.Delete(device.ID)
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("apppush device %s: %w", device.ID, err))
		}
	}
	return errors.Join(errs...)
}

// WebhookSender posts to a trusted relay that owns APNs/FCM/UnifiedPush
// credentials. It is intentionally provider-neutral: the relay receives the
// device token + notification metadata over HTTPS and performs the native send.
type WebhookSender struct {
	URL    string
	Token  string
	Client *http.Client
}

// NewWebhookSender builds a relay sender. Empty URL leaves it disabled.
func NewWebhookSender(url, token string) *WebhookSender {
	return &WebhookSender{
		URL:   strings.TrimSpace(url),
		Token: strings.TrimSpace(token),
		Client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Enabled reports whether the relay URL is configured.
func (s *WebhookSender) Enabled() bool {
	return s != nil && strings.TrimSpace(s.URL) != ""
}

type webhookRequest struct {
	Device       webhookDevice       `json:"device"`
	Notification webhookNotification `json:"notification"`
}

type webhookDevice struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	Token    string `json:"token"`
	Label    string `json:"label,omitempty"`
}

type webhookNotification struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	Tag        string `json:"tag,omitempty"`
	URL        string `json:"url,omitempty"`
	Urgency    string `json:"urgency,omitempty"`
	Priority   string `json:"priority,omitempty"`
	Kind       string `json:"kind,omitempty"`
	DedupKey   string `json:"dedup_key,omitempty"`
	Node       string `json:"node,omitempty"`
	Session    string `json:"session,omitempty"`
	TTLSeconds int64  `json:"ttl_seconds,omitempty"`
}

// Send performs one relay POST. It does not retry: notify.Dispatcher records
// the first failure and the next attention generation can retry naturally.
func (s *WebhookSender) Send(ctx context.Context, device appdevice.Device, ev notify.Event) error {
	if !s.Enabled() {
		return errors.New("apppush: relay webhook is not configured")
	}
	payload := webhookRequest{
		Device: webhookDevice{
			ID:       device.ID,
			Platform: device.Platform,
			Token:    device.Token,
			Label:    device.Label,
		},
		Notification: webhookNotification{
			Title:      ev.Notification.Title,
			Body:       ev.Notification.Body,
			Tag:        ev.Notification.Tag,
			URL:        ev.Notification.URL,
			Urgency:    ev.Notification.Urgency,
			Priority:   ev.Priority.String(),
			Kind:       ev.Kind,
			DedupKey:   ev.DedupKey,
			Node:       ev.Node,
			Session:    ev.Session,
			TTLSeconds: int64(ev.Notification.TTL / time.Second),
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "aicli-gateway/apppush")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusGone:
		return ErrDeviceGone
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("apppush relay rejected credentials: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	default:
		return fmt.Errorf("apppush relay returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
}
