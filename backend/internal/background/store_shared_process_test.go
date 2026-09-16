package background

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件用真实的多进程场景验证共享 background.sqlite 的并发基线：
//
//  1. 一个进程持续写入时，另一个进程的读不阻塞、不报错（WAL 多读单写）；
//  2. 另一进程持有长写锁时，读立即返回，写以 busy_timeout + 锁重试有界完成，
//     而不是挂在驱动默认的 60s 锁等待上（历史 /api/runtime/health 长挂的根因）；
//  3. 两个写进程并发提交时，两边作业都必须完整落库、可被重启后的新进程读到。
//
// 子进程复用测试二进制（-test.run 指定入口），不依赖外部脚本。

const (
	sharedHelperModeEnv = "AI_RUNTIME_BACKGROUND_SHARED_HELPER_MODE"
	sharedHelperPathEnv = "AI_RUNTIME_BACKGROUND_SHARED_HELPER_PATH"
	sharedHelperHoldEnv = "AI_RUNTIME_BACKGROUND_SHARED_HELPER_HOLD"
)

// TestBackgroundSharedStoreHelperProcess 是子进程入口：只有被本文件的测试以
// 子进程方式拉起（带 sharedHelperModeEnv）时才真正执行，否则跳过。
func TestBackgroundSharedStoreHelperProcess(t *testing.T) {
	mode := strings.TrimSpace(os.Getenv(sharedHelperModeEnv))
	if mode == "" {
		t.Skip("helper entry point; enabled only as a subprocess")
	}
	path := strings.TrimSpace(os.Getenv(sharedHelperPathEnv))
	if path == "" {
		t.Fatalf("helper store path missing")
	}
	hold, err := time.ParseDuration(strings.TrimSpace(os.Getenv(sharedHelperHoldEnv)))
	if err != nil || hold <= 0 {
		hold = 2500 * time.Millisecond
	}
	switch mode {
	case "writer":
		runBackgroundSharedWriterHelper(t, path, hold)
	case "locker":
		runBackgroundSharedLockerHelper(t, path, hold)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func runBackgroundSharedWriterHelper(t *testing.T, path string, hold time.Duration) {
	t.Helper()
	store, err := NewSQLiteStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("open helper store: %v", err)
	}
	defer store.Close()

	deadline := time.Now().Add(hold)
	written := 0
	for time.Now().Before(deadline) {
		id := fmt.Sprintf("child-job-%04d", written)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := store.SaveJob(ctx, Job{
			ID:        id,
			SessionID: "shared-child-session",
			Kind:      "shell",
			Command:   "echo child",
			Status:    StatusRunning,
			Message:   "helper write",
			CreatedAt: time.Now().UTC(),
		})
		cancel()
		if err != nil {
			t.Fatalf("helper write %s: %v", id, err)
		}
		written++
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Printf("child-writes=%d\n", written)
}

func runBackgroundSharedLockerHelper(t *testing.T, path string, hold time.Duration) {
	t.Helper()
	store, err := NewSQLiteStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("open helper store: %v", err)
	}
	defer store.Close()
	if err := store.ensure(); err != nil {
		t.Fatalf("open helper database: %v", err)
	}
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire helper connection: %v", err)
	}
	defer conn.Close()

	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		t.Fatalf("set helper busy_timeout: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "UPDATE background_jobs SET message = message WHERE id = 'helper-missing-job'"); err != nil {
		t.Fatalf("take write lock: %v", err)
	}
	fmt.Println("locked")
	time.Sleep(hold)
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("commit helper transaction: %v", err)
	}
}

