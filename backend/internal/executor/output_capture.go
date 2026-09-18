package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultRetainedOutputBytes = 256 * 1024
	DisableRetainedOutputLimit = -1

	captureOutputMarkerReserve  = 192
	captureOutputMinSegmentSize = 4 * 1024
)

type CombinedOutputCapture struct {
	Output               string
	Truncated            bool
	TotalBytes           int
	TotalLines           int
	RetainedBytes        int
	OmittedBytes         int
	CaptureLimitBytes    int
	CaptureLimitDisabled bool
}

type outputMirrorContextKey struct{}

// WithOutputMirror attaches a best-effort live output mirror to command
// execution contexts. The normal retained capture is still preserved.
func WithOutputMirror(ctx context.Context, writer io.Writer) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if writer == nil {
		return ctx
	}
	return context.WithValue(ctx, outputMirrorContextKey{}, writer)
}

// OutputMirrorFromContext resolves a live command output mirror, if configured.
func OutputMirrorFromContext(ctx context.Context) io.Writer {
	if ctx == nil {
		return nil
	}
	writer, _ := ctx.Value(outputMirrorContextKey{}).(io.Writer)
	return writer
}

// OutputCaptureAccumulator incrementally captures combined command output using
// the same truncation policy as CaptureCombinedOutput.
type OutputCaptureAccumulator struct {
	writer combinedOutputWriter
}

// NewOutputCaptureAccumulator creates an incremental combined-output capture buffer.
// Pass DisableRetainedOutputLimit to preserve the full output without truncation.
func NewOutputCaptureAccumulator(maxBytes int) *OutputCaptureAccumulator {
	return &OutputCaptureAccumulator{writer: newCombinedOutputWriter(maxBytes)}
}

func CaptureCombinedOutput(cmd *exec.Cmd, maxBytes int) (CombinedOutputCapture, error) {
	return CaptureCombinedOutputWithMirror(cmd, maxBytes, nil)
}

func CaptureCombinedOutputWithMirror(cmd *exec.Cmd, maxBytes int, mirror io.Writer) (CombinedOutputCapture, error) {
	writer := newCombinedOutputWriter(maxBytes)
	err := runCommandCapture(context.Background(), cmd, nil, commandOutputWriter(writer, mirror), mirror, "", 0, nil)
	return writer.Result(), err
}

// CaptureCombinedOutputWithArtifact captures combined stdout/stderr while also teeing
// the full raw output to an artifact file. The artifact file is kept only when the
// retained output window was truncated and the artifact write completed successfully.
// It returns the retained capture, the kept artifact path when available, the command
// execution error, and a best-effort artifact error that does not fail the command.
func CaptureCombinedOutputWithArtifact(cmd *exec.Cmd, maxBytes int, scope string, command string, preferredRoot string) (CombinedOutputCapture, string, error, error) {
	return CaptureCombinedOutputWithArtifactAndMirror(cmd, maxBytes, scope, command, preferredRoot, nil)
}

// CaptureCombinedOutputWithArtifactAndMirror captures command output for model
// history/artifacts while also teeing raw chunks to a live output mirror.
func CaptureCombinedOutputWithArtifactAndMirror(cmd *exec.Cmd, maxBytes int, scope string, command string, preferredRoot string, mirror io.Writer) (CombinedOutputCapture, string, error, error) {
	return captureCombinedOutput(context.Background(), cmd, nil, maxBytes, scope, command, preferredRoot, mirror, 0)
}

// GuardedCaptureOptions configures a guarded, artifact-aware command capture.
type GuardedCaptureOptions struct {
	MaxBytes      int
	Scope         string
	Command       string
	PreferredRoot string
	Mirror        io.Writer
	// QuietNotice emits a diagnostic line into the captured output when the
	// command produces no output for this long. Zero resolves the default from
	// AICLI_SHELL_QUIET_NOTICE_TIMEOUT; negative disables the notice.
	QuietNotice time.Duration
}

// CaptureCombinedOutputGuarded runs cmd under a ProcessGuard: the started
// process is attached to the guard's process tree, context cancellation
// terminates the whole tree, and WaitDelay bounds I/O that descendants keep
// open after the shell exited. It is the bounded counterpart of
// CaptureCombinedOutputWithArtifactAndMirror.
func CaptureCombinedOutputGuarded(ctx context.Context, cmd *exec.Cmd, guard *ProcessGuard, opts GuardedCaptureOptions) (CombinedOutputCapture, string, error, error) {
	notice := opts.QuietNotice
	if notice == 0 {
		notice = ResolveQuietNoticeTimeout()
	} else if notice < 0 {
		notice = 0
	}
	return captureCombinedOutput(ctx, cmd, guard, opts.MaxBytes, opts.Scope, opts.Command, opts.PreferredRoot, opts.Mirror, notice)
}

