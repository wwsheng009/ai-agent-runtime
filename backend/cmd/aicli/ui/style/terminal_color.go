package style

import (
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// BackgroundKind describes terminal background luminance class.
type BackgroundKind int

const (
	BackgroundUnknown BackgroundKind = iota
	BackgroundDark
	BackgroundLight
)

// RGB is a simple 8-bit channel triple.
type RGB struct {
	R, G, B uint8
}

// ColorProfile extends render.ColorProfile with background metadata.
type ColorProfile struct {
	render.ColorProfile
	DefaultFG  *RGB
	DefaultBG  *RGB
	Background BackgroundKind
}

// DetectOptions controls color profile inference.
type DetectOptions struct {
	// Interactive is true when stdin+stdout are TTYs.
	Interactive bool
	// ANSICapable is true after VT processing is confirmed.
	ANSICapable bool
	// ColorOverride: "", "auto", "always", "never".
	ColorOverride string
	// DepthOverride: "", "auto", "truecolor", "ansi256", "ansi16", "none".
	DepthOverride string
	// Environ supplies env lookup; nil uses os.Getenv.
	Environ func(string) string
	// DefaultFG/DefaultBG inject offline OSC 10/11 defaults (tests / host wiring).
	// When nil, DetectColorProfile may still read AICLI_OSC_FG / AICLI_OSC_BG.
	DefaultFG *RGB
	DefaultBG *RGB
	// OSCProbe is an optional live TTY probe invoked only when FG/BG are still
	// unset after injectable + env offline defaults, and AICLI_DISABLE_OSC_PROBE
	// is off. Must return quickly (see DefaultOSCProbeTimeout).
	OSCProbe OSCProbeFunc
}

func (o DetectOptions) env(key string) string {
	if o.Environ != nil {
		return o.Environ(key)
	}
	return os.Getenv(key)
}

// DetectColorProfile infers a conservative color profile from environment
// and terminal capability flags.
//
// Default color resolution order (later steps fill gaps only):
//  1. Explicit DefaultFG/DefaultBG
//  2. AICLI_OSC_FG / AICLI_OSC_BG offline payloads (unless AICLI_DISABLE_OSC_PROBE)
//  3. Optional OSCProbe live query (unless disabled; must be bounded by caller)
//
// AICLI_DISABLE_OSC_PROBE skips env + live probe but still honors explicit defaults.
func DetectColorProfile(opts DetectOptions) ColorProfile {
	env := opts.env
	override := strings.ToLower(strings.TrimSpace(opts.ColorOverride))
	if override == "" {
		override = "auto"
	}

	// Hard disable paths.
	if override == "never" || strings.TrimSpace(env("NO_COLOR")) != "" {
		return applyOSCDefaults(ColorProfile{
			ColorProfile: render.NoColorProfile(),
			Background:   detectBackground(env),
		}, opts)
	}

	forced := override == "always" || strings.TrimSpace(env("FORCE_COLOR")) != ""
	if !opts.Interactive && !forced {
		return applyOSCDefaults(ColorProfile{
			ColorProfile: render.NoColorProfile(),
			Background:   detectBackground(env),
		}, opts)
	}
	if !opts.ANSICapable && !forced {
		return applyOSCDefaults(ColorProfile{
			ColorProfile: render.NoColorProfile(),
			Background:   detectBackground(env),
		}, opts)
	}
	term := strings.ToLower(strings.TrimSpace(env("TERM")))
	if term == "dumb" && !forced {
		return applyOSCDefaults(ColorProfile{
			ColorProfile: render.NoColorProfile(),
			Background:   detectBackground(env),
		}, opts)
	}

	depth := detectDepth(opts)
	profile := ColorProfile{
		ColorProfile: render.ColorProfile{
			Enabled:    depth != render.ColorNone,
			Depth:      depth,
			Hyperlinks: depth != render.ColorNone && supportsHyperlink(env),
			Forced:     forced || strings.TrimSpace(opts.DepthOverride) != "" && strings.ToLower(opts.DepthOverride) != "auto",
		},
		Background: detectBackground(env),
	}
	return applyOSCDefaults(profile, opts)
}

// applyOSCDefaults merges injectable, env offline, and optional live OSC colors into p.
func applyOSCDefaults(p ColorProfile, opts DetectOptions) ColorProfile {
	fg := opts.DefaultFG
	bg := opts.DefaultBG
	if !envTruthy(opts.env("AICLI_DISABLE_OSC_PROBE")) {
		if fg == nil {
			if c, ok := parseOSCEnvColor(opts.env("AICLI_OSC_FG")); ok {
				cp := c
				fg = &cp
			}
		}
		if bg == nil {
			if c, ok := parseOSCEnvColor(opts.env("AICLI_OSC_BG")); ok {
				cp := c
				bg = &cp
			}
		}
		// Live probe only fills remaining gaps; never overrides injectable/env.
		if opts.OSCProbe != nil && (fg == nil || bg == nil) {
			pfg, pbg := opts.OSCProbe()
			if fg == nil && pfg != nil {
				cp := *pfg
				fg = &cp
			}
			if bg == nil && pbg != nil {
				cp := *pbg
				bg = &cp
			}
		}
	}
	if fg == nil && bg == nil {
		return p
	}
	return p.WithDefaults(fg, bg)
}

func parseOSCEnvColor(raw string) (RGB, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RGB{}, false
	}
	_, color, ok := ParseOSCColorReply(raw)
	return color, ok
}

