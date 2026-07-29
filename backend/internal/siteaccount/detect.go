package siteaccount

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type detectCandidate struct {
	Path     string
	SiteType SiteType
	Label    string
}

type detectProbeResult struct {
	Hit  EndpointHit
	Body []byte
}

// Client is the shared siteaccount capability entrypoint.
type Client struct {
	HTTP httpClient
}

// NewClient constructs a Client with optional custom HTTP transport.
func NewClient(httpDoer httpClient) *Client {
	if httpDoer == nil {
		httpDoer = newDefaultHTTPClient(defaultHTTPTimeout)
	}
	return &Client{HTTP: httpDoer}
}

// DetectSiteType probes well-known endpoints and scores new-api vs sub2api.
// Failures return SiteTypeUnknown with warnings rather than hard errors whenever possible.
func (c *Client) DetectSiteType(ctx context.Context, input DetectInput) (DetectResult, error) {
	if c == nil {
		c = NewClient(nil)
	}
	origin, err := OriginURL(input.BaseURL)
	if err != nil {
		return DetectResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := resolveTimeout(input.Timeout, defaultDetectTimeout)
	detectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	candidates := []detectCandidate{
		{Path: "/api/v1/status", SiteType: SiteTypeSub2API, Label: "Sub2API v1 status"},
		{Path: "/api/v1/settings/public", SiteType: SiteTypeSub2API, Label: "Sub2API public settings"},
		{Path: "/api/v1/auth/me", SiteType: SiteTypeSub2API, Label: "Sub2API auth me"},
		{Path: "/setup/status", SiteType: SiteTypeSub2API, Label: "Sub2API setup status"},
		{Path: "/health", SiteType: SiteTypeSub2API, Label: "Sub2API health"},
		{Path: "/api/status", SiteType: SiteTypeNewAPI, Label: "New-API status"},
		{Path: "/api/user/self", SiteType: SiteTypeNewAPI, Label: "New-API user self"},
		{Path: "/api/user/self/groups", SiteType: SiteTypeNewAPI, Label: "New-API user groups"},
	}

	probes := make([]detectProbeResult, len(candidates))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, candidate := range candidates {
		wg.Add(1)
		go func(i int, candidate detectCandidate) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-detectCtx.Done():
				probes[i] = detectProbeResult{
					Hit: EndpointHit{Path: candidate.Path, SiteType: candidate.SiteType, Detail: "canceled"},
				}
				return
			}
			url := origin + candidate.Path
			result, reqErr := doGET(detectCtx, c.HTTP, url, "application/json,text/plain,*/*", nil)
			if reqErr != nil {
				probes[i] = detectProbeResult{
					Hit: EndpointHit{
						Path:     candidate.Path,
						SiteType: candidate.SiteType,
						Detail:   reqErr.Error(),
					},
				}
				return
			}
			hit := EndpointHit{
				Path:       candidate.Path,
				SiteType:   candidate.SiteType,
				StatusCode: result.StatusCode,
			}
			protected := endpointIndicatesProtected(result)
			matchedJSON := endpointLooksLikeJSON(result)
			hit.Protected = protected
			hit.Matched = protected || (result.StatusCode >= 200 && result.StatusCode < 300 && matchedJSON)
			if version := strings.TrimSpace(result.Header.Get("X-New-Api-Version")); version != "" {
				hit.Detail = "X-New-Api-Version=" + version
			}
			probes[i] = detectProbeResult{Hit: hit, Body: append([]byte(nil), result.Body...)}
		}(i, candidate)
	}
	wg.Wait()

	scores := map[string]int{
		string(SiteTypeSub2API): 0,
		string(SiteTypeNewAPI):  0,
	}
	hints := map[string]any{}
	warnings := make([]string, 0)
	hits := make([]EndpointHit, 0, len(probes))

	for _, probe := range probes {
		hit := probe.Hit
		body := probe.Body
		hits = append(hits, hit)

		if strings.Contains(hit.Detail, "X-New-Api-Version=") {
			scores[string(SiteTypeNewAPI)] += 5
			hints["new_api_version"] = strings.TrimPrefix(hit.Detail, "X-New-Api-Version=")
		}
		if !hit.Matched {
			continue
		}
		switch hit.Path {
		case "/health":
			// weak signal only; ignore for scoring (matches gateway)
			continue
		case "/setup/status":
			if hit.SiteType == SiteTypeSub2API {
				scores[string(SiteTypeSub2API)] += 1
			}
			continue
		}
		if hit.Path == "/api/status" && hit.StatusCode >= 200 && hit.StatusCode < 300 {
			if cfg := NewAPIQuotaDisplayConfigFromStatusJSON(body); cfg.Scale != nil {
				hints["quota_per_unit"] = *cfg.Scale
				hints["quota_display_type"] = cfg.DisplayType
			}
			if looksLikeNewAPIStatus(body) {
				scores[string(SiteTypeNewAPI)] += 3
			}
		}
		if hit.Path == "/api/v1/settings/public" && hit.StatusCode >= 200 && hit.StatusCode < 300 && looksLikeSub2APISettings(body) {
			scores[string(SiteTypeSub2API)] += 4
		}
		if hit.Path == "/api/v1/status" && hit.StatusCode >= 200 && hit.StatusCode < 300 {
			scores[string(SiteTypeSub2API)] += 3
		}
		if hit.Protected {
			scores[string(hit.SiteType)] += 3
			continue
		}
		if hit.StatusCode >= 200 && hit.StatusCode < 300 {
			// Avoid double-counting the strong public endpoints already boosted above.
			if hit.Path == "/api/status" || hit.Path == "/api/v1/settings/public" || hit.Path == "/api/v1/status" {
				continue
			}
			scores[string(hit.SiteType)] += 3
		}
	}

	siteType := SiteTypeUnknown
	confidence := ConfidenceLow
	subScore := scores[string(SiteTypeSub2API)]
	newScore := scores[string(SiteTypeNewAPI)]
	if subScore > newScore && subScore >= 3 {
		siteType = SiteTypeSub2API
	} else if newScore > subScore && newScore >= 3 {
		siteType = SiteTypeNewAPI
	} else if subScore > 0 && subScore == newScore {
		warnings = append(warnings, "ambiguous site type scores; leaving as unknown")
	}
	if score := scores[string(siteType)]; score >= 6 {
		confidence = ConfidenceHigh
	} else if score >= 3 {
		confidence = ConfidenceMedium
	}

	return DetectResult{
		SiteType:      siteType,
		Confidence:    confidence,
		Score:         scores,
		Hits:          compactHits(hits),
		PlatformHints: hints,
		DetectedAt:    time.Now().UTC(),
		Warnings:      warnings,
	}, nil
}