func captureCombinedOutput(ctx context.Context, cmd *exec.Cmd, guard *ProcessGuard, maxBytes int, scope string, command string, preferredRoot string, mirror io.Writer, quietNotice time.Duration) (CombinedOutputCapture, string, error, error) {
	if maxBytes == DisableRetainedOutputLimit {
		writer := newCombinedOutputWriter(maxBytes)
		err := runCommandCapture(ctx, cmd, guard, commandOutputWriter(writer, mirror), mirror, command, quietNotice, guardPIDFunc(guard))
		return writer.Result(), "", err, nil
	}

	path, artifactFile, artifactOpenErr := openShellOutputArtifactFile(scope, command, preferredRoot)
	if artifactOpenErr != nil || artifactFile == nil {
		writer := newCombinedOutputWriter(maxBytes)
		err := runCommandCapture(ctx, cmd, guard, commandOutputWriter(writer, mirror), mirror, command, quietNotice, guardPIDFunc(guard))
		return writer.Result(), "", err, artifactOpenErr
	}

	writer := newArtifactTeeCombinedOutputWriter(maxBytes, artifactFile)
	runErr := runCommandCapture(ctx, cmd, guard, commandOutputWriter(writer, mirror), mirror, command, quietNotice, guardPIDFunc(guard))
	capture := writer.Result()
	artifactErr := writer.ArtifactError()
	if closeErr := artifactFile.Close(); closeErr != nil && artifactErr == nil {
		artifactErr = closeErr
	}
	if artifactErr != nil || !capture.Truncated {
		_ = removeShellOutputArtifactFile(path)
		return capture, "", runErr, artifactErr
	}
	return capture, path, runErr, nil
}

func commandOutputWriter(primary combinedOutputWriter, mirror io.Writer) io.Writer {
	if mirror == nil {
		return primary
	}
	return newMirrorCombinedOutputWriter(primary, mirror)
}

func guardPIDFunc(guard *ProcessGuard) func() int {
	if guard == nil {
		return nil
	}
	return guard.PID
}

// runCommandCapture runs cmd with optional process-tree guarding. Without a
// guard it degrades to the historical cmd.Run() path; with a guard it starts
// the process, attaches it to the platform tree handle, terminates the tree on
// context cancellation, bounds post-exit I/O via WaitDelay and reports quiet
// periods while the command is still running.
func runCommandCapture(ctx context.Context, cmd *exec.Cmd, guard *ProcessGuard, outputWriter io.Writer, mirror io.Writer, command string, quietNotice time.Duration, pid func() int) error {
	if cmd == nil {
		return fmt.Errorf("executor: nil command")
	}
	if guard == nil {
		cmd.Stdout = outputWriter
		cmd.Stderr = outputWriter
		err := cmd.Run()
		flushOutputMirror(mirror)
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	activity := newCommandActivity(outputWriter)
	cmd.Stdout = activity
	cmd.Stderr = activity
	if err := cmd.Start(); err != nil {
		guard.Close()
		flushOutputMirror(mirror)
		return err
	}
	if err := guard.Attach(cmd.Process); err != nil {
		guard.NoteAttachError(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			guard.Terminate()
		case <-stop:
		}
	}()
	if quietNotice > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			watchCommandQuiet(activity, outputWriter, command, quietNotice, pid, stop)
		}()
	}
	runErr := cmd.Wait()
	close(stop)
	wg.Wait()
	guard.Close()
	flushOutputMirror(mirror)
	return runErr
}

const (
	quietNoticeEnv     = "AICLI_SHELL_QUIET_NOTICE_TIMEOUT"
	quietNoticeDefault = 2 * time.Minute
	quietCheckInterval = 5 * time.Second
)

// ResolveQuietNoticeTimeout returns how long a running command may stay silent
// before the runtime appends a stall notice to its output. Zero disables it.
func ResolveQuietNoticeTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(quietNoticeEnv)); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			if parsed <= 0 {
				return 0
			}
			return parsed
		}
	}
	return quietNoticeDefault
}