func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func detectDepth(opts DetectOptions) render.ColorDepth {
	env := opts.env
	explicit := strings.ToLower(strings.TrimSpace(opts.DepthOverride))
	if explicit == "" || explicit == "auto" {
		explicit = strings.ToLower(strings.TrimSpace(env("AICLI_COLOR_DEPTH")))
	}
	switch explicit {
	case "none", "never", "0":
		return render.ColorNone
	case "ansi16", "16":
		return render.ColorANSI16
	case "ansi256", "256":
		return render.ColorANSI256
	case "truecolor", "24bit", "24":
		return render.ColorTrueColor
	}

	colorterm := strings.ToLower(strings.TrimSpace(env("COLORTERM")))
	if colorterm == "truecolor" || colorterm == "24bit" {
		return render.ColorTrueColor
	}
	// Windows Terminal frequently supports truecolor.
	if strings.TrimSpace(env("WT_SESSION")) != "" {
		return render.ColorTrueColor
	}
	termProgram := strings.ToLower(strings.TrimSpace(env("TERM_PROGRAM")))
	switch termProgram {
	case "vscode", "iterm.app", "hyper", "wezterm", "windows_terminal":
		return render.ColorTrueColor
	case "apple_terminal":
		return render.ColorANSI256
	}

	term := strings.ToLower(strings.TrimSpace(env("TERM")))
	switch {
	case strings.Contains(term, "truecolor") || strings.Contains(term, "24bit"):
		return render.ColorTrueColor
	case strings.Contains(term, "256color") || strings.Contains(term, "256"):
		return render.ColorANSI256
	case term == "" || term == "dumb":
		if opts.ANSICapable {
			// Conservative default on bare Windows VT.
			return render.ColorANSI16
		}
		return render.ColorNone
	default:
		if opts.ANSICapable {
			return render.ColorANSI16
		}
		return render.ColorNone
	}
}

func detectBackground(env func(string) string) BackgroundKind {
	if v := strings.TrimSpace(env("COLORFGBG")); v != "" {
		parts := strings.Split(v, ";")
		if len(parts) > 0 {
			bgRaw := strings.TrimSpace(parts[len(parts)-1])
			var bg int
			if _, err := parseInt(bgRaw, &bg); err == nil {
				if bg == 7 || bg == 15 {
					return BackgroundLight
				}
				return BackgroundDark
			}
		}
	}
	return BackgroundUnknown
}

func supportsHyperlink(env func(string) string) bool {
	if strings.TrimSpace(env("WT_SESSION")) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(env("TERM_PROGRAM"))) {
	case "iterm.app", "vscode", "hyper", "wezterm":
		return true
	}
	// Multiplexers often need passthrough; default off inside tmux/zellij.
	if strings.TrimSpace(env("TMUX")) != "" || strings.TrimSpace(env("ZELLIJ")) != "" {
		return false
	}
	return false
}

func parseInt(s string, out *int) (int, error) {
	n := 0
	if s == "" {
		return 0, errInvalidInt
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errInvalidInt
		}
		n = n*10 + int(r-'0')
	}
	*out = n
	return n, nil
}

