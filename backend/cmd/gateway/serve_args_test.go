package main

import "testing"

func TestNormalizeServeArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, nil},
		{"plain flags", []string{"--listen", ":8443"}, []string{"--listen", ":8443"}},
		{"serve prefix", []string{"serve", "--listen", ":8443"}, []string{"--listen", ":8443"}},
		{"bare serve", []string{"serve"}, []string{}},
	}
	for _, tc := range cases {
		got := normalizeServeArgs(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}