// TestBackgroundStoreSharedReadWhileWriterProcessRuns 验证：另一进程持续写入时，
// 本进程的读既不阻塞也不失败，并且能看到对方已提交的行。
func TestBackgroundStoreSharedReadWhileWriterProcessRuns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime", "background.sqlite")
	child := startBackgroundSharedHelper(t, path, "writer", 2500*time.Millisecond)

	store, err := NewSQLiteStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("open reader store: %v", err)
	}
	defer store.Close()

	deadline := time.Now().Add(4 * time.Second)
	reads := 0
	visible := 0
	var maxLatency time.Duration
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		start := time.Now()
		jobs, err := store.ListJobs(ctx, JobFilter{SessionID: "shared-child-session", Limit: 500})
		latency := time.Since(start)
		cancel()
		if err != nil {
			t.Fatalf("read failed while writer process runs: %v (latency=%s)", err, latency)
		}
		if latency > maxLatency {
			maxLatency = latency
		}
		if len(jobs) > visible {
			visible = len(jobs)
		}
		reads++
		time.Sleep(20 * time.Millisecond)
	}

	output := child.wait(t)
	if reads < 20 {
		t.Fatalf("expected the reader to keep polling, got %d reads", reads)
	}
	if maxLatency > time.Second {
		t.Fatalf("read latency under concurrent writer is too high: %s", maxLatency)
	}
	if visible == 0 {
		t.Fatalf("reader never observed committed writes from the writer process")
	}

	written := parseChildWriteCount(t, output)
	if written < 10 {
		t.Fatalf("writer process reported too few writes: %d", written)
	}
	jobs, err := store.ListJobs(context.Background(), JobFilter{SessionID: "shared-child-session", Limit: 1000})
	if err != nil {
		t.Fatalf("final list: %v", err)
	}
	if len(jobs) != written {
		t.Fatalf("expected %d persisted rows from the writer process, got %d", written, len(jobs))
	}
}

// TestBackgroundStoreReadNotBlockedByForeignWriteLock 验证：另一进程持有长写锁时，
// 读立即返回；写的等待被 busy_timeout + 锁重试限定且有界成功，不会挂在默认 60s 锁等待。
func TestBackgroundStoreReadNotBlockedByForeignWriteLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime", "background.sqlite")

	seed, err := NewSQLiteStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	if err := seed.SaveJob(context.Background(), Job{
		ID:        "seed-job",
		SessionID: "shared-seed-session",
		Kind:      "shell",
		Status:    StatusCompleted,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	child := startBackgroundSharedHelper(t, path, "locker", 2500*time.Millisecond)
	child.waitForOutput(t, "locked", 10*time.Second)

	store, err := NewSQLiteStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	readCtx, cancelRead := context.WithTimeout(context.Background(), time.Second)
	readStart := time.Now()
	if _, err := store.GetJob(readCtx, "seed-job"); err != nil {
		cancelRead()
		t.Fatalf("read was blocked by a foreign write lock: %v (elapsed=%s)", err, time.Since(readStart))
	}
	cancelRead()
	if elapsed := time.Since(readStart); elapsed > 500*time.Millisecond {
		t.Fatalf("read waited on the foreign write lock too long: %s", elapsed)
	}

	// 读不阻塞的前提是文件处于 WAL（多读单写）；这里直接核对基线已落到真实文件上。
	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if !strings.EqualFold(strings.TrimSpace(journalMode), "wal") {
		t.Fatalf("shared background store must run in WAL for concurrent readers, got %q", journalMode)
	}

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 30*time.Second)
	writeStart := time.Now()
	err = store.SaveJob(writeCtx, Job{
		ID:        "parent-job-after-lock",
		SessionID: "shared-parent-session",
		Kind:      "shell",
		Status:    StatusRunning,
		CreatedAt: time.Now().UTC(),
	})
	cancelWrite()
	elapsed := time.Since(writeStart)
	if err != nil {
		t.Fatalf("write should be retried until the foreign lock is released, got: %v", err)
	}
	if elapsed < 1500*time.Millisecond {
		t.Fatalf("write finished before the foreign lock was released (elapsed=%s)", elapsed)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("write waited too long behind the foreign lock: %s", elapsed)
	}

	child.wait(t)
	if _, err := store.GetJob(context.Background(), "parent-job-after-lock"); err != nil {
		t.Fatalf("read back own write: %v", err)
	}
}

