package runtimeapi

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func TestCloseSessionHubIsIdempotentAndBlocksLazyRecreation(t *testing.T) {
	handler := &Handler{}
	hub := chat.NewSessionHub(nil)
	handler.sessionHub = hub

	handler.CloseSessionHub()
	if got := handler.getSessionHub(); got != nil {
		t.Fatal("getSessionHub must not recreate a closed hub")
	}
	handler.CloseSessionHub()
}

func TestHandlerExecutorCloseUsesHandlerSessionHubLifecycle(t *testing.T) {
	handler := &Handler{}
	handler.sessionHub = chat.NewSessionHub(nil)

	if err := handler.Executor().Close(context.Background()); err != nil {
		t.Fatalf("executor close: %v", err)
	}
	if got := handler.getSessionHub(); got != nil {
		t.Fatal("executor close must close the handler hub")
	}
}
