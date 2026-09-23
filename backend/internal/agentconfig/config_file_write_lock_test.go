package agentconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 配置文件写锁（方案 §3.4/M11、§12 R4）回归：
//
//   - 同一份配置文件的「读-改-写」必须串行化（进程内 + 跨进程），否则后写者带着旧
//     快照落盘，静默丢掉前者的字段（丢更新）；
//   - 这把锁是**按文件统一**的一把：routing 之外的写点（chat / theme / provider /
//     provider 管理 / provider 代理 / config 层文档 / workspace 偏好）必须命中同一
//     把锁——各写各的锁等于没有互斥。
//
// 跨进程部分见 config_file_write_lock_xproc_test.go。

func TestLockConfigFileWriteBlocksUntilReleased(t *testing.T) {
	target := filepath.Join(t.TempDir(), "chat-prefs.yaml")
	unlock := LockConfigFileWrite(target)

	started := make(chan struct{})
	acquired := make(chan struct{})
	go func() {
		close(started)
		release := LockConfigFileWrite(target)
		close(acquired)
		release()
	}()
	<-started

	select {
	case <-acquired:
		unlock()
		t.Fatal("同一路径的第二个写者未等待锁释放（串行化失效）")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()

	select {
	case <-acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("锁释放后第二个写者仍未获得锁")
	}

	// 释放函数幂等；空路径为 no-op。
	unlock()
	LockConfigFileWrite("")()
}

// TestNormalizeConfigWriteLockKeyUnifiesPathSpellings 固定锁键归一化：同一份文件的不同
// 写法必须命中同一把锁（否则各拿一把锁、各写一个旁路锁文件，互斥形同虚设）。
func TestNormalizeConfigWriteLockKeyUnifiesPathSpellings(t *testing.T) {
	relative := filepath.Join(".", "relative-dir", "config.yaml")
	absolute, err := filepath.Abs(relative)
	require.NoError(t, err)
	assert.Equal(t, normalizeConfigWriteLockKey(absolute), normalizeConfigWriteLockKey(relative),
		"相对路径与绝对路径必须归一化到同一个锁键")

	assert.Equal(t, "", normalizeConfigWriteLockKey("   "), "空路径是 no-op 锁")

	if runtime.GOOS == "windows" {
		// Windows 文件系统大小写不敏感：大小写不同但指向同一份文件。
		assert.Equal(t, normalizeConfigWriteLockKey(`C:\Tmp\Prefs.yaml`), normalizeConfigWriteLockKey(`c:\tmp\prefs.yaml`),
			"Windows 下同一文件的不同大小写必须命中同一把锁")
	}
}

func TestUpdateWorkspaceRoutingSectionSerializesConcurrentWriters(t *testing.T) {
	workspacePath := t.TempDir()
	prefsPath := WorkspacePrefsPathForPath(workspacePath)
	require.NotEmpty(t, prefsPath, "workspace 偏好路径必须可解析")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(prefsPath)) })

	// 写者 A 先落 hard 档（等价于 TUI 端已完成的写入）。
	require.NoError(t, UpdateWorkspaceRoutingSection(workspacePath, &SessionRoutingPatch{
		MainAgent: &AICLISessionMainAgentRoutingOverride{
			Profiles: map[string]AICLISessionRouteProfileOverride{"hard": {Model: configLockTestStr("alpha-model")}},
		},
	}))

	// 锁被占住期间（写者 A 的「读-改-写」尚未结束），写者 B 必须等待。
	unlock := LockConfigFileWrite(prefsPath)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- UpdateWorkspaceRoutingSection(workspacePath, &SessionRoutingPatch{
			MainAgent: &AICLISessionMainAgentRoutingOverride{
				Profiles: map[string]AICLISessionRouteProfileOverride{"normal": {Model: configLockTestStr("beta-model")}},
			},
		})
	}()
	<-started

	select {
	case err := <-done:
		unlock()
		t.Fatalf("并发写未串行化：写者 B 在锁释放前完成（err=%v）", err)
	case <-time.After(200 * time.Millisecond):
	}
	unlock()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("锁释放后写者 B 仍未完成")
	}

	// 串行化 + 锁内重读的直接后果：两个写者的字段都必须保留。
	prefs, err := LoadWorkspaceRoutingPreferencesForPath(workspacePath)
	require.NoError(t, err)
	require.NotNil(t, prefs)
	require.NotNil(t, prefs.MainAgent)
	assert.Equal(t, "alpha-model", prefs.MainAgent.Profiles["hard"].Model)
	assert.Equal(t, "beta-model", prefs.MainAgent.Profiles["normal"].Model)
}

