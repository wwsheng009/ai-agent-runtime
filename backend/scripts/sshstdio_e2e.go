//go:build ignore

// Command sshstdio_e2e proves the SSH stdio transport against a real sshd.
//
// It starts a throwaway sshd-shaped server, runs `aicli-remote-tools serve --stdio`
// as the remote command over an SSH exec channel, and drives it with a real MCP
// client. This is the deployment shape used when a target must not listen on any
// port.
//
// Usage (on a Linux host or under WSL):
//
//	go run ./scripts/sshstdio_e2e.go -binary /tmp/aicli-rt/aicli-remote-tools -workspace /tmp/aicli-rt/ws
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/ssh"
)

func main() {
	binary := flag.String("binary", "/tmp/aicli-rt/aicli-remote-tools", "remote server binary")
	workspace := flag.String("workspace", "/tmp/aicli-rt/ws", "remote workspace")
	prefix := flag.String("prefix", "remote_ssh", "tool prefix")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	addr, stop := startSSHD(*binary)
	defer stop()

	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "tester",
		Auth:            []ssh.AuthMethod{ssh.Password("secret")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		fail("ssh dial", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		fail("ssh session", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		fail("stdin pipe", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		fail("stdout pipe", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		fail("stderr pipe", err)
	}

	var stderrBuf strings.Builder
	var bufMu sync.Mutex
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, readErr := stderr.Read(buffer)
			if n > 0 {
				bufMu.Lock()
				if stderrBuf.Len() < 8192 {
					stderrBuf.Write(buffer[:n])
				}
				bufMu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()

	// The remote config travels as JSON argv: the exec channel carries one string,
	// and re-parsing it through a shell is exactly the failure mode the --config-b64
	// design exists to avoid. Here the test server executes argv directly.
	commandBytes, err := json.Marshal([]string{
		*binary, "serve", "--stdio",
		"--config-b64", encodeConfig(*workspace, *prefix),
	})
	if err != nil {
		fail("encode argv", err)
	}
	if err := session.Start(string(commandBytes)); err != nil {
		fail("start remote command", err)
	}

	clientMCP := mcp.NewClient(&mcp.Implementation{Name: "ssh-e2e", Version: "1.0.0"}, nil)
	mcpSession, err := clientMCP.Connect(ctx, &mcp.IOTransport{
		Reader: io.NopCloser(stdout),
		Writer: nopWriteCloser{stdin},
	}, nil)
	if err != nil {
		bufMu.Lock()
		captured := stderrBuf.String()
		bufMu.Unlock()
		fail("mcp over ssh stdio", fmt.Errorf("%w (remote stderr: %s)", err, captured))
	}
	defer mcpSession.Close()

	tools, err := mcpSession.ListTools(ctx, nil)
	if err != nil {
		fail("list tools", err)
	}
	fmt.Printf("PASS ssh stdio tools/list: %d tools\n", len(tools.Tools))
	for _, tool := range tools.Tools {
		if !strings.HasPrefix(tool.Name, *prefix+"__") {
			fail("prefix", fmt.Errorf("tool %q lacks prefix %q", tool.Name, *prefix))
		}
	}

	body := call(ctx, mcpSession, *prefix+"__view", map[string]interface{}{"file_path": "src/main.go"})
	if !strings.Contains(body, "func main()") {
		fail("view over ssh", fmt.Errorf("missing content: %s", body))
	}
	fmt.Println("PASS ssh stdio view")

	denied := callExpectError(ctx, mcpSession, *prefix+"__view", map[string]interface{}{"file_path": "../../etc/passwd"})
	if !strings.Contains(denied, "PATH_DENIED") {
		fail("jail over ssh", fmt.Errorf("unexpected: %s", denied))
	}
	fmt.Println("PASS ssh stdio jail")

	// Closing the MCP session must close stdin, which ends the remote process.
	_ = mcpSession.Close()
	_ = session.Close()
	fmt.Println("ALL PASS")
}

// encodeConfig builds the base64 config the remote server accepts.
func encodeConfig(workspace, prefix string) string {
	payload := map[string]interface{}{
		"workspace":        workspace,
		"toolPrefix":       prefix,
		"targetName":       "ssh-target",
		"profile":          "readonly",
		"descriptionStyle": "explicit",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		fail("encode config", err)
	}
	return base64.StdEncoding.EncodeToString(encoded)
}

// startSSHD starts a minimal sshd that executes the requested command.
func startSSHD(binary string) (string, func()) {
	_, privateHostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fail("host key", err)
	}
	signer, err := ssh.NewSignerFromKey(privateHostKey)
	if err != nil {
		fail("signer", err)
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
		fail("listen", err)
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

// handleConn serves one SSH connection with one exec request.
func handleConn(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	serverConn, channels, requests, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go ssh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, channelRequests, acceptErr := newChannel.Accept()
		if acceptErr != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for request := range channelRequests {
				if request.Type != "exec" {
					_ = request.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				if unmarshalErr := ssh.Unmarshal(request.Payload, &payload); unmarshalErr != nil {
					_ = request.Reply(false, nil)
					return
				}
				_ = request.Reply(true, nil)
				runCommand(channel, payload.Command)
				return
			}
		}()
	}
}

// runCommand executes the JSON argv with the SSH channel as stdio.
func runCommand(channel ssh.Channel, command string) {
	var argv []string
	if err := json.Unmarshal([]byte(command), &argv); err != nil || len(argv) == 0 {
		_, _ = io.WriteString(channel.Stderr(), "cannot decode argv: "+command)
		return
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = channel
	cmd.Stdout = channel
	cmd.Stderr = channel.Stderr()
	if err := cmd.Run(); err != nil {
		_, _ = io.WriteString(channel.Stderr(), err.Error())
	}
}

func call(ctx context.Context, session *mcp.ClientSession, name string, args map[string]interface{}) string {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fail("call "+name, err)
	}
	if result.IsError {
		fail("call "+name, fmt.Errorf("tool error: %s", text(result)))
	}
	return text(result)
}

func callExpectError(ctx context.Context, session *mcp.ClientSession, name string, args map[string]interface{}) string {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		fail("call "+name, err)
	}
	if !result.IsError {
		fail("call "+name, fmt.Errorf("expected error, got: %s", text(result)))
	}
	return text(result)
}

func text(result *mcp.CallToolResult) string {
	var builder strings.Builder
	for _, content := range result.Content {
		if typed, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(typed.Text)
		}
	}
	return builder.String()
}

func fail(label string, err error) {
	fmt.Printf("FAIL %s: %v\n", label, err)
	os.Exit(1)
}

// nopWriteCloser adapts the SSH stdin pipe.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }