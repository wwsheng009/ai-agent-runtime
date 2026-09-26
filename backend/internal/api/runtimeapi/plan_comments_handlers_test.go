package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

func getStoredPlanComments(t *testing.T, router *mux.Router, planID, query string) (*httptest.ResponseRecorder, planCommentListResponse) {
	t.Helper()
	url := "/api/runtime/plans/" + planID + "/comments"
	if query != "" {
		url += "?" + query
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	var resp planCommentListResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	}
	return rec, resp
}

func postStoredPlanComment(t *testing.T, router *mux.Router, planID, body string) (*httptest.ResponseRecorder, planCommentListResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/plans/"+planID+"/comments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var resp planCommentListResponse
	if rec.Code == http.StatusCreated {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	}
	return rec, resp
}

func deleteStoredPlanComment(t *testing.T, router *mux.Router, planID, commentID string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/runtime/plans/"+planID+"/comments/"+commentID, nil))
	return rec
}

func TestStoredPlanCommentsCreateListAndReplayAcrossRounds(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	record := seedStoredPlanRounds(t, store,
		"# Plan\n\n1. ship it\n2. roll back\n",
		"# Plan\n\n0. prep\n\n1. ship it\n2. roll back\n",
	)
	router := newPlansTestRouter(t, store)

	// 锚定 v1 的 L3-4（"1. ship it" / "2. roll back"）：新建即命中。
	created, createResp := postStoredPlanComment(t, router, record.ID,
		`{"revision":1,"start_line":3,"end_line":4,"body":"补上回滚风险","author":"user"}`)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	require.Equal(t, 1, createResp.Count)
	comment := createResp.Comments[0]
	require.NotEmpty(t, comment.ID)
	require.Equal(t, "anchored", comment.Status)
	require.Equal(t, 1, comment.Revision)
	require.Equal(t, 1, comment.CurrentRevision)
	require.Equal(t, "补上回滚风险", comment.Body)
	require.Equal(t, "1. ship it\n2. roll back", comment.Excerpt)

	// 默认读：按最新轮（v2，前面插入了一段）重放 → 原文仍在但已移动，原锚点照旧可读。
	listed, listResp := getStoredPlanComments(t, router, record.ID, "")
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	require.Equal(t, 2, listResp.Revision)
	require.Equal(t, 2, listResp.LatestRevision)
	require.Equal(t, 1, listResp.Count)
	moved := listResp.Comments[0]
	require.Equal(t, "moved", moved.Status)
	require.Equal(t, 3, moved.StartLine)
	require.Equal(t, 4, moved.EndLine)
	require.Equal(t, 5, moved.CurrentStartLine)
	require.Equal(t, 6, moved.CurrentEndLine)

	// 显式锚定轮：同一评论在 v1 上仍是 anchored。
	_, anchored := getStoredPlanComments(t, router, record.ID, "revision=1")
	require.Equal(t, 1, anchored.Revision)
	require.Equal(t, "anchored", anchored.Comments[0].Status)
	require.Equal(t, 3, anchored.Comments[0].CurrentStartLine)

	// 删除幂等；删完列表为空。
	deleted := deleteStoredPlanComment(t, router, record.ID, comment.ID)
	require.Equal(t, http.StatusOK, deleted.Code, deleted.Body.String())
	require.Contains(t, deleted.Body.String(), `"deleted":true`)
	again := deleteStoredPlanComment(t, router, record.ID, comment.ID)
	require.Contains(t, again.Body.String(), `"deleted":false`)
	_, empty := getStoredPlanComments(t, router, record.ID, "")
	require.Zero(t, empty.Count)
	require.NotNil(t, empty.Comments)
}

func TestStoredPlanCommentDefaultsToLatestRoundAndRewritesAreRejected(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	record := seedStoredPlanRounds(t, store,
		"# Plan\n\n1. ship it\n",
		"# Plan\n\n1. ship it\n2. roll back\n",
	)
	router := newPlansTestRouter(t, store)

	// 省略 revision：锚定最新轮（v2）L4。
	created, resp := postStoredPlanComment(t, router, record.ID, `{"start_line":4,"body":"这一行要写清楚"}`)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	require.Equal(t, 2, resp.Comments[0].Revision)
	require.Equal(t, "2. roll back", resp.Comments[0].Excerpt)
}

func TestStoredPlanCommentsRejectBadInput(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	record := seedStoredPlanRounds(t, store, "# Plan\n\n1. ship it\n")
	router := newPlansTestRouter(t, store)

	cases := []struct {
		name   string
		body   string
		status int
		expect string
	}{
		{"empty body", `{"start_line":1,"body":"   "}`, http.StatusBadRequest, "empty body"},
		{"range past the round", `{"start_line":90,"end_line":95,"body":"x"}`, http.StatusBadRequest, "outside plan revision"},
		{"zero start line", `{"start_line":0,"body":"x"}`, http.StatusBadRequest, "outside plan revision"},
		{"unknown round", `{"revision":9,"start_line":1,"body":"x"}`, http.StatusNotFound, "plan revision not archived: 9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := postStoredPlanComment(t, router, record.ID, tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tc.expect)
		})
	}

	// 未知记录与未知轮次读侧同样是 404。
	unknown, _ := getStoredPlanComments(t, router, "demo/missing", "")
	require.Equal(t, http.StatusNotFound, unknown.Code)
	badRevision, _ := getStoredPlanComments(t, router, record.ID, "revision=9")
	require.Equal(t, http.StatusNotFound, badRevision.Code)
	require.Contains(t, badRevision.Body.String(), "plan revision not archived: 9")
	malformed, _ := getStoredPlanComments(t, router, record.ID, "revision=abc")
	require.Equal(t, http.StatusBadRequest, malformed.Code)

	// 删除未知评论：200 + deleted=false（与 DELETE /plans/{id} 同为幂等）。
	missing := deleteStoredPlanComment(t, router, record.ID, "c-missing")
	require.Equal(t, http.StatusOK, missing.Code)
	require.Contains(t, missing.Body.String(), `"deleted":false`)
}

func TestStoredPlanCommentsWithoutRoundsAreEmpty(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	record, err := store.Record(planstore.RecordOptions{
		SessionID:   "session-1",
		ProjectPath: "/work/demo",
		PlanPath:    "docs/plan.md",
	})
	require.NoError(t, err)
	router := newPlansTestRouter(t, store)

	// 只登记未裁决的记录：读侧 200 + 空列表（revision 0），写侧 404（没有可锚定的轮次）。
	rec, resp := getStoredPlanComments(t, router, record.ID, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Zero(t, resp.Revision)
	require.Zero(t, resp.Count)
	require.NotNil(t, resp.Comments)

	created, _ := postStoredPlanComment(t, router, record.ID, `{"start_line":1,"body":"x"}`)
	require.Equal(t, http.StatusNotFound, created.Code)
	require.Contains(t, created.Body.String(), "plan revision not archived: 0")
}

// 贪婪的 /plans/{id:.*} 明细路由注册在前就会吞掉 /comments 后缀：这条用例把
// 「必须注册在明细路由之前」钉在测试里。
func TestStoredPlanCommentRoutesOutrankTheGreedyDetailRoute(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	record := seedStoredPlanRounds(t, store, "# Plan\n\n1. ship it\n")
	router := newPlansTestRouter(t, store)

	rec, resp := getStoredPlanComments(t, router, record.ID, "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), `"content_available"`)
	require.Equal(t, record.ID, resp.PlanID)
}
