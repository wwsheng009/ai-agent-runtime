package agent

import (
	"context"
	"errors"
	"testing"
)

func TestNewControllerDisabled(t *testing.T) {
	c := NewController(Config{Enabled: false}, nil)
	if c == nil {
		t.Fatal("NewController must return a controller")
	}
	if got := c.Status().State; got != StateDisabled {
		t.Fatalf("state = %s, want disabled", got)
	}
}

func TestNewControllerEnabledStartsDisconnected(t *testing.T) {
	c := NewController(Config{Enabled: true, URL: "wss://gw", Token: "agw_x"}, nil)
	if got := c.Status().State; got != StateDisconnected {
		t.Fatalf("state = %s, want disconnected", got)
	}
}

func TestNewControllerNilRegisterFailsClosed(t *testing.T) {
	c := NewController(Config{Enabled: true, URL: "wss://gw", Token: "agw_x"}, nil)
	err := c.Connect(context.Background())
	var ce *ConnectError
	if !errors.As(err, &ce) || ce.Code != ErrCodeInternal {
		t.Fatalf("expected internal ConnectError for nil register, got %v", err)
	}
	if c.Status().State != StateError {
		t.Fatalf("state = %s, want error", c.Status().State)
	}
}
