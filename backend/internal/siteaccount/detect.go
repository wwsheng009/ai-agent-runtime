package siteaccount

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type endpointProbe struct {
	Path        string
	SiteType    SiteType
	Label       string
	Score       int
	Protected   bool
	BodyMatches func([]byte) bool
}

type detectProbeResult struct {
	Hit  EndpointHit
	Body []byte
}

// Client is the shared siteaccount capability entrypoint.
type Client struct {
	HTTP     httpClient
	Registry *AdapterRegistry
}

// NewClient constructs a Client with optional custom HTTP transport.
func NewClient(httpDoer httpClient) *Client {
	if httpDoer == nil {
		httpDoer = newDefaultHTTPClient(defaultHTTPTimeout)
	}
	return &Client{HTTP: httpDoer, Registry: DefaultAdapterRegistry()}
}

// DetectSiteType probes well-known endpoints and scores site families
// (deepseek, new-api, sub2api) by endpoint path plus response characteristics.
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

	registry := c.Registry
	if registry == nil {
		registry = DefaultAdapterRegistry()
	}
	var probeSpecs []endpointProbe
	for _, adapter := range registry.Adapters() {
		for _, probe := range adapter.Probes() {
			probeSpecs = append(probeSpecs, endpointProbe{
				Path: probe.Path, SiteType: adapter.SiteType(), Label: probe.Label,
				Score: probe.Score, Protected: probe.Protected, BodyMatches: probe.BodyMatches,
			})
		}
	}

	results := make([]detectProbeResult, len(probeSpecs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, spec := range probeSpecs {
		wg.Add(1)
		go func(i int, spec endpointProbe) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-detectCtx.Done():
				results[i] = detectProbeResult{
					Hit: EndpointHit{Path: spec.Path, SiteType: spec.SiteType, Detail: "canceled"},
				}
				return
			}
			url := origin + spec.Path
			result, reqErr := doGET(detectCtx, c.HTTP, url, "application/json,text/plain,*/*", nil)
			if reqErr != nil {
				results[i] = detectProbeResult{
					Hit: EndpointHit{
						Path:     spec.Path,
						SiteType: spec.SiteType,
						Detail:   reqErr.Error(),
					},
				}
				return
			}
			hit := EndpointHit{
				Path:       spec.Path,
				SiteType:   spec.SiteType,
				StatusCode: result.StatusCode,
			}
			protected := endpointIndicatesProtected(result)
			hit.Protected = protected
			hit.Matched = (spec.Protected && protected &&
				spec.BodyMatches != nil && spec.BodyMatches(result.Body)) ||
				(result.StatusCode >= 200 && result.StatusCode < 300 &&
					spec.BodyMatches != nil && spec.BodyMatches(result.Body))
			if version := strings.TrimSpace(result.Header.Get("X-New-Api-Version")); version != "" {
				hit.Detail = "X-New-Api-Version=" + version
			}
			results[i] = detectProbeResult{Hit: hit, Body: append([]byte(nil), result.Body...)}
		}(i, spec)
	}
	wg.Wait()

	scores := map[string]int{
		string(SiteTypeSub2API):  0,
		string(SiteTypeNewAPI):   0,
		string(SiteTypeDeepSeek): 0,
	}
	hints := map[string]any{}
	warnings := make([]string, 0)
	hits := make([]EndpointHit, 0, len(results))

	for _, result := range results {
		hit := result.Hit
		body := result.Body
		hits = append(hits, hit)

		spec := probeForHit(probeSpecs, hit)
		if spec == nil {
			continue
		}
		if strings.Contains(hit.Detail, "X-New-Api-Version=") && hit.SiteType == SiteTypeNewAPI {
			hints["new_api_version"] = strings.TrimPrefix(hit.Detail, "X-New-Api-Version=")
		}
		if !hit.Matched {
			continue
		}
		if hit.Path == "/api/status" && hit.SiteType == SiteTypeNewAPI && hit.StatusCode >= 200 && hit.StatusCode < 300 {
			if cfg := NewAPIQuotaDisplayConfigFromStatusJSON(body); cfg.Scale != nil {
				hints["quota_per_unit"] = *cfg.Scale
				hints["quota_display_type"] = cfg.DisplayType
			}
		}
		scores[string(hit.SiteType)] += spec.Score
	}

	siteType := SiteTypeUnknown
	confidence := ConfidenceLow
	subScore := scores[string(SiteTypeSub2API)]
	newScore := scores[string(SiteTypeNewAPI)]
	deepSeekScore := scores[string(SiteTypeDeepSeek)]
	topScore := max(subScore, newScore, deepSeekScore)
	topCount := 0
	for _, possible := range []SiteType{SiteTypeSub2API, SiteTypeNewAPI, SiteTypeDeepSeek} {
		if scores[string(possible)] == topScore && topScore >= 3 {
			siteType = possible
			topCount++
		}
	}
	if topCount != 1 {
		siteType = SiteTypeUnknown
	}
	if topScore > 0 && topCount > 1 {
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

func probeForHit(probeSpecs []endpointProbe, hit EndpointHit) *endpointProbe {
	for i := range probeSpecs {
		spec := &probeSpecs[i]
		if spec.Path == hit.Path && spec.SiteType == hit.SiteType {
			return spec
		}
	}
	return nil
}

// DetectSiteType is a package-level convenience using the default client.
func DetectSiteType(ctx context.Context, input DetectInput) (DetectResult, error) {
	return NewClient(nil).DetectSiteType(ctx, input)
}

func endpointIndicatesProtected(result httpResult) bool {
	if result.StatusCode == http.StatusUnauthorized {
		return true
	}
	if result.StatusCode != http.StatusForbidden {
		return false
	}
	return responseBodyIsJSON(result.Body)
}

func responseBodyIsJSON(body []byte) bool {
	return len(body) > 0 && json.Unmarshal(body, new(any)) == nil
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

func looksLikeSub2APIStatus(body []byte) bool {
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(payload.Status), "ok")
}

func looksLikeSub2APISetupStatus(body []byte) bool {
	var payload struct {
		SetupRequired *bool `json:"setup_required"`
		Initialized   *bool `json:"initialized"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload.SetupRequired != nil || payload.Initialized != nil
}

func looksLikeSub2APIAuthChallenge(body []byte) bool {
	var payload struct {
		Code    *int   `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Code == nil {
		return false
	}
	return strings.TrimSpace(payload.Message) != ""
}

func looksLikeNewAPIAuthChallenge(body []byte) bool {
	var payload struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Success == nil || *payload.Success {
		return false
	}
	return strings.TrimSpace(payload.Message) != ""
}

func looksLikeDeepSeekBalanceChallenge(body []byte) bool {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	return json.Unmarshal(body, &payload) == nil && len(payload.Error) > 0 && string(payload.Error) != "null"
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
