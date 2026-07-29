package ui

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/motion"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// Progress 进度条组件
type Progress struct {
	mu             sync.Mutex
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
	if total < 0 {
		total = 0
	}
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
	p.mu.Lock()
	defer p.mu.Unlock()
	p.theme = theme
	return p
}

// SetWidth 设置进度条宽度
func (p *Progress) SetWidth(width int) *Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	if width < 0 {
		width = 0
	}
	p.width = width
	return p
}

// ShowPercent 设置是否显示百分比
func (p *Progress) ShowPercent(show bool) *Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.showPercent = show
	return p
}

// ShowBar 设置是否显示进度条
func (p *Progress) ShowBar(show bool) *Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.showBar = show
	return p
}

// ShowSpinner 设置是否显示旋转器
func (p *Progress) ShowSpinner(show bool) *Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.showSpinner = show
	return p
}

// SetUpdateInterval 设置更新间隔
func (p *Progress) SetUpdateInterval(interval time.Duration) *Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updateInterval = interval
	return p
}

// Increment 增加进度
func (p *Progress) Increment(delta int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current += delta
}

// Set 设置当前进度
func (p *Progress) Set(current int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.current = current
}

// Update 更新进度
func (p *Progress) Update(current int64) {
	p.mu.Lock()
	p.current = current

	// 控制更新频率
	now := time.Now()
	if now.Sub(p.lastUpdate) < p.updateInterval {
		p.mu.Unlock()
		return
	}
	p.lastUpdate = now
	p.mu.Unlock()
	p.render()
}

// Render 渲染进度条
func (p *Progress) Render() {
	p.render()
}

// render 内部渲染方法
func (p *Progress) render() {
	_, _ = WriteTerminalText(os.Stdout, "\r"+p.Format())
}

// Document 返回进度条的结构化渲染模型。
func (p *Progress) Document() render.Document {
	p.mu.Lock()
	defer p.mu.Unlock()

	current := p.current
	if current < 0 {
		current = 0
	}
	if p.total > 0 && current > p.total {
		current = p.total
	}
	percent := 0.0
	if p.total > 0 {
		percent = float64(current) / float64(p.total)
	}
	spans := make([]render.Span, 0, 4)
	if p.showSpinner {
		policy := motion.Global()
		if policy.NeedsNextFrame() {
			p.spinnerFrame = (p.spinnerFrame + 1) % GetSpinnerFrameCount()
		}
		spinner := policy.ActivityFrame(time.Now())
		if spinner == " " || spinner == "" {
			spinner = GetSpinner(p.spinnerFrame)
		}
		spans = append(spans, semanticSpan(" "+spinner+" ", style.RoleProgress, true))
	}
	if p.showBar {
		filled := int(percent * float64(p.width))
		bar := "[" + strings.Repeat("█", filled) + strings.Repeat(" ", p.width-filled) + "]"
		spans = append(spans, semanticSpan(bar, style.RoleProgress, false))
	}
	if p.showPercent && p.total > 0 {
		spans = append(spans, semanticSpan(fmt.Sprintf(" %.1f%%", percent*100), style.RoleTextSecondary, false))
	}
	spans = append(spans, semanticSpan(fmt.Sprintf(" %d/%d", current, p.total), style.RoleTextMuted, false))
	return render.SingleLineDoc(spans...)
}

// Format 通过统一主题解析器编码进度条。
func (p *Progress) Format() string {
	p.mu.Lock()
	theme := p.theme
	p.mu.Unlock()
	return renderDocumentWithProfile(p.Document(), theme)
}

// Done 完成进度（换行）
func (p *Progress) Done() {
	p.mu.Lock()
	p.current = p.total
	p.mu.Unlock()
	p.render()
	_, _ = WriteTerminalLine(os.Stdout, "")
}

// Spinner 独立的旋转器组件
type Spinner struct {
	mu             sync.Mutex
	theme          *Theme
	message        string
	frame          int
	running        bool
	stopChan       chan struct{}
	lastWidth      int
	lastUpdate     time.Time
	updateInterval time.Duration
}

