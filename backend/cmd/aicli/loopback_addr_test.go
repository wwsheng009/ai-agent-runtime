package main

import "testing"

// TestResolveLoopbackServerAddr 锁定 --web-port / AICLI_PPROF / --pprof / --web 的
// 地址解析优先级与边界：flag > env > 随机端口，全空则不启动服务器。
// 该口径与 runtime-server 的 resolveRuntimeServerPprofAddr 保持一致（env 非空即启用）。
func TestResolveLoopbackServerAddr(t *testing.T) {
	cases := []struct {
		name       string
		pprofFlag  bool
		webFlag    bool
		debugFlag  bool
		webPort    int
		webPortSet bool
		webHost    string
		pprofEnv   string
		wantAddr   string
		wantErr    bool
	}{
		{name: "未设置任何开关", wantAddr: ""},
		{name: "--pprof 随机空闲端口", pprofFlag: true, wantAddr: "127.0.0.1:0"},
		{name: "--web 随机空闲端口", webFlag: true, wantAddr: "127.0.0.1:0"},
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
			webHost := tc.webHost
			if webHost == "" {
				webHost = "127.0.0.1"
			}
			got, err := resolveLoopbackServerAddrWithWeb(tc.pprofFlag, tc.webFlag, tc.debugFlag, tc.webPort, tc.webPortSet, webHost, tc.pprofEnv)
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

// TestResolveLoopbackServices 锁定服务面拆分：--pprof 不隐式打开 Web，
// --web 不隐式打开 pprof；--web-host / --web-port 的显式使用自动打开 Web。
func TestResolveLoopbackServices(t *testing.T) {
	cases := []struct {
		name       string
		pprofFlag  bool
		webFlag    bool
		debugFlag  bool
		webHostSet bool
		webPortSet bool
		pprofEnv   string
		want       loopbackServiceOptions
	}{
		{name: "无开关", want: loopbackServiceOptions{}},
		{name: "仅 pprof", pprofFlag: true, want: loopbackServiceOptions{pprof: true}},
		{name: "仅 web", webFlag: true, want: loopbackServiceOptions{web: true}},
		{name: "仅 web-host 自动开 web", webHostSet: true, want: loopbackServiceOptions{web: true}},
		{name: "仅 web-port 自动开 web", webPortSet: true, want: loopbackServiceOptions{web: true}},
		{name: "pprof + web-host 自动叠加 web", pprofFlag: true, webHostSet: true, want: loopbackServiceOptions{pprof: true, web: true}},
		{name: "pprof + web-port 自动叠加 web", pprofFlag: true, webPortSet: true, want: loopbackServiceOptions{pprof: true, web: true}},
		{name: "debug + web-host 自动叠加 web", debugFlag: true, webHostSet: true, want: loopbackServiceOptions{pprof: true, web: true}},
		{name: "env 不隐式开 web", pprofEnv: "127.0.0.1:6060", want: loopbackServiceOptions{pprof: true}},
		{name: "env + web-host 同时开两面", pprofEnv: "127.0.0.1:6060", webHostSet: true, want: loopbackServiceOptions{pprof: true, web: true}},
		{name: "web + pprof", pprofFlag: true, webFlag: true, want: loopbackServiceOptions{pprof: true, web: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveLoopbackServices(tc.pprofFlag, tc.webFlag, tc.debugFlag, tc.webHostSet, tc.webPortSet, tc.pprofEnv)
			if got != tc.want {
				t.Fatalf("resolveLoopbackServices() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
