package runtimeapi

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/gitbrowse"
)

// GitBrowseService 是 Git 只读浏览能力的注入接口（未注入时路由统一返回 503 git_unavailable）。
//
// 实现方为 internal/gitbrowse.Service；测试可用 fake 注入，不必拉起 git 进程。
type GitBrowseService interface {
	Status(ctx context.Context, req gitbrowse.RepoRequest) (*gitbrowse.StatusResult, error)
	Diff(ctx context.Context, req gitbrowse.DiffRequest) (*gitbrowse.DiffResult, error)
	Commits(ctx context.Context, req gitbrowse.CommitsRequest) (*gitbrowse.CommitsResult, error)
	Stage(ctx context.Context, req gitbrowse.StageRequest) (*gitbrowse.StageResult, error)
}

// RegisterGitBrowseRoutes 在 /api/runtime 子路由上注册 /git/* 端点
// （router 已经是 /api/runtime 子路由，因此这里只写相对路径）。
//
// service 为 nil 时仍然注册路由并统一返回 503 git_unavailable：
// 「服务未注入」必须表现为可解释的降级，而不是 404 或崩溃（§5.7）。
func RegisterGitBrowseRoutes(router *mux.Router, service GitBrowseService) {
	if router == nil {
		return
	}
	handlers := &gitHandlers{service: service}
	router.HandleFunc("/git/status", handlers.status).Methods(http.MethodGet)
	router.HandleFunc("/git/diff", handlers.diff).Methods(http.MethodGet)
	router.HandleFunc("/git/commits", handlers.commits).Methods(http.MethodGet)
	router.HandleFunc("/git/stage", handlers.stage).Methods(http.MethodPost)
}
