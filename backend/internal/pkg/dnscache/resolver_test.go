package dnscache

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func assertResolverRemoteAddr(t *testing.T, d *SmartDialer, want string) {
	t.Helper()
	if d == nil || d.resolver == nil || d.resolver.resolver == nil || d.resolver.resolver.Dial == nil {
		t.Fatalf("expected custom resolver dialer to be configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := d.resolver.resolver.Dial(ctx, "udp", "ignored:53")
	if err != nil {
		t.Fatalf("resolver dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if got := conn.RemoteAddr().String(); got != want {
		t.Fatalf("resolver dial target = %q, want %q", got, want)
	}
}

// 测试自动添加端口 53
func TestWithSmartDialerDNSServer(t *testing.T) {
	d := NewSmartDialer(5*time.Second, 30*time.Second, 100*time.Millisecond, 60*time.Second, true, WithSmartDialerDNSServer("8.8.8.8"))
	assertResolverRemoteAddr(t, d, "8.8.8.8:53")
	fmt.Println("✅ DNS dial target successful (auto-added :53)")
}

// 测试包含端口时不变
func TestWithSmartDialerDNSServerWithPort(t *testing.T) {
	d := NewSmartDialer(5*time.Second, 30*time.Second, 100*time.Millisecond, 60*time.Second, true, WithSmartDialerDNSServer("8.8.8.8:53"))
	assertResolverRemoteAddr(t, d, "8.8.8.8:53")
	fmt.Println("✅ DNS dial target successful (port already specified)")
}

// 测试 containsPort 函数
func TestContainsPort(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"8.8.8.8", false},
		{"8.8.8.8:53", true},
		{"223.5.5.5", false},
		{"223.5.5.5:53", true},
		{"dns.google", false},
		{"https://dns.google", false}, // 不是端口，是协议
		{"localhost", false},
		{"localhost:53", true},
		{"192.168.1.1", false},
		{"192.168.1.1:5353", true},
	}

	for _, tt := range tests {
		result := containsPort(tt.input)
		if result != tt.expected {
			t.Errorf("containsPort(%q) = %v, want %v", tt.input, result, tt.expected)
		} else {
			fmt.Printf("✅ containsPort(%q) = %v\n", tt.input, result)
		}
	}
}
