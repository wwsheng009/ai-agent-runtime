package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureCombinedOutput_ReturnsOriginalWhenWithinBudget(t *testing.T) {
	cmd := exec.Command("go", "env", "GOOS")

	capture, err := CaptureCombinedOutput(cmd, 4096)
	if err != nil {
		t.Fatalf("CaptureCombinedOutput failed: %v", err)
	}
	if capture.Truncated {
		t.Fatalf("did not expect truncation, got %+v", capture)
	}
	if strings.TrimSpace(capture.Output) == "" {
		t.Fatalf("expected non-empty output, got %+v", capture)
	}
	if strings.Contains(capture.Output, "exec output truncated at capture limit") {
		t.Fatalf("did not expect truncation marker, got %q", capture.Output)
	}
}

func TestCaptureCombinedOutput_TruncatesLargeOutputAndKeepsHeadTail(t *testing.T) {
	var cmd *exec.Cmd
	if IsWindows() {
		shell := DefaultUserShell()
		command := "$i=0; while($i -lt 600){ Write-Output (\"line-\" + $i + \"-abcdefghijklmnopqrstuvwxyz0123456789\"); $i++ }"
		args := shell.DeriveExecArgs(command, false)
		cmd = exec.Command(args[0], args[1:]...)
	} else {
		cmd = exec.Command("sh", "-c", "i=0; while [ $i -lt 600 ]; do printf 'line-%s-abcdefghijklmnopqrstuvwxyz0123456789\\n' \"$i\"; i=$((i+1)); done")
	}

	capture, err := CaptureCombinedOutput(cmd, 4096)
	if err != nil {
		t.Fatalf("CaptureCombinedOutput failed: %v", err)
	}
	if !capture.Truncated {
		t.Fatalf("expected truncation, got %+v", capture)
	}
	if !strings.Contains(capture.Output, "Total output lines: 600") {
		t.Fatalf("expected line count header, got %q", capture.Output)
	}
	if !strings.Contains(capture.Output, "exec output truncated at capture limit") {
		t.Fatalf("expected truncation marker, got %q", capture.Output)
	}
	if !strings.Contains(capture.Output, "line-0-") {
		t.Fatalf("expected head to be preserved, got %q", capture.Output)
	}
	if !strings.Contains(capture.Output, "line-599-") {
		t.Fatalf("expected tail to be preserved, got %q", capture.Output)
	}
	if capture.CaptureLimitDisabled {
		t.Fatalf("did not expect capture limit disabled, got %+v", capture)
	}
	if capture.CaptureLimitBytes != 4096 {
		t.Fatalf("expected capture limit 4096, got %+v", capture)
	}
	if capture.OmittedBytes <= 0 {
		t.Fatalf("expected omitted bytes metadata, got %+v", capture)
	}
	if capture.RetainedBytes <= 0 {
		t.Fatalf("expected retained bytes metadata, got %+v", capture)
	}
}

func TestCaptureCombinedOutput_DisableLimitPreservesFullOutput(t *testing.T) {
	var cmd *exec.Cmd
	if IsWindows() {
		shell := DefaultUserShell()
		command := "$i=0; while($i -lt 600){ Write-Output (\"line-\" + $i + \"-abcdefghijklmnopqrstuvwxyz0123456789\"); $i++ }"
		args := shell.DeriveExecArgs(command, false)
		cmd = exec.Command(args[0], args[1:]...)
	} else {
		cmd = exec.Command("sh", "-c", "i=0; while [ $i -lt 600 ]; do printf 'line-%s-abcdefghijklmnopqrstuvwxyz0123456789\\n' \"$i\"; i=$((i+1)); done")
	}

	capture, err := CaptureCombinedOutput(cmd, DisableRetainedOutputLimit)
	if err != nil {
		t.Fatalf("CaptureCombinedOutput failed: %v", err)
	}
	if capture.Truncated {
		t.Fatalf("did not expect truncation, got %+v", capture)
	}
	if !capture.CaptureLimitDisabled {
		t.Fatalf("expected capture limit disabled, got %+v", capture)
	}
	if capture.CaptureLimitBytes != 0 {
		t.Fatalf("expected capture limit bytes=0 when disabled, got %+v", capture)
	}
	if capture.TotalBytes != capture.RetainedBytes {
		t.Fatalf("expected retained bytes to equal total bytes, got %+v", capture)
	}
	if capture.OmittedBytes != 0 {
		t.Fatalf("expected omitted bytes=0, got %+v", capture)
	}
	if strings.Contains(capture.Output, "exec output truncated at capture limit") {
		t.Fatalf("did not expect truncation marker, got %q", capture.Output)
	}
	if !strings.Contains(capture.Output, "line-0-") || !strings.Contains(capture.Output, "line-599-") {
		t.Fatalf("expected full output to be preserved, got %q", capture.Output)
	}
}

