// Package restapi 提供连接器管理面与 webhook 入口的宿主无关 HTTP 层：
// 三个前端（gateway console / aicli 微前端 / runtime-server React）共用同一套
// `/connectors*` 契约与同一份 Schema（docs/bots/06-target-architecture.md §2.5）。
//
// 宿主挂载方式（gorilla/mux，与既有 runtimeapi 一致）：
//
//	r.PathPrefix("/api").Handler(restapi.NewRouter(deps))         // /api/connectors*
//	r.PathPrefix("/").Handler(restapi.NewHooksRouter(deps))       // /hooks/connectors/{id}
//
// 安全约定：
//   - 管理面必须注入 Authorize；Authorize 为 nil 时整体 403（避免宿主忘记接线导致裸奔）；
//   - webhook 入口不走 Authorize，由各 Provider 自行校验签名/挑战；
//   - 密钥只写不读：视图仅返回 secret_keys（字段名），不返回明文。
package restapi

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// SchemaVersion 是管理面 Schema 载荷的契约版本（字段新增向后兼容）。
const SchemaVersion = "1"

const defaultMaxBodyBytes = 1 << 20 // 1MiB

// Deps 是 restapi 的依赖注入点。
type Deps struct {
	// Service 提供 Schema/能力探测/入站投递；为 nil 时 handler 返回 503。
	Service *connectors.Service
	// Store 提供配置读写；为 nil 时 handler 返回 503。
	Store connectors.Store
	// Authorize 校验管理面请求；nil = 全部拒绝（安全默认）。
	Authorize func(r *http.Request) error
	// HostID 是本宿主的运行归属身份（与 connectors.Deps.HostID 一致）：
	// 视图用它计算 owned_by_current_host，「抢占」端点用它认领归属；
	// 为空时 takeover 返回 503（其余端点不受影响）。
	HostID string
	// Logf 可注入日志；默认 log.Printf。
	Logf func(format string, args ...any)
	// MaxBodyBytes 请求体上限，<=0 用默认 1MiB。
	MaxBodyBytes int64
}

func (d Deps) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (d Deps) maxBody() int64 {
	if d.MaxBodyBytes > 0 {
		return d.MaxBodyBytes
	}
	return defaultMaxBodyBytes
}

// NewRouter 返回管理面路由（宿主挂到 /api）：/connectors*。
func NewRouter(deps Deps) http.Handler {
	router := mux.NewRouter()
	router.Use(authorizeMiddleware(deps))
	handlers := &handlers{deps: deps}
	router.HandleFunc("/connectors", handlers.list).Methods(http.MethodGet)
	router.HandleFunc("/connectors", handlers.create).Methods(http.MethodPost)
	router.HandleFunc("/connectors/schema", handlers.schemaDocument).Methods(http.MethodGet)
	// 绑定管理（docs/bots/09 §5.2）：字面量 /bindings 必须先于 /{id} 注册，
	// 否则会被当作 id 吞掉。
	router.HandleFunc("/connectors/bindings", handlers.listBindings).Methods(http.MethodGet)
	router.HandleFunc("/connectors/{id}", handlers.get).Methods(http.MethodGet)
	// PATCH 是契约首选（部分更新语义：缺省字段保持不变）；PUT 作为别名兼容。
	router.HandleFunc("/connectors/{id}", handlers.update).Methods(http.MethodPatch, http.MethodPut)
	router.HandleFunc("/connectors/{id}", handlers.remove).Methods(http.MethodDelete)
	router.HandleFunc("/connectors/{id}/logs", handlers.logs).Methods(http.MethodGet)
	router.HandleFunc("/connectors/{id}/reload", handlers.reload).Methods(http.MethodPost)
	router.HandleFunc("/connectors/{id}/takeover", handlers.takeover).Methods(http.MethodPost)
	router.HandleFunc("/connectors/{id}/test", handlers.test).Methods(http.MethodPost)
	router.HandleFunc("/connectors/{id}/actions/{action}", handlers.action).Methods(http.MethodPost)
	router.HandleFunc("/connectors/{id}/bindings/{chat}", handlers.putBinding).Methods(http.MethodPut)
	router.HandleFunc("/connectors/{id}/bindings/{chat}", handlers.deleteBinding).Methods(http.MethodDelete)
	return router
}

// NewHooksRouter 返回公网 webhook 入口（宿主挂到根）：/hooks/connectors/{id}。
func NewHooksRouter(deps Deps) http.Handler {
	router := mux.NewRouter()
	handlers := &handlers{deps: deps}
	router.HandleFunc("/hooks/connectors/{id}", handlers.webhook).Methods(http.MethodPost)
	return router
}

func authorizeMiddleware(deps Deps) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if deps.Authorize == nil {
				writeError(w, http.StatusForbidden, "连接器管理面未启用（缺少 Authorizer 接线）")
				return
			}
			if err := deps.Authorize(r); err != nil {
				writeError(w, http.StatusUnauthorized, "未授权："+err.Error())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type handlers struct {
	deps Deps
}

func (h *handlers) ready(w http.ResponseWriter) bool {
	if h.deps.Store == nil || h.deps.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "连接器服务未接线")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

// decodeBody 解析 JSON 请求体；未知字段容忍（向前兼容），空体返回零值。
func decodeBody(deps Deps, w http.ResponseWriter, r *http.Request, out any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, deps.maxBody()))
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取请求体失败："+err.Error())
		return false
	}
	if len(body) == 0 {
		return true
	}
	if err := json.Unmarshal(body, out); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON："+err.Error())
		return false
	}
	return true
}
