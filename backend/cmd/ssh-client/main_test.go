package main

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/sshclient"
)

func TestParseForwardSpec(t *testing.T) {
	tests := []struct {
		name         string
		spec         string
		wantBind     string
		wantPort     string
		wantHost     string
		wantHostport string
		wantErr      bool
	}{
		// OpenSSH 常用简写：bind 省略，默认 localhost
		{"shorthand 3-part", "8080:localhost:80", "localhost", "8080", "localhost", "80", false},
		{"shorthand host", "2222:127.0.0.1:22", "localhost", "2222", "127.0.0.1", "22", false},
		// 显式 IPv4 bind
		{"ipv4 bind 4-part", "192.168.1.1:8080:localhost:80", "192.168.1.1", "8080", "localhost", "80", false},
		{"localhost bind", "127.0.0.1:8080:10.0.0.5:443", "127.0.0.1", "8080", "10.0.0.5", "443", false},
		// IPv6 bind（方括号）
		{"ipv6 bind", "[::1]:8080:localhost:80", "::1", "8080", "localhost", "80", false},
		{"ipv6 bind explicit", "[fe80::1]:8080:localhost:80", "fe80::1", "8080", "localhost", "80", false},
		// IPv6 远程主机（host 段带方括号，保留）
		{"ipv6 remote host", "8080:[::1]:80", "localhost", "8080", "[::1]", "80", false},
		// IPv6 bind + IPv6 远程主机
		{"ipv6 both", "[::1]:8080:[::2]:80", "::1", "8080", "[::2]", "80", false},
		// "*" 表示所有接口（OpenSSH 语义）
		{"star bind", "*:8080:localhost:80", "", "8080", "localhost", "80", false},
		// 错误
		{"too few parts", "8080:80", "localhost", "", "", "", true},
		{"no port", "localhost:80", "localhost", "", "", "", true},
		{"empty spec", "", "localhost", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bind, port, host, hostport, err := parseForwardSpec(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseForwardSpec(%q) expected error, got nil", tt.spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseForwardSpec(%q) unexpected error: %v", tt.spec, err)
			}
			if bind != tt.wantBind || port != tt.wantPort || host != tt.wantHost || hostport != tt.wantHostport {
				t.Errorf("parseForwardSpec(%q) = (%q,%q,%q,%q), want (%q,%q,%q,%q)",
					tt.spec, bind, port, host, hostport,
					tt.wantBind, tt.wantPort, tt.wantHost, tt.wantHostport)
			}
		})
	}
}

func TestApplyOptions(t *testing.T) {
	tests := []struct {
		name    string
		options []string
		check   func(*testing.T, *sshclient.Options)
		wantErr bool
	}{
		{
			name:    "key=value form",
			options: []string{"StrictHostKeyChecking=accept-new"},
			check: func(t *testing.T, o *sshclient.Options) {
				if o.StrictHostKeyChecking != "accept-new" {
					t.Errorf("StrictHostKeyChecking = %q, want accept-new", o.StrictHostKeyChecking)
				}
			},
		},
		{
			name:    "space-separated form (ssh_config style)",
			options: []string{"StrictHostKeyChecking accept-new"},
			check: func(t *testing.T, o *sshclient.Options) {
				if o.StrictHostKeyChecking != "accept-new" {
					t.Errorf("StrictHostKeyChecking = %q, want accept-new", o.StrictHostKeyChecking)
				}
			},
		},
		{
			name:    "space value with path containing spaces",
			options: []string{`UserKnownHostsFile C:\Users\me\.ssh\known_hosts`},
			check: func(t *testing.T, o *sshclient.Options) {
				if o.UserKnownHostsFile != `C:\Users\me\.ssh\known_hosts` {
					t.Errorf("UserKnownHostsFile = %q", o.UserKnownHostsFile)
				}
			},
		},
		{
			name:    "numeric option with space",
			options: []string{"ConnectTimeout 15"},
			check: func(t *testing.T, o *sshclient.Options) {
				if o.ConnectTimeout != 15*time.Second {
					t.Errorf("ConnectTimeout = %v, want 15s", o.ConnectTimeout)
				}
			},
		},
		{
			name:    "multiple options mixed forms",
			options: []string{"ServerAliveInterval=10", "ServerAliveCountMax 3"},
			check: func(t *testing.T, o *sshclient.Options) {
				if o.ServerAliveInterval != 10*time.Second || o.ServerAliveCountMax != 3 {
					t.Errorf("ServerAliveInterval=%v ServerAliveCountMax=%d", o.ServerAliveInterval, o.ServerAliveCountMax)
				}
			},
		},
		{
			name:    "unsupported option warns and continues",
			options: []string{"BatchMode=yes", "ConnectTimeout=20"},
			check: func(t *testing.T, o *sshclient.Options) {
				if o.ConnectTimeout != 20*time.Second {
					t.Errorf("ConnectTimeout = %v, want 20s (later supported option must still apply)", o.ConnectTimeout)
				}
			},
		},
		{
			name:    "invalid option without value",
			options: []string{"StrictHostKeyChecking"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := sshclient.Defaults()
			err := applyOptions(opts, tt.options)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("applyOptions(%v) expected error, got nil", tt.options)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyOptions(%v) unexpected error: %v", tt.options, err)
			}
			if tt.check != nil {
				tt.check(t, opts)
			}
		})
	}
}