// DetectSiteType is a package-level convenience using the default client.
func DetectSiteType(ctx context.Context, input DetectInput) (DetectResult, error) {
	return NewClient(nil).DetectSiteType(ctx, input)
}

func endpointLooksLikeJSON(result httpResult) bool {
	body := strings.TrimSpace(string(result.Body))
	if body == "" {
		return false
	}
	if strings.Contains(strings.ToLower(result.ContentType), "json") {
		return true
	}
	var decoded any
	return json.Unmarshal(result.Body, &decoded) == nil
}

func endpointIndicatesProtected(result httpResult) bool {
	if result.StatusCode == http.StatusUnauthorized {
		return true
	}
	if result.StatusCode != http.StatusForbidden {
		return false
	}
	return endpointLooksLikeJSON(result)
}

func looksLikeNewAPIStatus(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var envelope struct {
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	if envelope.Success != nil && !*envelope.Success {
		return false
	}
	data := envelope.Data
	if len(data) == 0 {
		data = body
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return false
	}
	_, hasVersion := payload["version"]
	_, hasQuota := payload["quota_per_unit"]
	_, hasSystem := payload["system_name"]
	return hasVersion || hasQuota || hasSystem
}

func looksLikeSub2APISettings(body []byte) bool {
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			SiteName            string `json:"site_name"`
			RegistrationEnabled *bool  `json:"registration_enabled"`
			PaymentEnabled      *bool  `json:"payment_enabled"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Code != 0 {
		return false
	}
	if strings.TrimSpace(envelope.Data.SiteName) == "" {
		return false
	}
	return envelope.Data.RegistrationEnabled != nil || envelope.Data.PaymentEnabled != nil
}

func compactHits(hits []EndpointHit) []EndpointHit {
	out := make([]EndpointHit, 0, len(hits))
	for _, hit := range hits {
		if hit.StatusCode == 0 && strings.TrimSpace(hit.Detail) == "" {
			continue
		}
		out = append(out, hit)
	}
	return out
}
