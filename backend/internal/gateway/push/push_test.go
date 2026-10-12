package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/big"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/hkdf"
)

// decrypt reverses EncryptPayload using the subscription's private key.
func decrypt(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, authSecret []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("body too short: %d", len(body))
	}
	salt := body[:16]
	idlen := int(body[20])
	if len(body) < 21+idlen {
		t.Fatalf("body shorter than header")
	}
	keyid := body[21 : 21+idlen]
	header := body[:21+idlen]
	ciphertext := body[21+idlen:]

	asPub, err := ecdh.P256().NewPublicKey(keyid)
	if err != nil {
		t.Fatalf("parse as public: %v", err)
	}
	ecdhSecret, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatalf("ecdh: %v", err)
	}
	authInfo := append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...)
	authInfo = append(authInfo, keyid...)
	prkAuth := hkdf.Extract(sha256.New, ecdhSecret, authSecret)
	ikm := hkdfExpand(prkAuth, authInfo, 32)
	prk := hkdf.Extract(sha256.New, ikm, salt)
	cek := hkdfExpand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfExpand(prk, []byte("Content-Encoding: nonce\x00"), 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, header)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(plaintext) == 0 || plaintext[len(plaintext)-1] != lastRecordDelimiter {
		t.Fatalf("missing record delimiter")
	}
	return plaintext[:len(plaintext)-1]
}

func TestEncryptPayloadRoundTrip(t *testing.T) {
	uaPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatal(err)
	}
	p256dh := base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes())
	auth := base64.RawURLEncoding.EncodeToString(authSecret)

	payload := []byte(`{"title":"turn done","body":"session s1 finished"}`)
	body, err := EncryptPayload(payload, p256dh, auth)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(body) <= len(payload) {
		t.Fatalf("ciphertext not larger than payload")
	}
	got := decrypt(t, body, uaPriv, authSecret)
	if string(got) != string(payload) {
		t.Fatalf("round trip mismatch: %q", got)
	}
	// Salt must differ per call (fresh randomness).
	body2, err := EncryptPayload(payload, p256dh, auth)
	if err != nil {
		t.Fatal(err)
	}
	if string(body[:16]) == string(body2[:16]) {
		t.Fatalf("salt reused across messages")
	}
}

func TestEncryptPayloadBadInput(t *testing.T) {
	if _, err := EncryptPayload([]byte("x"), "not-base64!!", "auth"); err == nil {
		t.Fatalf("expected error for invalid p256dh")
	}
}

func TestVAPIDPublicKeyAndJWT(t *testing.T) {
	v, err := LoadOrCreateVAPID("")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.RawURLEncoding.DecodeString(v.PublicKeyBase64())
	if err != nil {
		t.Fatalf("decode public: %v", err)
	}
	if len(pub) != 65 || pub[0] != 0x04 {
		t.Fatalf("unexpected public key encoding: len=%d first=%x", len(pub), pub[0])
	}
	token, err := v.SignVAPIDJWT("https://push.example.com", "mailto:a@b.c", 0)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("bad signature encoding: %v len=%d", err, len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&v.private.PublicKey, digest[:], r, s) {
		t.Fatalf("VAPID signature did not verify")
	}
}

func TestVAPIDKeyPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vapid.key")
	first, err := LoadOrCreateVAPID(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateVAPID(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.PublicKeyBase64() != second.PublicKeyBase64() {
		t.Fatalf("public key changed across reload")
	}
}

func TestStoreAddRemovePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	sub := Subscription{Endpoint: "https://push.example.com/abc", P256dh: "p", Auth: "a", Label: "phone"}
	if err := store.Add(sub); err != nil {
		t.Fatal(err)
	}
	if store.Count() != 1 {
		t.Fatalf("count = %d", store.Count())
	}
	// Reopen to confirm persistence.
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Count() != 1 {
		t.Fatalf("reopened count = %d", reopened.Count())
	}
	if !reopened.Remove(sub.Endpoint) {
		t.Fatalf("remove returned false")
	}
	if reopened.Remove(sub.Endpoint) {
		t.Fatalf("second remove should be false")
	}
	if reopened.Count() != 0 {
		t.Fatalf("count after remove = %d", reopened.Count())
	}
}

