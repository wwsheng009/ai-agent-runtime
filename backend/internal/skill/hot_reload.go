package skill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ReloadEvent 重载事件
type ReloadEvent struct {
	Type      ReloadEventType `json:"type"`
	SkillName string          `json:"skillName,omitempty"`
	FilePath  string          `json:"filePath,omitempty"`
	Error     string          `json:"error,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
}

// ReloadEventType 重载事件类型
type ReloadEventType string

const (
	ReloadEventSkillAdded    ReloadEventType = "skill_added"
	ReloadEventSkillUpdated  ReloadEventType = "skill_updated"
	ReloadEventSkillRemoved  ReloadEventType = "skill_removed"
	ReloadEventError         ReloadEventType = "error"
	ReloadEventReloadStarted ReloadEventType = "reload_started"
	ReloadEventReloadDone    ReloadEventType = "reload_done"
)

// ReloadCallback 重载回调函数类型
type ReloadCallback func(event *ReloadEvent)

// HotReload 热加载器
type HotReload struct {
	watcher     *fsnotify.Watcher
	loader      *Loader
	registry    *Registry
	callbacks   []ReloadCallback
	eventBuffer chan *ReloadEvent
	debounceMap map[string]time.Time
	skillFiles  map[string]string
	mu          sync.RWMutex
	skillDir    string
	skillDirs   []string

	// 配置
	enabled      bool
	debounceTime time.Duration

	// 上下文
	ctx    context.Context
	cancel context.CancelFunc

	// 扫描器状态
	scanning bool
	scanMu   sync.Mutex

	// watchedDirs 记录已登记监听的目录（含运行中动态创建的子目录），目录被
	// 删除/重命名时据此反注册其中登记过的 skill。
	// scheduledDirScans 合并同一个新目录的补齐扫描，避免一次安装反复重扫。
	watchedDirs       map[string]struct{}
	scheduledDirScans map[string]struct{}
	// pendingRoots 是"候选根目录"（启动时尚不存在，如新工作区的
	// `<cwd>/.agents/skills`）：先监听最近已存在的祖先，目录创建后自动接管。
	pendingRoots map[string]struct{}
	// reloadMu 串行化目录补齐 / 全量重载，防抖回调可能并发触发。
	reloadMu sync.Mutex
}

// NewHotReload 创建热加载器
func NewHotReload(loader *Loader, registry *Registry) (*HotReload, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &HotReload{
		watcher:           watcher,
		loader:            loader,
		registry:          registry,
		callbacks:         make([]ReloadCallback, 0),
		eventBuffer:       make(chan *ReloadEvent, 100),
		debounceMap:       make(map[string]time.Time),
		skillFiles:        make(map[string]string),
		watchedDirs:       make(map[string]struct{}),
		scheduledDirScans: make(map[string]struct{}),
		pendingRoots:      make(map[string]struct{}),
		ctx:               ctx,
		cancel:            cancel,
		enabled:           true,
		debounceTime:      500 * time.Millisecond, // 默认防抖时间
	}, nil
}

// Start 启动热加载
func (h *HotReload) Start(skillDir string) error {
	return h.StartMany([]string{skillDir})
}

// StartMany 启动多个目录的热加载
func (h *HotReload) StartMany(skillDirs []string) error {
	// 检查是否已经在扫描
	h.scanMu.Lock()
	if h.scanning {
		h.scanMu.Unlock()
		return fmt.Errorf("hot reload already scanning")
	}
	h.scanMu.Unlock()

	normalized := normalizeSkillDirs(skillDirs)
	if len(normalized) == 0 {
		return fmt.Errorf("skill directory is required")
	}

	h.mu.Lock()
	h.skillDirs = normalized
	h.skillDir = normalized[0]
	h.mu.Unlock()

	for _, dir := range normalized {
		if err := h.watchDir(dir); err != nil {
			return fmt.Errorf("failed to add directory to watcher: %w", err)
		}
		if err := h.addSubdirectories(dir); err != nil {
			return fmt.Errorf("failed to add subdirectories: %w", err)
		}
	}

	h.scanMu.Lock()
	h.scanning = true
	h.scanMu.Unlock()

	// 启动事件处理 goroutine
	go h.processEvents()
	go h.watch()

	// 发送启动事件
	h.emitEvent(&ReloadEvent{
		Type:      ReloadEventReloadStarted,
		Timestamp: time.Now(),
	})

	return nil
}

// StartEmpty 启动事件循环但不监听任何现有根：调用方随后通过 WatchRoots 登记
// 候选安装位（启动时不存在的标准目录），由目录创建事件接管。
//
// 用于“启动时一个技能目录都不存在”的会话/服务：保留热加载能力，第一次安装
// 即可生效，而不是因为空集合直接关闭整条链路。
func (h *HotReload) StartEmpty() error {
	h.scanMu.Lock()
	if h.scanning {
		h.scanMu.Unlock()
		return fmt.Errorf("hot reload already scanning")
	}
	h.scanning = true
	h.scanMu.Unlock()

	go h.processEvents()
	go h.watch()

	h.emitEvent(&ReloadEvent{
		Type:      ReloadEventReloadStarted,
		Timestamp: time.Now(),
	})
	return nil
}

// WatchRoots 登记一组"候选根目录"：目录已存在则直接监听；不存在则监听最近
// 已存在的祖先，待目录被创建（动态安装的第一步）后自动接管并补齐扫描。
//
// 标准安装位（`<cwd>/.agents/skills`、`~/.agents/skills` 等）在启动时可能
// 尚不存在，只有 StartMany 的现有根会让"第一次安装"失去热加载能力。
//
// WatchRoots 不参与加载（loader 的目录集合仍由调用方提供），只扩展监听面。
func (h *HotReload) WatchRoots(dirs []string) {
	if h == nil || h.watcher == nil {
		return
	}
	for _, dir := range normalizeSkillDirs(dirs) {
		dir = canonicalizeSkillTreePath(dir, false)
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			if err := h.watchDir(dir); err == nil {
				_ = h.addSubdirectories(dir)
				h.mu.Lock()
				h.adoptSkillDirLocked(dir)
				h.mu.Unlock()
			}
			continue
		}
		h.watchPendingRoot(dir)
	}
}

// SkillDirs 返回当前参与全量重载的 skill 根快照：启动时的现有根 + 运行中接管的
// 候选安装位。与 loader 的发现集合不同，这里只描述 Reload() 的扫描面。
func (h *HotReload) SkillDirs() []string {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.skillDirs...)
}

// adoptSkillDirLocked 把运行中接管/新登记的根并入 skillDirs（调用方持 h.mu）。
func (h *HotReload) adoptSkillDirLocked(dir string) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" {
		return
	}
	for _, existing := range h.skillDirs {
		if skillWatchPathEqual(existing, dir) {
			return
		}
	}
	h.skillDirs = append(h.skillDirs, dir)
	if strings.TrimSpace(h.skillDir) == "" {
		h.skillDir = dir
	}
}

// watchPendingRoot 为尚不存在的目标目录登记 pending 监听：向上找到最近的
// 已存在祖先挂监听，创建事件到达时由 advancePendingRoots 逐级推进接管。
func (h *HotReload) watchPendingRoot(target string) {
	target = filepath.Clean(strings.TrimSpace(target))
	if target == "" {
		return
	}
	for ancestor := filepath.Dir(target); ancestor != "" && ancestor != filepath.Dir(ancestor); ancestor = filepath.Dir(ancestor) {
		info, err := os.Stat(ancestor)
		if err != nil || !info.IsDir() {
			continue
		}
		if err := h.watchDir(ancestor); err != nil {
			return
		}
		h.mu.Lock()
		h.pendingRoots[target] = struct{}{}
		h.mu.Unlock()
		return
	}
}

// advancePendingRoots 在目录创建/重命名事件后推进候选根：已到位则登记监听、
// 补齐子目录并扫描其中的 skill；中间目录则先挂上，继续等待更深一层。
func (h *HotReload) advancePendingRoots(created string) {
	created = filepath.Clean(strings.TrimSpace(created))
	if created == "" {
		return
	}

	h.mu.RLock()
	targets := make([]string, 0, len(h.pendingRoots))
	for target := range h.pendingRoots {
		if skillWatchPathEqual(created, target) || strings.HasPrefix(target, created+string(os.PathSeparator)) {
			targets = append(targets, target)
		}
	}
	h.mu.RUnlock()
	if len(targets) == 0 {
		return
	}

	for _, target := range targets {
		if info, err := os.Stat(target); err == nil && info.IsDir() {
			if err := h.watchDir(target); err == nil {
				h.mu.Lock()
				delete(h.pendingRoots, target)
				h.adoptSkillDirLocked(target)
				h.mu.Unlock()
				_ = h.addSubdirectories(target)
				h.scheduleDirectoryScan(target)
			}
			continue
		}
		// 中间目录刚出现：挂上监听，目标目录就绪前继续等待下一级创建事件。
		if info, err := os.Stat(created); err == nil && info.IsDir() {
			_ = h.watchDir(created)
		}
	}
}

// Stop 停止热加载
func (h *HotReload) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.scanning {
		return nil
	}

	if h.watcher != nil {
		h.watcher.Close()
	}

	h.cancel()

	h.scanMu.Lock()
	h.scanning = false
	h.scanMu.Unlock()

	// 发送停止事件
	h.emitEvent(&ReloadEvent{
		Type:      ReloadEventReloadDone,
		Timestamp: time.Now(),
	})

	return nil
}

// IsEnabled 检查是否启用
func (h *HotReload) IsEnabled() bool {
	return h.enabled
}

// SetEnabled 设置启用状态
func (h *HotReload) SetEnabled(enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enabled = enabled
}

// SetDebounceTime 设置防抖时间
func (h *HotReload) SetDebounceTime(duration time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.debounceTime = duration
}

// AddCallback 添加回调
func (h *HotReload) AddCallback(callback ReloadCallback) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.callbacks = append(h.callbacks, callback)
}

// Reload 手动触发重载
func (h *HotReload) Reload() error {
	h.mu.Lock()
	enabled := h.enabled
	skillDirs := append([]string(nil), h.skillDirs...)
	h.mu.Unlock()

	if !enabled {
		return fmt.Errorf("hot reload is disabled")
	}

	// 发送重载开始事件
	h.emitEvent(&ReloadEvent{
		Type:      ReloadEventReloadStarted,
		Timestamp: time.Now(),
	})

	// 重新加载所有 skills
	if err := h.reloadAllSkills(skillDirs); err != nil {
		h.emitEvent(&ReloadEvent{
			Type:      ReloadEventError,
			Error:     err.Error(),
			Timestamp: time.Now(),
		})
		return err
	}

	// 发送重载完成事件
	h.emitEvent(&ReloadEvent{
		Type:      ReloadEventReloadDone,
		Timestamp: time.Now(),
	})

	return nil
}

// watch 监听文件变化
func (h *HotReload) watch() {
	defer h.watcher.Close()

	for {
		select {
		case <-h.ctx.Done():
			return
		case event, ok := <-h.watcher.Events:
			if !ok {
				return
			}
			h.handleEvent(event)
		case err, ok := <-h.watcher.Errors:
			if !ok {
				return
			}
			h.emitEvent(&ReloadEvent{
				Type:      ReloadEventError,
				Error:     err.Error(),
				Timestamp: time.Now(),
			})
		}
	}
}

// handleEvent 处理文件事件
func (h *HotReload) handleEvent(event fsnotify.Event) {
	h.mu.Lock()
	enabled := h.enabled
	debounceTime := h.debounceTime
	h.mu.Unlock()

	if !enabled {
		return
	}

	// 目录级事件必须优先于清单文件过滤处理：
	//   - 新建目录：动态安装 skill 的第一步（mkdir + 写入 SKILL.md/skill.yaml）。
	//     Linux inotify 的目录监听是非递归的，新建的子目录不会被父目录监听覆盖，
	//     这里立即登记监听并安排一次目录补齐扫描，避免“装完不生效”；
	//   - 删除/重命名目录：注销目录内已登记的 skill，避免 registry 残留幽灵技能。
	// 另外先推进候选根：启动时不存在的标准安装位一旦被创建即自动接管。
	if event.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			h.advancePendingRoots(event.Name)
		}
	}
	if event.Op&fsnotify.Create == fsnotify.Create {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			h.handleDirectoryCreate(event.Name)
			h.scheduleDirectoryScan(event.Name)
			return
		}
	}
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 && h.isWatchedDir(event.Name) {
		h.unwatchDir(event.Name)
		h.removeSkillsUnderDir(event.Name)
		return
	}

	manifestPath, kind := skillManifestPathForWatchedFile(event.Name)
	if manifestPath == "" {
		return
	}

	// 防抖处理
	h.mu.Lock()
	lastTime, exists := h.debounceMap[event.Name]
	if exists {
		elapsed := time.Since(lastTime)
		if elapsed < debounceTime {
			h.mu.Unlock()
			return
		}
	}
	h.debounceMap[event.Name] = time.Now()
	h.mu.Unlock()

	// 延迟后处理，确保文件写入完成
	time.AfterFunc(debounceTime, func() {
		h.processFile(manifestPath, kind, event.Op)
	})
}

// handleDirectoryCreate 处理目录创建事件
func (h *HotReload) handleDirectoryCreate(path string) {
	if err := h.watchDir(path); err != nil {
		// 添加失败通常意味着路径不存在或已在监听，忽略错误。
		return
	}
	_ = h.addSubdirectories(path)
}

// watchDir 登记目录监听并记账（幂等）。
func (h *HotReload) watchDir(path string) error {
	path = canonicalizeSkillTreePath(path, false)
	if path == "" {
		return nil
	}
	h.mu.Lock()
	if _, exists := h.watchedDirs[path]; exists {
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()

	if err := h.watcher.Add(path); err != nil {
		return err
	}
	h.mu.Lock()
	h.watchedDirs[path] = struct{}{}
	h.mu.Unlock()
	return nil
}

// unwatchDir 移除目录监听与记账（含此前登记过的子目录）。
func (h *HotReload) unwatchDir(path string) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return
	}
	prefix := path + string(os.PathSeparator)

	h.mu.Lock()
	removed := make([]string, 0, 1)
	for watched := range h.watchedDirs {
		if skillWatchPathEqual(watched, path) || strings.HasPrefix(filepath.Clean(watched), prefix) {
			removed = append(removed, watched)
			delete(h.watchedDirs, watched)
		}
	}
	h.mu.Unlock()

	for _, watched := range removed {
		_ = h.watcher.Remove(watched)
	}
}

// isWatchedDir 报告路径是否是已登记的监听目录。
func (h *HotReload) isWatchedDir(path string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for watched := range h.watchedDirs {
		if skillWatchPathEqual(watched, path) {
			return true
		}
	}
	return false
}

// scheduleDirectoryScan 在新目录出现后安排一次防抖补齐扫描：安装动作可能是
// “先建目录再写文件”，目录监听登记存在竞态窗口，直接扫目录可以兜住丢失的事件。
// 同一个目录只保留一次待执行扫描。
func (h *HotReload) scheduleDirectoryScan(dir string) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" {
		return
	}
	h.mu.Lock()
	if _, exists := h.scheduledDirScans[dir]; exists {
		h.mu.Unlock()
		return
	}
	h.scheduledDirScans[dir] = struct{}{}
	debounceTime := h.debounceTime
	h.mu.Unlock()

	time.AfterFunc(debounceTime, func() {
		h.mu.Lock()
		delete(h.scheduledDirScans, dir)
		h.mu.Unlock()
		h.reloadSkillsUnderDir(dir)
	})
}

// reloadSkillsUnderDir 扫描一个目录下的 skill 清单并注册/刷新（动态安装补齐）。
func (h *HotReload) reloadSkillsUnderDir(dir string) {
	if h == nil || h.registry == nil {
		return
	}
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()

	seen := make(map[string]struct{})
	_, err := walkSkillTree(dir, !isCodexSystemSkillRoot(dir), func(entry skillTreeEntry) error {
		if entry.Info == nil || entry.Info.IsDir() || !shouldParseSkillManifest(entry.Path) {
			return nil
		}
		path := filepath.Clean(entry.Path)
		if _, exists := seen[path]; exists {
			return nil
		}
		seen[path] = struct{}{}
		h.reloadSkill(path)
		return nil
	})
	if err != nil {
		h.emitEvent(&ReloadEvent{
			Type:      ReloadEventError,
			FilePath:  dir,
			Error:     fmt.Sprintf("failed to scan new skill directory: %v", err),
			Timestamp: time.Now(),
		})
	}
}

// removeSkillsUnderDir 注销目录下已登记的 skill（目录被删除/重命名时）。
func (h *HotReload) removeSkillsUnderDir(dir string) {
	if h == nil || h.registry == nil {
		return
	}
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" {
		return
	}
	prefix := dir + string(os.PathSeparator)

	h.mu.RLock()
	paths := make([]string, 0, len(h.skillFiles))
	for path := range h.skillFiles {
		if skillWatchPathEqual(path, dir) || strings.HasPrefix(filepath.Clean(path), prefix) {
			paths = append(paths, path)
		}
	}
	h.mu.RUnlock()

	for _, path := range paths {
		h.removeSkillByManifest(path)
	}
}

// skillWatchPathEqual 比较监听记账路径；Windows 文件系统大小写不敏感。
func skillWatchPathEqual(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

type skillManifestKind int

const (
	skillManifestKindUnknown skillManifestKind = iota
	skillManifestKindManifest
	skillManifestKindCompanion
)

func skillManifestPathForWatchedFile(path string) (string, skillManifestKind) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return "", skillManifestKindUnknown
	}

	base := filepath.Base(path)
	switch {
	case strings.EqualFold(base, "prompt.md"):
		if manifest := locateLegacySkillManifest(path); manifest != "" {
			return manifest, skillManifestKindCompanion
		}
		return "", skillManifestKindUnknown
	case strings.EqualFold(base, "openai.yaml") && strings.EqualFold(filepath.Base(filepath.Dir(path)), "agents"):
		return codexSkillPathForMetadataPath(path), skillManifestKindCompanion
	default:
		if shouldParseSkillManifest(path) {
			return path, skillManifestKindManifest
		}
		return "", skillManifestKindUnknown
	}
}

func locateLegacySkillManifest(path string) string {
	dir := filepath.Dir(filepath.Clean(strings.TrimSpace(path)))
	for _, candidate := range []string{
		"skill.yaml",
		"skill.yml",
		"system.yaml",
		"system.yml",
		"extra.yaml",
		"extra.yml",
	} {
		manifest := filepath.Join(dir, candidate)
		if info, err := os.Stat(manifest); err == nil && !info.IsDir() {
			return manifest
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	candidates := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.EqualFold(name, "prompt.md") || strings.EqualFold(name, "SKILL.md") || strings.EqualFold(name, "openai.yaml") {
			continue
		}
		manifest := filepath.Join(dir, name)
		if !shouldParseSkillManifest(manifest) {
			continue
		}
		candidates = append(candidates, manifest)
	}
	sort.Strings(candidates)
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

// processFile 处理文件变更
func (h *HotReload) processFile(manifestPath string, kind skillManifestKind, op fsnotify.Op) {
	h.mu.Lock()
	enabled := h.enabled
	h.mu.Unlock()

	if !enabled {
		return
	}

	// 如果是删除事件
	if op&fsnotify.Remove == fsnotify.Remove {
		if kind == skillManifestKindManifest {
			h.removeSkillByManifest(manifestPath)
			return
		}
		h.reloadSkill(manifestPath)
		return
	}

	// 其他事件（创建、写入、重命名）
	if op&fsnotify.Create == fsnotify.Create ||
		op&fsnotify.Write == fsnotify.Write ||
		op&fsnotify.Rename == fsnotify.Rename {
		// 重加载 skill
		h.reloadSkill(manifestPath)
	}
}

func (h *HotReload) discoverSkillStub(filePath string) (*Skill, error) {
	if h == nil || h.loader == nil {
		return nil, fmt.Errorf("hot reload loader is not configured")
	}

	summary, err := h.loader.DiscoverFile(filePath)
	if err != nil {
		return nil, err
	}
	if summary == nil {
		return nil, fmt.Errorf("skill summary is nil")
	}
	stub := summary.ToSkillStub()
	if stub == nil {
		return nil, fmt.Errorf("skill stub is nil")
	}
	if stub.Source == nil {
		stub.Source = &SkillSource{}
	}
	stub.Source.Path = filePath
	stub.Source.Dir = filepath.Dir(filePath)
	stub.Source.Layer = h.sourceLayerForFile(filePath)
	stub.Source.DiscoveryOnly = true
	return stub, nil
}

// reloadSkill 重加载单个 skill
func (h *HotReload) reloadSkill(filePath string) {
	if h.registry == nil {
		return
	}
	// discovery skill stub
	skill, err := h.discoverSkillStub(filePath)
	if err != nil {
		h.emitEvent(&ReloadEvent{
			Type:      ReloadEventError,
			FilePath:  filePath,
			Error:     fmt.Sprintf("failed to load skill: %v", err),
			Timestamp: time.Now(),
		})
		return
	}
	InvalidateHydratedSkill(skillHydrationCacheKey(skill))
	if h.registry != nil {
		h.registry.InvalidateLoadedSkill(skillHydrationCacheKey(skill))
	}

	existing, exists := h.registry.GetByPath(filePath)
	if !exists && skillSourceFormat(skill) != SkillSourceFormatCodex {
		if existingByName, ok := h.registry.Get(skill.Name); ok {
			if !h.shouldReplaceExistingSkill(existingByName, filePath) {
				return
			}
			if existingByName != nil && existingByName.Source != nil {
				h.registry.UnregisterByPath(skillIdentityPath(existingByName))
			} else {
				h.registry.Unregister(skill.Name)
			}
		}
	} else if exists && existing != nil {
		h.registry.UnregisterByPath(filePath)
	}

	if err := h.registry.Register(skill); err != nil {
		h.emitEvent(&ReloadEvent{
			Type:      ReloadEventError,
			SkillName: skill.Name,
			FilePath:  filePath,
			Error:     fmt.Sprintf("failed to register skill: %v", err),
			Timestamp: time.Now(),
		})
		return
	}

	h.mu.Lock()
	h.skillFiles[filePath] = skill.Name
	h.mu.Unlock()

	eventType := ReloadEventSkillAdded
	if exists {
		eventType = ReloadEventSkillUpdated
	}

	h.emitEvent(&ReloadEvent{
		Type:      eventType,
		SkillName: skill.Name,
		FilePath:  filePath,
		Timestamp: time.Now(),
	})
}

func (h *HotReload) removeSkillByManifest(filePath string) {
	h.mu.Lock()
	skillName, exists := h.skillFiles[filePath]
	if exists {
		delete(h.skillFiles, filePath)
	}
	h.mu.Unlock()

	InvalidateHydratedSkill(filePath)
	InvalidateHydratedSkill(skillName)

	if !exists || skillName == "" {
		if h.registry != nil {
			if tracked, ok := h.registry.GetByPath(filePath); ok && tracked != nil {
				skillName = tracked.Name
				exists = true
			} else {
				return
			}
		} else {
			return
		}
	}

	if h.registry != nil {
		h.registry.InvalidateLoadedSkill(filePath)
		h.registry.InvalidateLoadedSkill(skillName)
		h.registry.UnregisterByPath(filePath)
		h.registry.Unregister(skillName)
	}
	h.emitEvent(&ReloadEvent{
		Type:      ReloadEventSkillRemoved,
		SkillName: skillName,
		FilePath:  filePath,
		Timestamp: time.Now(),
	})
}

func (h *HotReload) reloadAllSkills(skillDirs []string) error {
	InvalidateAllHydratedSkills()
	if h.registry != nil {
		h.registry.ClearLoadedCache()
	}
	normalized := normalizeSkillDirs(skillDirs)
	if len(normalized) == 0 {
		return fmt.Errorf("skill directory is empty")
	}

	type fileSkill struct {
		filePath string
		skill    *Skill
	}

	loaded := make([]fileSkill, 0)
	seenSkillPaths := make(map[string]struct{})
	for _, skillDir := range normalized {
		_, err := walkSkillTree(skillDir, !isCodexSystemSkillRoot(skillDir), func(entry skillTreeEntry) error {
			if entry.Info == nil || entry.Info.IsDir() {
				return nil
			}

			if !shouldParseSkillManifest(entry.Path) {
				return nil
			}

			skill, loadErr := h.discoverSkillStub(entry.Path)
			if loadErr != nil {
				return loadErr
			}
			pathKey := skillIdentityPath(skill)
			if pathKey == "" {
				pathKey = entry.Path
			}
			if _, exists := seenSkillPaths[pathKey]; exists {
				return nil
			}
			seenSkillPaths[pathKey] = struct{}{}
			loaded = append(loaded, fileSkill{filePath: entry.Path, skill: skill})
			return nil
		})
		if err != nil {
			return err
		}
	}

	if h.registry == nil {
		return fmt.Errorf("registry is not configured")
	}
	h.registry.Clear()
	newSkillFiles := make(map[string]string, len(loaded))
	for _, item := range loaded {
		if err := h.registry.Register(item.skill); err != nil {
			return err
		}
		if stored, ok := h.registry.GetByPath(item.filePath); !ok || stored != item.skill {
			continue
		}
		newSkillFiles[item.filePath] = item.skill.Name
	}

	h.mu.Lock()
	h.skillFiles = newSkillFiles
	h.mu.Unlock()
	return nil
}

func (h *HotReload) sourceLayerForFile(path string) string {
	normalizedPath := filepath.Clean(path)
	for index, dir := range h.skillDirs {
		normalizedDir := filepath.Clean(dir)
		if normalizedPath == normalizedDir || strings.HasPrefix(normalizedPath, normalizedDir+string(os.PathSeparator)) {
			if isCodexSystemSkillRoot(normalizedDir) {
				return SkillSourceLayerSystem
			}
			if index == 0 {
				return SkillSourceLayerSystem
			}
			return SkillSourceLayerExternal
		}
	}
	return SkillSourceLayerUnknown
}

func (h *HotReload) shouldReplaceExistingSkill(existing *Skill, incomingPath string) bool {
	if existing == nil || existing.Source == nil || existing.Source.Path == "" {
		return true
	}

	existingPath := filepath.Clean(existing.Source.Path)
	incomingPath = filepath.Clean(incomingPath)
	if existingPath == incomingPath {
		return true
	}

	return h.sourceRankForPath(incomingPath) < h.sourceRankForPath(existingPath)
}

func (h *HotReload) sourceRankForPath(path string) int {
	normalizedPath := filepath.Clean(path)
	for index, dir := range h.skillDirs {
		normalizedDir := filepath.Clean(dir)
		if normalizedPath == normalizedDir || strings.HasPrefix(normalizedPath, normalizedDir+string(os.PathSeparator)) {
			return index
		}
	}
	return len(h.skillDirs) + 1
}

// processEvents 处理事件
func (h *HotReload) processEvents() {
	for {
		select {
		case <-h.ctx.Done():
			return
		case event := <-h.eventBuffer:
			h.mu.Lock()
			callbacks := make([]ReloadCallback, len(h.callbacks))
			copy(callbacks, h.callbacks)
			h.mu.Unlock()

			for _, callback := range callbacks {
				if callback != nil {
					callback(event)
				}
			}
		}
	}
}

// emitEvent 发送事件
func (h *HotReload) emitEvent(event *ReloadEvent) {
	select {
	case h.eventBuffer <- event:
	default:
		// buffer 已满，丢弃事件
	}
}

// addSubdirectories 递归添加子目录
func (h *HotReload) addSubdirectories(dir string) error {
	_, err := walkSkillTree(dir, !isCodexSystemSkillRoot(dir), func(entry skillTreeEntry) error {
		if entry.Info == nil || !entry.Info.IsDir() {
			return nil
		}
		if err := h.watchDir(entry.Path); err != nil {
			// 忽略错误，可能是权限问题或已监听。
		}
		return nil
	})
	return err
}

// GetStats 获取统计信息
func (h *HotReload) GetStats() map[string]interface{} {
	h.mu.RLock()
	defer h.mu.RUnlock()

	skillCount := 0
	if h.registry != nil {
		// 使用 Count() 获取数量
		skillCount = h.registry.Count()
	}

	return map[string]interface{}{
		"enabled":       h.enabled,
		"skillDir":      h.skillDir,
		"skillDirs":     append([]string(nil), h.skillDirs...),
		"skillCount":    skillCount,
		"watching":      h.scanning,
		"callbackCount": len(h.callbacks),
		"debounceTime":  h.debounceTime.String(),
	}
}

// GetEvents 获取事件通道（用于外部监听）
func (h *HotReload) GetEvents() <-chan *ReloadEvent {
	return h.eventBuffer
}

// GetRegistry 获取 Registry
func (h *HotReload) GetRegistry() *Registry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.registry
}

// GetLoader 获取 Loader
func (h *HotReload) GetLoader() *Loader {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.loader
}
