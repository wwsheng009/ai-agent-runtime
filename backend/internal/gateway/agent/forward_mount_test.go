package agent

import (
	"net/http"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func mountStreamOpen(slug, path string) wire.StreamOpen {
	return wire.StreamOpen{
		Method: http.MethodPost,
		Path:   path,
		Headers: map[string][]string{
			mountHeader: {slug},
		},
	}
}

func TestResolveStreamTargetDefault(t *testing.T) {
	conn := &wsConn{forward: ForwardConfig{LocalBaseURL: "http://127.0.0.1:61772/"}}
	target, err := conn.resolveStreamTarget(wire.StreamOpen{Path: "/web/api/screen"})
	if err != nil {
		t.Fatalf("resolveStreamTarget: %v", err)
	}
	if target.baseURL != "http://127.0.0.1:61772" {
		t.Fatalf("baseURL = %q", target.baseURL)
	}
	if target.path != "/web/api/screen" || target.mount != "" {
		t.Fatalf("target = %+v", target)
	}
	if target.timeout != defaultForwardTimeout {
		t.Fatalf("timeout = %v", target.timeout)
	}
	if target.headerName != "" {
		t.Fatalf("default target must not inject a header: %+v", target)
	}
}

func TestResolveStreamTargetMount(t *testing.T) {
	conn := &wsConn{forward: ForwardConfig{
		LocalBaseURL: "http://127.0.0.1:61772",
		Mounts: []MountForward{{
			Slug:         "k7f3qz2a",
			BaseURL:      "http://127.0.0.1:18765/",
			TokenEnv:     "MOUNT_TOKEN",
			Timeout:      10 * time.Minute,
			MaxBodyBytes: 8 << 20,
		}},
		Getenv: func(name string) string {
			if name == "MOUNT_TOKEN" {
				return "local-secret"
			}
			return ""
		},
	}}
	target, err := conn.resolveStreamTarget(mountStreamOpen("k7f3qz2a", "/mcp?x=1"))
	if err != nil {
		t.Fatalf("resolveStreamTarget: %v", err)
	}
	if target.baseURL != "http://127.0.0.1:18765" {
		t.Fatalf("baseURL = %q", target.baseURL)
	}
	if target.path != "/mcp?x=1" {
		t.Fatalf("path = %q, want /mcp?x=1", target.path)
	}
	if target.mount != "k7f3qz2a" {
		t.Fatalf("mount = %q", target.mount)
	}
	if target.headerName != "Authorization" || target.headerValue != "Bearer local-secret" {
		t.Fatalf("auth header = %q: %q", target.headerName, target.headerValue)
	}
	if target.timeout != 10*time.Minute || target.maxBody != 8<<20 {
		t.Fatalf("limits = %v / %d", target.timeout, target.maxBody)
	}
}

func TestResolveStreamTargetUnknownMountFailsClosed(t *testing.T) {
	conn := &wsConn{forward: ForwardConfig{
		LocalBaseURL: "http://127.0.0.1:61772",
		Mounts:       []MountForward{{Slug: "known", BaseURL: "http://127.0.0.1:18765"}},
	}}
	_, err := conn.resolveStreamTarget(mountStreamOpen("revoked", "/mcp"))
	if err == nil {
		t.Fatalf("unknown mount resolved successfully; it must fail closed")
	}
	code, _ := errorCode(err)
	if code != "mount_unknown" {
		t.Fatalf("error code = %q, want mount_unknown", code)
	}
}

func TestResolveStreamTargetMissingTokenEnvFailsClosed(t *testing.T) {
	conn := &wsConn{forward: ForwardConfig{
		Mounts: []MountForward{{Slug: "k7f3qz2a", BaseURL: "http://127.0.0.1:18765", TokenEnv: "ABSENT"}},
		Getenv: func(string) string { return "" },
	}}
	_, err := conn.resolveStreamTarget(mountStreamOpen("k7f3qz2a", "/mcp"))
	if err == nil {
		t.Fatalf("empty token env was accepted")
	}
	if code, _ := errorCode(err); code != "mount_token_missing" {
		t.Fatalf("error code = %q, want mount_token_missing", code)
	}
}

func TestResolveStreamTargetAuthModes(t *testing.T) {
	getenv := func(string) string { return "raw-token" }

	t.Run("none", func(t *testing.T) {
		conn := &wsConn{forward: ForwardConfig{
			Mounts: []MountForward{{Slug: "s", BaseURL: "http://127.0.0.1:18765", AuthType: "none", TokenEnv: "T"}},
			Getenv: getenv,
		}}
		target, err := conn.resolveStreamTarget(mountStreamOpen("s", "/mcp"))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if target.headerName != "" || target.headerValue != "" {
			t.Fatalf("auth none injected %q: %q", target.headerName, target.headerValue)
		}
	})

	t.Run("custom header", func(t *testing.T) {
		conn := &wsConn{forward: ForwardConfig{
			Mounts: []MountForward{{Slug: "s", BaseURL: "http://127.0.0.1:18765", AuthType: "header", TokenEnv: "T", Header: "X-Api-Key"}},
			Getenv: getenv,
		}}
		target, err := conn.resolveStreamTarget(mountStreamOpen("s", "/mcp"))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if target.headerName != "X-Api-Key" || target.headerValue != "raw-token" {
			t.Fatalf("auth header = %q: %q", target.headerName, target.headerValue)
		}
	})

	t.Run("unsupported", func(t *testing.T) {
		conn := &wsConn{forward: ForwardConfig{
			Mounts: []MountForward{{Slug: "s", BaseURL: "http://127.0.0.1:18765", AuthType: "oauth"}},
			Getenv: getenv,
		}}
		if _, err := conn.resolveStreamTarget(mountStreamOpen("s", "/mcp")); err == nil {
			t.Fatalf("unsupported auth type was accepted")
		}
	})
}

func TestMountPathStripsPrefixAndRejectsTraversal(t *testing.T) {
	mount := MountForward{StripPrefix: "/m/k7f3qz2a"}
	cases := map[string]string{
		"/m/k7f3qz2a/mcp":        "/mcp",
		"/m/k7f3qz2a/mcp?a=1":    "/mcp?a=1",
		"/m/k7f3qz2a":            "/",
		"/m/k7f3qz2a/nested/mcp": "/nested/mcp",
	}
	for input, want := range cases {
		got, err := mountPath(mount, input)
		if err != nil {
			t.Fatalf("mountPath(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("mountPath(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"/other/mcp", "/m/k7f3qz2a/../admin", "/m/k7f3qz2a/mcp/../../x"} {
		if _, err := mountPath(mount, input); err == nil {
			t.Fatalf("mountPath(%q) = nil error, want rejection", input)
		}
	}
}

func TestHandleStreamOpenBindsMountTarget(t *testing.T) {
	conn := &wsConn{forward: ForwardConfig{
		Mounts: []MountForward{{Slug: "k7f3qz2a", BaseURL: "http://127.0.0.1:18765"}},
	}, sendCredit: make(map[uint32]int64)}
	frame, err := wire.NewJSONFrame(wire.TypeStreamOpen, 7, mountStreamOpen("k7f3qz2a", "/mcp"))
	if err != nil {
		t.Fatalf("NewJSONFrame: %v", err)
	}
	conn.handleStreamOpen(7, frame)
	in := conn.streams[7]
	if in == nil {
		t.Fatalf("stream was not registered")
	}
	if in.target.mount != "k7f3qz2a" || in.target.baseURL != "http://127.0.0.1:18765" {
		t.Fatalf("bound target = %+v", in.target)
	}
}

func TestCopyForwardRequestHeadersDropsInternalHeaders(t *testing.T) {
	src := map[string][]string{
		"Authorization": {"Bearer amt_external"},
		"Cookie":        {"session=1"},
		"Host":          {"gw.example.com"},
		"X-AICLI-Mount": {"k7f3qz2a"},
		"X-AICLI-Token": {"local-write-token"},
		"Content-Type":  {"application/json"},
		"Accept":        {"application/json, text/event-stream"},
	}
	dst := http.Header{}
	copyForwardRequestHeaders(dst, src)

	for _, dropped := range []string{"Authorization", "Cookie", "Host", "X-AICLI-Mount", "X-AICLI-Token"} {
		if got := dst.Get(dropped); got != "" {
			t.Fatalf("%s leaked through: %q", dropped, got)
		}
	}
	if got := dst.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := dst.Get("Accept"); got == "" {
		t.Fatalf("Accept was dropped")
	}
}
