package consolehost

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestExtractFlag(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantEnabled bool
		wantArgs    []string
		wantErr     bool
	}{
		{
			name:     "absent",
			args:     []string{"chat", "--compat-mode"},
			wantArgs: []string{"chat", "--compat-mode"},
		},
		{
			name:        "bare flag",
			args:        []string{"chat", "--console-host", "--compat-mode"},
			wantEnabled: true,
			wantArgs:    []string{"chat", "--compat-mode"},
		},
		{
			name:     "equals false",
			args:     []string{"--console-host=false", "chat"},
			wantArgs: []string{"chat"},
		},
		{
			name:     "separate false",
			args:     []string{"--console-host", "false", "chat"},
			wantArgs: []string{"chat"},
		},
		{
			name:        "separate true",
			args:        []string{"--console-host", "true", "chat"},
			wantEnabled: true,
			wantArgs:    []string{"chat"},
		},
		{
			name:        "last occurrence wins",
			args:        []string{"--console-host=false", "chat", "--console-host=true"},
			wantEnabled: true,
			wantArgs:    []string{"chat"},
		},
		{
			name:        "delimiter preserves literal flag",
			args:        []string{"--console-host", "--", "--console-host"},
			wantEnabled: true,
			wantArgs:    []string{"--", "--console-host"},
		},
		{
			name:     "similar flag is untouched",
			args:     []string{"--console-hostname", "example"},
			wantArgs: []string{"--console-hostname", "example"},
		},
		{
			name:    "invalid equals value",
			args:    []string{"--console-host=maybe"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enabled, gotArgs, err := ExtractFlag(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ExtractFlag() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if enabled != tt.wantEnabled {
				t.Fatalf("ExtractFlag() enabled = %v, want %v", enabled, tt.wantEnabled)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Fatalf("ExtractFlag() args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestBootstrapSelfDisabledDoesNotTouchRuntime(t *testing.T) {
	args := []string{"chat", "--message", "hello"}
	panicIfCalled := func() bool {
		t.Fatal("runtime probe must not run when --console-host is absent")
		return false
	}

	got, err := bootstrapSelf(args, bootstrapRuntime{
		supported:  panicIfCalled,
		hasConsole: panicIfCalled,
	})
	if err != nil {
		t.Fatalf("bootstrapSelf() error = %v", err)
	}
	if got.Launched || got.ExitCode != 0 {
		t.Fatalf("bootstrapSelf() result = %+v, want no launch", got)
	}
	if !reflect.DeepEqual(got.Args, args) {
		t.Fatalf("bootstrapSelf() args = %#v, want %#v", got.Args, args)
	}
}

func TestBootstrapSelfUsesCurrentConsoleWithoutRelaunch(t *testing.T) {
	runCalled := false
	got, err := bootstrapSelf(
		[]string{"--console-host", "chat", "--message", "hello"},
		bootstrapRuntime{
			supported:  func() bool { return true },
			hasConsole: func() bool { return true },
			run: func(string, []string) (int, error) {
				runCalled = true
				return 0, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("bootstrapSelf() error = %v", err)
	}
	if runCalled {
		t.Fatal("bootstrapSelf() launched a child despite an existing console")
	}
	if got.Launched || got.ExitCode != 0 {
		t.Fatalf("bootstrapSelf() result = %+v, want no launch", got)
	}
	wantArgs := []string{"chat", "--message", "hello"}
	if !reflect.DeepEqual(got.Args, wantArgs) {
		t.Fatalf("bootstrapSelf() args = %#v, want %#v", got.Args, wantArgs)
	}
}

func TestBootstrapSelfRelaunchesWithoutHostFlag(t *testing.T) {
	const executable = `C:\Program Files\aicli.exe`
	wantArgs := []string{"chat", "--message", "hello world"}
	var gotExecutable string
	var gotArgs []string

	got, err := bootstrapSelf(
		[]string{"chat", "--console-host=true", "--message", "hello world"},
		bootstrapRuntime{
			supported:  func() bool { return true },
			hasConsole: func() bool { return false },
			executable: func() (string, error) {
				return executable, nil
			},
			run: func(target string, args []string) (int, error) {
				gotExecutable = target
				gotArgs = append([]string(nil), args...)
				return 37, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("bootstrapSelf() error = %v", err)
	}
	if !got.Launched || got.ExitCode != 37 {
		t.Fatalf("bootstrapSelf() result = %+v, want launched exit code 37", got)
	}
	if gotExecutable != executable {
		t.Fatalf("bootstrapSelf() executable = %q, want %q", gotExecutable, executable)
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("bootstrapSelf() child args = %#v, want %#v", gotArgs, wantArgs)
	}
	if !reflect.DeepEqual(got.Args, wantArgs) {
		t.Fatalf("bootstrapSelf() remaining args = %#v, want %#v", got.Args, wantArgs)
	}
}

func TestBootstrapSelfReportsUnsupportedPlatform(t *testing.T) {
	_, err := bootstrapSelf(
		[]string{"--console-host"},
		bootstrapRuntime{supported: func() bool { return false }},
	)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("bootstrapSelf() error = %v, want ErrUnsupported", err)
	}
}

func TestBootstrapSelfWrapsExecutableResolutionFailure(t *testing.T) {
	_, err := bootstrapSelf(
		[]string{"--console-host"},
		bootstrapRuntime{
			supported:  func() bool { return true },
			hasConsole: func() bool { return false },
			executable: func() (string, error) {
				return "", errors.New("probe failure")
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "resolve current executable") {
		t.Fatalf("bootstrapSelf() error = %v, want executable resolution context", err)
	}
}
