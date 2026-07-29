package skills

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
)

// FlexibleString accepts either a JSON string or number and stores its textual value.
type FlexibleString string

func (s *FlexibleString) UnmarshalJSON(data []byte) error {
	if s == nil {
		return stderrors.New("nil FlexibleString receiver")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*s = ""
		return nil
	}
	if data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*s = FlexibleString(strings.TrimSpace(text))
		return nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return err
	}
	*s = FlexibleString(strings.TrimSpace(number.String()))
	return nil
}

type SiteAccountDetectRequest struct {
	BaseURL   string `json:"base_url"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type SiteAccountDetectResult struct {
	Detect *siteaccount.DetectResult `json:"detect"`
}

type SiteAccountFetchRequest struct {
	BaseURL           string         `json:"base_url"`
	SiteType          string         `json:"site_type,omitempty"`
	APIKey            string         `json:"api_key,omitempty"`
	SystemAccessToken string         `json:"system_access_token,omitempty"`
	SubjectUserID     FlexibleString `json:"subject_user_id,omitempty"`
	TimeoutMs         int            `json:"timeout_ms,omitempty"`
	Days              int            `json:"days,omitempty"`
}

type SiteAccountFetchResult struct {
	Detect      *siteaccount.DetectResult  `json:"detect,omitempty"`
	Account     *siteaccount.AccountSnapshot `json:"account,omitempty"`
	AccountView *siteaccount.AccountView   `json:"account_view,omitempty"`
	BalanceLine string                     `json:"balance_line,omitempty"`
	Warnings    []string                   `json:"warnings,omitempty"`
}

type SiteAccountRefreshRequest struct {
	SiteType          string         `json:"site_type,omitempty"`
	APIKey            string         `json:"api_key,omitempty"`
	SystemAccessToken string         `json:"system_access_token,omitempty"`
	SubjectUserID     FlexibleString `json:"subject_user_id,omitempty"`
	SkipDetect        bool           `json:"skip_detect,omitempty"`
	TimeoutMs         int            `json:"timeout_ms,omitempty"`
	Days              int            `json:"days,omitempty"`
	Persist           *bool          `json:"persist,omitempty"`
	SaveAccountAuth   *bool          `json:"save_account_auth,omitempty"`
}

type SiteAccountRefreshResult struct {
	Provider            string                               `json:"provider"`
	SiteType           string                               `json:"site_type"`
	SiteTypeConfidence string                               `json:"site_type_confidence,omitempty"`
	SiteTypeDetectedAt string                               `json:"site_type_detected_at,omitempty"`
	SiteTypeScores     map[string]int                       `json:"site_type_scores,omitempty"`
	Detect             *siteaccount.DetectResult             `json:"detect,omitempty"`
	Account            *siteaccount.AccountSnapshot          `json:"account,omitempty"`
	AccountView        *siteaccount.AccountView              `json:"account_view,omitempty"`
	AccountCache       *agentconfig.ProviderAccountSnapshot  `json:"account_cache,omitempty"`
	AccountAuthRef     string                               `json:"account_auth_ref,omitempty"`
	BalanceLine        string                               `json:"balance_line,omitempty"`
	Warnings           []string                             `json:"warnings,omitempty"`
	Persisted          bool                                 `json:"persisted"`
}

type SiteAccountService interface {
	Detect(context.Context, SiteAccountDetectRequest) (*SiteAccountDetectResult, error)
	Fetch(context.Context, SiteAccountFetchRequest) (*SiteAccountFetchResult, error)
	RefreshProvider(context.Context, string, SiteAccountRefreshRequest) (*SiteAccountRefreshResult, error)
}

func (h *Handler) SetSiteAccountService(service SiteAccountService) {
	if h == nil {
		return
	}
	h.siteAccountService = service
}

func (h *Handler) DetectRuntimeSiteAccount(w http.ResponseWriter, r *http.Request) {
	if h.siteAccountService == nil {
		h.writeError(w, http.StatusServiceUnavailable, runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "siteaccount service not configured"))
		return
	}
	var req SiteAccountDetectRequest
	if err := decodeSiteAccountRequest(r, &req, false); err != nil {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "invalid siteaccount detect payload"))
		return
	}
	if strings.TrimSpace(req.BaseURL) == "" {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "base_url is required"))
		return
	}
	result, err := h.siteAccountService.Detect(r.Context(), req)
	if err != nil {
		h.writeSiteAccountError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) FetchRuntimeSiteAccount(w http.ResponseWriter, r *http.Request) {
	if h.siteAccountService == nil {
		h.writeError(w, http.StatusServiceUnavailable, runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "siteaccount service not configured"))
		return
	}
	var req SiteAccountFetchRequest
	if err := decodeSiteAccountRequest(r, &req, false); err != nil {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "invalid siteaccount fetch payload"))
		return
	}
	if strings.TrimSpace(req.BaseURL) == "" {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "base_url is required"))
		return
	}
	result, err := h.siteAccountService.Fetch(r.Context(), req)
	if err != nil {
		h.writeSiteAccountError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func (h *Handler) RefreshRuntimeProviderAccount(w http.ResponseWriter, r *http.Request) {
	if h.siteAccountService == nil {
		h.writeError(w, http.StatusServiceUnavailable, runtimeerrors.New(runtimeerrors.ErrConfigInvalid, "siteaccount service not configured"))
		return
	}
	providerName := strings.TrimSpace(mux.Vars(r)["name"])
	if providerName == "" {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "provider name is required"))
		return
	}
	var req SiteAccountRefreshRequest
	if err := decodeSiteAccountRequest(r, &req, true); err != nil {
		h.writeError(w, http.StatusBadRequest, runtimeerrors.New(runtimeerrors.ErrValidationFailed, "invalid siteaccount refresh payload"))
		return
	}
	result, err := h.siteAccountService.RefreshProvider(r.Context(), providerName, req)
	if err != nil {
		h.writeSiteAccountError(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, result)
}

func decodeSiteAccountRequest(r *http.Request, target interface{}, allowEmpty bool) error {
	if r == nil || r.Body == nil {
		if allowEmpty {
			return nil
		}
		return io.EOF
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if allowEmpty && stderrors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !stderrors.Is(err, io.EOF) {
		if err == nil {
			return stderrors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func (h *Handler) writeSiteAccountError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case stderrors.Is(err, siteaccount.ErrInvalidInput):
		status = http.StatusBadRequest
	case stderrors.Is(err, siteaccount.ErrMissingCredential):
		status = http.StatusBadRequest
	case stderrors.Is(err, siteaccount.ErrUnauthorized):
		status = http.StatusUnauthorized
	case stderrors.Is(err, siteaccount.ErrUnsupportedSite):
		status = http.StatusUnprocessableEntity
	default:
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "required") || strings.Contains(message, "invalid") {
			status = http.StatusBadRequest
		} else if strings.Contains(message, "not found") {
			status = http.StatusNotFound
		}
	}
	h.writeError(w, status, runtimeerrors.Wrap(runtimeerrors.ErrConfigInvalid, "siteaccount request failed", err))
}
