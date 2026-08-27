package main

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"
)

func TestConsoleHostFlagDefaultsToChat(t *testing.T) {
	root := &cobra.Command{Use: "aicli"}
	root.PersistentFlags().Bool("console-host", false, "")
	chat := &cobra.Command{Use: "chat"}

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "bare flag",
			args: []string{"--console-host"},
			want: []string{"chat", "--console-host"},
		},
		{
			name: "explicit false",
			args: []string{"--console-host", "false"},
			want: []string{"chat", "--console-host", "false"},
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
