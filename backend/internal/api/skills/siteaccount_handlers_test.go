package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type fakeSiteAccountService struct {
	detectReq  SiteAccountDetectRequest
	fetchReq   SiteAccountFetchRequest
	refreshReq SiteAccountRefreshRequest
	provider   string

	detectResult  *SiteAccountDetectResult
	fetchResult   *SiteAccountFetchResult
	refreshResult *SiteAccountRefreshResult
	err           error
}

func (s *fakeSiteAccountService) Detect(_ context.Context, req SiteAccountDetectRequest) (*SiteAccountDetectResult, error) {
	s.detectReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.detectResult, nil
}

func (s *fakeSiteAccountService) Fetch(_ context.Context, req SiteAccountFetchRequest) (*SiteAccountFetchResult, error) {
	s.fetchReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.fetchResult, nil
}

func (s *fakeSiteAccountService) RefreshProvider(_ context.Context, provider string, req SiteAccountRefreshRequest) (*SiteAccountRefreshResult, error) {
	s.provider = provider
	s.refreshReq = req
	if s.err != nil {
		return nil, s.err
	}
	return s.refreshResult, nil
}

func TestFlexibleStringUnmarshalJSON(t *testing.T) {
	var payload struct {
		SubjectUserID FlexibleString `json:"subject_user_id"`
	}

	require.NoError(t, json.Unmarshal([]byte(`{"subject_user_id":"42"}`), &payload))
	require.Equal(t, FlexibleString("42"), payload.SubjectUserID)

	require.NoError(t, json.Unmarshal([]byte(`{"subject_user_id":7}`), &payload))
	require.Equal(t, FlexibleString("7"), payload.SubjectUserID)

	require.NoError(t, json.Unmarshal([]byte(`{"subject_user_id":null}`), &payload))
	require.Equal(t, FlexibleString(""), payload.SubjectUserID)
}

func TestDetectRuntimeSiteAccount(t *testing.T) {
	service := &fakeSiteAccountService{
		detectResult: &SiteAccountDetectResult{
			Detect: &siteaccount.DetectResult{
				SiteType:   siteaccount.SiteTypeSub2API,
				Confidence: siteaccount.ConfidenceHigh,
			},
		},
	}
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSiteAccountService(service)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"base_url":"https://example.com","timeout_ms":1500}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/siteaccount/detect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "https://example.com", service.detectReq.BaseURL)
	require.Equal(t, 1500, service.detectReq.TimeoutMs)

	var payload SiteAccountDetectResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.NotNil(t, payload.Detect)
	require.Equal(t, siteaccount.SiteTypeSub2API, payload.Detect.SiteType)
}

func TestFetchRuntimeSiteAccount(t *testing.T) {
	remaining := 12.34
	service := &fakeSiteAccountService{
		fetchResult: &SiteAccountFetchResult{
			Account: &siteaccount.AccountSnapshot{
				SiteType:       siteaccount.SiteTypeSub2API,
				Source:         "v1_usage",
				QuotaRemaining: &remaining,
			},
			BalanceLine: "remaining (USD) 12.34 USD (quota_limited, source=v1_usage)",
		},
	}
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSiteAccountService(service)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"base_url":"https://example.com","site_type":"sub2api","api_key":"sk-test","subject_user_id":9}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/siteaccount/fetch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "https://example.com", service.fetchReq.BaseURL)
	require.Equal(t, "sub2api", service.fetchReq.SiteType)
	require.Equal(t, "sk-test", service.fetchReq.APIKey)
	require.Equal(t, FlexibleString("9"), service.fetchReq.SubjectUserID)

	var payload SiteAccountFetchResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.NotNil(t, payload.Account)
	require.Equal(t, remaining, *payload.Account.QuotaRemaining)
}

func TestRefreshRuntimeProviderAccount(t *testing.T) {
	service := &fakeSiteAccountService{
		refreshResult: &SiteAccountRefreshResult{
			Provider:  "alpha",
			SiteType:  string(siteaccount.SiteTypeNewAPI),
			Persisted: true,
		},
	}
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSiteAccountService(service)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"site_type":"new-api","system_access_token":"tok","subject_user_id":"88","skip_detect":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/providers/alpha/account/refresh", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "alpha", service.provider)
	require.Equal(t, "new-api", service.refreshReq.SiteType)
	require.Equal(t, "tok", service.refreshReq.SystemAccessToken)
	require.Equal(t, FlexibleString("88"), service.refreshReq.SubjectUserID)
	require.True(t, service.refreshReq.SkipDetect)

	var payload SiteAccountRefreshResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "alpha", payload.Provider)
	require.True(t, payload.Persisted)
}

func TestDetectRuntimeSiteAccount_ServiceMissing(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"base_url":"https://example.com"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/siteaccount/detect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestDetectRuntimeSiteAccount_InvalidInput(t *testing.T) {
	service := &fakeSiteAccountService{
		err: siteaccount.ErrInvalidInput,
	}
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSiteAccountService(service)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"base_url":"https://example.com"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/siteaccount/detect", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}
