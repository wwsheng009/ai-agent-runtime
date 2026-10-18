package qqbot

// 官方 API 调用指南：业务失败看响应体 err_code（成功为 0），不能只看 HTTP。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestAPIJSONTreatsErrCodeAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1", "expires_in": "7200",
			})
		case "/v2/users/u1/messages":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"err_code": 40034005, "message": "回复消息msg_id已过期",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	err := provider.Send(context.Background(), cfg, "user:u1",
		[]connectors.OutboundMessage{{Text: "hi"}})
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want apiError", err)
	}
	if apiErr.code != 40034005 {
		t.Fatalf("code = %d, want 40034005", apiErr.code)
	}
}

func TestAPIJSONErrCodeZeroIsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1", "expires_in": "7200",
			})
		case "/v2/users/u1/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "msg-1", "err_code": 0,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	if err := provider.Send(context.Background(), cfg, "user:u1",
		[]connectors.OutboundMessage{{Text: "hi"}}); err != nil {
		t.Fatalf("err_code=0 must succeed: %v", err)
	}
}

func TestAPIErrCodeParsesStringAndData(t *testing.T) {
	code, message, ok := apiErrCode([]byte(`{"err_code":"40034005","message":"expired"}`))
	if !ok || code != 40034005 || message != "expired" {
		t.Fatalf("top-level string code = %d/%q/%v", code, message, ok)
	}
	code, message, ok = apiErrCode([]byte(`{"data":{"err_code":304023,"message":"audit"}}`))
	if !ok || code != 304023 || message != "audit" {
		t.Fatalf("nested code = %d/%q/%v", code, message, ok)
	}
	if _, _, ok := apiErrCode([]byte(`{"id":"x"}`)); ok {
		t.Fatal("missing err_code must not be treated as failure")
	}
}

// 官方：201/202 是异步操作成功，但会返回 error body（304023/304024 审核中）。
// 这类响应必须按成功处理，否则会触发降级重发造成重复消息。
func TestAPIJSONAsyncAuditAccepted(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   int
	}{
		{http.StatusCreated, 304023},
		{http.StatusAccepted, 304024},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/app/getAppAccessToken":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": "token-1", "expires_in": "7200",
				})
			case "/v2/users/u1/messages":
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"err_code": tc.code, "message": "waiting for audit",
				})
			default:
				http.NotFound(w, r)
			}
		}))

		provider := New(WithAPIBase(server.URL))
		cfg := testConfig("qq-1", "app-1", "secret-1")
		if err := provider.Send(context.Background(), cfg, "user:u1",
			[]connectors.OutboundMessage{{Text: "hi"}}); err != nil {
			t.Fatalf("status=%d code=%d must be accepted as success: %v", tc.status, tc.code, err)
		}
		server.Close()
	}
}

// 非 201/202 的审核码（例如 400 + 304023）仍是失败，不能吞掉。
func TestAPIJSONAuditCodeWithErrorStatusFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "token-1", "expires_in": "7200",
			})
		case "/v2/users/u1/messages":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"err_code": 304023, "message": "audit",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	if err := provider.Send(context.Background(), cfg, "user:u1",
		[]connectors.OutboundMessage{{Text: "hi"}}); err == nil {
		t.Fatal("400 + audit code must remain an error")
	}
}
