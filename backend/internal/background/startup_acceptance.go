package background

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	backgroundMetaLaunchState       = "launch_state"
	backgroundMetaProcessStarted    = "process_started"
	backgroundMetaStartupProbe      = "startup_probe"
	backgroundMetaStartupGraceMs    = "startup_grace_ms"
	backgroundMetaStartupTimeoutMs  = "startup_timeout_ms"
	backgroundMetaStartupAddress    = "startup_address"
	backgroundMetaStartupURL        = "startup_url"
	backgroundMetaStartupAcceptedAt = "startup_accepted_at"
	backgroundMetaHealthcheckState  = "healthcheck_state"
	backgroundMetaHealthcheckError  = "healthcheck_error"

	launchStateQueued         = "queued"
	launchStateProcessCreated = "process_created"
	launchStateAccepting      = "accepting"
	launchStateAccepted       = "accepted"
	launchStateFailed         = "failed"

	healthcheckStateNotConfigured = "not_configured"
	healthcheckStatePending       = "pending"
	healthcheckStatePassed        = "passed"
	healthcheckStateFailed        = "failed"
)

const (
	defaultStartupGracePeriod  = 200 * time.Millisecond
	defaultStartupProbeTimeout = 5 * time.Second
	maxStartupProbeTimeout     = 60 * time.Second
)

type startupProbeFunc func(context.Context, StartupAcceptance, func() bool) error

var executeStartupProbe startupProbeFunc = runStartupProbe

func normalizeStartupAcceptance(startup *StartupAcceptance) StartupAcceptance {
	if startup == nil {
		return StartupAcceptance{Probe: StartupProbeProcess, GracePeriodMs: int(defaultStartupGracePeriod.Milliseconds())}
	}
	normalized := *startup
	normalized.Probe = StartupProbeType(strings.ToLower(strings.TrimSpace(string(normalized.Probe))))
	if normalized.Probe == "" {
		normalized.Probe = StartupProbeProcess
	}
	if normalized.GracePeriodMs < 0 {
		normalized.GracePeriodMs = 0
	}
	if normalized.TimeoutMs < 0 {
		normalized.TimeoutMs = 0
	}
	normalized.Address = strings.TrimSpace(normalized.Address)
	normalized.URL = strings.TrimSpace(normalized.URL)
	return normalized
}

func validateStartupAcceptance(startup StartupAcceptance) error {
	switch startup.Probe {
	case StartupProbeNone, StartupProbeProcess:
		return nil
	case StartupProbeTCP:
		if startup.Address == "" {
			return fmt.Errorf("startup_acceptance.address is required for tcp probe")
		}
		if _, _, err := net.SplitHostPort(startup.Address); err != nil {
			return fmt.Errorf("startup_acceptance.address must be host:port: %w", err)
		}
		return nil
	case StartupProbeHTTP:
		parsed, err := url.ParseRequestURI(startup.URL)
		if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("startup_acceptance.url must be an absolute http or https URL")
		}
		return nil
	default:
		return fmt.Errorf("startup_acceptance.probe must be one of none, process, tcp, or http")
	}
}

func startupProbeTimeout(startup StartupAcceptance) time.Duration {
	timeout := time.Duration(startup.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultStartupProbeTimeout
	}
	if timeout > maxStartupProbeTimeout {
		timeout = maxStartupProbeTimeout
	}
	return timeout
}

func runStartupProbe(ctx context.Context, startup StartupAcceptance, processAlive func() bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if processAlive == nil {
		processAlive = func() bool { return true }
	}
	if startup.Probe == StartupProbeNone {
		return nil
	}
	deadline := time.Now().Add(startupProbeTimeout(startup))
	graceDeadline := time.Now().Add(time.Duration(startup.GracePeriodMs) * time.Millisecond)
	var lastErr error
	for {
		if !processAlive() {
			return fmt.Errorf("process exited before startup acceptance")
		}
		if time.Now().Before(graceDeadline) {
			if err := waitStartupProbeInterval(ctx, graceDeadline); err != nil {
				return err
			}
			continue
		}
		switch startup.Probe {
		case StartupProbeProcess:
			return nil
		case StartupProbeTCP:
			probeCtx, cancel := context.WithTimeout(ctx, startupProbeAttemptTimeout(deadline))
			var dialer net.Dialer
			conn, err := dialer.DialContext(probeCtx, "tcp", startup.Address)
			cancel()
			if err == nil {
				_ = conn.Close()
				return nil
			}
			lastErr = err
		case StartupProbeHTTP:
			probeCtx, cancel := context.WithTimeout(ctx, startupProbeAttemptTimeout(deadline))
			req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, startup.URL, nil)
			if err == nil {
				response, requestErr := http.DefaultClient.Do(req)
				if requestErr == nil {
					_ = response.Body.Close()
					if response.StatusCode >= 200 && response.StatusCode < 400 {
						cancel()
						return nil
					}
					requestErr = fmt.Errorf("http startup probe returned status %d", response.StatusCode)
				}
				lastErr = requestErr
			} else {
				lastErr = err
			}
			cancel()
		}
		if time.Now().After(deadline) {
			if lastErr == nil {
				lastErr = context.DeadlineExceeded
			}
			return fmt.Errorf("startup probe did not pass within %s: %w", startupProbeTimeout(startup), lastErr)
		}
		if err := waitStartupProbeInterval(ctx, time.Now().Add(100*time.Millisecond)); err != nil {
			return err
		}
	}
}

func startupProbeAttemptTimeout(deadline time.Time) time.Duration {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Millisecond
	}
	if remaining > time.Second {
		return time.Second
	}
	return remaining
}

func waitStartupProbeInterval(ctx context.Context, until time.Time) error {
	delay := time.Until(until)
	if delay <= 0 {
		return nil
	}
	if delay > 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