func TestUpdateAICLIRoutingSectionSerializesConcurrentWriters(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	// 先落一份带无关节的既有配置：路由写入不得丢掉它。
	require.NoError(t, writeFileAtomic(configPath, []byte("aicli:\n  chat:\n    default_model: keep-me\n")))

	require.NoError(t, UpdateAICLIRoutingSection(configPath, &AICLIMainAgentRoutingConfig{
		Profiles: map[string]AICLISubagentRouteProfile{"hard": {Model: "alpha-model"}},
	}, true, nil, false))

	unlock := LockConfigFileWrite(configPath)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- UpdateAICLIRoutingSection(configPath, &AICLIMainAgentRoutingConfig{
			Profiles: map[string]AICLISubagentRouteProfile{"normal": {Model: "beta-model"}},
		}, true, nil, false)
	}()
	<-started

	select {
	case err := <-done:
		unlock()
		t.Fatalf("并发写未串行化：config 写者在锁释放前完成（err=%v）", err)
	case <-time.After(200 * time.Millisecond):
	}
	unlock()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("锁释放后 config 写者仍未完成")
	}

	raw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "beta-model")
	assert.Contains(t, string(raw), "keep-me", "路由写入不得丢掉配置文件中的无关节")
}

// configFileWriteCase 描述一个必须命中共享写锁的写点。
type configFileWriteCase struct {
	name    string
	prepare func(t *testing.T, path string)
	write   func(t *testing.T, path string) error
	// lockPath 允许写点的目标文件与 path 不同（workspace 偏好文件）。
	lockPath func(path string) string
}

// configFileWriteCases 覆盖 config.yaml 的全部写点 + config 层文档写点。新增写点若
// 忘了走共享锁（config_file_write.go 事务或 LockConfigFileWrite），本表会红。
func configFileWriteCases() []configFileWriteCase {
	prepareProviderConfig := func(t *testing.T, path string) {
		t.Helper()
		require.NoError(t, writeFileAtomic(path, []byte(
			"providers:\n  default_provider: alpha\n  items:\n    alpha:\n      enabled: true\n      protocol: openai\n      base_url: https://alpha.example.com\n    beta:\n      enabled: true\n      protocol: openai\n")))
	}
	return []configFileWriteCase{
		{
			name:    "chat",
			prepare: prepareProviderConfig,
			write: func(_ *testing.T, path string) error {
				_, err := UpdateAICLIChatPreferences(path, AICLIChatPreferenceUpdate{DefaultModel: configLockTestStr("chat-model")})
				return err
			},
		},
		{
			name:    "theme",
			prepare: prepareProviderConfig,
			write: func(_ *testing.T, path string) error {
				_, err := UpdateAICLIThemePreferences(path, AICLIThemePreferenceUpdate{Name: configLockTestStr("solarized")})
				return err
			},
		},
		{
			name:    "provider",
			prepare: prepareProviderConfig,
			write: func(_ *testing.T, path string) error {
				_, err := UpdateProviderConfig(path, ProviderConfigUpdate{Name: "alpha", BaseURL: configLockTestStr("https://beta.example.com")})
				return err
			},
		},
		{
			name:    "routing",
			prepare: prepareProviderConfig,
			write: func(_ *testing.T, path string) error {
				return UpdateAICLIRoutingSection(path, &AICLIMainAgentRoutingConfig{
					Profiles: map[string]AICLISubagentRouteProfile{"hard": {Model: "routing-model"}},
				}, true, nil, false)
			},
		},
		{
			name:    "provider_enable",
			prepare: prepareProviderConfig,
			write: func(_ *testing.T, path string) error {
				_, err := SetProvidersEnabledConfig(path, []string{"alpha"}, true)
				return err
			},
		},
		{
			name:    "provider_default",
			prepare: prepareProviderConfig,
			write: func(_ *testing.T, path string) error {
				_, err := SetDefaultProviderConfig(path, "beta")
				return err
			},
		},
		{
			name:    "provider_delete",
			prepare: prepareProviderConfig,
			write: func(_ *testing.T, path string) error {
				_, err := DeleteProvidersConfig(path, ProviderDeleteRequest{Names: []string{"beta"}})
				return err
			},
		},
		{
			name:    "global_proxy",
			prepare: prepareProviderConfig,
			write: func(_ *testing.T, path string) error {
				_, err := SetGlobalProxyConfig(path, GlobalProxyUpdate{HTTP: configLockTestStr("http://proxy.example.com:8080")})
				return err
			},
		},
		{
			name:    "layer_document",
			prepare: prepareProviderConfig,
			write: func(_ *testing.T, path string) error {
				return applyDocumentChanges(path, []DocumentChange{
					{Path: []string{"aicli", "chat", "default_model"}, Value: "layer-model"},
				})
			},
		},
	}
}

