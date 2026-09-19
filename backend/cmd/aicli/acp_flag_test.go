package main

import (
	"reflect"
	"testing"
)

func TestPrependACPFlagRewrite(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"bare flag", []string{"chat", "--acp", "--yolo"}, []string{"acp", "--yolo"}},
		{"short flag", []string{"chat", "-a", "-P", "unsee"}, []string{"acp", "-P", "unsee"}},
		{"explicit true", []string{"chat", "--acp=true", "-m", "glm"}, []string{"acp", "-m", "glm"}},
		{"explicit false ignored", []string{"chat", "--acp=false"}, []string{"chat", "--acp=false"}},
		{"no flag passthrough", []string{"chat", "--yolo"}, []string{"chat", "--yolo"}},
		{"empty", []string{}, []string{}},
		{"flag value not consumed", []string{"--acp", "--provider", "x", "-m", "y"},
			[]string{"acp", "--provider", "x", "-m", "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prependACPFlag(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("prependACPFlag(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestPrependACPFlagOverridesDefaultChat(t *testing.T) {
	// Simulate the real pipeline: prependDefaultChatCommand injects "chat",
	// then prependACPFlag must replace it with "acp" — not nest acp under chat.
	args := prependDefaultChatCommand([]string{"--acp", "--yolo"}, nil, nil)
	got := prependACPFlag(args)
	want := []string{"acp", "--yolo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pipeline = %v, want %v", got, want)
	}
}
