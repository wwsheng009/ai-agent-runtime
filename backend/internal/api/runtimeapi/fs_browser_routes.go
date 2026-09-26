package runtimeapi

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/filebrowse"
)

// FSBrowserService 是文件浏览器后端能力的注入接口（未注入时路由统一返回 503 service_unavailable）。
//
// 实现方为 internal/filebrowse.Service；测试可用 fake 注入，不必碰真实磁盘。
type FSBrowserService interface {
	ListRoots(ctx context.Context) ([]filebrowse.Root, error)
	List(ctx context.Context, req filebrowse.ListRequest) (*filebrowse.ListResult, error)
	Search(ctx context.Context, req filebrowse.SearchRequest) (*filebrowse.SearchResult, error)
	Stat(ctx context.Context, req filebrowse.PathRequest) (*filebrowse.EntryStat, error)
	Preview(ctx context.Context, req filebrowse.PreviewRequest) (*filebrowse.Preview, error)
	OpenDownload(ctx context.Context, req filebrowse.PathRequest) (*filebrowse.DownloadTarget, error)
	InitUpload(ctx context.Context, req filebrowse.UploadInitRequest) (*filebrowse.UploadInitResult, error)
	PutChunk(ctx context.Context, req filebrowse.UploadChunkRequest) (*filebrowse.UploadChunkResult, error)
	UploadStatus(ctx context.Context, uploadID string) (*filebrowse.UploadStatusResult, error)
	CompleteUpload(ctx context.Context, req filebrowse.UploadCompleteRequest) (*filebrowse.UploadCompleteResult, error)
	AbortUpload(ctx context.Context, uploadID string) error
}

// RegisterFSBrowserRoutes 在 /api/runtime 子路由上注册 /fs/* 端点
// （router 已经是 /api/runtime 子路由，因此这里只写相对路径）。
//
// service 为 nil 时仍然注册路由并统一返回 503 service_unavailable：
// 「服务未注入」必须表现为可解释的降级，而不是 404 或崩溃（规划 §5.7）。
func RegisterFSBrowserRoutes(router *mux.Router, service FSBrowserService) {
	if router == nil {
		return
	}
	handlers := &fsBrowserHandlers{service: service}
	router.HandleFunc("/fs/roots", handlers.roots).Methods(http.MethodGet)
	router.HandleFunc("/fs/list", handlers.list).Methods(http.MethodGet)
	router.HandleFunc("/fs/search", handlers.search).Methods(http.MethodGet)
	router.HandleFunc("/fs/stat", handlers.stat).Methods(http.MethodGet)
	router.HandleFunc("/fs/preview", handlers.preview).Methods(http.MethodGet)
	// GET 与 HEAD 共用一个 handler：http.ServeContent 会自动处理 HEAD（只写头不写体）。
	router.HandleFunc("/fs/download", handlers.download).Methods(http.MethodGet, http.MethodHead)

	router.HandleFunc("/fs/upload/init", handlers.uploadInit).Methods(http.MethodPost)
	router.HandleFunc("/fs/upload/{upload_id}/chunk", handlers.uploadChunk).Methods(http.MethodPut)
	router.HandleFunc("/fs/upload/{upload_id}/complete", handlers.uploadComplete).Methods(http.MethodPost)
	router.HandleFunc("/fs/upload/{upload_id}", handlers.uploadStatus).Methods(http.MethodGet)
	router.HandleFunc("/fs/upload/{upload_id}", handlers.uploadAbort).Methods(http.MethodDelete)
}
