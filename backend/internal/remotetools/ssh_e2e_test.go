package remotetools_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/ssh"
)

// TestSSHStdioEndToEnd proves the P-1 spike claim: stdio MCP survives an SSH
// exec channel (the transport the design depends on for zero-listener targets).
//
// It runs a real sshd in-process, executes the built binary as the remote
// command, and drives it with a real MCP client. Skipped when the binary has not
// been built (CI builds it separately) so the unit suite stays hermetic.
func TestSSHStdioEndToEnd(t *testing.T) {
	binary := os.Getenv("AICLI_REMOTE_TOOLS_BIN")
	if binary == "" {
		t.Skip("set AICLI_REMOTE_TOOLS_BIN to run the SSH stdio end-to-end test")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Skipf("binary not found: %v", err)
	}

	workspace := newTestWorkspace(t)
	address, stop := startTestSSHD(t, binary)
	defer stop()

	// ssh.Client is the transport the production ssh-client wraps; using the
	// library directly keeps the test free of a second process.
	client, err := ssh.Dial("tcp", address, &ssh.ClientConfig{
		User:            "tester",
		Auth:            []ssh.AuthMethod{ssh.Password("secret")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("ssh session: %v", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	// stderr must be drained: a full pipe would block the remote process.
	var stderrBuf strings.Builder
	go func() {
		// Keep a bounded copy for diagnostics; draining is mandatory because a
		// full pipe would block the remote process.
		buffer := make([]byte, 4096)
		for {
			n, readErr := stderr.Read(buffer)
			if n > 0 && stderrBuf.Len() < 8192 {
				stderrBuf.Write(buffer[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()

	// The command travels as a JSON argv array. Exec channels carry one string,
	// and re-parsing that string through a platform shell would make this test
	// about cmd.exe vs POSIX quoting rather than about the channel plumbing it is
	// meant to cover. POSIX escaping itself is covered by the quoting unit test.
	commandBytes, marshalErr := json.Marshal([]string{
		binary, "serve", "--stdio", "--workspace", workspace, "--tool-prefix", "remote_dev",
	})
	if marshalErr != nil {
		t.Fatalf("encode argv: %v", marshalErr)
	}
	command := string(commandBytes)
	if err := session.Start(command); err != nil {
		t.Fatalf("start remote command: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	clientMCP := mcp.NewClient(&mcp.Implementation{Name: "ssh-e2e", Version: "1.0.0"}, nil)
	mcpSession, err := clientMCP.Connect(ctx, &mcp.IOTransport{
		Reader: io.NopCloser(stdout),
		Writer: nopWriteCloser{stdin},
	}, nil)
	if err != nil {
		t.Fatalf("mcp connect over ssh stdio: %v (remote stderr: %s)", err, stderrBuf.String())
	}
	defer mcpSession.Close()

	result := callTool(t, ctx, mcpSession, "remote_dev__view", map[string]interface{}{
		"file_path": "src/main.go",
	})
	if !strings.Contains(result, "func main()") {
		t.Fatalf("view over ssh stdio returned unexpected content: %s", result)
	}

	_ = mcpSession.Close()
	_ = session.Close()
}

// nopWriteCloser adapts the SSH stdin pipe to the io.WriteCloser the MCP
// transport requires without closing the channel before the session ends.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// startTestSSHD runs a minimal sshd that executes the requested command.
//
// It supports one password-authenticated user and one exec request per
// connection, which is exactly what the stdio transport needs.
func startTestSSHD(t *testing.T, binary string) (string, func()) {
	t.Helper()

	_, privateHostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateHostKey)
	if err != nil {
		t.Fatalf("signer: %v", err)
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
		t.Fatalf("listen: %v", err)
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
				handleSSHConn(conn, cfg)
			}(conn)
		}
	}()
	return listener.Addr().String(), stop
}

// handleSSHConn serves one SSH connection: accept auth, then run one exec request.
func handleSSHConn(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	serverConn, channels, requests, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go ssh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels are supported")
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
				runRemoteCommand(channel, payload.Command)
				return
			}
		}()
	}
}

// runRemoteCommand executes the request with the SSH channel as stdio.
//
// The test client sends a JSON argv array so the exec channel string never has
// to pass through a platform shell; POSIX escaping is covered by its own unit
// test. This keeps the end-to-end test focused on the channel plumbing.
func runRemoteCommand(channel ssh.Channel, command string) {
	var argv []string
	if err := json.Unmarshal([]byte(command), &argv); err != nil || len(argv) == 0 {
		_, _ = io.WriteString(channel.Stderr(), "cannot decode argv: "+(command))
		return
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = channel
	cmd.Stdout = channel
	cmd.Stderr = channel.Stderr()
	if err := cmd.Start(); err != nil {
		_, _ = io.WriteString(channel.Stderr(), err.Error())
		return
	}
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			_ = channel.CloseWrite()
			_ = exitErr
			return
		}
	}
}

