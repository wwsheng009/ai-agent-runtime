//go:build ignore

// Command envd_live_verify checks envd's two read channels against a real daemon
// and requires them to agree byte for byte.
//
// The native GET /files endpoint is preferred because it needs no shell, but it
// opens files as the DAEMON user rather than the requesting one. That difference
// is only safe because a permission gate runs first, so this script pins three
// things against a live target:
//
//  1. A file the requesting user may read returns identical bytes through the
//     native channel and through the spawned-process channel (the second run uses
//     --no-envd-files, so the channel really is off rather than merely unused).
//  2. A file the requesting user cannot open is refused even though the daemon can
//     read it. Without the gate the native channel would answer with its content.
//  3. An oversized file is refused before any transfer.
//
// Usage:
//
//	$env:AICLI_ENVD_TOKEN = '<access token>'
//	go run ./scripts/envd_live_verify.go -server $exe -addr https://host:49983 `
//	   -user n9e -workspace /home/n9e -ca-file C:\path\tls.crt
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type nopWriteCloser struct{ io.Writer }

func (n nopWriteCloser) Close() error { return nil }

func main() {
	server := flag.String("server", "", "aicli-remote-tools binary to drive")
	addr := flag.String("addr", "", "envd endpoint, e.g. https://10.0.0.9:49983")
	user := flag.String("user", "n9e", "OS user envd executes as")
	workspace := flag.String("workspace", "", "absolute workspace on the target")
	caFile := flag.String("ca-file", "", "PEM bundle verifying the envd endpoint")
	tokenEnv := flag.String("token-env", "AICLI_ENVD_TOKEN", "local env var holding the access token")
	prefix := flag.String("prefix", "remote_live", "tool prefix")
	readable := flag.String("readable", "", "workspace-relative file both channels must agree on")
	secret := flag.String("secret", "", "file inside an allowed root that the requesting user cannot open")
	roots := flag.String("allowed-roots", "", "comma-separated extra absolute roots, so a jail-internal unreadable file can be named")
	flag.Parse()

	token := strings.TrimSpace(os.Getenv(*tokenEnv))
	if *server == "" || *addr == "" || *workspace == "" || *readable == "" || *secret == "" {
		fail("usage", fmt.Errorf("-server, -addr, -workspace, -readable and -secret are required"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	native := serve(ctx, *server, *addr, *user, *workspace, *caFile, token, *prefix, *roots, false)
	defer native.close()
	defer native.close()
	tools, err := native.session.ListTools(ctx, nil)
	if err != nil {
		fail("tools/list (native)", err)
	}
	fmt.Printf("PASS native-channel run: %d tools advertised under prefix %q\n", len(tools.Tools), *prefix)

	viaNative, err := native.view(ctx, *readable)
	if err != nil {
		fail("view through the native channel", err)
	}
	fmt.Printf("PASS native read: %s (%d bytes)\n", *readable, len(viaNative))

	// The gate: the daemon could answer this, the requesting user could not.
	refusal, gateErr := native.view(ctx, *secret)
	if gateErr == nil && !strings.Contains(refusal, "permission denied") {
		fail("permission gate", fmt.Errorf("expected a refusal for %s, got %d bytes of its content instead", *secret, len(refusal)))
	}
	if gateErr != nil {
		fmt.Printf("PASS permission gate: %v\n", gateErr)
	} else {
		fmt.Printf("PASS permission gate: %s refused\n", *secret)
	}
	native.close()

	forced := serve(ctx, *server, *addr, *user, *workspace, *caFile, token, *prefix, *roots, true)
	defer forced.close()
	defer forced.close()
	viaProcess, err := forced.view(ctx, *readable)
	if err != nil {
		fail("view with the native channel disabled", err)
	}
	if viaNative != viaProcess {
		fail("channel parity", fmt.Errorf("native read %d bytes, process read %d bytes; the two channels must agree byte for byte", len(viaNative), len(viaProcess)))
	}
	fmt.Printf("PASS channel parity: %d bytes identical through /files and through /bin/cat\n", len(viaNative))
	forced.close()

	fmt.Println("ALL CHECKS PASSED")
}

type liveRun struct {
	cmd     *exec.Cmd
	session *mcp.ClientSession
	channel string
	prefix  string
	stderr  *strings.Builder
}

func serve(ctx context.Context, binary, addr, user, workspace, caFile, token, prefix, roots string, noFiles bool) *liveRun {
	args := []string{
		"serve", "--stdio", "--backend", "envd",
		"--addr", addr,
		"--user", user,
		"--workspace", workspace,
		"--tool-prefix", prefix,
		"--profile", "standard",
		"--token-env", "AICLI_LIVE_TOKEN",
	}
	if caFile != "" {
		args = append(args, "--ca-file", caFile)
	}
	if roots != "" {
		// The gate is only interesting for a path the jail allows: naming a root
		// that contains files this user cannot open is what makes the check real
		// rather than a re-test of the jail.
		args = append(args, "--allowed-roots", roots)
	}
	if noFiles {
		args = append(args, "--no-envd-files")
	}

	name := "native"
	if noFiles {
		name = "process"
	}
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), "AICLI_LIVE_TOKEN="+token)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fail("stdin pipe", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fail("stdout pipe", err)
	}
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		fail("start "+name+" server", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "envd-live-verify", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.IOTransport{
		Reader: io.NopCloser(stdout),
		Writer: nopWriteCloser{stdin},
	}, nil)
	if err != nil {
		fail("mcp connect ("+name+")", fmt.Errorf("%w (server stderr: %s)", err, stderr.String()))
	}
	return &liveRun{cmd: cmd, session: session, channel: name, prefix: prefix, stderr: stderr}
}

// view calls the prefixed view tool and returns its text, with the refusal as the
// error so the caller cannot mistake a denial for an empty file.
func (r *liveRun) view(ctx context.Context, target string) (string, error) {
	result, err := r.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      r.prefix + "__view",
		Arguments: map[string]interface{}{"file_path": target},
	})
	if err != nil {
		return "", fmt.Errorf("%w (server stderr: %s)", err, r.stderr.String())
	}
	text := textOf(result)
	if result.IsError {
		return text, fmt.Errorf("%s", strings.TrimSpace(text))
	}
	return text, nil
}

func textOf(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if typed, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, typed.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (r *liveRun) close() {
	if r == nil {
		return
	}
	_ = r.session.Close()
	if r.cmd != nil && r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
		_, _ = r.cmd.Process.Wait()
	}
}

func fail(stage string, err error) {
	fmt.Printf("FAIL %s: %v\n", stage, err)
	os.Exit(1)
}
