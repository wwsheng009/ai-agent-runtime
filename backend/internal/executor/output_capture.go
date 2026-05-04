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
	outputWriter := io.Writer(writer)
	if mirror != nil {
		outputWriter = newMirrorCombinedOutputWriter(writer, mirror)
	}
	cmd.Stdout = outputWriter
	cmd.Stderr = outputWriter

	err := cmd.Run()
	flushOutputMirror(mirror)
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
	if maxBytes == DisableRetainedOutputLimit {
		capture, err := CaptureCombinedOutputWithMirror(cmd, maxBytes, mirror)
		return capture, "", err, nil
	}

	path, artifactFile, artifactOpenErr := openShellOutputArtifactFile(scope, command, preferredRoot)
	if artifactOpenErr != nil || artifactFile == nil {
		capture, err := CaptureCombinedOutputWithMirror(cmd, maxBytes, mirror)
		return capture, "", err, artifactOpenErr
	}

	writer := newArtifactTeeCombinedOutputWriter(maxBytes, artifactFile)
	outputWriter := io.Writer(writer)
	if mirror != nil {
		outputWriter = newMirrorCombinedOutputWriter(writer, mirror)
	}
	cmd.Stdout = outputWriter
	cmd.Stderr = outputWriter
	runErr := cmd.Run()
	flushOutputMirror(mirror)
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
