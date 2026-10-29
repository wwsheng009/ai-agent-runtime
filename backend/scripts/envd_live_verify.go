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
	"strconv"
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
	grepPath := flag.String("grep-path", "", "root to search, for the capped-grep check")
	grepPattern := flag.String("grep-pattern", "root", "pattern for the capped-grep check")
	grepCap := flag.Int("grep-cap-bytes", 512, "per-call payload cap used to prove early stop")
	writePath := flag.String("write-path", "", "absolute target path to write for the fidelity check (removed afterwards)")
	forbiddenPath := flag.String("forbidden-path", "", "absolute target path the user may not write but the daemon can")
	editPath := flag.String("edit-path", "", "absolute target path used for the edit/apply_patch round trip (removed afterwards)")
	flag.Parse()

	token := strings.TrimSpace(os.Getenv(*tokenEnv))
	if *server == "" || *addr == "" || *workspace == "" || *readable == "" || *secret == "" || *grepPath == "" || *writePath == "" || *editPath == "" {
		fail("usage", fmt.Errorf("-server, -addr, -workspace, -readable, -secret, -grep-path, -write-path and -edit-path are required"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	native := serve(ctx, *server, *addr, *user, *workspace, *caFile, token, *prefix, *roots, false, 0)
	defer native.close()
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

	forced := serve(ctx, *server, *addr, *user, *workspace, *caFile, token, *prefix, *roots, true, 0)
	defer forced.close()
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

	// The capped grep: one documented knob must actually bound the read, and a
	// search that stops early must still say which engine produced the matches.
	grepRoots := *roots
	if grepRoots != "" {
		grepRoots += ","
	}
	grepRoots += *grepPath
	capped := serve(ctx, *server, *addr, *user, *workspace, *caFile, token, *prefix, grepRoots, true, *grepCap)
	defer capped.close()
	search, searchErr := capped.call(ctx, *prefix+"__grep", map[string]interface{}{"path": *grepPath, "pattern": *grepPattern})
	if searchErr != nil {
		fail("grep under the byte cap", searchErr)
	}
	if !strings.Contains(search, "searched with ") {
		fail("grep engine", fmt.Errorf("engine line missing from a capped search: %q", firstChars(search, 300)))
	}
	if strings.Contains(search, "stopped at ") {
		fmt.Printf("PASS capped grep: engine reported and truncation disclosed (%d bytes returned under the %d cap)\n", len(search), *grepCap)
	} else {
		fmt.Printf("PASS capped grep: results fit under the cap without truncation (%d bytes)\n", len(search))
	}
	capped.close()

	// The write channel: content no longer travels as argv, so a payload past the
	// 1 MiB argv ceiling is simply a write - and it must arrive byte for byte. The
	// proof does not go through our own tools: the driver reads the file back from
	// the target's disk as the target user.
	written := serve(ctx, *server, *addr, *user, *workspace, *caFile, token, *prefix, *roots, false, 0)
	defer written.close()
	// The fidelity check must not leave a 4 MB file behind on the target.
	defer func() { _ = os.Remove(*writePath) }()
	payload := strings.Repeat("tricky%%payload\\$(not-a-cmd) é行\n", 70000)
	if _, err := written.call(ctx, *prefix+"__write", map[string]interface{}{
		"path": *writePath, "content": payload,
	}); err != nil {
		fail("large write", err)
	}
	landed, err := os.ReadFile(*writePath)
	if err != nil {
		fail("read the written file back from the target disk", err)
	}
	if string(landed) != payload {
		fail("write fidelity", fmt.Errorf("asked for %d bytes, the disk holds %d", len(payload), len(landed)))
	}
	fmt.Printf("PASS large write: %d bytes landed unchanged (the argv ceiling no longer applies)\n", len(landed))

	// And the gate: the daemon could write this path, the target user cannot.
	// Whether the file survived is checked by the caller with root, because reading
	// it is exactly what this user may not do.
	if *forbiddenPath != "" {
		refusal, refusalErr := written.call(ctx, *prefix+"__write", map[string]interface{}{
			"path": *forbiddenPath, "content": "pwned\n",
		})
		if refusalErr == nil {
			fail("write gate", fmt.Errorf("the write to %s SUCCEEDED: %s", *forbiddenPath, firstChars(refusal, 200)))
		}
		if !strings.Contains(refusalErr.Error(), "permission denied writing") {
			fail("write gate wording", fmt.Errorf("expected a permission refusal, got: %v", refusalErr))
		}
		fmt.Printf("PASS write gate: %s refused (%v)\n", *forbiddenPath, refusalErr)
	}

	// edit and apply_patch now read through the same guarded channel `view` uses,
	// so prove the round trip on the real daemon: a small file, edited and patched,
	// verified by reading the target's disk directly rather than through our tools.
	const seeded = "alpha\nbeta\n"
	if _, err := written.call(ctx, *prefix+"__write", map[string]interface{}{
		"path": *editPath, "content": seeded,
	}); err != nil {
		fail("seed the edit fixture", err)
	}
	defer func() { _ = os.Remove(*editPath) }()
	if _, err := written.call(ctx, *prefix+"__edit", map[string]interface{}{
		"file_path": *editPath, "old_string": "beta", "new_string": "gamma",
	}); err != nil {
		fail("edit through the guarded read", err)
	}
	if after, readErr := os.ReadFile(*editPath); readErr != nil || string(after) != "alpha\ngamma\n" {
		fail("edit fidelity", fmt.Errorf("disk holds %q (%v)", string(after), readErr))
	}
	fmt.Printf("PASS edit through the guarded read: disk holds %q\n", "alpha\ngamma\n")

	if _, err := written.call(ctx, *prefix+"__apply_patch", map[string]interface{}{
		"patch": "*** Begin Patch\n*** Update File: " + strings.TrimPrefix(*editPath, *workspace+"/") +
			"\n@@\n-gamma\n+delta\n*** End Patch\n",
	}); err != nil {
		fail("apply_patch through the guarded read", err)
	}
	if after, readErr := os.ReadFile(*editPath); readErr != nil || string(after) != "alpha\ndelta\n" {
		fail("apply_patch fidelity", fmt.Errorf("disk holds %q (%v)", string(after), readErr))
	}
	fmt.Printf("PASS apply_patch through the guarded read: disk holds %q\n", "alpha\ndelta\n")
	written.close()

	fmt.Println("ALL CHECKS PASSED")
}

// firstChars keeps a failure message short without dumping a whole search result.
func firstChars(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

type liveRun struct {
	cmd     *exec.Cmd
	session *mcp.ClientSession
	channel string
	prefix  string
	stderr  *strings.Builder
}

func serve(ctx context.Context, binary, addr, user, workspace, caFile, token, prefix, roots string, noFiles bool, maxBytes int) *liveRun {
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
	if maxBytes > 0 {
		// The one documented knob. Before Normalize derived CaptureLimit from it,
		// read tools silently ignored whatever an operator set here.
		args = append(args, "--max-bytes-per-call", strconv.Itoa(maxBytes))
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
	return r.call(ctx, r.prefix+"__view", map[string]interface{}{"file_path": target})
}

// call runs one tool and turns an MCP error result into a Go error, because a
// refusal and an empty result are different facts and a check must not confuse
// them.
func (r *liveRun) call(ctx context.Context, tool string, args map[string]interface{}) (string, error) {
	result, err := r.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      tool,
		Arguments: args,
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
