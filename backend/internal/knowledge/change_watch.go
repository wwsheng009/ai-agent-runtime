package knowledge

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// 本文件是 04 §5 Phase 5 交付 1 的第三类变更源：fsnotify 可选源。
//
// 分工：edit hook 覆盖"agent 自己改的文件"，外部校正（git/stat）覆盖"判定点
// 能看到的落后"，watcher 覆盖**空闲期**——没有 turn、没有判定点时，外部变更
// （编辑器保存 / 脚本写盘 / git checkout）也能被即时发现并入队，最坏延迟从
// "一个 turn"降到"一次事件"。
//
// 边界与降级（Degrade-Not-Fail）：
//   - 可选：`knowledge.watch` 默认 off（watch 是系统级资源，默认开启会改变既有
//     部署的资源画像）；
//   - 只监听目录；事件按**索引口径**过滤（`resolveIndexTargets`：忽略内置集与
//     .gitignore，非代码后缀不收），因此知识层自己的 `.aicli/*.db(-wal/-shm)`
//     写入不会触发回环；
//   - 遍历剪枝：内置忽略集 + 隐藏目录（`.git` 等）不监听，避免把 watch 配额
//     花在永不索引的目录上；
//   - 目录数超过 `maxWatchDirs` → 显式降级并记录原因（状态面可见），不静默
//     半监听；
//   - 启动失败（权限 / 资源耗尽）只记录原因，绝不让 Activation/Open 失败。

// maxWatchDirs 是单工作区监听目录数上限（inotify watch 是系统级资源）。
const maxWatchDirs = 2048

// watchSource 把工作区内的文件系统事件翻译成工作区相对路径并交给变更队列。
// debounce 与串行增量由队列统一负责，本类型只负责"发现"。
type watchSource struct {
	cfg   Config
	queue *ChangeQueue

	// maxDirs 可被测试调小以覆盖"配额耗尽降级"路径。
	maxDirs int

	watcher *fsnotify.Watcher
	done    chan struct{}
	once    sync.Once

	mu       sync.Mutex
	dirs     map[string]struct{}
	degraded string
}

// newWatchSource 建立 fsnotify watcher（不监听、不起协程）。
func newWatchSource(cfg Config, queue *ChangeQueue) (*watchSource, error) {
	if queue == nil {
		return nil, errors.New("knowledge: change queue is not available")
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("knowledge: fsnotify is unavailable: %w", err)
	}
	return &watchSource{
		cfg:     cfg,
		queue:   queue,
		maxDirs: maxWatchDirs,
		watcher: watcher,
		done:    make(chan struct{}),
		dirs:    make(map[string]struct{}),
	}, nil
}

// startWatchSource 建立并启动监听；失败时返回 nil + 原因（不返回错误：
// watcher 是可选源，任何失败都不应阻断知识层）。
func startWatchSource(cfg Config, queue *ChangeQueue) (*watchSource, string) {
	src, err := newWatchSource(cfg, queue)
	if err != nil {
		return nil, err.Error()
	}
	src.start()
	return src, ""
}

// start 加入初始目录集合并开始消费事件。
func (s *watchSource) start() {
	s.addTree()
	go s.loop()
}

// Close 停止监听并等待事件循环退出；nil-safe 且幂等。
func (s *watchSource) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		_ = s.watcher.Close()
		<-s.done
	})
}

// Dirs 返回当前监听的目录数。
func (s *watchSource) Dirs() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.dirs)
}

// DegradedReason 返回降级原因（空串 = 完整监听）。
func (s *watchSource) DegradedReason() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.degraded
}

// addTree 递归把工作区目录加入监听（跳过永不索引的目录与 .gitignore 目录）。
func (s *watchSource) addTree() {
	root := strings.TrimSpace(s.cfg.Workspace)
	if root == "" {
		s.noteDegraded("workspace is empty")
		return
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		s.noteDegraded(fmt.Sprintf("workspace path is not resolvable: %v", err))
		return
	}
	useGitignore := s.cfg.Index.UseGitignoreEnabled()
	_ = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// 单个目录不可读不阻断整棵树（与索引遍历同口径：尽力而为）。
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != absRoot {
			name := d.Name()
			if ignoreDirs[name] || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			if useGitignore {
				if rel, relErr := filepath.Rel(absRoot, path); relErr == nil &&
					isIgnoredByGitignore(absRoot, normalizeRelPath(rel), true) {
					return fs.SkipDir
				}
			}
		}
		s.mu.Lock()
		reachedCap := len(s.dirs) >= s.maxDirs
		s.mu.Unlock()
		if reachedCap {
			s.noteDegraded(fmt.Sprintf("watch directory cap reached (%d)", s.maxDirs))
			return fs.SkipDir
		}
		if err := s.watcher.Add(path); err != nil {
			// 配额/权限问题：记录原因并继续（其余目录仍可监听）。
			s.noteDegraded(fmt.Sprintf("cannot watch %s: %v", path, err))
			return nil
		}
		s.mu.Lock()
		s.dirs[path] = struct{}{}
		s.mu.Unlock()
		return nil
	})
}

// loop 消费 fsnotify 事件；关闭 watcher 时退出。
func (s *watchSource) loop() {
	defer close(s.done)
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			s.handle(event)
		case _, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			// 错误不致命：记录一次原因并继续消费（避免静默失去事件）。
			s.noteDegraded("watch error: see fsnotify error channel")
		}
	}
}

// handle 把一个文件系统事件翻译成"是否要重索引"。
func (s *watchSource) handle(event fsnotify.Event) {
	path := event.Name
	if path == "" {
		return
	}
	if event.Op&fsnotify.Remove != 0 || event.Op&fsnotify.Rename != 0 {
		s.mu.Lock()
		delete(s.dirs, path)
		s.mu.Unlock()
	}
	if event.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			s.watchNewDir(path)
			return
		}
	}
	if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
		return
	}
	// 便宜的预筛：非代码后缀直接丢（知识库自己的 .db/-wal/-shm、锁文件、
	// 编辑器临时文件都走这条路径，避免每个事件都去读 .gitignore）。
	if !hasCodeExtension(path) {
		return
	}
	paths := indexableChangePaths(s.cfg, []string{path})
	if len(paths) == 0 {
		return
	}
	s.queue.Mark(paths...)
}

// watchNewDir 给新建目录补监听（其子目录由后续 Create 事件继续补）。
func (s *watchSource) watchNewDir(path string) {
	name := filepath.Base(path)
	if ignoreDirs[name] || strings.HasPrefix(name, ".") {
		return
	}
	s.mu.Lock()
	reachedCap := len(s.dirs) >= s.maxDirs
	s.mu.Unlock()
	if reachedCap {
		s.noteDegraded(fmt.Sprintf("watch directory cap reached (%d)", s.maxDirs))
		return
	}
	if err := s.watcher.Add(path); err != nil {
		s.noteDegraded(fmt.Sprintf("cannot watch %s: %v", path, err))
		return
	}
	s.mu.Lock()
	s.dirs[path] = struct{}{}
	s.mu.Unlock()
}

// hasCodeExtension 是事件预筛：只有内置 adapter 认识的后缀才值得走完整口径过滤。
func hasCodeExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return false
	}
	_, ok := codeExtensions[ext]
	return ok
}

func (s *watchSource) noteDegraded(reason string) {
	if reason == "" {
		return
	}
	s.mu.Lock()
	if s.degraded == "" {
		s.degraded = reason
	}
	s.mu.Unlock()
}
