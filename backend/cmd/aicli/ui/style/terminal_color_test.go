package style

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

func TestDetectColorProfileNOCOLOR(t *testing.T) {
	env := map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor"}
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ:     func(k string) string { return env[k] },
	})
	if p.Enabled || p.Depth != render.ColorNone {
		t.Fatalf("NO_COLOR should disable: %+v", p)
	}
}

func TestDetectColorProfileTrueColor(t *testing.T) {
	env := map[string]string{"COLORTERM": "truecolor", "TERM": "xterm-256color"}
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ:     func(k string) string { return env[k] },
	})
	if p.Depth != render.ColorTrueColor {
		t.Fatalf("depth=%v want truecolor", p.Depth)
	}
}

func TestDetectColorProfileANSI256(t *testing.T) {
	env := map[string]string{"TERM": "xterm-256color"}
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ:     func(k string) string { return env[k] },
	})
	if p.Depth != render.ColorANSI256 {
		t.Fatalf("depth=%v want ansi256", p.Depth)
	}
}

func TestDetectColorProfileDepthOverride(t *testing.T) {
	env := map[string]string{"COLORTERM": "truecolor"}
	p := DetectColorProfile(DetectOptions{
		Interactive:   true,
		ANSICapable:   true,
		DepthOverride: "ansi16",
		Environ:       func(k string) string { return env[k] },
	})
	if p.Depth != render.ColorANSI16 {
		t.Fatalf("override failed: %+v", p)
	}
}

func TestDetectColorProfileAutoDepthHonorsEnvironmentOverride(t *testing.T) {
	env := map[string]string{
		"AICLI_COLOR_DEPTH": "ansi16",
		"COLORTERM":         "truecolor",
	}
	p := DetectColorProfile(DetectOptions{
		Interactive:   true,
		ANSICapable:   true,
		DepthOverride: "auto",
		Environ:       func(k string) string { return env[k] },
	})
	if p.Depth != render.ColorANSI16 {
		t.Fatalf("environment override lost behind auto option: %+v", p)
	}
}

func TestDetectBackgroundCOLORFGBG(t *testing.T) {
	env := map[string]string{"COLORFGBG": "0;15"}
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ:     func(k string) string { return env[k] },
	})
	if p.Background != BackgroundLight {
		t.Fatalf("bg=%v want light", p.Background)
	}
}

func TestContrastRatio(t *testing.T) {
	// Black on white should be high.
	ratio := ContrastRatio(RGB{0, 0, 0}, RGB{255, 255, 255})
	if ratio < 20 {
		t.Fatalf("contrast too low: %v", ratio)
	}
}

func TestParseOSCColorReply(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ps   int
		rgb  RGB
	}{
		{
			name: "osc11-bel",
			raw:  "\x1b]11;rgb:ffff/ffff/ffff\x07",
			ps:   11,
			rgb:  RGB{255, 255, 255},
		},
		{
			name: "osc10-st",
			raw:  "\x1b]10;rgb:0000/0000/0000\x1b\\",
			ps:   10,
			rgb:  RGB{0, 0, 0},
		},
		{
			name: "four-digit-grey",
			raw:  "]11;rgb:8080/8080/8080",
			ps:   11,
			rgb:  RGB{128, 128, 128},
		},
		{
			name: "two-digit",
			raw:  "rgb:ff/80/00",
			ps:   0,
			rgb:  RGB{255, 128, 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps, color, ok := ParseOSCColorReply(tc.raw)
			if !ok {
				t.Fatalf("parse failed for %q", tc.raw)
			}
			if ps != tc.ps {
				t.Fatalf("ps=%d want %d", ps, tc.ps)
			}
			if color != tc.rgb {
				t.Fatalf("color=%+v want %+v", color, tc.rgb)
			}
		})
	}
}

func TestParseOSCColorReplyRejectsJunk(t *testing.T) {
	for _, raw := range []string{"", "not-a-color", "\x1b]11;rgba:ff/ff/ff\x07", "\x1b]11;rgb:zz/00/00\x07"} {
		if _, _, ok := ParseOSCColorReply(raw); ok {
			t.Fatalf("expected reject for %q", raw)
		}
	}
}

