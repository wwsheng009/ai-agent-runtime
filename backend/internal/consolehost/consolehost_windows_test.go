//go:build windows

package consolehost

import (
	"reflect"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestBuildWindowsCommandLine(t *testing.T) {
	got := buildWindowsCommandLine(
		`C:\Program Files\aicli.exe`,
		[]string{
			"chat",
			"--prompt",
			"hello world",
			`quote"inside`,
			"",
			`trailing\`,
		},
	)
	want := `"C:\Program Files\aicli.exe" chat --prompt "hello world" quote\"inside "" trailing\`
	if got != want {
		t.Fatalf("buildWindowsCommandLine() = %q, want %q", got, want)
	}
}

func TestBuildWindowsCommandLineRoundTrip(t *testing.T) {
	executable := `C:\Program Files\AI CLI\aicli-中文.exe`
	args := []string{
		"chat",
		"",
		"hello world",
		`quote"inside`,
		`C:\path with space\`,
		"中文提示",
		`{"message":"hello world"}`,
		"line one\r\nline two",
	}

	commandLine, err := windows.UTF16PtrFromString(buildWindowsCommandLine(executable, args))
	if err != nil {
		t.Fatalf("encode command line: %v", err)
	}
	var argc int32
	argv, err := windows.CommandLineToArgv(commandLine, &argc)
	if err != nil {
		t.Fatalf("CommandLineToArgv() error = %v", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(argv)))

	got := make([]string, 0, argc)
	for i := 0; i < int(argc); i++ {
		got = append(got, windows.UTF16PtrToString(&argv[i][0]))
	}
	want := append([]string{executable}, args...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command line round trip = %#v, want %#v", got, want)
	}
}
