package aiclipaths

import "testing"

func assertBuildProfileDefaults(t *testing.T, configFile, cliConfigFile, runtimeConfigFile, sessionHistoryFile string) {
	t.Helper()
	if DefaultConfigFileName != configFile {
		t.Fatalf("DefaultConfigFileName = %q, want %q", DefaultConfigFileName, configFile)
	}
	if DefaultCLIConfigFileName != cliConfigFile {
		t.Fatalf("DefaultCLIConfigFileName = %q, want %q", DefaultCLIConfigFileName, cliConfigFile)
	}
	if DefaultRuntimeConfigFileName != runtimeConfigFile {
		t.Fatalf("DefaultRuntimeConfigFileName = %q, want %q", DefaultRuntimeConfigFileName, runtimeConfigFile)
	}
	if DefaultRuntimeConfigRelativePath != "configs/"+runtimeConfigFile {
		t.Fatalf("DefaultRuntimeConfigRelativePath = %q, want %q", DefaultRuntimeConfigRelativePath, "configs/"+runtimeConfigFile)
	}
	if DefaultSessionHistoryFileName != sessionHistoryFile {
		t.Fatalf("DefaultSessionHistoryFileName = %q, want %q", DefaultSessionHistoryFileName, sessionHistoryFile)
	}
}