type commandActivity struct {
	inner          io.Writer
	startedAt      time.Time
	lastWriteNanos int64
	totalBytes     int64
}

func newCommandActivity(inner io.Writer) *commandActivity {
	now := time.Now()
	return &commandActivity{inner: inner, startedAt: now, lastWriteNanos: now.UnixNano()}
}

func (w *commandActivity) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	if n > 0 {
		atomic.StoreInt64(&w.lastWriteNanos, time.Now().UnixNano())
		atomic.AddInt64(&w.totalBytes, int64(n))
	}
	return n, err
}

func (w *commandActivity) quietFor(now time.Time) time.Duration {
	last := atomic.LoadInt64(&w.lastWriteNanos)
	if last <= 0 {
		return 0
	}
	return now.Sub(time.Unix(0, last))
}

func (w *commandActivity) elapsed(now time.Time) time.Duration {
	return now.Sub(w.startedAt)
}

// watchCommandQuiet appends a runtime notice to the captured output when the
// command stops producing output but keeps running, so the model/UI can tell a
// silent hang apart from legitimately slow work.
func watchCommandQuiet(activity *commandActivity, sink io.Writer, command string, quiet time.Duration, pid func() int, stop <-chan struct{}) {
	if activity == nil || sink == nil || quiet <= 0 {
		return
	}
	interval := quietCheckInterval
	if quiet < interval {
		interval = quiet
	}
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastNotice time.Time
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			idle := activity.quietFor(now)
			if idle < quiet {
				continue
			}
			if !lastNotice.IsZero() && now.Sub(lastNotice) < quiet {
				continue
			}
			lastNotice = now
			pidText := ""
			if pid != nil {
				if value := pid(); value > 0 {
					pidText = fmt.Sprintf(", pid=%d", value)
				}
			}
			commandText := strings.Join(strings.Fields(command), " ")
			if len(commandText) > 96 {
				commandText = commandText[:96] + "..."
			}
			notice := fmt.Sprintf(
				"\n[runtime] shell command has been quiet for %s (no output%s, elapsed %s) and is still running: %s\n[runtime] Press Esc to interrupt; long-running or daemon-style commands should use the background task tool instead.\n",
				idle.Round(time.Second),
				pidText,
				activity.elapsed(now).Round(time.Second),
				commandText,
			)
			_, _ = io.WriteString(sink, notice)
		}
	}
}

// Write appends a new chunk of command output to the accumulator.
func (a *OutputCaptureAccumulator) Write(p []byte) (int, error) {
	if a == nil {
		return len(p), nil
	}
	return a.writer.Write(p)
}

// Result returns the aggregated capture state accumulated so far.
func (a *OutputCaptureAccumulator) Result() CombinedOutputCapture {
	if a == nil {
		return CombinedOutputCapture{}
	}
	return a.writer.Result()
}

type combinedOutputWriter interface {
	io.Writer
	Result() CombinedOutputCapture
}

type artifactAwareCombinedOutputWriter interface {
	combinedOutputWriter
	ArtifactError() error
}

func newCombinedOutputWriter(maxBytes int) combinedOutputWriter {
	if maxBytes == DisableRetainedOutputLimit {
		return newFullCombinedWriter()
	}
	if maxBytes <= 0 {
		maxBytes = DefaultRetainedOutputBytes
	}
	return newCappedCombinedWriter(maxBytes)
}

type cappedCombinedWriter struct {
	mu        sync.Mutex
	maxBytes  int
	headLimit int
	tailLimit int

	head []byte
	tail []byte

	totalBytes      int
	newlineCount    int
	endsWithNewline bool
	wroteAny        bool
}

type fullCombinedWriter struct {
	mu sync.Mutex

	buf bytes.Buffer

	totalBytes      int
	newlineCount    int
	endsWithNewline bool
	wroteAny        bool
}

type artifactTeeCombinedOutputWriter struct {
	mu sync.Mutex

	primary  combinedOutputWriter
	artifact io.Writer

	artifactErr error
}

type mirrorCombinedOutputWriter struct {
	mu sync.Mutex

	primary io.Writer
	mirror  io.Writer
}

