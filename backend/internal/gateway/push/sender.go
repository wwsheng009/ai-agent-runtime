package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrGone reports that the push service rejected the subscription (404/410):
// the caller should delete it.
var ErrGone = errors.New("push: subscription is gone")

// ErrRejected reports a permanent rejection (401/403). In practice this is a
// VAPID identity mismatch (key rotated, restored from a different data dir, or
// a broken multi-instance setup): the gateway's signature no longer matches the
// subscription, so the device must subscribe again. Unlike ErrGone the
// subscription is kept, but the failure is logged loudly instead of vanishing.
var ErrRejected = errors.New("push: subscription rejected, re-subscribe required")

const (
	defaultUrgency  = "normal"
	defaultTTL      = time.Hour
	maxSendAttempts = 3
	maxRetryWait    = 5 * time.Second
)

// Notification is the JSON payload delivered to the service worker.
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag,omitempty"`
	URL   string `json:"url,omitempty"`

	// Delivery hints. They become push-service HTTP headers and are never part
	// of the encrypted payload: Urgency (RFC 8030) lets high-priority attention
	// pierce Doze-like deferral, TTL bounds how long a stale notification may
	// still pop, Topic lets the push service collapse same-topic messages.
	Urgency string        `json:"-"`
	TTL     time.Duration `json:"-"`
	Topic   string        `json:"-"`
}

// Sender delivers Web Push messages for one VAPID identity.
type Sender struct {
	vapid   *VAPID
	subject string
	client  *http.Client
	// retryBackoff is the base delay before retrying a transient failure
	// (doubles per attempt, capped at maxRetryWait); tests shorten it.
	retryBackoff time.Duration
}

// NewSender builds a Sender. subject should be a mailto: or https: URL.
func NewSender(v *VAPID, subject string) *Sender {
	if subject == "" {
		subject = "mailto:gateway@localhost"
	}
	return &Sender{
		vapid:        v,
		subject:      subject,
		client:       &http.Client{Timeout: 15 * time.Second},
		retryBackoff: 500 * time.Millisecond,
	}
}

// Send encrypts and delivers one notification to a subscription. Transient
// failures (network errors, 429, 5xx) are retried with backoff; 404/410 map to
// ErrGone and 401/403 to ErrRejected so the caller can prune or surface them.
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

	urgency := n.Urgency
	if urgency == "" {
		urgency = defaultUrgency
	}
	ttl := n.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}

	backoff := s.retryBackoff
	if backoff <= 0 {
		backoff = 500 * time.Millisecond
	}
	var lastErr error
	var retryAfter time.Duration
	for attempt := 0; attempt < maxSendAttempts; attempt++ {
		if attempt > 0 {
			wait := backoff
			if retryAfter > wait {
				wait = retryAfter
			}
			if wait > maxRetryWait {
				wait = maxRetryWait
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			backoff *= 2
		}
		var err error
		retryAfter, err = s.sendOnce(ctx, sub.Endpoint, body, urgency, ttl, n.Topic, jwt)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryable(err) {
			return err
		}
	}
	return lastErr
}

// isRetryable classifies a send error as transient (network failure, 429, 5xx).
func isRetryable(err error) bool {
	var te *transientError
	return errors.As(err, &te)
}

// transientError marks a failure worth retrying.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// sendOnce performs a single delivery attempt. It returns the Retry-After hint
// (zero when absent) and either nil or a classified error.
func (s *Sender) sendOnce(ctx context.Context, endpoint string, body []byte, urgency string, ttl time.Duration, topic, jwt string) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	req.Header.Set("Urgency", urgency)
	if topic != "" {
		req.Header.Set("Topic", topic)
	}
	req.Header.Set("Authorization", fmt.Sprintf("vapid t=%s, k=%s", jwt, s.vapid.PublicKeyBase64()))

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, &transientError{err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusCreated, resp.StatusCode == http.StatusOK, resp.StatusCode == http.StatusAccepted:
		return 0, nil
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		return 0, ErrGone
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return 0, fmt.Errorf("%w (HTTP %d)", ErrRejected, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return parseRetryAfter(resp.Header.Get("Retry-After")),
			&transientError{err: fmt.Errorf("push: service returned %d", resp.StatusCode)}
	default:
		return 0, fmt.Errorf("push: service returned %d", resp.StatusCode)
	}
}

// parseRetryAfter understands the seconds form of Retry-After (the HTTP-date
// form is ignored: push services in practice send seconds).
func parseRetryAfter(v string) time.Duration {
	sec, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
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
			log.Printf("push: dropped expired subscription %s", endpointHost(sub.Endpoint))
		case errors.Is(err, ErrRejected):
			// Permanent: usually a VAPID key mismatch. Do not stay silent —
			// otherwise every device silently stops receiving notifications.
			log.Printf("push: %s rejected the push (VAPID mismatch?); device must re-subscribe: %v", endpointHost(sub.Endpoint), err)
		default:
			log.Printf("push: delivery to %s failed: %v", endpointHost(sub.Endpoint), err)
		}
	}
	return delivered
}

// endpointHost returns just the push service host so logs never carry the
// subscription capability URL.
func endpointHost(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return "unknown-endpoint"
}
