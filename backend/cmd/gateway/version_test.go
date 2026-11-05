package main

import "testing"

func TestIsVersionRequest(t *testing.T) {
	for _, args := range [][]string{
		{"version"},
		{"--version"},
		{"-V"},
		{"serve", "version"},
		{"serve", "--version"},
	} {
		if !isVersionRequest(args) {
			t.Fatalf("isVersionRequest(%v) = false, want true", args)
		}
	}
	for _, args := range [][]string{
		nil,
		{},
		{"serve"},
		{"version", "extra"},
		{"serve", "version", "extra"},
		{"--help"},
	} {
		if isVersionRequest(args) {
			t.Fatalf("isVersionRequest(%v) = true, want false", args)
		}
	}
}
