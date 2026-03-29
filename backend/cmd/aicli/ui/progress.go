package ui

import (
	"fmt"
	"strings"
	"time"
)

// Progress 进度条组件
type Progress struct {
	theme          *Theme
	total          int64
	current        int64
	width          int
	showPercent    bool
	showBar        bool
	showSpinner    bool
	spinnerFrame   int
	lastUpdate     time.Time
	updateInterval time.Duration
}

// NewProgress 创建新的进度条
func NewProgress(total int64) *Progress {
	return &Progress{
		theme:          GetTheme(ThemeAuto),
		total:          total,
		current:        0,
		width:          40,
		showPercent:    true,
		showBar:        true,
		showSpinner:    false,
		lastUpdate:     time.Now(),
		updateInterval: 100 * time.Millisecond,
	}
}

// SetTheme 设置主题
func (p *Progress) SetTheme(theme *Theme) *Progress {
	p.theme = theme
	return p
}

// SetWidth 设置进度条宽度
func (p *Progress) SetWidth(width int) *Progress {
	p.width = width
	return p
}

// ShowPercent 设置是否显示百分比
func (p *Progress) ShowPercent(show bool) *Progress {
	p.showPercent = show
	return p
}

// ShowBar 设置是否显示进度条
func (p *Progress) ShowBar(show bool) *Progress {
	p.showBar = show
	return p
}

// ShowSpinner 设置是否显示旋转器
func (p *Progress) ShowSpinner(show bool) *Progress {
	p.showSpinner = show
	return p
}

// SetUpdateInterval 设置更新间隔
func (p *Progress) SetUpdateInterval(interval time.Duration) *Progress {
	p.updateInterval = interval
	return p
}

// Increment 增加进度
func (p *Progress) Increment(delta int64) {
	p.current += delta
}

// Set 设置当前进度
func (p *Progress) Set(current int64) {
	p.current = current
}

// Update 更新进度
func (p *Progress) Update(current int64) {
	p.current = current

	// 控制更新频率
	now := time.Now()
	if now.Sub(p.lastUpdate) < p.updateInterval {
		return
	}
	p.lastUpdate = now

	p.render()
}

// Render 渲染进度条
func (p *Progress) Render() {
	p.render()
}

// render 内部渲染方法
func (p *Progress) render() {
	var parts []string

	// Spinner
	if p.showSpinner {
		p.spinnerFrame = (p.spinnerFrame + 1) % GetSpinnerFrameCount()
		spinner := GetSpinner(p.spinnerFrame)
		parts = append(parts, fmt.Sprintf(" %s ", spinner))
	}

	// 进度条
	if p.showBar {
		percent := float64(p.current) / float64(p.total)
		if p.total == 0 {
			percent = 0
		}
		if percent > 1 {
			percent = 1
		}

		filled := int(percent * float64(p.width))
		bar := fmt.Sprintf("[%s%s]",
			strings.Repeat("█", filled),
			strings.Repeat(" ", p.width-filled))
		parts = append(parts, p.theme.ProgressColor.Sprint(bar))
	}

	// 百分比
	if p.showPercent && p.total > 0 {
		percent := float64(p.current) / float64(p.total) * 100
		parts = append(parts, fmt.Sprintf(" %.1f%%", percent))
	}

	// 数值
	parts = append(parts, fmt.Sprintf(" %d/%d", p.current, p.total))

	// 使用 \r 回车，不换行
	fmt.Printf("\r%s", strings.Join(parts, ""))
}

// Done 完成进度（换行）
func (p *Progress) Done() {
	p.current = p.total
	p.render()
	fmt.Println()
}

// Spinner 独立的旋转器组件
type Spinner struct {
	theme        *Theme
	message      string
	frame        int
	running      bool
	stopChan     chan struct{}
	lastUpdate   time.Time
	updateInterval time.Duration
}

// NewSpinner 创建新的旋转器
func NewSpinner(message string) *Spinner {
	return &Spinner{
		theme:        GetTheme(ThemeAuto),
		message:      message,
		frame:        0,
		running:      false,
		stopChan:     make(chan struct{}),
		updateInterval: 100 * time.Millisecond,
	}
}

// SetTheme 设置主题
func (s *Spinner) SetTheme(theme *Theme) *Spinner {
	s.theme = theme
	return s
}

// SetMessage 设置消息
func (s *Spinner) SetMessage(msg string) *Spinner {
	s.message = msg
	return s
}

// SetUpdateInterval 设置更新间隔
func (s *Spinner) SetUpdateInterval(interval time.Duration) *Spinner {
	s.updateInterval = interval
	return s
}

// Start 启动旋转器
func (s *Spinner) Start() {
	if s.running {
		return
	}
	s.running = true

	go func() {
		ticker := time.NewTicker(s.updateInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if s.running {
					s.render()
					s.frame = (s.frame + 1) % GetSpinnerFrameCount()
				}
			case <-s.stopChan:
				return
			}
		}
	}()
}

// Stop 停止旋转器
func (s *Spinner) Stop() {
	if !s.running {
		return
	}
	s.running = false
	close(s.stopChan)

	// 清除旋转器行
	fmt.Printf("\r%s\r", strings.Repeat(" ", len(s.message)+4))
}

// render 渲染旋转器
func (s *Spinner) render() {
	spinner := GetSpinner(s.frame)
	fmt.Printf("\r%s %s %s", spinner, s.theme.ProgressColor.Sprint("loading"), s.message)
}

// PrintProgress 快捷方法：打印简单的进度指示
func PrintProgress(current, total int64, message string) {
	fmt.Printf("%s: %d/%d", message, current, total)
}

// PrintSpinner 快捷方法：打印一帧旋转器
func PrintSpinner(message string) {
	theme := GetTheme(ThemeAuto)
	frame := 0
	spinner := GetSpinner(frame)
	fmt.Printf("%s %s %s", spinner, theme.ProgressColor.Sprint("loading"), message)
}
