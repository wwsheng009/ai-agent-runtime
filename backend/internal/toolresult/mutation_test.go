package toolresult

import "testing"

func TestMutationSummary(t *testing.T) {
	testCases := []struct {
		name     string
		metadata map[string]interface{}
		want     string
	}{
		{
			name: "changed paths",
			metadata: map[string]interface{}{
				"mutated_paths": []string{"a.go", "b.go"},
			},
			want: "Tool completed successfully; changed 2 files: a.go, b.go.",
		},
		{
			name: "nested metadata",
			metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{
					"mutated_paths": []interface{}{"changed.go"},
				},
			},
			want: "Tool completed successfully; changed 1 file: changed.go.",
		},
		{
			name: "idempotent replay",
			metadata: map[string]interface{}{
				"mutated_paths":     []string{},
				"idempotent_replay": true,
				"file_path":         "same.txt",
			},
			want: "Tool completed successfully; no file changes were needed: same.txt.",
		},
		{
			name:     "unrelated metadata",
			metadata: map[string]interface{}{"file_path": "readme.md"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MutationSummary(tc.metadata); got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}
