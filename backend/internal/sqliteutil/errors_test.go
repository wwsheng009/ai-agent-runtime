package sqliteutil

import (
	"errors"
	"testing"
)

func TestIsCorruptError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "malformed image", err: errors.New("load artifact: sqlite3: database disk image is malformed"), want: true},
		{name: "not database", err: errors.New("file is not a database"), want: true},
		{name: "locked", err: errors.New("database is locked"), want: false},
		{name: "other", err: errors.New("constraint failed"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCorruptError(tc.err); got != tc.want {
				t.Fatalf("IsCorruptError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