// TestBackgroundStoreConcurrentWritersPersistAllRows 验证：两个写进程并发提交时，
// 双方作业都完整落库，重启后的新进程（新 store 实例）能读到全部行。
func TestBackgroundStoreConcurrentWritersPersistAllRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime", "background.sqlite")
	child := startBackgroundSharedHelper(t, path, "writer", 1500*time.Millisecond)

	parent, err := NewSQLiteStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("open parent store: %v", err)
	}
	const parentWrites = 8
	for i := 0; i < parentWrites; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := parent.SaveJob(ctx, Job{
			ID:        fmt.Sprintf("parent-job-%04d", i),
			SessionID: "shared-parent-session",
			Kind:      "shell",
			Command:   "echo parent",
			Status:    StatusRunning,
			CreatedAt: time.Now().UTC(),
		})
		cancel()
		if err != nil {
			t.Fatalf("parent write %d failed while another process writes: %v", i, err)
		}
	}
	if err := parent.Close(); err != nil {
		t.Fatalf("close parent store: %v", err)
	}

	written := parseChildWriteCount(t, child.wait(t))

	reopened, err := NewSQLiteStore(&StoreConfig{Path: path})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	parentJobs, err := reopened.ListJobs(context.Background(), JobFilter{SessionID: "shared-parent-session", Limit: 100})
	if err != nil {
		t.Fatalf("list parent jobs: %v", err)
	}
	childJobs, err := reopened.ListJobs(context.Background(), JobFilter{SessionID: "shared-child-session", Limit: 1000})
	if err != nil {
		t.Fatalf("list child jobs: %v", err)
	}
	if len(parentJobs) != parentWrites {
		t.Fatalf("expected %d parent rows after reopen, got %d", parentWrites, len(parentJobs))
	}
	if len(childJobs) != written {
		t.Fatalf("expected %d child rows after reopen, got %d", written, len(childJobs))
	}
}

type backgroundSharedHelper struct {
	cmd       *exec.Cmd
	lines     chan string
	done      chan error
	stderr    *bytes.Buffer
	mu        sync.Mutex
	collected []string
}

func startBackgroundSharedHelper(t *testing.T, path, mode string, hold time.Duration) *backgroundSharedHelper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestBackgroundSharedStoreHelperProcess$")
	cmd.Env = append(os.Environ(),
		sharedHelperModeEnv+"="+mode,
		sharedHelperPathEnv+"="+path,
		sharedHelperHoldEnv+"="+hold.String(),
	)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe %s helper process stdout: %v", mode, err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s helper process: %v", mode, err)
	}
	helper := &backgroundSharedHelper{
		cmd:    cmd,
		lines:  make(chan string, 128),
		done:   make(chan error, 1),
		stderr: stderr,
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			helper.mu.Lock()
			helper.collected = append(helper.collected, line)
			helper.mu.Unlock()
			helper.lines <- line
		}
		close(helper.lines)
		helper.done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return helper
}

func (h *backgroundSharedHelper) waitForOutput(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-h.lines:
			if !ok {
				t.Fatalf("helper process exited before reporting %q (stdout=%q stderr=%q)", want, h.output(), h.stderr.String())
			}
			if strings.Contains(line, want) {
				return
			}
		case <-deadline:
			t.Fatalf("helper process did not report %q within %s (stdout=%q stderr=%q)", want, timeout, h.output(), h.stderr.String())
		}
	}
}

func (h *backgroundSharedHelper) wait(t *testing.T) string {
	t.Helper()
	deadline := time.After(60 * time.Second)
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatalf("helper process failed: %v (stdout=%q stderr=%q)", err, h.output(), h.stderr.String())
		}
		return h.output()
	case <-deadline:
		t.Fatalf("helper process did not finish in time (stdout=%q stderr=%q)", h.output(), h.stderr.String())
	}
	return ""
}

func (h *backgroundSharedHelper) output() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.collected, "\n") + "\n"
}

func parseChildWriteCount(t *testing.T, output string) int {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "child-writes=") {
			continue
		}
		value, err := strconv.Atoi(strings.TrimPrefix(line, "child-writes="))
		if err != nil {
			t.Fatalf("parse child write count from %q: %v", line, err)
		}
		return value
	}
	t.Fatalf("helper process did not report child-writes count (output=%q)", output)
	return 0
}
