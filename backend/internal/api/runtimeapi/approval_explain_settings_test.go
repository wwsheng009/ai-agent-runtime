package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

const approvalExplainSettingsRoutePath = "/api/runtime/config/approval-explain"

func newApprovalExplainSettingsHandler() *Handler {
	return NewHandler(skill.NewRegistry(nil), nil, nil)
}

func putApprovalExplainMode(t *testing.T, handler *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, approvalExplainSettingsRoutePath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.UpdateApprovalExplainSettings(rec, req)
	return rec
}

func TestGetApprovalExplainSettingsDefaultsToOnDemand(t *testing.T) {
	handler := newApprovalExplainSettingsHandler()

	req := httptest.NewRequest(http.MethodGet, approvalExplainSettingsRoutePath, nil)
	rec := httptest.NewRecorder()
	handler.GetApprovalExplainSettings(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp approvalExplainSettingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, string(ApprovalExplainModeOnDemand), resp.Mode)
	require.Equal(t, []string{
		string(ApprovalExplainModeOff),
		string(ApprovalExplainModeOnDemand),
		string(ApprovalExplainModePreGenerate),
	}, resp.Supported, "模式清单必须与后端枚举同源")
	require.False(t, resp.Updated)
}

func TestUpdateApprovalExplainSettingsSwitchesAndEchoes(t *testing.T) {
	handler := newApprovalExplainSettingsHandler()

	rec := putApprovalExplainMode(t, handler, `{"mode":"pre_generate"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp approvalExplainSettingsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.True(t, resp.Updated)
	require.Equal(t, string(ApprovalExplainModePreGenerate), resp.Mode)
	require.Equal(t, ApprovalExplainModePreGenerate, handler.ApprovalExplainMode())

	// 别名形态规范化后回显规范值（env 解析与接口同一套取值）。
	rec = putApprovalExplainMode(t, handler, `{"mode":"disabled"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, string(ApprovalExplainModeOff), resp.Mode)
	require.Equal(t, ApprovalExplainModeOff, handler.ApprovalExplainMode())

	// GET 反映新值（前端刷新即得真实状态）。
	getRec := httptest.NewRecorder()
	handler.GetApprovalExplainSettings(getRec, httptest.NewRequest(http.MethodGet, approvalExplainSettingsRoutePath, nil))
	require.NoError(t, json.NewDecoder(getRec.Body).Decode(&resp))
	require.Equal(t, string(ApprovalExplainModeOff), resp.Mode)
}

func TestUpdateApprovalExplainSettingsRejectsInvalidMode(t *testing.T) {
	handler := newApprovalExplainSettingsHandler()
	require.NoError(t, handler.SetApprovalExplainMode("off"))

	for _, body := range []string{`{"mode":"always"}`, `{"mode":"pre-generate-everything"}`, `{`} {
		rec := putApprovalExplainMode(t, handler, body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		require.Equal(t, ApprovalExplainModeOff, handler.ApprovalExplainMode(),
			"非法请求不得改变当前模式")
	}

	// 空 mode 等同默认（on_demand）——与 env 解析一致，不做「空值报错」。
	rec := putApprovalExplainMode(t, handler, `{"mode":""}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, ApprovalExplainModeOnDemand, handler.ApprovalExplainMode())
}

// 路由注册（路径 + 方法）必须真的挂在 /api/runtime 下，前端才不至于 404。
func TestApprovalExplainSettingsRoutesRegistered(t *testing.T) {
	handler := newApprovalExplainSettingsHandler()
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, approvalExplainSettingsRoutePath, nil))
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())

	putRec := httptest.NewRecorder()
	putReq := httptest.NewRequest(http.MethodPut, approvalExplainSettingsRoutePath, strings.NewReader(`{"mode":"pre_generate"}`))
	putReq.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(putRec, putReq)
	require.Equal(t, http.StatusOK, putRec.Code, putRec.Body.String())

	var resp approvalExplainSettingsResponse
	require.NoError(t, json.NewDecoder(putRec.Body).Decode(&resp))
	require.Equal(t, string(ApprovalExplainModePreGenerate), resp.Mode)
}
