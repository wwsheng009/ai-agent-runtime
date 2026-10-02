//go:build live_semantic

package lsp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// live_second_instance_cost_test.go 是**跨会话共享收益的量化实验**（2026-10-01）。
//
// 要回答的问题不是"能不能共享"（gopls 有 -listen，协议侧我们的 DialFunc 已经
// 抽象成 io.ReadWriteCloser），而是"不共享要付多少钱"。钱有两个维度：
//
//	常驻内存：同一模块再起一个 gopls 的边际成本（崩溃根因是整机内存耗尽）
//	冷启延迟：第二个会话是否要重付一遍索引与首答延迟（code_* 的首次延迟）
//
// 判据：两个实例都打开同一批真实文件并拿到诊断后，各实例的 RSS 之差。
// 共享方案的价值 = 边际成本 × (会话数-1)；若边际成本很小，这一整轮就不该做。
func TestLiveSecondInstanceCostExperiment(t *testing.T) {
	if testing.Short() {
		t.Skip("live 实验")
	}
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moduleRoot, "go.mod")); err != nil {
		t.Skipf("找不到 backend/go.mod（仓库布局与实验假设不符）: %v", err)
	}
	files := pickRealGoFiles(t, moduleRoot, 12)

	warm := func(label string) *Client {
		t.Helper()
		spec := firstPreset(t)
		if len(spec) == 0 {
			t.Skip("gopls 预设缺失")
		}
		started := time.Now()
		client, err := NewClient(ClientOptions{Spec: spec[0], Root: moduleRoot, StartupTimeout: 120 * time.Second})
		if err != nil {
			t.Fatalf("%s NewClient: %v", label, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
		defer cancel()
		if err := client.Start(ctx); err != nil {
			cancel()
			t.Fatalf("%s Start: %v", label, err)
		}
		firstAnswer := time.Duration(0)
		for _, file := range files {
			content, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			if _, err := client.OpenOrUpdate(ctx, file, content); err != nil {
				t.Logf("%s OpenOrUpdate(%s): %v", label, filepath.Base(file), err)
				continue
			}
			// textDocument/diagnostic 触发真正的类型检查 → 逼出索引与 SSA 内存。
			raw, err := client.Call(ctx, "textDocument/diagnostic", map[string]interface{}{
				"textDocument": map[string]interface{}{"uri": PathToURI(file)},
			})
			if err != nil {
				continue
			}
			if firstAnswer == 0 && len(raw) > 0 {
				firstAnswer = time.Since(started)
			}
		}
		t.Cleanup(func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer shutdownCancel()
			client.Shutdown(shutdownCtx)
		})
		t.Logf("%s pid=%d 首答延迟=%s", label, client.PID(), firstAnswer.Round(time.Millisecond))
		return client
	}

	// 第一实例：单独跑到内存稳定，记下"一个实例的真实稳态成本"。
	//
	// 必须等稳定：gopls 的内存是惰性增长的（实测同一实例从 516MB 自己长到
	// 1439MB），不稳定的读数会让边际成本变成负数这种荒谬结论。
	a := warm("instance_A")
	rssA1 := waitStableRSS(t, a.PID(), 90*time.Second)
	t.Logf("instance_A 稳态 RSS = %s", formatRSS(rssA1))

	// 第二实例：完全相同的负载，然后**两个都**再等一次稳定。
	b := warm("instance_B")
	rssB := waitStableRSS(t, b.PID(), 90*time.Second)
	rssA2 := waitStableRSS(t, a.PID(), 90*time.Second)
	t.Logf("instance_B 稳态 RSS = %s", formatRSS(rssB))
	t.Logf("instance_A 双实例期稳态 RSS = %s", formatRSS(rssA2))

	if a.PID() == b.PID() {
		t.Fatalf("两个实例共用了同一进程 pid=%d，实验前提不成立", a.PID())
	}
	if rssB > 0 && rssA2 > 0 {
		marginal := rssB - rssA2
		t.Logf("边际成本：第二个实例相对已有实例 %+s（%.0f%%）",
			formatRSS(marginal), 100*marginal/rssA2)
		t.Logf("两实例合计 = %s；共享方案可省 = %s（≈ 第二个实例的全部成本）",
			formatRSS(rssA2+rssB), formatRSS(rssB))
	}
}

// waitStableRSS 轮询到 RSS 相对上一采样增长 <3% 为止（连续两次满足），
// 返回最后的读数。超时则返回最后的读数并由调用方按"未稳定"解读。
func waitStableRSS(t *testing.T, pid int, budget time.Duration) float64 {
	t.Helper()
	deadline := time.Now().Add(budget)
	last := readRSSMB(t, pid)
	stable := 0
	for time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
		cur := readRSSMB(t, pid)
		if cur <= 0 {
			break
		}
		if last > 0 && cur-last < last*0.03 {
			stable++
			if stable >= 2 {
				return cur
			}
		} else {
			stable = 0
		}
		last = cur
	}
	return last
}

// pickRealGoFiles 取一批真实文件（含跨包引用），避免只打开一个文件时 gopls
// 的索引规模小到看不出差异。
func pickRealGoFiles(t *testing.T, root string, limit int) []string {
	t.Helper()
	var files []string
	_ = filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	if len(files) > limit {
		files = files[:limit]
	}
	if len(files) == 0 {
		t.Skip("找不到真实 go 文件")
	}
	return files
}

// readRSSMB 读进程常驻内存（MB）；读不到返回 0（调用方据此跳过断言，不臆测）。
func readRSSMB(t *testing.T, pid int) float64 {
	t.Helper()
	if pid <= 0 {
		return 0
	}
	if runtime.GOOS != "windows" {
		return 0
	}
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return 0
	}
	line := strings.TrimSpace(string(out))
	if line == "" || !strings.Contains(line, strconv.Itoa(pid)) {
		return 0
	}
	// CSV 末列即 Mem Usage，形如 "1,234,567 K"。
	idx := strings.LastIndex(line, `"`)
	if idx <= 0 {
		return 0
	}
	fields := strings.Split(line[:idx], `","`)
	if len(fields) < 5 {
		return 0
	}
	value := strings.ReplaceAll(strings.TrimSpace(fields[4]), ",", "")
	value = strings.TrimSpace(strings.TrimSuffix(value, " K"))
	kb, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return kb / 1024
}

func formatRSS(mb float64) string {
	if mb <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0fMB", mb)
}