func newCappedCombinedWriter(maxBytes int) *cappedCombinedWriter {
	if maxBytes <= 0 {
		maxBytes = DefaultRetainedOutputBytes
	}
	headLimit := maxBytes * 2 / 3
	tailLimit := maxBytes - headLimit
	if maxBytes >= captureOutputMinSegmentSize*2 {
		if headLimit < captureOutputMinSegmentSize {
			headLimit = captureOutputMinSegmentSize
			tailLimit = maxBytes - headLimit
		}
		if tailLimit < captureOutputMinSegmentSize {
			tailLimit = captureOutputMinSegmentSize
			headLimit = maxBytes - tailLimit
		}
	} else {
		headLimit = maxBytes / 2
		tailLimit = maxBytes - headLimit
	}
	return &cappedCombinedWriter{
		maxBytes:  maxBytes,
		headLimit: headLimit,
		tailLimit: tailLimit,
	}
}

func newFullCombinedWriter() *fullCombinedWriter {
	return &fullCombinedWriter{}
}

func newArtifactTeeCombinedOutputWriter(maxBytes int, artifact io.Writer) *artifactTeeCombinedOutputWriter {
	return &artifactTeeCombinedOutputWriter{
		primary:  newCombinedOutputWriter(maxBytes),
		artifact: artifact,
	}
}

func newMirrorCombinedOutputWriter(primary io.Writer, mirror io.Writer) *mirrorCombinedOutputWriter {
	return &mirrorCombinedOutputWriter{
		primary: primary,
		mirror:  mirror,
	}
}

func (w *cappedCombinedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(p) == 0 {
		return 0, nil
	}

	if !w.wroteAny {
		w.wroteAny = true
	}
	w.totalBytes += len(p)
	w.newlineCount += bytes.Count(p, []byte{'\n'})
	w.endsWithNewline = len(p) > 0 && p[len(p)-1] == '\n'

	w.appendHeadTail(p)
	return len(p), nil
}

func (w *fullCombinedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(p) == 0 {
		return 0, nil
	}

	if !w.wroteAny {
		w.wroteAny = true
	}
	w.totalBytes += len(p)
	w.newlineCount += bytes.Count(p, []byte{'\n'})
	w.endsWithNewline = len(p) > 0 && p[len(p)-1] == '\n'

	return w.buf.Write(p)
}

func (w *artifactTeeCombinedOutputWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := w.primary.Write(p)
	if err != nil {
		return n, err
	}
	if w.artifact != nil && w.artifactErr == nil && len(p) > 0 {
		if _, artifactErr := w.artifact.Write(p); artifactErr != nil {
			w.artifactErr = artifactErr
		}
	}
	return len(p), nil
}

func (w *mirrorCombinedOutputWriter) Write(p []byte) (int, error) {
	if w == nil || w.primary == nil {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err := w.primary.Write(p)
	if err != nil {
		return n, err
	}
	if w.mirror != nil && len(p) > 0 {
		_, _ = w.mirror.Write(p)
	}
	return len(p), nil
}

func flushOutputMirror(writer io.Writer) {
	if writer == nil {
		return
	}
	if flusher, ok := writer.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
}

func (w *cappedCombinedWriter) appendHeadTail(p []byte) {
	if w.headLimit > len(w.head) {
		headRoom := w.headLimit - len(w.head)
		if headRoom > len(p) {
			headRoom = len(p)
		}
		w.head = append(w.head, p[:headRoom]...)
		p = p[headRoom:]
	}

	if len(p) == 0 || w.tailLimit <= 0 {
		return
	}
	if len(p) >= w.tailLimit {
		w.tail = append([]byte(nil), p[len(p)-w.tailLimit:]...)
		return
	}
	if len(w.tail)+len(p) <= w.tailLimit {
		w.tail = append(w.tail, p...)
		return
	}

	overflow := len(w.tail) + len(p) - w.tailLimit
	if overflow >= len(w.tail) {
		w.tail = append([]byte(nil), p[len(p)-w.tailLimit:]...)
		return
	}
	next := make([]byte, 0, w.tailLimit)
	next = append(next, w.tail[overflow:]...)
	next = append(next, p...)
	w.tail = next
}

func (w *cappedCombinedWriter) Result() CombinedOutputCapture {
	w.mu.Lock()
	defer w.mu.Unlock()

	output := string(w.head) + string(w.tail)
	truncated := w.totalBytes > len(output)
	totalLines := 0
	if w.totalBytes > 0 {
		totalLines = w.newlineCount
		if !w.endsWithNewline {
			totalLines++
		}
	}
	if truncated {
		omitted := w.totalBytes - len(w.head) - len(w.tail)
		if omitted < 0 {
			omitted = 0
		}
		output = fmt.Sprintf(
			"Total output lines: %d\nTotal output bytes: %d\n\n%s\n\n[exec output truncated at capture limit: omitted %d bytes from the middle]\n\n%s",
			totalLines,
			w.totalBytes,
			string(w.head),
			omitted,
			string(w.tail),
		)
	}

	return CombinedOutputCapture{
		Output:            output,
		Truncated:         truncated,
		TotalBytes:        w.totalBytes,
		TotalLines:        totalLines,
		RetainedBytes:     len(w.head) + len(w.tail),
		OmittedBytes:      omittedBytes(w.totalBytes, len(w.head), len(w.tail)),
		CaptureLimitBytes: w.maxBytes,
	}
}

func (w *fullCombinedWriter) Result() CombinedOutputCapture {
	w.mu.Lock()
	defer w.mu.Unlock()

	totalLines := 0
	if w.totalBytes > 0 {
		totalLines = w.newlineCount
		if !w.endsWithNewline {
			totalLines++
		}
	}

	return CombinedOutputCapture{
		Output:               w.buf.String(),
		Truncated:            false,
		TotalBytes:           w.totalBytes,
		TotalLines:           totalLines,
		RetainedBytes:        w.buf.Len(),
		CaptureLimitDisabled: true,
	}
}

func (w *artifactTeeCombinedOutputWriter) Result() CombinedOutputCapture {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.primary.Result()
}

func (w *artifactTeeCombinedOutputWriter) ArtifactError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.artifactErr
}

