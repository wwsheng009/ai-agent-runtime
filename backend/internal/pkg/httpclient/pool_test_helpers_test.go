package httpclient

import "time"

func recordDialResultAt(now time.Time, success bool, latency time.Duration) {
	recordDialResultAtWithStore(getSharedMetricsStore(), now, success, latency)
}