func TestColorProfileWithDefaults(t *testing.T) {
	base := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ:     func(string) string { return "" },
	})
	fg := RGB{0, 0, 0}
	bg := RGB{255, 255, 255}
	p := base.WithDefaults(&fg, &bg)
	if p.DefaultFG == nil || *p.DefaultFG != fg {
		t.Fatalf("DefaultFG=%v", p.DefaultFG)
	}
	if p.DefaultBG == nil || *p.DefaultBG != bg {
		t.Fatalf("DefaultBG=%v", p.DefaultBG)
	}
	if p.Background != BackgroundLight {
		t.Fatalf("bg kind=%v want light", p.Background)
	}
	dark := base.WithDefaults(nil, &RGB{0, 0, 0})
	if dark.Background != BackgroundDark {
		t.Fatalf("bg kind=%v want dark", dark.Background)
	}
}

func TestDetectColorProfileAppliesOSCEnvDefaults(t *testing.T) {
	env := map[string]string{
		"COLORTERM":    "truecolor",
		"TERM":         "xterm-256color",
		"AICLI_OSC_FG": "rgb:0000/0000/0000",
		"AICLI_OSC_BG": "rgb:ffff/ffff/ffff",
	}
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ:     func(k string) string { return env[k] },
	})
	if p.DefaultFG == nil || *p.DefaultFG != (RGB{0, 0, 0}) {
		t.Fatalf("DefaultFG=%v", p.DefaultFG)
	}
	if p.DefaultBG == nil || *p.DefaultBG != (RGB{255, 255, 255}) {
		t.Fatalf("DefaultBG=%v", p.DefaultBG)
	}
	if p.Background != BackgroundLight {
		t.Fatalf("bg kind=%v want light", p.Background)
	}
}

func TestDetectColorProfileDisableOSCProbeSkipsEnv(t *testing.T) {
	env := map[string]string{
		"COLORTERM":               "truecolor",
		"TERM":                    "xterm-256color",
		"AICLI_OSC_FG":            "rgb:0000/0000/0000",
		"AICLI_OSC_BG":            "rgb:ffff/ffff/ffff",
		"AICLI_DISABLE_OSC_PROBE": "1",
	}
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ:     func(k string) string { return env[k] },
	})
	if p.DefaultFG != nil || p.DefaultBG != nil {
		t.Fatalf("disable should skip env OSC defaults: fg=%v bg=%v", p.DefaultFG, p.DefaultBG)
	}
}

func TestDetectColorProfileInjectableOverridesEnv(t *testing.T) {
	env := map[string]string{
		"COLORTERM":    "truecolor",
		"TERM":         "xterm-256color",
		"AICLI_OSC_FG": "rgb:ffff/0000/0000",
		"AICLI_OSC_BG": "rgb:0000/0000/ffff",
	}
	fg := RGB{1, 2, 3}
	bg := RGB{4, 5, 6}
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		DefaultFG:   &fg,
		DefaultBG:   &bg,
		Environ:     func(k string) string { return env[k] },
	})
	if p.DefaultFG == nil || *p.DefaultFG != fg {
		t.Fatalf("DefaultFG=%v want %+v", p.DefaultFG, fg)
	}
	if p.DefaultBG == nil || *p.DefaultBG != bg {
		t.Fatalf("DefaultBG=%v want %+v", p.DefaultBG, bg)
	}
}

func TestDetectColorProfileRejectsJunkOSCEnv(t *testing.T) {
	env := map[string]string{
		"COLORTERM":    "truecolor",
		"TERM":         "xterm-256color",
		"AICLI_OSC_FG": "not-a-color",
		"AICLI_OSC_BG": "rgba:ff/ff/ff",
	}
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		Environ:     func(k string) string { return env[k] },
	})
	if p.DefaultFG != nil || p.DefaultBG != nil {
		t.Fatalf("junk OSC env must be ignored: fg=%v bg=%v", p.DefaultFG, p.DefaultBG)
	}
}

func TestDetectColorProfileDisableStillHonorsInjectable(t *testing.T) {
	env := map[string]string{
		"COLORTERM":               "truecolor",
		"TERM":                    "xterm-256color",
		"AICLI_OSC_BG":            "rgb:ffff/ffff/ffff",
		"AICLI_DISABLE_OSC_PROBE": "true",
	}
	bg := RGB{10, 20, 30}
	p := DetectColorProfile(DetectOptions{
		Interactive: true,
		ANSICapable: true,
		DefaultBG:   &bg,
		Environ:     func(k string) string { return env[k] },
	})
	if p.DefaultBG == nil || *p.DefaultBG != bg {
		t.Fatalf("injectable bg should apply under disable: %v", p.DefaultBG)
	}
	if p.DefaultFG != nil {
		t.Fatalf("env fg should stay skipped under disable")
	}
}