func omittedBytes(totalBytes, headBytes, tailBytes int) int {
	omitted := totalBytes - headBytes - tailBytes
	if omitted < 0 {
		return 0
	}
	return omitted
}

func openShellOutputArtifactFile(scope string, command string, preferredRoot string) (string, *os.File, error) {
	dir := resolveShellOutputArtifactDir(scope, preferredRoot)
	if strings.TrimSpace(dir) == "" {
		return "", nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create shell output artifact dir: %w", err)
	}
	pattern := fmt.Sprintf("%s_*.txt", shellOutputArtifactLabel(command))
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", nil, fmt.Errorf("create shell output artifact file: %w", err)
	}
	return file.Name(), file, nil
}

func removeShellOutputArtifactFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// PersistShellOutputArtifact writes the provided full raw output into an artifact file and
// returns the kept artifact path. Empty content is ignored. Failures clean up any partially
// written file and are returned without mutating the original command result.
func PersistShellOutputArtifact(scope string, command string, preferredRoot string, content string) (string, error) {
	if content == "" {
		return "", nil
	}
	path, artifactFile, err := openShellOutputArtifactFile(scope, command, preferredRoot)
	if err != nil {
		return "", err
	}
	if artifactFile == nil {
		return "", nil
	}
	if _, writeErr := artifactFile.WriteString(content); writeErr != nil {
		_ = artifactFile.Close()
		_ = removeShellOutputArtifactFile(path)
		return "", writeErr
	}
	if closeErr := artifactFile.Close(); closeErr != nil {
		_ = removeShellOutputArtifactFile(path)
		return "", closeErr
	}
	return path, nil
}

func resolveShellOutputArtifactDir(scope string, preferredRoot string) string {
	root := strings.TrimSpace(os.Getenv("AICLI_SHELL_OUTPUT_ARTIFACT_DIR"))
	if root == "" {
		root = strings.TrimSpace(preferredRoot)
	}
	if root == "" {
		root = filepath.Join(os.TempDir(), "ai-agent-runtime", "shell-output")
	}
	if !filepath.IsAbs(root) {
		if absRoot, err := filepath.Abs(root); err == nil {
			root = absRoot
		}
	}
	scope = shellOutputArtifactLabel(scope)
	if scope == "" {
		return root
	}
	return filepath.Join(root, scope)
}

func shellOutputArtifactLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "command"
	}
	fields := strings.Fields(value)
	label := value
	if len(fields) > 0 {
		label = fields[0]
	}
	label = strings.ToLower(strings.TrimSpace(label))
	replacer := strings.NewReplacer(
		"<", "_",
		">", "_",
		":", "_",
		"\"", "_",
		"/", "_",
		"\\", "_",
		"|", "_",
		"?", "_",
		"*", "_",
		" ", "_",
	)
	label = replacer.Replace(label)
	label = strings.Trim(label, "._-")
	if label == "" {
		label = "command"
	}
	if len(label) > 48 {
		label = label[:48]
	}
	return label
}