// TestConfigFileWritersHonorSharedWriteLock 是本轮「统一锁范围」的核心回归：对每个写点，
// 先占住目标文件的共享写锁，再发起该写点的写入——写入必须等待锁释放，证明它走的是
// 同一把锁，而不是自己另起一套（或完全没锁）。
func TestConfigFileWritersHonorSharedWriteLock(t *testing.T) {
	for _, tc := range configFileWriteCases() {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			tc.prepare(t, path)

			unlock := LockConfigFileWrite(path)
			done := make(chan error, 1)
			go func() { done <- tc.write(t, path) }()

			select {
			case err := <-done:
				unlock()
				t.Fatalf("写点未走共享写锁：锁被占用时写入已完成（err=%v）", err)
			case <-time.After(200 * time.Millisecond):
			}
			unlock()

			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("锁释放后写点仍未完成")
			}
		})
	}
}

// TestWorkspacePrefsWritersHonorSharedWriteLock 覆盖 chat-prefs.yaml 的两个写点
// （SaveWorkspaceChatPreferencesForPath 与 UpdateWorkspaceRoutingSection）：它们与
// config.yaml 的写者不同文件、不同入口，但必须按目标文件命中同一把锁。
func TestWorkspacePrefsWritersHonorSharedWriteLock(t *testing.T) {
	workspacePath := t.TempDir()
	prefsPath := WorkspacePrefsPathForPath(workspacePath)
	require.NotEmpty(t, prefsPath, "workspace 偏好路径必须可解析")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(prefsPath)) })

	writers := []struct {
		name  string
		write func() error
	}{
		{
			name: "chat_prefs",
			write: func() error {
				return SaveWorkspaceChatPreferencesForPath(workspacePath, AICLIChatPreferenceUpdate{DefaultModel: configLockTestStr("workspace-model")})
			},
		},
		{
			name: "routing",
			write: func() error {
				return UpdateWorkspaceRoutingSection(workspacePath, &SessionRoutingPatch{
					MainAgent: &AICLISessionMainAgentRoutingOverride{
						Profiles: map[string]AICLISessionRouteProfileOverride{"hard": {Model: configLockTestStr("workspace-routing-model")}},
					},
				})
			},
		},
	}
	for _, tc := range writers {
		t.Run(tc.name, func(t *testing.T) {
			unlock := LockConfigFileWrite(prefsPath)
			done := make(chan error, 1)
			go func() { done <- tc.write() }()

			select {
			case err := <-done:
				unlock()
				t.Fatalf("workspace 写点未走共享写锁：锁被占用时写入已完成（err=%v）", err)
			case <-time.After(200 * time.Millisecond):
			}
			unlock()

			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("锁释放后 workspace 写点仍未完成")
			}
		})
	}
}

// TestConcurrentConfigSectionWritersPreserveEverySection 是端到端的丢更新回归：chat /
// theme / provider / routing 四个写点并发改写同一份 config.yaml 的不同节，每个写点各写
// iterations 次；只要有一个写点没走共享锁，它的节就会被别人的旧快照抹掉。
func TestConcurrentConfigSectionWritersPreserveEverySection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, writeFileAtomic(path, []byte(
		"providers:\n  default_provider: alpha\n  items:\n    alpha:\n      enabled: true\n      protocol: openai\n")))

	const iterations = 8
	writers := []struct {
		name  string
		write func(i int) error
		final func(i int) string
	}{
		{
			name: "chat",
			write: func(i int) error {
				_, err := UpdateAICLIChatPreferences(path, AICLIChatPreferenceUpdate{
					DefaultModel: configLockTestStr(fmt.Sprintf("chat-model-%d", i)),
				})
				return err
			},
			final: func(i int) string { return fmt.Sprintf("chat-model-%d", i) },
		},
		{
			name: "theme",
			write: func(i int) error {
				_, err := UpdateAICLIThemePreferences(path, AICLIThemePreferenceUpdate{
					Name: configLockTestStr(fmt.Sprintf("theme-name-%d", i)),
				})
				return err
			},
			final: func(i int) string { return fmt.Sprintf("theme-name-%d", i) },
		},
		{
			name: "provider",
			write: func(i int) error {
				_, err := UpdateProviderConfig(path, ProviderConfigUpdate{
					Name:    "alpha",
					BaseURL: configLockTestStr(fmt.Sprintf("https://provider-%d.example.com", i)),
				})
				return err
			},
			final: func(i int) string { return fmt.Sprintf("https://provider-%d.example.com", i) },
		},
		{
			name: "routing",
			write: func(i int) error {
				return UpdateAICLIRoutingSection(path, &AICLIMainAgentRoutingConfig{
					Profiles: map[string]AICLISubagentRouteProfile{"hard": {Model: fmt.Sprintf("routing-model-%d", i)}},
				}, true, nil, false)
			},
			final: func(i int) string { return fmt.Sprintf("routing-model-%d", i) },
		},
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(writers))
	for _, writer := range writers {
		wg.Add(1)
		go func(writer struct {
			name  string
			write func(i int) error
			final func(i int) string
		}) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if err := writer.write(i); err != nil {
					errs <- fmt.Errorf("写点 %s 第 %d 次写入失败: %w", writer.name, i, err)
					return
				}
			}
			errs <- nil
		}(writer)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(raw)
	last := iterations - 1
	for _, writer := range writers {
		assert.Contains(t, text, writer.final(last),
			"写点 %s 的最后一次写入被并发写者覆盖（丢更新：说明该写点未走共享锁）", writer.name)
	}
}

