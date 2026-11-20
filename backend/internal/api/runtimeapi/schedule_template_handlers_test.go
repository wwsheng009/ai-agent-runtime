package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

func TestScheduleTemplateAPI_CreateUpdateDelete(t *testing.T) {
	_, router := newScheduleTestHandler(t)

	createBody := `{
		"title":"Project check",
		"taskTitle":"Check project",
		"category":"maintenance",
		"description":"A custom saved preset",
		"prompt":"Inspect the project",
		"kind":"every",
		"amount":"300",
		"targetKind":"session"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/templates", strings.NewReader(createBody))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var created struct {
		Template schedule.Template `json:"template"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	require.NotEmpty(t, created.Template.ID)
	require.Equal(t, "Project check", created.Template.Title)
	require.Equal(t, "Check project", created.Template.TaskTitle)

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/schedule/templates", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	var listed struct {
		Templates []schedule.Template `json:"templates"`
		Count     int                 `json:"count"`
	}
	require.NoError(t, json.Unmarshal(listRec.Body.Bytes(), &listed))
	require.Equal(t, 5, listed.Count)
	require.Len(t, listed.Templates, 5)

	updateBody := `{
		"title":"Updated project check",
		"taskTitle":"Recheck project",
		"category":"maintenance",
		"description":"Updated preset",
		"prompt":"Inspect the project again",
		"kind":"every",
		"amount":"600",
		"targetKind":"session"
	}`
	updateReq := httptest.NewRequest(
		http.MethodPut,
		"/api/runtime/schedule/templates/"+created.Template.ID,
		strings.NewReader(updateBody),
	)
	updateRec := httptest.NewRecorder()
	router.ServeHTTP(updateRec, updateReq)
	require.Equal(t, http.StatusOK, updateRec.Code, updateRec.Body.String())
	var updated struct {
		Template schedule.Template `json:"template"`
	}
	require.NoError(t, json.Unmarshal(updateRec.Body.Bytes(), &updated))
	require.Equal(t, created.Template.ID, updated.Template.ID)
	require.Equal(t, "Updated project check", updated.Template.Title)
	require.Equal(t, "600", updated.Template.Amount)

	deleteReq := httptest.NewRequest(
		http.MethodDelete,
		"/api/runtime/schedule/templates/"+created.Template.ID,
		nil,
	)
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	require.Equal(t, http.StatusOK, deleteRec.Code, deleteRec.Body.String())
	var deleted struct {
		ID      string `json:"id"`
		Deleted bool   `json:"deleted"`
	}
	require.NoError(t, json.Unmarshal(deleteRec.Body.Bytes(), &deleted))
	require.Equal(t, created.Template.ID, deleted.ID)
	require.True(t, deleted.Deleted)

	missingReq := httptest.NewRequest(
		http.MethodDelete,
		"/api/runtime/schedule/templates/"+created.Template.ID,
		nil,
	)
	missingRec := httptest.NewRecorder()
	router.ServeHTTP(missingRec, missingReq)
	require.Equal(t, http.StatusNotFound, missingRec.Code, missingRec.Body.String())
}

func TestScheduleTemplateAPI_RejectsInvalidAndMissingTemplates(t *testing.T) {
	_, router := newScheduleTestHandler(t)

	invalid := `{
		"title":"Invalid",
		"taskTitle":"Invalid task",
		"prompt":"Run a task",
		"kind":"every",
		"amount":"0",
		"targetKind":"session"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/schedule/templates", strings.NewReader(invalid))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	updateReq := httptest.NewRequest(http.MethodPut, "/api/runtime/schedule/templates/missing", strings.NewReader(invalid))
	updateRec := httptest.NewRecorder()
	router.ServeHTTP(updateRec, updateReq)
	require.Equal(t, http.StatusNotFound, updateRec.Code, updateRec.Body.String())
}
