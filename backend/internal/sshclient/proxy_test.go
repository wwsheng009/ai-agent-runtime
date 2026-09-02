package sshclient

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 全局 relay 程序路径，由 TestMain 构建一次。
var relayPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sshclient-proxy-test")
	if err != nil {
		panic(err)
	}
	relayPath = filepath.Join(dir, "relay.exe")

	build := exec.Command("go", "build", "-o", relayPath, "./testrelay")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		os.RemoveAll(dir)
		panic(fmt.Sprintf("build test relay: %v", err))
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// 单元测试：splitCommandLine
// ---------------------------------------------------------------------------

func TestSplitCommandLine(t *testing.T) {
	cases := []struct {
		in   string
		want []string // nil 表示期待错误
	}{
		{
			in:   `"C:/Program Files/Git/mingw64/bin/connect.exe" -H 127.0.0.1:10810 %h %p`,
			want: []string{`C:/Program Files/Git/mingw64/bin/connect.exe`, "-H", "127.0.0.1:10810", "%h", "%p"},
		},
		{
			in:   `connect.exe -S 127.0.0.1:10808 -a none %h %p`,
			want: []string{"connect.exe", "-S", "127.0.0.1:10808", "-a", "none", "%h", "%p"},
		},
		{
			in:   `ssh -W %h:%p -p 22 jump-host`,
			want: []string{"ssh", "-W", "%h:%p", "-p", "22", "jump-host"},
		},
		{
			in:   `"C:\Program Files\OpenSSH\ssh.exe" -W %h:%p jump`,
			want: []string{`C:\Program Files\OpenSSH\ssh.exe`, "-W", "%h:%p", "jump"},
		},
		{in: ``, want: nil},          // 空命令
		{in: `   `, want: nil},       // 全空白
		{in: `"unterminated`, want: nil}, // 引号未闭合
	}

	for _, tc := range cases {
		got, err := splitCommandLine(tc.in)
		if tc.want == nil {
			if err == nil {
				t.Errorf("splitCommandLine(%q): expected error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitCommandLine(%q): %v", tc.in, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("splitCommandLine(%q): len mismatch\ngot  %v\nwant %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitCommandLine(%q)[%d]:\ngot  %q\nwant %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 单元测试：expandProxyCommand
// ---------------------------------------------------------------------------

func TestExpandProxyCommand(t *testing.T) {
	// 基本令牌展开
	got := expandProxyCommand(
		`"C:/x/connect.exe" -H 127.0.0.1:10810 %h %p -r %r -n %n %%h`,
		"192.0.2.10", "gateway", "2222", "alice",
	)
	want := `"C:/x/connect.exe" -H 127.0.0.1:10810 192.0.2.10 2222 -r alice -n gateway %h`
	if got != want {
		t.Fatalf("expand:\ngot  %q\nwant %q", got, want)
	}

	// 未知令牌保留原样
	got = expandProxyCommand("a %x b", "h", "n", "p", "r")
	want = "a %x b"
	if got != want {
		t.Fatalf("expand unknown token:\ngot  %q\nwant %q", got, want)
	}

	// 纯文字（无令牌）
	got = expandProxyCommand("connect.exe -H proxy:8080", "h", "n", "p", "r")
	want = "connect.exe -H proxy:8080"
	if got != want {
		t.Fatalf("expand literal:\ngot  %q\nwant %q", got, want)
	}

	// %k 等价于 %n
	got = expandProxyCommand("host=%k alias=%h", "192.0.2.1", "myhost", "2222", "bob")
	want = "host=myhost alias=192.0.2.1"
	if got != want {
		t.Fatalf("expand %%k:\ngot  %q\nwant %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// 端到端测试：通过代理连接真实的 SSH 服务器
// ---------------------------------------------------------------------------

func TestNewClientViaProxyCommand(t *testing.T) {
	host, port, _ := newStubSSHServer(t)

	opts := newTestOpts(host, port, 10*time.Second)
	opts.ProxyCommand = fmt.Sprintf(`"%s" %%h %%p`, relayPath)
	opts.OriginalHost = host

	client, err := NewClient(opts, io.Discard)
	if err != nil {
		t.Fatalf("connect through proxy command: %v", err)
	}
	defer client.Close()

	// 双向链路验证：通过已建立的连接发送一个全局请求（stub 服务器
	// 的 DiscardRequests 会回复 WantReply=false，证明数据流双向贯通）。
	_, _, err = client.SSHSession().SendRequest("proxy-e2e@test", true, nil)
	if err != nil {
		t.Fatalf("send request through proxy: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 超时测试：代理连接半开目标（accept 后无响应），ConnectTimeout 必须生效
// ---------------------------------------------------------------------------

func TestNewClientProxyHandshakeTimeout(t *testing.T) {
	s := newSilentListener(t)
	host, portStr, err := net.SplitHostPort(s.ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	opts := newTestOpts(host, port, time.Second)
	opts.ProxyCommand = fmt.Sprintf(`"%s" %%h %%p`, relayPath)
	opts.OriginalHost = host

	start := time.Now()
	client, err := NewClient(opts, io.Discard)
	elapsed := time.Since(start)
	if err == nil {
		_ = client.Close()
		t.Fatal("expected handshake timeout through proxy, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("proxy handshake hung too long: %v (err=%v)", elapsed, err)
	}
	if elapsed < 700*time.Millisecond {
		t.Fatalf("proxy handshake returned too early (%v), deadline not enforced: %v", elapsed, err)
	}
	t.Logf("proxy handshake timed out in %v: %v", elapsed, err)
}

// ---------------------------------------------------------------------------
// 错误场景：代理命令启动失败
// ---------------------------------------------------------------------------

func TestNewClientProxyStartFailure(t *testing.T) {
	opts := newTestOpts("example.invalid", 22, time.Second)
	opts.ProxyCommand = `C:\no\such\proxy.exe %h %p`
	_, err := NewClient(opts, io.Discard)
	if err == nil {
		t.Fatal("expected start failure error")
	}
	if !strings.Contains(err.Error(), "proxy command") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 集成测试：ApplyConfig 从配置文件正确解析 ProxyCommand
// ---------------------------------------------------------------------------

func TestApplyConfigProxyCommand(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	content := "Host gw\n  HostName 192.0.2.10\n  Port 2222\n  User alice\n  ProxyCommand \"C:/Program Files/Git/mingw64/bin/connect.exe\" -H 127.0.0.1:10810 %h %p\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadResolvedConfig(cfgPath, "gw")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	opts := Defaults()
	opts.Host = "gw"
	opts.OriginalHost = "gw"
	opts.ApplyConfig(cfg)

	if opts.Host != "192.0.2.10" {
		t.Fatalf("HostName not applied: got %q, want 192.0.2.10", opts.Host)
	}
	if opts.Port != 2222 {
		t.Fatalf("port: got %d, want 2222", opts.Port)
	}
	if opts.User != "alice" {
		t.Fatalf("user: got %q, want alice", opts.User)
	}
	if opts.ProxyCommand == "" {
		t.Fatal("ProxyCommand should have been applied")
	}
	if !strings.Contains(opts.ProxyCommand, "%h %p") {
		t.Fatalf("ProxyCommand malformed: %q", opts.ProxyCommand)
	}
}