func TestAudience(t *testing.T) {
	got, err := audience("https://fcm.googleapis.com/fcm/send/xyz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://fcm.googleapis.com" {
		t.Fatalf("audience = %q", got)
	}
	if _, err := audience("not a url"); err == nil {
		t.Fatalf("expected error for malformed endpoint")
	}
}

func TestSenderSendGone(t *testing.T) {
	// A subscription pointing at a 410 endpoint must map to ErrGone.
	srv := newGoneServer(t)
	defer srv.Close()
	v, err := LoadOrCreateVAPID("")
	if err != nil {
		t.Fatal(err)
	}
	sender := NewSender(v, "mailto:a@b.c")
	sub := Subscription{Endpoint: srv.URL + "/sub", P256dh: mustP256dh(t), Auth: mustAuth(t)}
	if err := sender.Send(t.Context(), sub, Notification{Title: "x"}); err != ErrGone {
		t.Fatalf("err = %v, want ErrGone", err)
	}
}

func TestSenderSendDeliveryHeaders(t *testing.T) {
	var gotUrgency, gotTTL, gotTopic string
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUrgency = r.Header.Get("Urgency")
		gotTTL = r.Header.Get("TTL")
		gotTopic = r.Header.Get("Topic")
		w.WriteHeader(http.StatusCreated)
	})
	defer srv.Close()
	v, _ := LoadOrCreateVAPID("")
	sender := NewSender(v, "mailto:a@b.c")
	sub := Subscription{Endpoint: srv.URL + "/sub", P256dh: mustP256dh(t), Auth: mustAuth(t)}
	if err := sender.Send(t.Context(), sub, Notification{
		Title: "x", Urgency: "high", TTL: 5 * time.Minute, Topic: "alert",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotUrgency != "high" || gotTTL != "300" || gotTopic != "alert" {
		t.Fatalf("headers = urgency %q ttl %q topic %q, want high/300/alert", gotUrgency, gotTTL, gotTopic)
	}
}

func TestSenderSendDefaultDeliveryHeaders(t *testing.T) {
	var gotUrgency, gotTTL string
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUrgency = r.Header.Get("Urgency")
		gotTTL = r.Header.Get("TTL")
		w.WriteHeader(http.StatusCreated)
	})
	defer srv.Close()
	v, _ := LoadOrCreateVAPID("")
	sender := NewSender(v, "mailto:a@b.c")
	sub := Subscription{Endpoint: srv.URL + "/sub", P256dh: mustP256dh(t), Auth: mustAuth(t)}
	if err := sender.Send(t.Context(), sub, Notification{Title: "x"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotUrgency != "normal" || gotTTL != "3600" {
		t.Fatalf("headers = urgency %q ttl %q, want normal/3600", gotUrgency, gotTTL)
	}
}

func TestSenderRetriesTransientFailure(t *testing.T) {
	attempts := 0
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	defer srv.Close()
	v, _ := LoadOrCreateVAPID("")
	sender := NewSender(v, "mailto:a@b.c")
	sender.retryBackoff = time.Millisecond
	sub := Subscription{Endpoint: srv.URL + "/sub", P256dh: mustP256dh(t), Auth: mustAuth(t)}
	if err := sender.Send(t.Context(), sub, Notification{Title: "x"}); err != nil {
		t.Fatalf("send after retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestSenderRejectsPermanentFailure(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	defer srv.Close()
	v, _ := LoadOrCreateVAPID("")
	sender := NewSender(v, "mailto:a@b.c")
	sub := Subscription{Endpoint: srv.URL + "/sub", P256dh: mustP256dh(t), Auth: mustAuth(t)}
	err := sender.Send(t.Context(), sub, Notification{Title: "x"})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("120"); got != 2*time.Minute {
		t.Fatalf("parseRetryAfter(120) = %v", got)
	}
	for _, bad := range []string{"", "0", "-1", "soon", "Wed, 21 Oct 2015 07:28:00 GMT"} {
		if got := parseRetryAfter(bad); got != 0 {
			t.Fatalf("parseRetryAfter(%q) = %v, want 0", bad, got)
		}
	}
}

func TestManagerBroadcastPrunes(t *testing.T) {
	srv := newGoneServer(t)
	defer srv.Close()
	v, _ := LoadOrCreateVAPID("")
	store, _ := OpenStore("")
	_ = store.Add(Subscription{Endpoint: srv.URL + "/sub", P256dh: mustP256dh(t), Auth: mustAuth(t)})
	mgr := NewManager(store, v, "mailto:a@b.c")
	if n := mgr.Broadcast(t.Context(), Notification{Title: "hi"}); n != 0 {
		t.Fatalf("delivered = %d, want 0", n)
	}
	if store.Count() != 0 {
		t.Fatalf("gone subscription not pruned: %d", store.Count())
	}
}

func TestManagerBroadcastSuccess(t *testing.T) {
	var gotAuth, gotEncoding string
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotEncoding = r.Header.Get("Content-Encoding")
		w.WriteHeader(http.StatusCreated)
	})
	defer srv.Close()
	v, _ := LoadOrCreateVAPID("")
	store, _ := OpenStore("")
	_ = store.Add(Subscription{Endpoint: srv.URL + "/sub", P256dh: mustP256dh(t), Auth: mustAuth(t)})
	mgr := NewManager(store, v, "mailto:a@b.c")
	if n := mgr.Broadcast(t.Context(), Notification{Title: "hi"}); n != 1 {
		t.Fatalf("delivered = %d, want 1", n)
	}
	if !strings.HasPrefix(gotAuth, "vapid t=") {
		t.Fatalf("missing vapid auth header: %q", gotAuth)
	}
	if gotEncoding != "aes128gcm" {
		t.Fatalf("content-encoding = %q", gotEncoding)
	}
	if store.Count() != 1 {
		t.Fatalf("live subscription pruned: %d", store.Count())
	}
}

func mustP256dh(t *testing.T) string {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes())
}

func mustAuth(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
