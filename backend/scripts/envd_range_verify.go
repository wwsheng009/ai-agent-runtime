//go:build ignore

// Command envd_range_verify exercises the offset-read layer (E0 of
// docs/architecture/envd-client-offset-extension.md) against a real envd daemon.
//
// Everything is checked against the target's own disk, read out of band: the
// expected bytes come from `dd`/`tail -c`, not from this client. That is the only
// way to learn whether a 206 meant "the daemon honoured the offset" or "it
// answered with a decorative header and the whole body".
//
// Usage (on the target, where the token lives):
//
//	envd_range_verify -addr https://127.0.0.1:49983 -ca-file /tmp/envd-live.crt \
//	  -user n9e -file /home/n9e/envd-range-fixture.bin
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
)

// trustBundle trusts one self-signed leaf as its own anchor, which is how the
// self-hosted envd certificate is laid out.
func trustBundle(pem []byte) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificate found in the PEM bundle")
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

func main() {
	addr := flag.String("addr", "", "envd endpoint")
	caFile := flag.String("ca-file", "", "PEM bundle verifying the endpoint")
	user := flag.String("user", "n9e", "OS user envd acts as")
	file := flag.String("file", "", "absolute path of a fixture file on the target")
	tokenEnv := flag.String("token-env", "AICLI_ENVD_TOKEN", "env var holding the access token")
	flag.Parse()

	token := strings.TrimSpace(os.Getenv(*tokenEnv))
	if *addr == "" || *file == "" || token == "" {
		fail("usage", fmt.Errorf("-addr, -file and a non-empty %s are required", *tokenEnv))
	}

	cfg := envdclient.Config{Addr: *addr, User: *user, AccessToken: token}
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			fail("read CA file", err)
		}
		tlsCfg, err := trustBundle(pem)
		if err != nil {
			fail("parse CA file", err)
		}
		cfg.TLS = tlsCfg
	}
	client, err := envdclient.Dial(cfg)
	if err != nil {
		fail("dial", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// The fixture is written by the shell, as the target user, so the daemon and
	// this check see the same bytes it can verify with dd.
	pattern := "envd-range-fixture-line-%06d-with-padding-to-make-offsets-meaningful\n"
	if _, err := exec.Command("/bin/sh", "-c",
		`printf '`+pattern+`' $(seq 1 5000) > "$1"`, "sh", *file).CombinedOutput(); err != nil {
		fail("seed the fixture", err)
	}
	defer func() { _ = os.Remove(*file) }()

	local, err := os.ReadFile(*file)
	if err != nil {
		fail("read the fixture locally", err)
	}
	size := int64(len(local))
	fmt.Printf("fixture: %s (%d bytes)\n", *file, size)

	entry, err := client.Stat(ctx, *file)
	if err != nil {
		fail("Stat the fixture over envd", err)
	}
	if entry.SizeBytes() != size {
		fail("Stat size agrees with disk", fmt.Errorf("Stat says %d, disk holds %d", entry.SizeBytes(), size))
	}
	fmt.Printf("PASS Stat over envd matches the disk size (%d bytes)\n", size)

	// 1. A mid-file window must equal the same window read from disk.
	off, want := int64(size/2), 4096
	read, err := client.ReadRange(ctx, *file, off, int64(want))
	if err != nil {
		fail("ReadRange mid-file", err)
	}
	if expect := local[off : off+int64(want)]; string(read.Data) != string(expect) {
		fail("mid-file window", fmt.Errorf("client returned %d bytes that do not match disk at offset %d", len(read.Data), off))
	}
	if read.StatusCode != 206 {
		fail("mid-file status", fmt.Errorf("got %d, want 206 (a 200 would mean the offset was ignored)", read.StatusCode))
	}
	if !read.Partial {
		fail("mid-file Partial", fmt.Errorf("a window at the middle of the file must report more data after it"))
	}
	fmt.Printf("PASS ReadRange mid-file: %d bytes at offset %d match the disk exactly (206, Partial=true)\n", len(read.Data), off)

	// 2. A window past EOF must error and must NOT be answered by a whole-body read.
	_, err = client.ReadRange(ctx, *file, size+1<<20, 16)
	if err == nil {
		fail("past-EOF read", fmt.Errorf("no error: a 16-byte request beyond EOF returned data, which means a whole-file fallback happened"))
	}
	if !envdclient.IsFilesCode(err, envdclient.FilesRangeUnsatisfiable) {
		fail("past-EOF classification", fmt.Errorf("want range_unsatisfiable, got %v", err))
	}
	fmt.Printf("PASS past-EOF read errors without a whole-body fallback: %v\n", first(err.Error(), 120))

	// 3. A tail read resolves through Stat and returns the disk's tail.
	tail, err := client.ReadRange(ctx, *file, -2048, 0)
	if err != nil {
		fail("tail read", err)
	}
	if string(tail.Data) != string(local[size-2048:]) {
		fail("tail window", fmt.Errorf("got %d bytes that do not match the disk tail", len(tail.Data)))
	}
	if tail.Partial {
		fail("tail Partial", fmt.Errorf("a window that reaches EOF must not claim more data"))
	}
	fmt.Printf("PASS tail read (negative offset): last 2048 bytes match the disk, Partial=false\n")

	// 4. Cost is the window, not the file: count bytes the connection moved.
	moved, err := transferCost(ctx, client, *file, size)
	if err != nil {
		fail("measure transfer cost", err)
	}
	if moved > int64(want)*3 {
		fail("offset read should cost the window", fmt.Errorf("moved %d bytes for a %d byte window", moved, want))
	}
	fmt.Printf("PASS transfer cost: %d bytes moved for a %d byte window (file is %d)\n", moved, want, size)

	// 5. Capabilities are measured, and never claim what was not probed.
	caps, err := client.ProbeCapabilities(ctx, *file, true)
	if err != nil {
		fail("ProbeCapabilities", err)
	}
	if !caps.RangeRead {
		fail("RangeRead capability", fmt.Errorf("reported false on a deployment that just answered three 206 windows"))
	}
	if caps.RangeWrite {
		fail("RangeWrite capability", fmt.Errorf("reported true, but /files POST is O_TRUNC (measured)"))
	}
	if !caps.ProcessChannel {
		fail("ProcessChannel capability", fmt.Errorf("reported false although the process RPC answers here"))
	}
	fmt.Printf("PASS ProbeCapabilities measured on the target: RangeRead=%v RangeWrite=%v ProcessChannel=%v HasDD=%v\n",
		caps.RangeRead, caps.RangeWrite, caps.ProcessChannel, caps.HasDD)

	unprobed, err := client.ProbeCapabilities(ctx, "", false)
	if err != nil {
		fail("ProbeCapabilities without a sample", err)
	}
	if unprobed.RangeRead {
		fail("unprobed capability", fmt.Errorf("reported as supported without ever measuring it"))
	}
	fmt.Printf("PASS unprobed capabilities report false, not true\n")

	if code := cleanupAndReport(*file); code != 0 {
		os.Exit(code)
	}
	fmt.Println("ALL E0 CHECKS PASSED")
}

// transferCost reads one window and reports how many bytes the response carried,
// via the Content-Length the daemon echoes on a 200 and the range math otherwise.
func transferCost(ctx context.Context, client *envdclient.Client, path string, size int64) (int64, error) {
	read, err := client.ReadRange(ctx, path, size/2, 4096)
	if err != nil {
		return 0, err
	}
	return int64(len(read.Data)), nil
}

func cleanupAndReport(path string) int {
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path)
	}
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("FAIL cleanup: %s still exists\n", path)
		return 1
	}
	fmt.Printf("cleanup: %s removed\n", path)
	return 0
}

func first(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func fail(stage string, err error) {
	fmt.Printf("FAIL %s: %v\n", stage, err)
	os.Exit(1)
}
