package patchutil

import (
	"reflect"
	"testing"
)

func TestExtractPathsSupportsUnifiedDiff(t *testing.T) {
	patch := "" +
		"diff --git a/backend/a.txt b/backend/a.txt\n" +
		"--- a/backend/a.txt\n" +
		"+++ b/backend/a.txt\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n"

	got := ExtractPaths(patch)
	want := []string{"backend/a.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractPaths() = %v, want %v", got, want)
	}
}

func TestExtractPathsSupportsCodexApplyPatchFormat(t *testing.T) {
	patch := "" +
		"*** Begin Patch\n" +
		"*** Update File: backend/old.txt\n" +
		"*** Move to: backend/new.txt\n" +
		"@@\n" +
		"-old\n" +
		"+new\n" +
		"*** Add File: backend/added.txt\n" +
		"+hello\n" +
		"*** Delete File: backend/deleted.txt\n" +
		"*** End Patch\n"

	got := ExtractPaths(patch)
	want := []string{"backend/old.txt", "backend/new.txt", "backend/added.txt", "backend/deleted.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractPaths() = %v, want %v", got, want)
	}
}
