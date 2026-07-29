package ui

import (
	"os"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

var (
	liveOSCOnce sync.Once
	liveOSCFG   *style.RGB
	liveOSCBG   *style.RGB
)

// LiveOSCProbe returns a process-once OSC 10/11 probe against stdin/stdout.
//
// Safe to attach to style.DetectOptions: DetectColorProfile still honors
// AICLI_DISABLE_OSC_PROBE, skips when colors are already known from injectable
// or env defaults, and only invokes the probe to fill remaining gaps.
//
// The underlying ProbeOSCDefaultColors refuses unbounded readers and applies a
// short read deadline when the handle supports it, so missing replies cannot
// hang startup.
func LiveOSCProbe() style.OSCProbeFunc {
	return func() (fg, bg *style.RGB) {
		liveOSCOnce.Do(func() {
			liveOSCFG, liveOSCBG = style.ProbeOSCDefaultColors(style.OSCProbeOptions{
				Writer:  os.Stdout,
				Reader:  os.Stdin,
				Timeout: style.DefaultOSCProbeTimeout,
			})
		})
		return liveOSCFG, liveOSCBG
	}
}

// ResetLiveOSCProbeForTest clears the process-once OSC probe cache.
// Production code must not call this.
func ResetLiveOSCProbeForTest() {
	liveOSCOnce = sync.Once{}
	liveOSCFG, liveOSCBG = nil, nil
}
