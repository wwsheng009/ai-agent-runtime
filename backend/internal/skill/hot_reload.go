package skill

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
}

// NewHotReload 创建热加载器
func NewHotReload(loader *Loader, registry *Registry) (*HotReload, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &HotReload{
		watcher:      watcher,
		loader:       loader,
		registry:     registry,
		callbacks:    make([]ReloadCallback, 0),
		eventBuffer:  make(chan *ReloadEvent, 100),
		debounceMap:  make(map[string]time.Time),
		skillFiles:   make(map[string]string),
		ctx:          ctx,
		cancel:       cancel,
		enabled:      true,
		debounceTime: 500 * time.Millisecond, // 默认防抖时间
	}, nil
}

// Start 启动热加载
func (h *HotReload) Start(skillDir string) error {
	return h.StartMany([]string{skillDir})
}

// StartMany 启动多个目录的热加载
func (h *HotReload) StartMany(skillDirs []string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

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

	h.skillDirs = normalized
	h.skillDir = normalized[0]

	for _, dir := range normalized {
		if err := h.watcher.Add(dir); err != nil {
			return fmt.Errorf("failed to add directory to watcher: %w", err)
		}
		if err := h.addSubdirectories(dir); err != nil {
			return fmt.Errorf("failed to add subdirectories: %w", err)
		}
	}

	h.scanning = true

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

	// 只处理 skill.yaml 和 skill.yml
	ext := filepath.Ext(event.Name)
	if ext != ".yaml" && ext != ".yml" {
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
		h.processFile(event.Name, event.Op)
	})

	// 处理目录创建事件（监听新创建的子目录）
	if event.Op&fsnotify.Create == fsnotify.Create {
		h.handleDirectoryCreate(event.Name)
	}
}

// handleDirectoryCreate 处理目录创建事件
func (h *HotReload) handleDirectoryCreate(path string) {
	// fsnotify.Watcher 不提供直接检查是否监听的方法
	// 直接尝试添加目录监听，如果已监听 fsnotify 会返回错误
	if err := h.watcher.Add(path); err == nil {
		return // 成功添加
	}
	// 添加失败通常意味着已经在监听，忽略错误
}

// processFile 处理文件变更
func (h *HotReload) processFile(filePath string, op fsnotify.Op) {
	h.mu.Lock()
	enabled := h.enabled
	h.mu.Unlock()

	if !enabled {
		return
	}

	// 如果是删除事件
	if op&fsnotify.Remove == fsnotify.Remove {
		h.removeSkillByFile(filePath)
		return
	}

	// 其他事件（创建、写入、重命名）
	if op&fsnotify.Create == fsnotify.Create ||
		op&fsnotify.Write == fsnotify.Write ||
		op&fsnotify.Rename == fsnotify.Rename {
		// 重加载 skill
		h.reloadSkill(filePath)
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
	InvalidateHydratedSkill(filePath)
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
	if h.registry != nil {
		h.registry.InvalidateLoadedSkill(skill.Name)
	}

	// 检查是否已存在
	existing, exists := h.registry.Get(skill.Name)

	if exists {
		if !h.shouldReplaceExistingSkill(existing, filePath) {
			return
		}

		// 更新 skill - 先删除再重新注册
		h.registry.Unregister(skill.Name)
		if err := h.registry.Register(skill); err != nil {
			h.emitEvent(&ReloadEvent{
				Type:      ReloadEventError,
				SkillName: skill.Name,
				FilePath:  filePath,
				Error:     fmt.Sprintf("failed to update skill: %v", err),
				Timestamp: time.Now(),
			})
			return
		}

		h.mu.Lock()
		h.skillFiles[filePath] = skill.Name
		h.mu.Unlock()

		h.emitEvent(&ReloadEvent{
			Type:      ReloadEventSkillUpdated,
			SkillName: skill.Name,
			FilePath:  filePath,
			Timestamp: time.Now(),
		})
	} else {
		// 新增 skill
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

		h.emitEvent(&ReloadEvent{
			Type:      ReloadEventSkillAdded,
			SkillName: skill.Name,
			FilePath:  filePath,
			Timestamp: time.Now(),
		})
	}
}

func (h *HotReload) removeSkillByFile(filePath string) {
	InvalidateHydratedSkill(filePath)
	h.mu.Lock()
	skillName, exists := h.skillFiles[filePath]
	if exists {
		delete(h.skillFiles, filePath)
	}
	h.mu.Unlock()

	if !exists || skillName == "" {
		h.emitEvent(&ReloadEvent{
			Type:      ReloadEventError,
			FilePath:  filePath,
			Error:     "skill file removed but mapping not found",
			Timestamp: time.Now(),
		})
		return
	}

	if h.registry != nil {
		h.registry.InvalidateLoadedSkill(skillName)
	}
	h.registry.Unregister(skillName)
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
	seenSkillNames := make(map[string]struct{})
	for _, skillDir := range normalized {
		err := filepath.Walk(skillDir, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				if info.Name() != "" && info.Name()[0] == '.' {
					return filepath.SkipDir
				}
				return nil
			}

			ext := filepath.Ext(path)
			if ext != ".yaml" && ext != ".yml" {
				return nil
			}

			skill, loadErr := h.discoverSkillStub(path)
			if loadErr != nil {
				return loadErr
			}
			if _, exists := seenSkillNames[skill.Name]; exists {
				return nil
			}
			seenSkillNames[skill.Name] = struct{}{}
			loaded = append(loaded, fileSkill{filePath: path, skill: skill})
			return nil
		})
		if err != nil {
			return err
		}
	}

	h.registry.Clear()
	newSkillFiles := make(map[string]string, len(loaded))
	for _, item := range loaded {
		if err := h.registry.Register(item.skill); err != nil {
			return err
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
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 跳过隐藏目录
		if filepath.Base(path)[0] == '.' {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// 添加目录监听
		if info.IsDir() {
			if err := h.watcher.Add(path); err != nil {
				// 忽略错误，可能是权限问题
			}
		}

		return nil
	})
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
