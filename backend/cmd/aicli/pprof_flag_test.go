package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestPprofFlagParsing 验证 --pprof 布尔 flag 在根命令上的注册与解析，
// 确保 PersistentPreRunE 中 GetBool("pprof") 能读到 true。
func TestPprofFlagParsing(t *testing.T) {
	rootCmd := &cobra.Command{Use: "aicli"}
	rootCmd.PersistentFlags().Bool("pprof", false, "enable pprof")
	child := &cobra.Command{Use: "test", Run: func(cmd *cobra.Command, args []string) {}}
	rootCmd.AddCommand(child)

	rootCmd.SetArgs([]string{"--pprof", "test"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	flag, err := rootCmd.Flags().GetBool("pprof")
	if err != nil {
		t.Fatalf("GetBool(pprof) error = %v", err)
	}
	if !flag {
		t.Fatal("GetBool(pprof) = false, want true after --pprof")
	}
}

// TestWebFlagParsing 验证新增的 --web 持久 flag 能被子命令解析。
func TestWebFlagParsing(t *testing.T) {
	rootCmd := &cobra.Command{Use: "aicli"}
	rootCmd.PersistentFlags().Bool("web", false, "enable web client")
	child := &cobra.Command{Use: "test", Run: func(cmd *cobra.Command, args []string) {}}
	rootCmd.AddCommand(child)

	rootCmd.SetArgs([]string{"--web", "test"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	flag, err := rootCmd.Flags().GetBool("web")
	if err != nil {
		t.Fatalf("GetBool(web) error = %v", err)
	}
	if !flag {
		t.Fatal("GetBool(web) = false, want true after --web")
	}
}
