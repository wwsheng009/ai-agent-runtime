package envdclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// HostMetrics is the payload of envd's GET /metrics endpoint.
//
// /metrics is the one endpoint that is plain HTTP rather than Connect-RPC, and the
// one that needs no user context: a bare GET returns it. That makes it the right
// choice for a liveness probe — it proves the daemon is up without touching any
// process or file.
//
// Only the fields observed from a live envd are named. Everything else is kept in
// Raw so a future envd that adds counters does not require a client change.
type HostMetrics struct {
	// Timestamp is envd's own clock, in Unix seconds.
	Timestamp int64 `json:"ts"`

	// CPUCount is the number of usable CPUs.
	CPUCount int64 `json:"cpu_count"`

	// CPUUsedPct is CPU utilisation in percent.
	CPUUsedPct float64 `json:"cpu_used_pct"`

	// MemTotal is total memory in bytes.
	MemTotal int64 `json:"mem_total"`

	// MemUsed is used memory in bytes.
	MemUsed int64 `json:"mem_used"`

	// MemFree is free memory in bytes.
	MemFree int64 `json:"mem_free"`

	// MemCached is page-cache memory in bytes.
	MemCached int64 `json:"cached"`

	// MemBuffers is buffer memory in bytes.
	MemBuffers int64 `json:"buffers"`

	// MemAvailable is the kernel's availability estimate, in bytes.
	MemAvailable int64 `json:"available"`

	// DiskUsed is used disk space in bytes.
	DiskUsed int64 `json:"disk_used"`

	// DiskTotal is total disk space in bytes.
	DiskTotal int64 `json:"disk_total"`

	// Raw is the original body.
	Raw json.RawMessage `json:"-"`
}

// Metrics fetches host metrics.
//
// This is the cheapest way to prove a target is reachable and to size a run
// against the host's actual capacity rather than a guess.
func (c *Client) Metrics(ctx context.Context) (HostMetrics, error) {
	ctx, cancel := context.WithTimeout(ctx, c.tmo)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/metrics", nil)
	if err != nil {
		return HostMetrics{}, fmt.Errorf("envdclient: build metrics request: %w", err)
	}
	c.applyHeaders(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		return HostMetrics{}, fmt.Errorf("envdclient: fetch metrics: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readLimited(resp.Body, maxFramePayload)
	if err != nil {
		return HostMetrics{}, fmt.Errorf("envdclient: read metrics: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return HostMetrics{}, decodeUnaryError(resp.StatusCode, body)
	}

	var metrics HostMetrics
	if err := json.Unmarshal(body, &metrics); err != nil {
		return HostMetrics{}, fmt.Errorf("envdclient: decode metrics: %w", err)
	}
	metrics.Raw = append(metrics.Raw[:0], body...)
	return metrics, nil
}

// Ping reports whether the daemon answers its metrics endpoint.
//
// It returns the metrics on success so a single call can both prove liveness and
// collect capacity.
func (c *Client) Ping(ctx context.Context) (HostMetrics, error) {
	return c.Metrics(ctx)
}
