//go:build ignore

// Command ssh_transports_e2e proves BOTH transports with the REAL ssh-client
// binary, not an in-process stand-in:
//
//	ssh-stdio : ssh-client -T --stdio user@host -- <binary> serve --stdio --config-b64 <b64>
//	ssh-http  : ssh-client -N -L <port>:127.0.0.1:<port> user@host
//	            + streamable HTTP MCP against 127.0.0.1:<port>
//
// A minimal sshd is still needed (no real sshd on this host), but everything
// under test — argument quoting, stdin piping, local port forwarding, the MCP
// handshake — runs through the production binaries.
//
// Usage (inside WSL, from backend/):
//
//	go run ./scripts/ssh_transports_e2e.go \
//	  -binary /tmp/aicli-rt3/aicli-remote-tools \
//	  -sshclient /tmp/aicli-rt3/ssh-client \
//	  -workspace /tmp/aicli-rt3/ws
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/ssh"
)

func main() {
	binary := flag.String("binary", "/tmp/aicli-rt3/aicli-remote-tools", "remote server binary")
	sshclient := flag.String("sshclient", "/tmp/aicli-rt3/ssh-client", "ssh-client binary under test")
	workspace := flag.String("workspace", "/tmp/aicli-rt3/ws", "remote workspace")
	flag.Parse()

	addr, stop := startSSHD()
	defer stop()
	host, portStr, _ := net.SplitHostPort(addr)

	failures := 0
	check := func(name string, err error) {
		if err != nil {
			failures++
			fmt.Printf("FAIL %s: %v\n", name, err)
			return
		}
		fmt.Printf("PASS %s\n", name)
	}

	fmt.Println("=== transport 1: ssh-stdio through the real ssh-client ===")
	check("ssh-stdio round trip", runStdio(*sshclient, host, portStr, *binary, *workspace))

	fmt.Println()
	fmt.Println("=== transport 2: ssh-http (local forward) through the real ssh-client ===")
	check("ssh-http round trip", runForward(*sshclient, host, portStr, *binary, *workspace))

	fmt.Println()
	if failures > 0 {
		fmt.Printf("FAILED: %d check(s)\n", failures)
		os.Exit(1)
	}
	fmt.Println("ALL PASS (both transports, real ssh-client)")
}

// encodeConfig builds the base64 config the remote server accepts.
func encodeConfig(workspace, prefix string) string {
	payload := map[string]interface{}{
		"workspace":        workspace,
		"toolPrefix":       prefix,
		"targetName":       "ssh-target",
		"profile":          "standard",
		"descriptionStyle": "explicit",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(encoded)
}

// runStdio drives `serve --stdio` over an SSH exec channel.
//
// It uses the exact argv shape remotetargets expands for transport ssh-stdio, so
// a quoting bug in that expansion shows up here.
func runStdio(sshclient, host, port, binary, workspace string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := encodeConfig(workspace, "remote_ssh")
	args := []string{
		"-T",
		"-p", port,
		"--password", "secret",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"--stdio",
		"tester@" + host,
		"--",
		binary, "serve", "--stdio", "--config-b64", cfg,
	}
	cmd := exec.CommandContext(ctx, sshclient, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ssh-client: %w", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "ssh-stdio-e2e", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{
		Reader: io.NopCloser(stdout),
		Writer: nopWriteCloser{stdin},
	}, nil)
	if err != nil {
		return fmt.Errorf("mcp over ssh stdio: %w (stderr: %s)", err, stderrBuf.String())
	}
	defer session.Close()

	if err := exercise(ctx, session, "remote_ssh"); err != nil {
		return fmt.Errorf("%w (stderr: %s)", err, stderrBuf.String())
	}
	return nil
}

// runForward starts `ssh-client -N -L` and speaks streamable HTTP to the far side.
func runForward(sshclient, host, port, binary, workspace string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	localPort, err := freePort()
	if err != nil {
		return err
	}

	// The remote daemon is launched by the test sshd's exec handler, which here
	// simulates the operator's "start the daemon on the target" step.
	remoteAddr := fmt.Sprintf("127.0.0.1:%d", 18801)
	daemonCfg := encodeDaemonConfig(workspace, "remote_fwd", remoteAddr)
	daemon := exec.Command(binary, "serve", "--http", "--config-b64", daemonCfg)
	daemon.Env = append(os.Environ(), "AICLI_REMOTE_TOKEN=0123456789abcdef0123456789abcdef")
	var daemonErr bytes.Buffer
	daemon.Stderr = &daemonErr
	if err := daemon.Start(); err != nil {
		return fmt.Errorf("start remote daemon: %w", err)
	}
	defer func() {
		_ = daemon.Process.Kill()
		_, _ = daemon.Process.Wait()
	}()
	if err := waitTCP(remoteAddr, 15*time.Second); err != nil {
		return fmt.Errorf("remote daemon not listening: %w (%s)", err, daemonErr.String())
	}

	forwardSpec := fmt.Sprintf("%d:%s", localPort, remoteAddr)
	fwd := exec.CommandContext(ctx, sshclient,
		"-N", "-p", port,
		"--password", "secret",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-L", forwardSpec,
		"tester@"+host,
	)
	var fwdErr bytes.Buffer
	fwd.Stderr = &fwdErr
	if err := fwd.Start(); err != nil {
		return fmt.Errorf("start ssh-client forward: %w", err)
	}
	defer func() {
		_ = fwd.Process.Kill()
		_, _ = fwd.Process.Wait()
	}()

	localURL := fmt.Sprintf("127.0.0.1:%d", localPort)
	if err := waitTCP(localURL, 20*time.Second); err != nil {
		return fmt.Errorf("local forward never came up: %w (%s)", err, fwdErr.String())
	}

	// Talk real streamable HTTP over the tunnel.
	url := "http://" + localURL + "/mcp"
	token := "0123456789abcdef0123456789abcdef"
	if err := httpHandshake(ctx, url, token); err != nil {
		return fmt.Errorf("forwarded http: %w (%s)", err, fwdErr.String())
	}

	// A second call through the same tunnel proves the forward is stable, not a
	// one-shot connection.
	if err := httpHandshake(ctx, url, token); err != nil {
		return fmt.Errorf("forwarded http (second call): %w", err)
	}
	return nil
}

