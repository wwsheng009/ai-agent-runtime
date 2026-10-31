//go:build ignore

// Command envd_seam_access_verify checks the envd seam adapter (remotetools.NewEnvdAccess)
// against a live daemon, read-only.
//
// Why this exists: the adapter's contract tests run against a described target, which
// proves the adapter is self-consistent but says nothing about whether envd answers the way
// the adapter assumes. One assumption in particular decides whether follow mode can work at
// all on a remote tree: that the daemon reports the FINAL path component as a symlink while
// resolving the components before it. If that is wrong - if envd resolves the last component
// too - then Lstat and Stat collapse into the same answer, the cycle rule never fires, and a
// remote walk would descend through links that the local walk refuses. Every one of those
// outcomes looks like a normal result.
//
// This script reads. It never writes, creates, deletes or chmods anything on the target: it
// stats, lists, and reads ranges of paths that already exist, so it is safe to run against a
// production-looking box. Paths that are absent are reported SKIP, not FAIL, so the run
// stays meaningful on a minimal image.
//
// Usage:
//
//	$env:AICLI_ENVD_TOKEN = '<access token>'
//	go run ./scripts/envd_seam_access_verify.go -addr https://host:49983 `
//	   -user n9e -workspace /home/n9e -ca-file C:\path\tls.crt
//
// Exits non-zero on the first FAIL, with the consequence spelled out rather than a bare
// error string.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

var (
	failed  int
	passed  int
	skipped int
)

func report(name string, ok bool, detail string) {
	switch {
	case ok:
		passed++
		fmt.Printf("PASS  %-46s %s\n", name, detail)
	case strings.HasPrefix(detail, "skip:"):
		skipped++
		fmt.Printf("SKIP  %-46s %s\n", name, strings.TrimPrefix(detail, "skip: "))
	default:
		failed++
		fmt.Printf("FAIL  %-46s %s\n", name, detail)
	}
}