// NewSpinner 创建新的旋转器
func NewSpinner(message string) *Spinner {
	return &Spinner{
		theme:          GetTheme(ThemeAuto),
		message:        message,
		frame:          0,
		running:        false,
		stopChan:       make(chan struct{}),
		updateInterval: 100 * time.Millisecond,
	}
}

// SetTheme 设置主题
func (s *Spinner) SetTheme(theme *Theme) *Spinner {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.theme = theme
	return s
}

// SetMessage 设置消息
func (s *Spinner) SetMessage(msg string) *Spinner {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.message = msg
	return s
}

// SetUpdateInterval 设置更新间隔
func (s *Spinner) SetUpdateInterval(interval time.Duration) *Spinner {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateInterval = interval
	return s
}

// Start 启动旋转器
func (s *Spinner) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.stopChan = make(chan struct{})
	stopChan := s.stopChan
	updateInterval := s.updateInterval
	s.mu.Unlock()

	go func() {
		policy := motion.Global()
		// Components must not invent tickers when motion is Off.
		if !policy.NeedsNextFrame() {
			s.renderIfCurrent(stopChan)
			return
		}
		interval := policy.Interval()
		if interval <= 0 {
			interval = updateInterval
		}
		if interval <= 0 {
			interval = 80 * time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s.renderIfCurrent(stopChan)
			case <-stopChan:
				return
			}
		}
	}()
}

// Stop 停止旋转器
func (s *Spinner) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stopChan)
	width := s.lastWidth
	s.lastWidth = 0
	s.mu.Unlock()

	// 清除旋转器行
	_, _ = WriteTerminalText(os.Stdout, "\r"+strings.Repeat(" ", width)+"\r")
}

func (s *Spinner) renderIfCurrent(stopChan chan struct{}) {
	s.mu.Lock()
	if !s.running || s.stopChan != stopChan {
		s.mu.Unlock()
		return
	}
	doc := spinnerDocument(s.frame, s.message)
	theme := s.theme
	s.frame = (s.frame + 1) % GetSpinnerFrameCount()
	s.lastWidth = render.Width(doc.PlainText())
	s.mu.Unlock()
	_, _ = WriteTerminalText(os.Stdout, "\r"+renderDocumentWithProfile(doc, theme))
}

func spinnerDocument(frame int, message string) render.Document {
	spinner := motion.Global().ActivityFrame(time.Now())
	if spinner == " " || spinner == "" {
		spinner = GetSpinner(frame)
	}
	message = strings.Join(strings.Fields(SanitizeTerminalText(message)), " ")
	spans := []render.Span{
		semanticSpan(spinner+" ", style.RoleProgress, true),
		semanticSpan("loading", style.RoleProgress, false),
	}
	if message != "" {
		spans = append(spans, semanticSpan(" "+message, style.RoleTextPrimary, false))
	}
	return render.SingleLineDoc(spans...)
}

// Document 返回旋转器当前帧的结构化模型。
func (s *Spinner) Document() render.Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	return spinnerDocument(s.frame, s.message)
}

// Format 通过统一主题解析器编码旋转器当前帧。
func (s *Spinner) Format() string {
	s.mu.Lock()
	theme := s.theme
	doc := spinnerDocument(s.frame, s.message)
	s.mu.Unlock()
	return renderDocumentWithProfile(doc, theme)
}

// PrintProgress 快捷方法：打印简单的进度指示
func PrintProgress(current, total int64, message string) {
	message = strings.Join(strings.Fields(SanitizeTerminalText(message)), " ")
	doc := render.SingleLineDoc(
		semanticSpan(message+": ", style.RoleTextPrimary, false),
		semanticSpan(fmt.Sprintf("%d/%d", current, total), style.RoleProgress, false),
	)
	_, _ = WriteTerminalText(os.Stdout, renderDocumentWithProfile(doc, GetTheme(ThemeAuto)))
}

// PrintSpinner 快捷方法：打印一帧旋转器
func PrintSpinner(message string) {
	doc := spinnerDocument(0, message)
	_, _ = WriteTerminalText(os.Stdout, renderDocumentWithProfile(doc, GetTheme(ThemeAuto)))
}
