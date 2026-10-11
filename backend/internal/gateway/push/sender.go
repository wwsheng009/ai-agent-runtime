package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ErrGone reports that the push service rejected the subscription (404/410):
// the caller should delete it.
var ErrGone = errors.New("push: subscription is gone")

// Notification is the JSON payload delivered to the service worker.
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag,omitempty"`
	URL   string `json:"url,omitempty"`
}

// Sender delivers Web Push messages for one VAPID identity.
type Sender struct {
	vapid   *VAPID
	subject string
	client  *http.Client
}

// NewSender builds a Sender. subject should be a mailto: or https: URL.
func NewSender(v *VAPID, subject string) *Sender {
	if subject == "" {
		subject = "mailto:gateway@localhost"
	}
	return &Sender{vapid: v, subject: subject, client: &http.Client{Timeout: 15 * time.Second}}
}

// Send encrypts and delivers one notification to a subscription.
func (s *Sender) Send(ctx context.Context, sub Subscription, n Notification) error {
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	body, err := EncryptPayload(payload, sub.P256dh, sub.Auth)
	if err != nil {
		return err
	}
	aud, err := audience(sub.Endpoint)
	if err != nil {
		return err
	}
	jwt, err := s.vapid.SignVAPIDJWT(aud, s.subject, 12*time.Hour)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "3600")
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Authorization", fmt.Sprintf("vapid t=%s, k=%s", jwt, s.vapid.PublicKeyBase64()))

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusCreated, resp.StatusCode == http.StatusOK, resp.StatusCode == http.StatusAccepted:
		return nil
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		return ErrGone
	default:
		return fmt.Errorf("push: service returned %d", resp.StatusCode)
	}
}

func audience(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("push: invalid endpoint %q", endpoint)
	}
	return u.Scheme + "://" + u.Host, nil
}

// Manager ties a subscription store to a Sender and prunes dead subscriptions.
type Manager struct {
	store  *Store
	sender *Sender
}

// NewManager builds a Manager from a store and VAPID identity.
func NewManager(store *Store, vapid *VAPID, subject string) *Manager {
	return &Manager{store: store, sender: NewSender(vapid, subject)}
}

// Store exposes the underlying subscription store.
func (m *Manager) Store() *Store { return m.store }

// PublicKey returns the VAPID public key for the service worker.
func (m *Manager) PublicKey() string { return m.sender.vapid.PublicKeyBase64() }

// Broadcast sends a notification to every subscription, dropping dead ones.
// It returns the number of successful deliveries.
func (m *Manager) Broadcast(ctx context.Context, n Notification) int {
	delivered := 0
	for _, sub := range m.store.List() {
		err := m.sender.Send(ctx, sub, n)
		switch {
		case err == nil:
			delivered++
		case errors.Is(err, ErrGone):
			m.store.Remove(sub.Endpoint)
		}
	}
	return delivered
}