func main() {
	addr := flag.String("addr", "", "envd endpoint, e.g. https://10.0.0.9:49983 (required)")
	user := flag.String("user", "", "OS user envd executes as (required)")
	workspace := flag.String("workspace", "", "absolute workspace on the target (required)")
	tokenEnv := flag.String("token-env", "AICLI_ENVD_TOKEN", "local env var holding the access token")
	caFile := flag.String("ca-file", "", "PEM bundle verifying the envd endpoint")
	link := flag.String("probe-link", "/etc/localtime", "a path expected to be a symlink on the target")
	nested := flag.String("probe-nested", "/usr/bin/python3", "a path behind possibly-linked directories")
	file := flag.String("probe-file", "/etc/hostname", "a small regular file for the range-read agreement")
	unreadable := flag.String("unreadable", "", "optional path the requesting user may not open (permission-is-not-absence probe)")
	flag.Parse()

	// Validate before touching the network: a missing flag is a usage problem, not a
	// connection failure, and it must not look like the daemon misbehaving.
	missing := []string{}
	if strings.TrimSpace(*addr) == "" {
		missing = append(missing, "-addr")
	}
	if strings.TrimSpace(*user) == "" {
		missing = append(missing, "-user")
	}
	if strings.TrimSpace(*workspace) == "" {
		missing = append(missing, "-workspace")
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "missing required flag(s): %s\n", strings.Join(missing, ", "))
		flag.Usage()
		os.Exit(2)
	}

	cfg := envdclient.Config{Addr: *addr, User: *user, Timeout: 20 * time.Second}
	if token := strings.TrimSpace(os.Getenv(*tokenEnv)); token != "" {
		cfg.AccessToken = token
	}
	if *caFile != "" {
		pem, err := os.ReadFile(*caFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read -ca-file: %v\n", err)
			os.Exit(2)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			fmt.Fprintf(os.Stderr, "-ca-file holds no usable certificate\n")
			os.Exit(2)
		}
		cfg.TLS = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		if !strings.Contains(cfg.Addr, "://") {
			cfg.Addr = "https://" + cfg.Addr
		}
	}

	client, err := envdclient.Dial(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial envd: %v\n", err)
		os.Exit(2)
	}
	access := remotetools.NewEnvdAccess(client)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// 1. The directory must not answer IsRegular. This is the single most damaging
	//    confusion the adapter could have: the core picks file-mode vs tree-mode search on
	//    info.Mode().IsRegular(), and envd sends the mode as permission bits only.
	if info, err := access.Stat(ctx, *workspace); err != nil {
		report("Stat(workspace) answers at all", false, fmt.Sprint(err))
	} else {
		report("workspace is a directory for the core too",
			info.IsDir() && info.Mode()&fs.ModeDir != 0 && !info.Mode().IsRegular(),
			fmt.Sprintf("mode=%v size=%d name=%q", info.Mode(), info.Size(), info.Name()))
	}

	// 2. Listing one level, and whether the target's order came back raw.
	entries, err := access.ReadDir(ctx, *workspace)
	if err != nil {
		report("ReadDir(workspace)", false, fmt.Sprint(err))
	} else {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		raw := strings.Join(names, ",")
		sorted := append([]string(nil), names...)
		sort.Strings(sorted)
		// A DirEntry that disagrees with its own Info() would let one core answer "is a
		// directory" two different ways in the same walk, so this is the assertion worth
		// making. Whether the target's order happens to be lexical is a fact to record, not
		// a claim to test: walkViaAccess sorts precisely because it must not depend on it.
		consistent := true
		for _, entry := range entries {
			info, infoErr := entry.Info()
			if infoErr != nil || info.IsDir() != entry.IsDir() {
				consistent = false
			}
		}
		report("listing entries are self-consistent", consistent,
			fmt.Sprintf("%d entries, order already lexical: %v; first three: %s", len(names),
				raw == strings.Join(sorted, ","), strings.Join(names[:min(3, len(names))], " | ")))
	}

	// 3. The decisive probe: Lstat reports the link, Stat follows it.
	linkInfo, linkErr := access.Lstat(ctx, *link)
	if linkErr != nil {
		report("Lstat(probe-link)", false, fmt.Sprintf("%v - the link probe is what this run is for; pass -probe-link with a symlink that exists, or fix the daemon user's read permission", linkErr))
	} else if linkInfo.Mode()&fs.ModeSymlink == 0 {
		report("Lstat keeps the final symlink visible", false,
			fmt.Sprintf("%s came back as mode=%v (not a link). If the daemon resolves the last component too, Lstat and Stat collapse: the cycle rule never fires and a remote walk descends links the local one refuses.", *link, linkInfo.Mode()))
	} else {
		followed, err := access.Stat(ctx, *link)
		ok := err == nil && followed.Mode()&fs.ModeSymlink == 0
		report("Lstat keeps the final symlink visible", ok, fmt.Sprintf("lstat mode=%v, stat mode=%v", linkInfo.Mode(), followed.Mode()))
		if resolver, isResolver := access.(toolcore.SymlinkResolver); !isResolver {
			report("accessor provides the link capability", false, "NewEnvdAccess must implement toolcore.SymlinkResolver")
		} else {
			resolved, err := resolver.EvalLinkPath(ctx, *link)
			report("EvalLinkPath yields an absolute existing target", err == nil && strings.HasPrefix(resolved, "/"),
				fmt.Sprintf("%s -> %s (%v)", *link, resolved, err))
		}
	}

	// 4. Intermediate components: the assumption is the daemon follows them, so a path
	//    behind a linked directory must still Stat cleanly.
	if _, err := access.Stat(ctx, *nested); err != nil && !toolcore.IsNotExist(err) {
		report("Stat through possibly-linked directories", false, fmt.Sprint(err))
	} else {
		report("Stat through possibly-linked directories", !toolcore.IsNotExist(err),
			func() string {
				if toolcore.IsNotExist(err) {
					return "skip: " + *nested + " is not on this image (absence, not a resolution failure)"
				}
				return *nested + " resolved"
			}())
	}

	// 5. Range reads must agree with the native whole-file read, including after a rewind.
	body, err := accessFileViaSeam(ctx, access, *file)
	if err != nil {
		report("Open+ReadAll a small file", false, fmt.Sprint(err))
	} else {
		native, nativeErr := client.ReadFile(ctx, *file, 1<<20)
		if nativeErr != nil {
			report("Open+ReadAll a small file", false, "the native channel disagreed: "+nativeErr.Error())
		} else {
			report("seam bytes == native bytes", string(body) == string(native.Data),
				fmt.Sprintf("%d bytes via seam, %d via /files", len(body), len(native.Data)))
			tail, err := accessFileFrom(ctx, access, *file, 3)
			report("rewind-and-reread reproduces the suffix",
				err == nil && string(tail) == string(native.Data[3:]), fmt.Sprint(err))
		}
	}

	// 6. Absence classifies as absence, and (when probed) permission does not.
	if _, err := access.Stat(ctx, strings.TrimRight(*workspace, "/")+"/definitely-not-here-8f3a2c"); !toolcore.IsNotExist(err) {
		report("a missing path classifies as absent", false, fmt.Sprintf("%v", err))
	} else {
		report("a missing path classifies as absent", true, err.Error())
	}
	if *unreadable != "" {
		_, err := access.Open(ctx, *unreadable)
		// The adapter is transport-only by design: it serves what the daemon serves,
		// and the target-user readability gate lives in the runtime wrapper
		// (remotetools/envd_guarded_access.go). So a raw adapter that can read this
		// path is today's expected shape, reported as information; the invariant that
		// still belongs to the adapter is "a permission problem must not be reported
		// as absence". The *wired* refusal is pinned by
		// TestEnvDGrepSkipsTheTargetUsersUnreadableFiles (offline) and by the
		// MCP-level live driver (canary A/B on the real target).
		switch {
		case err == nil:
			report("read gate is the runtime wrapper's job (raw adapter serves the daemon)", true,
				*unreadable+" is open to the daemon user; the wired runtime refuses it")
		case toolcore.IsNotExist(err):
			report("a denied path is not reported as absent", false, "absence reported for "+*unreadable)
		default:
			report("a denied path is not reported as absent", true, err.Error())
		}
	} else {
		report("a denied path is not reported as absent", true, "skip: pass -unreadable to probe it")
	}

	// 7. The honesty check: the adapter must NOT claim a filesystem id, because Entry has
	//    no device and no inode. Claiming one would let one_file_system compare invented
	//    values instead of declining.
	if _, ok := access.(toolcore.FilesystemIdentifier); ok {
		report("adapter does not fake a filesystem id", false, "it implements FilesystemIdentifier, which envd cannot answer")
	} else {
		report("adapter does not fake a filesystem id", true, "one_file_system will be declined for remote searches (design §2 Q2)")
	}

	fmt.Printf("\npassed=%d failed=%d skipped=%d\n", passed, failed, skipped)
	if failed > 0 {
		os.Exit(1)
	}
}

func accessFileViaSeam(ctx context.Context, access toolcore.Access, filePath string) ([]byte, error) {
	handle, err := access.Open(ctx, filePath)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	return io.ReadAll(handle)
}

func accessFileFrom(ctx context.Context, access toolcore.Access, filePath string, offset int64) ([]byte, error) {
	handle, err := access.Open(ctx, filePath)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	if _, err := handle.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(handle)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