func TestOutputCaptureAccumulator_UsesSameTruncationPolicy(t *testing.T) {
	accumulator := NewOutputCaptureAccumulator(32)
	chunks := [][]byte{
		[]byte("hello\n"),
		[]byte("middle-content-that-should-be-partially-omitted\n"),
		[]byte("tail\n"),
	}
	for _, chunk := range chunks {
		if _, err := accumulator.Write(chunk); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}

	capture := accumulator.Result()
	if !capture.Truncated {
		t.Fatalf("expected truncation, got %+v", capture)
	}
	if capture.CaptureLimitBytes != 32 {
		t.Fatalf("expected capture limit 32, got %+v", capture)
	}
	if capture.OmittedBytes <= 0 {
		t.Fatalf("expected omitted bytes metadata, got %+v", capture)
	}
	if !strings.Contains(capture.Output, "exec output truncated at capture limit") {
		t.Fatalf("expected truncation marker, got %q", capture.Output)
	}
	if !strings.Contains(capture.Output, "hello") || !strings.Contains(capture.Output, "tail") {
		t.Fatalf("expected head and tail to be preserved, got %q", capture.Output)
	}
}

func TestCaptureCombinedOutputWithArtifact_PersistsFullRawOutputWhenTruncated(t *testing.T) {
	artifactRoot := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestCaptureCombinedOutputWithArtifact_HelperProcess", "--")
	cmd.Env = append(os.Environ(),
		"GO_WANT_CAPTURE_ARTIFACT_HELPER=1",
		"CAPTURE_ARTIFACT_HELPER_LINES=400",
	)

	capture, artifactPath, err, artifactErr := CaptureCombinedOutputWithArtifact(cmd, 1024, "executor-test", "git diff", artifactRoot)
	if err != nil {
		t.Fatalf("CaptureCombinedOutputWithArtifact failed: %v", err)
	}
	if artifactErr != nil {
		t.Fatalf("did not expect artifact error, got %v", artifactErr)
	}
	if !capture.Truncated {
		t.Fatalf("expected truncated capture, got %+v", capture)
	}
	if strings.TrimSpace(artifactPath) == "" {
		t.Fatalf("expected artifact path, got empty")
	}
	if !strings.HasPrefix(artifactPath, filepath.Join(artifactRoot, "executor-test")) {
		t.Fatalf("expected artifact under %q, got %q", filepath.Join(artifactRoot, "executor-test"), artifactPath)
	}
	data, readErr := os.ReadFile(artifactPath)
	if readErr != nil {
		t.Fatalf("read artifact: %v", readErr)
	}
	content := string(data)
	if strings.Contains(content, "exec output truncated at capture limit") {
		t.Fatalf("artifact should keep full raw output, got %q", content)
	}
	if !strings.Contains(content, "line-0-") || !strings.Contains(content, "line-399-") {
		t.Fatalf("expected full raw output in artifact, got %q", content)
	}
}

func TestCaptureCombinedOutputWithArtifact_DropsArtifactWhenNotTruncated(t *testing.T) {
	artifactRoot := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestCaptureCombinedOutputWithArtifact_HelperProcess", "--")
	cmd.Env = append(os.Environ(),
		"GO_WANT_CAPTURE_ARTIFACT_HELPER=1",
		"CAPTURE_ARTIFACT_HELPER_LINES=2",
	)

	capture, artifactPath, err, artifactErr := CaptureCombinedOutputWithArtifact(cmd, 4096, "executor-test", "git status", artifactRoot)
	if err != nil {
		t.Fatalf("CaptureCombinedOutputWithArtifact failed: %v", err)
	}
	if artifactErr != nil {
		t.Fatalf("did not expect artifact error, got %v", artifactErr)
	}
	if capture.Truncated {
		t.Fatalf("did not expect truncation, got %+v", capture)
	}
	if artifactPath != "" {
		t.Fatalf("expected artifact path to be dropped when not truncated, got %q", artifactPath)
	}
}

func TestPersistShellOutputArtifact_WritesFullContent(t *testing.T) {
	artifactRoot := t.TempDir()
	content := strings.Repeat("diff-line-abcdefghijklmnopqrstuvwxyz0123456789\n", 400)

	artifactPath, err := PersistShellOutputArtifact("executor-test", "git diff", artifactRoot, content)
	if err != nil {
		t.Fatalf("PersistShellOutputArtifact failed: %v", err)
	}
	if strings.TrimSpace(artifactPath) == "" {
		t.Fatal("expected artifact path")
	}
	data, readErr := os.ReadFile(artifactPath)
	if readErr != nil {
		t.Fatalf("read artifact: %v", readErr)
	}
	if string(data) != content {
		t.Fatalf("expected full content to be preserved")
	}
}

func TestCaptureCombinedOutputWithArtifact_HelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CAPTURE_ARTIFACT_HELPER") != "1" {
		return
	}
	lines := 10
	if raw := strings.TrimSpace(os.Getenv("CAPTURE_ARTIFACT_HELPER_LINES")); raw != "" {
		if _, scanErr := fmt.Sscanf(raw, "%d", &lines); scanErr != nil {
			lines = 10
		}
	}
	for i := 0; i < lines; i++ {
		fmt.Printf("line-%d-abcdefghijklmnopqrstuvwxyz0123456789\n", i)
	}
	os.Exit(0)
}
