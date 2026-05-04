package main

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"
)

func TestPrependDefaultChatCommand(t *testing.T) {
	root := &cobra.Command{Use: "aicli"}
	root.PersistentFlags().StringP("config", "c", "", "")
	root.PersistentFlags().StringP("logfile", "l", "", "")
	root.PersistentFlags().String("theme", "", "")
	root.PersistentFlags().Bool("envelope", false, "")

	chat := &cobra.Command{Use: "chat"}
	chat.Flags().StringP("provider", "p", "", "")
	chat.Flags().StringP("model", "m", "", "")
	chat.Flags().StringP("message", "M", "", "")
	chat.Flags().BoolP("stream", "s", false, "")
	chat.Flags().Bool("no-interactive", false, "")
	root.AddCommand(chat)

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "empty args default to chat",
			args: nil,
			want: []string{"chat"},
		},
		{
			name: "only root flags default to chat",
			args: []string{"--config", "config.yaml", "--theme", "focus"},
			want: []string{"chat", "--config", "config.yaml", "--theme", "focus"},
		},
		{
			name: "chat flags default to chat",
			args: []string{"-M", "hello", "--no-interactive"},
			want: []string{"chat", "-M", "hello", "--no-interactive"},
		},
		{
			name: "mixed root and chat flags default to chat",
			args: []string{"-c", "config.yaml", "-p", "nvidia", "-s"},
			want: []string{"chat", "-c", "config.yaml", "-p", "nvidia", "-s"},
		},
		{
			name: "boolean flag with explicit value default to chat",
			args: []string{"-s", "true", "-M", "hello"},
			want: []string{"chat", "-s", "true", "-M", "hello"},
		},
		{
			name: "help flag preserves root help",
			args: []string{"--help"},
			want: []string{"--help"},
		},
		{
			name: "help flag with explicit value preserves root help",
			args: []string{"--help=true"},
			want: []string{"--help=true"},
		},
		{
			name: "explicit command is unchanged",
			args: []string{"chat", "--message", "hello"},
			want: []string{"chat", "--message", "hello"},
		},
		{
			name: "explicit positional arg is unchanged",
			args: []string{"config"},
			want: []string{"config"},
		},
		{
			name: "double dash followed by args is unchanged",
			args: []string{"--", "config"},
			want: []string{"--", "config"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prependDefaultChatCommand(tt.args, root.PersistentFlags(), chat.Flags())
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("prependDefaultChatCommand(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
