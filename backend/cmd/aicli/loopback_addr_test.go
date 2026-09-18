package main

import "testing"

// TestResolveLoopbackServerAddr 锁定 --web-port / AICLI_PPROF / --pprof 的
// 地址解析优先级与边界：flag > env > 随机端口，全空则不启动服务器。
// 该口径与 runtime-server 的 resolveRuntimeServerPprofAddr 保持一致（env 非空即启用）。
func TestResolveLoopbackServerAddr(t *testing.T) {
	cases := []struct {
		name       string
		pprofFlag  bool
		debugFlag  bool
		webPort    int
		webPortSet bool
		pprofEnv   string
		wantAddr   string
		wantErr    bool
	}{
		{name: "未设置任何开关", wantAddr: ""},
		{name: "--pprof 随机空闲端口", pprofFlag: true, wantAddr: "127.0.0.1:0"},
		{name: "--debug 随机空闲端口", debugFlag: true, wantAddr: "127.0.0.1:0"},
		{name: "AICLI_PPROF 单独启用并覆盖随机", pprofEnv: "127.0.0.1:6060", wantAddr: "127.0.0.1:6060"},
		{name: "AICLI_PPROF 保留自定义 host 语义", pprofEnv: "[::1]:7070", wantAddr: "[::1]:7070"},
		{name: "--web-port 单独启用", webPort: 55124, webPortSet: true, wantAddr: "127.0.0.1:55124"},
		{name: "--web-port 覆盖 --pprof 随机端口", pprofFlag: true, webPort: 55124, webPortSet: true, wantAddr: "127.0.0.1:55124"},
		{name: "--web-port 覆盖 AICLI_PPROF", webPort: 55124, webPortSet: true, pprofEnv: "127.0.0.1:6060", wantAddr: "127.0.0.1:55124"},
		{name: "--web-port 0 视为笔误直接报错", webPort: 0, webPortSet: true, wantErr: true},
		{name: "--web-port 越界报错", webPort: 70000, webPortSet: true, wantErr: true},
		{name: "--web-port 负数报错", webPort: -1, webPortSet: true, wantErr: true},
		{name: "--web-port 65535 合法边界", webPort: 65535, webPortSet: true, wantAddr: "127.0.0.1:65535"},
		{name: "--web-port 1 合法边界", webPort: 1, webPortSet: true, wantAddr: "127.0.0.1:1"},
		{name: "env 仅空白视为未设置且不启用", pprofEnv: "   ", wantAddr: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveLoopbackServerAddr(tc.pprofFlag, tc.debugFlag, tc.webPort, tc.webPortSet, tc.pprofEnv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveLoopbackServerAddr() err = nil, want error (addr=%q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveLoopbackServerAddr() unexpected error: %v", err)
			}
			if got != tc.wantAddr {
				t.Fatalf("resolveLoopbackServerAddr() = %q, want %q", got, tc.wantAddr)
			}
		})
	}
}