// exercise runs read + jail checks against a connected MCP session.
func exercise(ctx context.Context, session *mcp.ClientSession, prefix string) error {
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	if len(tools.Tools) < 4 {
		return fmt.Errorf("expected at least 4 tools, got %d", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if !strings.HasPrefix(tool.Name, prefix+"__") {
			return fmt.Errorf("tool %q lacks prefix %q", tool.Name, prefix)
		}
	}
	fmt.Printf("     tools/list: %d tools\n", len(tools.Tools))

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      prefix + "__view",
		Arguments: map[string]interface{}{"file_path": "src/main.go"},
	})
	if err != nil {
		return fmt.Errorf("view: %w", err)
	}
	body := textOf(result)
	if !strings.Contains(body, "func main()") {
		return fmt.Errorf("view content missing: %s", body)
	}
	fmt.Println("     view: content ok")

	denied, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      prefix + "__view",
		Arguments: map[string]interface{}{"file_path": "../../etc/passwd"},
	})
	if err != nil {
		return fmt.Errorf("jail call: %w", err)
	}
	jailBody := textOf(denied)
	if !strings.Contains(jailBody, "PATH_DENIED") {
		return fmt.Errorf("jail did not deny: %s", jailBody)
	}
	fmt.Println("     jail: traversal denied")
	return nil
}

func textOf(result *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

// encodeDaemonConfig builds the HTTP daemon config for --config-b64.
func encodeDaemonConfig(workspace, prefix, listen string) string {
	payload := map[string]interface{}{
		"workspace":        workspace,
		"toolPrefix":       prefix,
		"targetName":       "ssh-fwd",
		"profile":          "readonly",
		"descriptionStyle": "explicit",
		"http": map[string]interface{}{
			"listen":   listen,
			"path":     "/mcp",
			"tokenEnv": "AICLI_REMOTE_TOKEN",
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(encoded)
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// httpHandshake performs a real Streamable HTTP MCP handshake and one tool call
// through the SSH tunnel.
//
// It uses the SDK client rather than hand-rolled JSON so the test exercises the
// same transport code the product uses.
func httpHandshake(ctx context.Context, url, token string) error {
	httpClient := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &bearerTransport{
			token: token,
			base:  http.DefaultTransport,
		},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "ssh-fwd-e2e", Version: "1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: httpClient,
	}, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer session.Close()
	return exercise(ctx, session, "remote_fwd")
}

// bearerTransport injects the Authorization header on every request, including
// the ones the SDK makes for session teardown.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

func waitTCP(addr string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return lastErr
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// --- minimal sshd with exec + direct-tcpip ---

func startSSHD() (string, func()) {
	_, privateHostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	signer, err := ssh.NewSignerFromKey(privateHostKey)
	if err != nil {
		panic(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) == "secret" {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	cfg.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}

	var wg sync.WaitGroup
	stop := func() {
		_ = listener.Close()
		wg.Wait()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			wg.Add(1)
			go func(conn net.Conn) {
				defer wg.Done()
				handleConn(conn, cfg)
			}(conn)
		}
	}()
	return listener.Addr().String(), stop
}

func handleConn(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	serverConn, channels, requests, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go ssh.DiscardRequests(requests)

	for newChannel := range channels {
		switch newChannel.ChannelType() {
		case "session":
			channel, channelRequests, acceptErr := newChannel.Accept()
			if acceptErr != nil {
				continue
			}
			go serveSession(channel, channelRequests)
		case "direct-tcpip":
			go serveDirectTCPIP(newChannel)
		default:
			_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
}

// serveDirectTCPIP is what `ssh-client -L` needs: it dials the target-side
// address and splices the bytes.
func serveDirectTCPIP(newChannel ssh.NewChannel) {
	var req struct {
		DestAddr string
		DestPort uint32
		OrigAddr string
		OrigPort uint32
	}
	if err := ssh.Unmarshal(newChannel.ExtraData(), &req); err != nil {
		_ = newChannel.Reject(ssh.Prohibited, "bad payload")
		return
	}
	target := net.JoinHostPort(req.DestAddr, fmt.Sprintf("%d", req.DestPort))
	outbound, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	channel, channelRequests, err := newChannel.Accept()
	if err != nil {
		_ = outbound.Close()
		return
	}
	go ssh.DiscardRequests(channelRequests)
	go func() {
		defer channel.Close()
		defer outbound.Close()
		_, _ = io.Copy(channel, outbound)
	}()
	_, _ = io.Copy(outbound, channel)
	_ = outbound.Close()
}

func serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			return
		}
		_ = request.Reply(true, nil)
		fmt.Fprintf(os.Stderr, "[sshd] exec payload: %q\n", payload.Command)
		runCommand(channel, payload.Command)
		return
	}
}

// runCommand executes the shell-quoted command ssh-client sent.
//
// ssh-client --stdio POSIX-quotes each argv element, so the remote side is a
// normal shell command string; running it through `sh -c` is exactly what a real
// sshd does.
func runCommand(channel ssh.Channel, command string) {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Stdin = channel
	cmd.Stdout = channel
	cmd.Stderr = channel.Stderr()
	if err := cmd.Run(); err != nil {
		_, _ = io.WriteString(channel.Stderr(), err.Error())
	}
}