type invalidIntError struct{}

func (invalidIntError) Error() string { return "invalid int" }

var errInvalidInt = invalidIntError{}

// RelativeLuminance computes sRGB relative luminance (0..1).
func RelativeLuminance(c RGB) float64 {
	toLinear := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return pow((s+0.055)/1.055, 2.4)
	}
	r := toLinear(c.R)
	g := toLinear(c.G)
	b := toLinear(c.B)
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// ContrastRatio returns WCAG contrast ratio between two colors (>=1).
func ContrastRatio(a, b RGB) float64 {
	l1 := RelativeLuminance(a)
	l2 := RelativeLuminance(b)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

func pow(x, y float64) float64 {
	// Small local pow to avoid math import churn in hot path; y is 2.4.
	// Use math.Pow via a thin wrapper would be fine; keep simple.
	return powFloat(x, y)
}

// ParseOSCColorReply parses a terminal OSC color reply offline.
//
// Accepted forms (xterm-compatible):
//
//	ESC ] <ps> ; rgb:<r>/<g>/<b> BEL
//	ESC ] <ps> ; rgb:<r>/<g>/<b> ST   (ESC \)
//	] <ps> ; rgb:<r>/<g>/<b>          (ESC already stripped)
//	rgb:<r>/<g>/<b>                   (bare payload; ps left 0)
//
// Each channel is 1–4 hex digits. Values are scaled to 8-bit RGB.
// Used by both offline env defaults and bounded live OSC probes.
func ParseOSCColorReply(raw string) (ps int, color RGB, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, RGB{}, false
	}
	// Normalize common terminators / wrappers.
	s = strings.TrimPrefix(s, "\x1b")
	s = strings.TrimPrefix(s, "]")
	if i := strings.IndexByte(s, '\x07'); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, "\x1b\\"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)

	payload := s
	if strings.HasPrefix(strings.ToLower(payload), "rgb:") {
		ps = 0
	} else {
		semi := strings.IndexByte(payload, ';')
		if semi <= 0 {
			return 0, RGB{}, false
		}
		psRaw := strings.TrimSpace(payload[:semi])
		if _, err := parseInt(psRaw, &ps); err != nil {
			return 0, RGB{}, false
		}
		payload = strings.TrimSpace(payload[semi+1:])
	}

	lower := strings.ToLower(payload)
	if !strings.HasPrefix(lower, "rgb:") {
		return 0, RGB{}, false
	}
	rest := payload[4:]
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return 0, RGB{}, false
	}
	r, okR := parseHexChannel(parts[0])
	g, okG := parseHexChannel(parts[1])
	b, okB := parseHexChannel(parts[2])
	if !okR || !okG || !okB {
		return 0, RGB{}, false
	}
	return ps, RGB{R: r, G: g, B: b}, true
}

// parseHexChannel scales a 1–4 digit hex channel to 8-bit.
func parseHexChannel(s string) (uint8, bool) {
	s = strings.TrimSpace(s)
	n := len(s)
	if n < 1 || n > 4 {
		return 0, false
	}
	v := 0
	for i := 0; i < n; i++ {
		c := s[i]
		var d int
		switch {
		case c >= '0' && c <= '9':
			d = int(c - '0')
		case c >= 'a' && c <= 'f':
			d = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = int(c-'A') + 10
		default:
			return 0, false
		}
		v = (v << 4) | d
	}
	max := (1 << (4 * n)) - 1
	if max <= 0 {
		return 0, false
	}
	// Round to nearest 8-bit value.
	scaled := (v*255 + max/2) / max
	if scaled > 255 {
		scaled = 255
	}
	return uint8(scaled), true
}

// WithDefaults returns a copy of p with DefaultFG/DefaultBG filled from OSC
// replies (or any offline source). When bg is set, Background is derived from
// relative luminance (>= 0.5 => light).
func (p ColorProfile) WithDefaults(fg, bg *RGB) ColorProfile {
	out := p
	if fg != nil {
		c := *fg
		out.DefaultFG = &c
	}
	if bg != nil {
		c := *bg
		out.DefaultBG = &c
		if RelativeLuminance(c) >= 0.5 {
			out.Background = BackgroundLight
		} else {
			out.Background = BackgroundDark
		}
	}
	return out
}