// configLockTestStr 返回字符串指针（字段级语义：nil=不修改）。
func configLockTestStr(value string) *string {
	return &value
}

// TestLockConfigFileWriteAllDedupesRepeatedTargets：同一目标文件以不同写法重复出现时
// 只取一次锁。若没去重，同一 goroutine 会在同一把锁上二次等待 → 自锁（用例会超时）。
func TestLockConfigFileWriteAllDedupesRepeatedTargets(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")

	done := make(chan func(), 1)
	go func() {
		done <- LockConfigFileWriteAll(target, target, filepath.Join(dir, ".", "config.yaml"))
	}()

	select {
	case unlock := <-done:
		unlock()
	case <-time.After(5 * time.Second):
		t.Fatal("重复目标未去重：LockConfigFileWriteAll 对同一文件二次取锁导致自锁")
	}
}

// TestLockConfigFileWriteAllHoldsEveryTarget：一次事务里的每个目标文件都必须真的被占住
// （读 A → 写 B 的跨文件事务两侧都要互斥），统一释放后逐个恢复可取。
func TestLockConfigFileWriteAllHoldsEveryTarget(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.yaml")
	second := filepath.Join(dir, "b.yaml")

	// 探测者拿到锁后不自己归还，而是交给 main 放行——否则统一释放的瞬间等待者会
	// 抢到锁并一直持有，后面「释放后应可再取」的断言就会永久阻塞（自锁）。
	type lockProbe struct {
		acquired chan func()
		release  chan struct{}
		finished chan struct{}
	}
	// 先占住两个目标，再起探测者；顺序反过来会让探测者先拿到锁，main 反而卡在
	// LockConfigFileWriteAll 上（用例自身死锁，与实现无关）。
	unlockAll := LockConfigFileWriteAll(first, second)

	probes := make([]*lockProbe, 0, 2)
	for _, target := range []string{first, second} {
		probe := &lockProbe{
			acquired: make(chan func(), 1),
			release:  make(chan struct{}),
			finished: make(chan struct{}),
		}
		probes = append(probes, probe)
		go func(path string, p *lockProbe) {
			defer close(p.finished)
			unlock := LockConfigFileWrite(path)
			p.acquired <- unlock
			<-p.release
			unlock()
		}(target, probe)
	}

	// 占锁期间：两个探测者都必须被挡在锁外。
	for i, probe := range probes {
		select {
		case <-probe.acquired:
			// 不该发生：目标没被占住。先恢复现场（放行全部探测者并等它们退出），再报告。
			unlockAll()
			for _, p := range probes {
				close(p.release)
			}
			for _, p := range probes {
				<-p.finished
			}
			t.Fatalf("LockConfigFileWriteAll 未占住第 %d 个目标", i+1)
		case <-time.After(200 * time.Millisecond):
		}
	}

	// 统一释放后，探测者应逐个拿到锁并交还。
	unlockAll()
	for i, probe := range probes {
		select {
		case <-probe.acquired:
		case <-time.After(10 * time.Second):
			t.Fatalf("统一释放后第 %d 个目标仍取不到锁（可能被统一释放函数漏放）", i+1)
		}
		close(probe.release)
	}
	for _, probe := range probes {
		select {
		case <-probe.finished:
		case <-time.After(10 * time.Second):
			t.Fatal("探测者未能在放行后归还锁")
		}
	}

	// 探测者全部退出后，锁应当再次可取（说明引用计数/表回收没有把条目留成孤儿）。
	unlockFirst := LockConfigFileWrite(first)
	unlockSecond := LockConfigFileWrite(second)
	unlockSecond()
	unlockFirst()
}
