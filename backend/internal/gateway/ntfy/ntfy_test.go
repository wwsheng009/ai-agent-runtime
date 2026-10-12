package ntfy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSenderDisabled(t *testing.T) {
	for _, s := range []*Sender{
		nil,
		NewSender("", "topic", ""),
		NewSender("https://ntfy.example.com", "", ""),
	} {
		if s.Enabled() {
			t.Fatalf("sender %+v should be disabled", s)
		}
		if err := s.Send(context.Background(), Message{Body: "x"}); err == nil {
			t.Fatal("disabled sender must return an error")
		}
	}
}

func TestSenderPublishesJSON(t *testing.T) {
	var gotPath, gotContentType, gotAuth string
	var got publishRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))
	defer srv.Close()

	sender := NewSender(srv.URL, "aicli-gateway", "tk_secret")
	if !sender.Enabled() {
		t.Fatal("sender should be enabled")
	}
	err := sender.Send(context.Background(), Message{
		Title:    "会话需要处理",
		Body:     "laptop: approval",
		URL:      "https://gw.example.com/n/n1/?session=s1",
		Priority: PriorityMax,
		Tags:     []string{"warning"},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/" {
		t.Fatalf("publish path = %q, want /", gotPath)
	}
	if gotContentType != "application/json" {
		t.Fatalf("content type = %q", gotContentType)
	}
	if gotAuth != "Bearer tk_secret" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if got.Topic != "aicli-gateway" || got.Title != "会话需要处理" || got.Priority != 5 {
		t.Fatalf("payload = %+v", got)
	}
	if got.Click != "https://gw.example.com/n/n1/?session=s1" {
		t.Fatalf("click = %q", got.Click)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "warning" {
		t.Fatalf("tags = %v", got.Tags)
	}
}

func TestSenderDefaultsPriority(t *testing.T) {
	var got publishRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := NewSender(srv.URL, "t", "").Send(context.Background(), Message{Body: "x"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got.Priority != PriorityDefault {
		t.Fatalf("priority = %d, want default", got.Priority)
	}
}

func TestSenderReportsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "topic is reserved", http.StatusForbidden)
	}))
	defer srv.Close()

	err := NewSender(srv.URL, "t", "").Send(context.Background(), Message{Body: "x"})
	if err == nil {
		t.Fatal("expected an error for HTTP 403")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "topic is reserved") {
		t.Fatalf("error = %v", err)
	}
}
