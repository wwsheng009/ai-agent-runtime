package runtimeapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

const agentMaxStepsRoutePath = "/api/runtime/config/agent/max-steps"

func TestUpdateAgentMaxStepsPersistsValue(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	var received []int
	handler.SetAgentMaxStepsPersister(func(maxSteps int) (string, error) {
		received = append(received, maxSteps)
		return "  E:/tmp/runtime.yaml  ", nil
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPut, agentMaxStepsRoutePath, strings.NewReader(`{"max_steps": 12}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int{12}, received)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, true, payload["updated"])
	require.Equal(t, float64(12), payload["max_steps"])
	require.Equal(t, "E:/tmp/runtime.yaml", payload["config_file"])
}

func TestUpdateAgentMaxStepsUnlimitedZeroIsAccepted(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetAgentMaxStepsPersister(func(maxSteps int) (string, error) {
		require.Equal(t, 0, maxSteps)
		return "runtime.yaml", nil
	})

	req := httptest.NewRequest(http.MethodPut, agentMaxStepsRoutePath, strings.NewReader(`{"max_steps": 0}`))
	rec := httptest.NewRecorder()
	handler.UpdateAgentMaxSteps(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestUpdateAgentMaxStepsRejectsInvalidPayload(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "missing field", body: `{}`},
		{name: "broken json", body: `{"max_steps":`},
		{name: "negative", body: `{"max_steps": -3}`},
		{name: "too large", body: `{"max_steps": 101}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewHandler(skill.NewRegistry(nil), nil, nil)
			called := false
			handler.SetAgentMaxStepsPersister(func(maxSteps int) (string, error) {
				called = true
				return "", nil
			})

			req := httptest.NewRequest(http.MethodPut, agentMaxStepsRoutePath, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			handler.UpdateAgentMaxSteps(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.False(t, called, "persister must not be called for invalid payloads")
		})
	}
}

func TestUpdateAgentMaxStepsWithoutPersister(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)

	req := httptest.NewRequest(http.MethodPut, agentMaxStepsRoutePath, strings.NewReader(`{"max_steps": 4}`))
	rec := httptest.NewRecorder()
	handler.UpdateAgentMaxSteps(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestUpdateAgentMaxStepsPersisterFailure(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetAgentMaxStepsPersister(func(maxSteps int) (string, error) {
		return "runtime.yaml", errors.New("disk is read-only")
	})

	req := httptest.NewRequest(http.MethodPut, agentMaxStepsRoutePath, strings.NewReader(`{"max_steps": 6}`))
	rec := httptest.NewRecorder()
	handler.UpdateAgentMaxSteps(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Contains(t, payload["error"], "disk is read-only")
}

func TestGetAgentMaxStepsReturnsServerDefault(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetAgentMaxStepsProvider(func() (int, string, error) {
		// 路径带前后空格：handler 必须 trim 后再回给前端。
		return 12, "  E:/tmp/runtime.yaml  ", nil
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, agentMaxStepsRoutePath, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, float64(runtimeAgentMaxStepsLimit), payload["limit"])
	require.Equal(t, float64(12), payload["max_steps"])
	require.Equal(t, "E:/tmp/runtime.yaml", payload["config_file"])
	require.Len(t, payload, 3, "GET 只读接口的响应只带 limit/max_steps/config_file")
}

func TestGetAgentMaxStepsWithoutProvider(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)

	req := httptest.NewRequest(http.MethodGet, agentMaxStepsRoutePath, nil)
	rec := httptest.NewRecorder()
	handler.GetAgentMaxSteps(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestGetAgentMaxStepsProviderFailure(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetAgentMaxStepsProvider(func() (int, string, error) {
		return 0, "", errors.New("runtime config is not loaded")
	})

	req := httptest.NewRequest(http.MethodGet, agentMaxStepsRoutePath, nil)
	rec := httptest.NewRecorder()
	handler.GetAgentMaxSteps(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Contains(t, payload["error"], "runtime config is not loaded")
}

func TestGetAgentMaxStepsIncludesRuntimeLayers(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetAgentMaxStepsProvider(func() (int, string, error) {
		return 5, "  C:/Users/x/.aicli/runtime.yaml  ", nil
	})
	handler.SetRuntimeConfigLayersProvider(func() []ConfigDocumentLayer {
		return []ConfigDocumentLayer{
			{Kind: "portable", Path: "configs/runtime.yaml", Present: false, ReadOnly: true},
			{Kind: "user", Path: "C:/Users/x/.aicli/runtime.yaml", Present: false},
			{Kind: "project", Path: ".aicli/runtime.yaml", Present: false},
		}
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, agentMaxStepsRoutePath, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		ConfigFile string                `json:"config_file"`
		Layers     []ConfigDocumentLayer `json:"layers"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "C:/Users/x/.aicli/runtime.yaml", payload.ConfigFile)
	require.Len(t, payload.Layers, 3)
	require.True(t, payload.Layers[0].ReadOnly)
	require.False(t, payload.Layers[1].ReadOnly)
	require.False(t, payload.Layers[2].ReadOnly)
	require.Equal(t, "user", payload.Layers[1].Kind)
}

func TestGetAgentMaxStepsOmitsLayersWithoutProvider(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetAgentMaxStepsProvider(func() (int, string, error) {
		return 5, "C:/tmp/runtime.yaml", nil
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, agentMaxStepsRoutePath, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "layers")
}
