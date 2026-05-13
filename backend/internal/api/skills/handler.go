package skills

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimecontext "github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextpack"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	mcpcatalog "github.com/wwsheng009/ai-agent-runtime/internal/mcp/catalog"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	toolbrokersessionctx "github.com/wwsheng009/ai-agent-runtime/internal/toolbroker/sessionctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
	"go.uber.org/zap/zapcore"
)

type searchMode string

const (
	searchModeAuto     searchMode = "auto"
	searchModeLexical  searchMode = "lexical"
	searchModeSemantic searchMode = "semantic"
	searchModeHybrid   searchMode = "hybrid"

	canonicalRuntimeEntrypoint   = "/api/runtime"
	canonicalAgentChatEntrypoint = "/api/agent/chat"

	skillMutationActionCreate         = "skill_create"
	skillMutationActionUpdate         = "skill_update"
	skillMutationActionDelete         = "skill_delete"
	skillMutationActionBatchCreate    = "skill_batch_create"
	skillMutationActionImport         = "skill_import"
	skillMutationActionReload         = "skill_reload"
	skillMutationActionConfigWrite    = "skill_config_write"
	skillMutationActionHotReloadStart = "skill_hot_reload_start"
	skillMutationActionHotReloadStop  = "skill_hot_reload_stop"
	skillMutationActionHotReloadRun   = "skill_hot_reload_reload"
	skillHotReloadActionAdded         = "skill_hot_reload_added"
	skillHotReloadActionUpdated       = "skill_hot_reload_updated"
	skillHotReloadActionRemoved       = "skill_hot_reload_removed"
	skillsChangedEventType            = "skills.changed"

	apiProfileContextReference = sessionmeta.LegacyAPIProfileReference
	apiProfileContextName      = sessionmeta.ProfileName
	apiProfileContextAgent     = sessionmeta.ProfileAgent
	apiProfileContextRoot      = sessionmeta.ProfileRoot

	sessionActorLeaseOwnerKind = "runtime-server-actor"
	agentChatLeaseOwnerKind    = "runtime-server-agent-chat"
)

// Handler Skills API 处理器
type Handler struct {
	skillRegistry                  *skill.Registry
	skillLoader                    *skill.Loader
	mcpManager                     skill.MCPManager
	llmRuntime                     *llm.LLMRuntime
	sessionManager                 *chat.SessionManager
	hotReload                      *skill.HotReload
	embeddingRouter                *skill.SemanticEmbeddingRouter
	embeddingHotReloadSyncAttached bool
	searchTelemetry                *searchTelemetry
	searchAdminToken               string
	searchReindexCooldown          time.Duration
	searchReindexMu                sync.Mutex
	lastSearchReindexAt            time.Time
	mutationPolicyMu               sync.RWMutex
	mutationPolicy                 MutationPolicy
	usagePolicyMu                  sync.RWMutex
	usagePolicy                    UsagePolicy
	usageTracker                   *usageTracker
	usageLedgerStore               UsageLedgerStore
	authPolicyPersister            AuthPolicyPersister
	usagePolicyPersister           UsagePolicyPersister
	mutationPolicyPersister        MutationPolicyPersister
	runtimeEventBus                *runtimeevents.Bus
	runtimeToolCatalog             *mcpcatalog.Gateway
	runtimeToolCatalogConfigKey    string
	runtimeMCPBridgeOnce           sync.Once
	runtimeEventBridgeOnce         sync.Once
	scopeResolverMu                sync.RWMutex
	scopeResolverConfig            ScopeResolverConfig
	runtimeConfig                  *runtimecfg.RuntimeConfig
	runtimeConfigFile              string
	runtimeConfigResolver          func(UsageScope) *runtimecfg.RuntimeConfig
	configDocumentService          ConfigDocumentService
	serviceControlService          RuntimeServiceControlService
	fileTransferService            FileTransferService
	logFilePath                    string
	profileRegistry                *profilesys.Registry
	profileDefaultRef              string
	profileGlobalRuntimePath       string
	profileGlobalMCPPath           string
	profileGlobalSkillDirs         []string
	profileMCPAutoConnect          bool

	teamStoreMu        sync.RWMutex
	teamStoreConfigKey string
	teamStore          team.Store
	teamOrchestrator   *team.Orchestrator
	teamClaimsManager  *team.PathClaimManager
	teamLifecycle      *handlerTeamLifecycleService

	agentControlMu               sync.RWMutex
	agentControlRegistryService  *agentcontrol.RegistryService
	agentControlRegistryStoreKey string
	agentControlMailboxStore     agentcontrol.GlobalMailboxRegistryStore
	agentControlMailboxStoreKey  string
	agentControlMailboxStoreAuto bool
	agentControlAgentStore       agentcontrol.AgentRegistryStore
	agentControlAgentStoreKey    string
	agentControlAgentStoreAuto   bool

	sessionRuntimeMu       sync.RWMutex
	sessionHub             *chat.SessionHub
	sessionRuntimeStore    chat.RuntimeStateStore
	sessionEventStore      chat.EventStore
	sessionRuntimeStoreKey string

	backgroundMu        sync.Mutex
	backgroundManager   *background.Manager
	backgroundConfigKey string

	codexSkillsListMu           sync.RWMutex
	codexSkillsListCache        map[string]codexSkillsListResponse
	codexSkillsListCacheVersion uint64
}

type searchTelemetry struct {
	mu                 sync.RWMutex
	totalRequests      int
	totalResults       int
	embeddingRequests  int
	reindexCount       int
	lastQuery          string
	lastRequestedMode  string
	lastResolvedMode   string
	lastResultCount    int
	lastEmbeddingUsed  bool
	lastReindexStatus  string
	lastReindexAt      time.Time
	requestedModeCount map[string]int
	resolvedModeCount  map[string]int
}

type routeHeaderOptions struct {
	canonicalEntrypoint string
	canonicalResolver   func(*http.Request) string
	mode                string
	warning             string
	warningResolver     func(*http.Request) string
}

// MutationPolicy 控制 skills 变更接口的轻量治理策略。
type MutationPolicy struct {
	ReadOnly         bool
	DisableImport    bool
	DisablePersist   bool
	DisableReloadOps bool
	DisableHotReload bool
}

// UsagePolicy 控制 usage tracking 与 quota。
type UsagePolicy struct {
	TrackingEnabled    bool
	QuotaEnabled       bool
	DefaultMaxRequests int
	DefaultMaxTokens   int
	TenantQuotas       map[string]UsageQuotaLimit
	ProjectQuotas      map[string]UsageQuotaLimit
	UserQuotas         map[string]UsageQuotaLimit
}

type UsageQuotaLimit struct {
	MaxRequests *int `json:"max_requests,omitempty"`
	MaxTokens   *int `json:"max_tokens,omitempty"`
}

type ScopeResolverConfig struct {
	Enabled          bool
	TenantHeaders    []string
	ProjectHeaders   []string
	UserHeaders      []string
	RoleHeaders      []string
	JWTClaimsEnabled bool
	JWTSecret        string
	TenantClaims     []string
	ProjectClaims    []string
	UserClaims       []string
	RoleClaims       []string
	AdminRoles       []string
	APIKeyScopes     map[string]UsageScope
}

type usagePolicyUpdateRequest struct {
	Replace            bool                       `json:"replace,omitempty"`
	TrackingEnabled    *bool                      `json:"tracking_enabled,omitempty"`
	QuotaEnabled       *bool                      `json:"quota_enabled,omitempty"`
	DefaultMaxRequests *int                       `json:"default_max_requests,omitempty"`
	DefaultMaxTokens   *int                       `json:"default_max_tokens,omitempty"`
	Tenants            map[string]UsageQuotaLimit `json:"tenants,omitempty"`
	Projects           map[string]UsageQuotaLimit `json:"projects,omitempty"`
	Users              map[string]UsageQuotaLimit `json:"users,omitempty"`
}

type authPolicyUpdateRequest struct {
	Replace          bool                  `json:"replace,omitempty"`
	Enabled          *bool                 `json:"enabled,omitempty"`
	JWTClaimsEnabled *bool                 `json:"jwt_claims_enabled,omitempty"`
	TenantHeaders    []string              `json:"tenant_headers,omitempty"`
	ProjectHeaders   []string              `json:"project_headers,omitempty"`
	UserHeaders      []string              `json:"user_headers,omitempty"`
	RoleHeaders      []string              `json:"role_headers,omitempty"`
	TenantClaims     []string              `json:"tenant_claims,omitempty"`
	ProjectClaims    []string              `json:"project_claims,omitempty"`
	UserClaims       []string              `json:"user_claims,omitempty"`
	RoleClaims       []string              `json:"role_claims,omitempty"`
	AdminRoles       []string              `json:"admin_roles,omitempty"`
	APIKeyScopes     map[string]UsageScope `json:"api_key_scopes,omitempty"`
}

type mutationPolicyUpdateRequest struct {
	ReadOnly         *bool `json:"read_only,omitempty"`
	DisableImport    *bool `json:"disable_import,omitempty"`
	DisablePersist   *bool `json:"disable_persist,omitempty"`
	DisableReloadOps *bool `json:"disable_reload_ops,omitempty"`
	DisableHotReload *bool `json:"disable_hot_reload,omitempty"`
}

type UsageLedgerStore interface {
	Create(history *entity.TokenUsageHistory) error
	GetSince(since time.Time, limit int) ([]*entity.TokenUsageHistory, error)
}

type AuthPolicyPersister func(ScopeResolverConfig, string) error
type UsagePolicyPersister func(UsagePolicy, string) error
type MutationPolicyPersister func(MutationPolicy, string) error

type usageTracker struct {
	mu    sync.RWMutex
	users map[string]*UsageSnapshot
}

type UsageScope struct {
	TenantID  string `json:"tenant_id"`
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
	ScopeKey  string `json:"scope_key"`
}

type UsageSnapshot struct {
	TenantID         string         `json:"tenant_id"`
	ProjectID        string         `json:"project_id"`
	UserID           string         `json:"user_id"`
	ScopeKey         string         `json:"scope_key"`
	RequestCount     int            `json:"request_count"`
	ExecuteCount     int            `json:"execute_count"`
	AgentChatCount   int            `json:"agent_chat_count"`
	SuccessCount     int            `json:"success_count"`
	FailureCount     int            `json:"failure_count"`
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	TotalTokens      int            `json:"total_tokens"`
	LastSkill        string         `json:"last_skill,omitempty"`
	LastEntrypoint   string         `json:"last_entrypoint,omitempty"`
	LastRequestAt    time.Time      `json:"last_request_at,omitempty"`
	EntrypointCounts map[string]int `json:"entrypoint_counts,omitempty"`
	SkillCounts      map[string]int `json:"skill_counts,omitempty"`
}

// NewHandler 创建 Skills API 处理器
func NewHandler(
	registry *skill.Registry,
	loader *skill.Loader,
	mcpManager skill.MCPManager,
) *Handler {
	return &Handler{
		skillRegistry:               registry,
		skillLoader:                 loader,
		mcpManager:                  mcpManager,
		runtimeEventBus:             runtimeevents.NewBusWithRetention(2048),
		searchReindexCooldown:       30 * time.Second,
		codexSkillsListCache:        make(map[string]codexSkillsListResponse),
		codexSkillsListCacheVersion: 0,
		usageTracker: &usageTracker{
			users: make(map[string]*UsageSnapshot),
		},
		searchTelemetry: &searchTelemetry{
			requestedModeCount: make(map[string]int),
			resolvedModeCount:  make(map[string]int),
		},
	}
}

// SetLLMRuntime 设置 LLM Runtime
func (h *Handler) SetLLMRuntime(runtime *llm.LLMRuntime) {
	h.llmRuntime = runtime
}

// SetSessionManager 设置 Session Manager
func (h *Handler) SetSessionManager(manager *chat.SessionManager) {
	h.sessionManager = manager
}

// SetHotReload 设置 HotReload
func (h *Handler) SetHotReload(hotReload *skill.HotReload) {
	h.hotReload = hotReload
	h.attachEmbeddingHotReloadSync()
}

// SetEmbeddingRouter 设置 Embedding 路由器
func (h *Handler) SetEmbeddingRouter(router *skill.SemanticEmbeddingRouter) {
	h.embeddingRouter = router
	h.attachEmbeddingHotReloadSync()
}

// SetSearchAdminToken 设置搜索运维接口的管理员令牌
func (h *Handler) SetSearchAdminToken(token string) {
	h.searchAdminToken = strings.TrimSpace(token)
}

// SetAdminToken 设置 skills 管理接口的管理员令牌
func (h *Handler) SetAdminToken(token string) {
	h.SetSearchAdminToken(token)
}

// SetMutationPolicy 设置 skills 变更治理策略
func (h *Handler) SetMutationPolicy(policy MutationPolicy) {
	h.mutationPolicyMu.Lock()
	defer h.mutationPolicyMu.Unlock()
	h.mutationPolicy = policy
}

// SetUsagePolicy 设置 usage tracking / quota 策略
func (h *Handler) SetUsagePolicy(policy UsagePolicy) {
	if policy.QuotaEnabled {
		policy.TrackingEnabled = true
	}
	h.usagePolicyMu.Lock()
	defer h.usagePolicyMu.Unlock()
	h.usagePolicy = cloneUsagePolicy(policy)
}

// SetUsageLedgerStore 设置 usage ledger 持久化存储
func (h *Handler) SetUsageLedgerStore(store UsageLedgerStore) {
	h.usageLedgerStore = store
}

// SetAuthPolicyPersister 设置 auth/scope policy 持久化回调
func (h *Handler) SetAuthPolicyPersister(persister AuthPolicyPersister) {
	h.authPolicyPersister = persister
}

// SetUsagePolicyPersister 设置 usage/quota policy 持久化回调
func (h *Handler) SetUsagePolicyPersister(persister UsagePolicyPersister) {
	h.usagePolicyPersister = persister
}

// SetMutationPolicyPersister 设置 mutation policy 持久化回调
func (h *Handler) SetMutationPolicyPersister(persister MutationPolicyPersister) {
	h.mutationPolicyPersister = persister
}

// SetScopeResolverConfig 设置 scope 解析配置
func (h *Handler) SetScopeResolverConfig(config ScopeResolverConfig) {
	h.scopeResolverMu.Lock()
	defer h.scopeResolverMu.Unlock()
	h.scopeResolverConfig = cloneScopeResolverConfig(config)
}

// SetRuntimeConfig 设置 skills runtime 配置快照与路径
func (h *Handler) SetRuntimeConfig(config *runtimecfg.RuntimeConfig, configFile string) {
	if config != nil {
		sessionruntime.ApplyDefaults(config, sessionruntime.ResolveOptions{
			Config:     config,
			ConfigFile: configFile,
			Mode:       sessionruntime.ModeServer,
		})
	}
	h.runtimeConfig = config
	h.runtimeConfigFile = strings.TrimSpace(configFile)
	_, _ = h.refreshTeamStore(config, h.runtimeConfigFile, "", "")
	_, _ = h.refreshSessionRuntimeStore(config, h.runtimeConfigFile)
	_, _ = h.refreshAgentControlRegistryService(config, h.runtimeConfigFile)
}

// SetRuntimeConfigResolver 设置 runtime config 选择器（用于 rollout）
func (h *Handler) SetRuntimeConfigResolver(resolver func(UsageScope) *runtimecfg.RuntimeConfig) {
	h.runtimeConfigResolver = resolver
}

// SetRuntimeLogFilePath 设置 runtime 服务日志文件路径。
func (h *Handler) SetRuntimeLogFilePath(path string) {
	path = aiclipaths.ExpandUserPath(path)
	if path != "" {
		if absolutePath, err := filepath.Abs(path); err == nil {
			path = absolutePath
		}
	}
	h.logFilePath = path
}

// SetSearchReindexCooldown 设置索引重建冷却时间
func (h *Handler) SetSearchReindexCooldown(cooldown time.Duration) {
	if cooldown < 0 {
		cooldown = 0
	}
	h.searchReindexCooldown = cooldown
}

func (h *Handler) withRouteHeaders(next http.HandlerFunc, options routeHeaderOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r != nil && r.URL != nil {
			w.Header().Set("X-AI-Gateway-Entrypoint", r.URL.Path)
		}
		canonicalEntrypoint := strings.TrimSpace(options.canonicalEntrypoint)
		if options.canonicalResolver != nil {
			canonicalEntrypoint = strings.TrimSpace(options.canonicalResolver(r))
		}
		warning := strings.TrimSpace(options.warning)
		if options.warningResolver != nil {
			warning = strings.TrimSpace(options.warningResolver(r))
		}
		if canonicalEntrypoint != "" {
			w.Header().Set("X-AI-Gateway-Canonical-Entrypoint", canonicalEntrypoint)
			if warning != "" || (r != nil && r.URL != nil && r.URL.Path != canonicalEntrypoint) {
				w.Header().Set("Link", fmt.Sprintf("<%s>; rel=\"canonical\"", canonicalEntrypoint))
			}
		}
		if options.mode != "" {
			w.Header().Set("X-AI-Gateway-Entrypoint-Mode", options.mode)
		}
		if warning != "" {
			w.Header().Set("Warning", warning)
		}
		next(w, r)
	}
}

func adminDebugRouteWarning(entrypoint, preferredPath string) string {
	entrypoint = strings.TrimSpace(entrypoint)
	preferredPath = strings.TrimSpace(preferredPath)
	if entrypoint == "" || preferredPath == "" {
		return ""
	}
	return fmt.Sprintf(`299 ai-agent-runtime "%s is an admin/debug endpoint; prefer %s for normal usage"`, entrypoint, preferredPath)
}

func requestPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	return strings.TrimSpace(r.URL.Path)
}

// RegisterRoutes 注册路由
func (h *Handler) RegisterRoutes(router *mux.Router) *mux.Router {
	runtimeRouter := router.PathPrefix(canonicalRuntimeEntrypoint).Subrouter()
	agentRouter := router.PathPrefix("/api/agent").Subrouter()

	agentRouter.HandleFunc("/chat", h.withRouteHeaders(h.AgentChat, routeHeaderOptions{
		canonicalEntrypoint: canonicalAgentChatEntrypoint,
		mode:                "canonical",
	})).Methods(http.MethodPost)

	// Skills 管理与执行
	runtimeRouter.HandleFunc("/skills", h.ListSkills).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/skills/list", h.ListCodexSkills).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/skills", h.CreateSkill).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/skills/search", h.SearchSkills).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/skills/search/stats", h.GetSearchStats).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/skills/search/reindex", h.ReindexSearchIndex).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/capabilities", h.ListCapabilities).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/models", h.GetRuntimeModels).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/skills/batch", h.BatchCreateSkills).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/skills/stats", h.GetStats).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/skills/reload", h.ReloadSkills).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/skills/validate", h.ValidateSkill).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/skills/export", h.ExportSkills).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/skills/import", h.ImportSkills).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/skills/hot-reload/start", h.StartHotReload).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/skills/hot-reload/stop", h.StopHotReload).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/skills/hot-reload/reload", h.ReloadHotReload).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/skills/hot-reload/stats", h.GetHotReloadStats).Methods(http.MethodGet)

	// Runtime / governance / observability
	runtimeRouter.HandleFunc("/usage/stats", h.GetUsageStats).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/usage/ledger", h.GetUsageLedger).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/usage/reset", h.ResetUsageStats).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/usage/policy", h.GetUsagePolicy).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/usage/policy", h.UpdateUsagePolicy).Methods(http.MethodPut)
	runtimeRouter.HandleFunc("/usage/policy", h.DeleteUsagePolicyEntry).Methods(http.MethodDelete)
	runtimeRouter.HandleFunc("/mutation/policy", h.GetMutationPolicy).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/mutation/policy", h.UpdateMutationPolicy).Methods(http.MethodPut)
	runtimeRouter.HandleFunc("/auth/policy", h.GetAuthPolicy).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/auth/policy", h.UpdateAuthPolicy).Methods(http.MethodPut)
	runtimeRouter.HandleFunc("/auth/policy", h.DeleteAuthPolicyEntry).Methods(http.MethodDelete)
	runtimeRouter.HandleFunc("/governance/policy", h.GetGovernancePolicy).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/config/document", h.GetConfigDocument).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/config/document/preview", h.PreviewConfigDocument).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/config/document", h.UpdateConfigDocument).Methods(http.MethodPut)
	runtimeRouter.HandleFunc("/skills/config/write", h.WriteConfigDocument).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/service", h.GetRuntimeServiceStatus).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/service/restart", h.RestartRuntimeService).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/fs/read-file", h.ReadRuntimeFile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/fs/write-file", h.WriteRuntimeFile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/fs/append-file", h.AppendRuntimeFile).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/debug/prompt-layout", h.withRouteHeaders(h.PreviewPromptLayout, routeHeaderOptions{
		canonicalEntrypoint: canonicalRuntimeEntrypoint + "/debug/prompt-layout",
		mode:                "admin-debug",
		warning:             adminDebugRouteWarning(canonicalRuntimeEntrypoint+"/debug/prompt-layout", canonicalAgentChatEntrypoint),
	})).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/status", h.GetRuntimeStatus).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/health", h.GetRuntimeHealth).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/events", h.ListRuntimeEvents).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/logs", h.ListRuntimeLogs).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/logs/stream", h.StreamRuntimeLogs).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/traces/stats", h.GetRuntimeTraceStats).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/traces/governance", h.GetRuntimeTraceGovernance).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/traces", h.GetRuntimeTraces).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/traces/{trace_id}", h.GetRuntimeTrace).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/background/jobs", h.ListBackgroundJobs).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/background/jobs/{id}", h.GetBackgroundJob).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/background/jobs/{id}/cancel", h.CancelBackgroundJob).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/background/jobs/{id}/events", h.ListBackgroundJobEvents).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/background/jobs/{id}/output", h.GetBackgroundJobOutput).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/mcps/reload", h.ReloadRuntimeMCPs).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/reload", h.ReloadRuntimeTeams).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/validate", h.ValidateRuntimeConfig).Methods(http.MethodGet)

	// Sessions
	runtimeRouter.HandleFunc("/sessions", h.ListSessions).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions", h.CreateSession).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/search", h.SearchSessions).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/batch/delete", h.BatchDeleteSessions).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/batch/archive", h.BatchArchiveSessions).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/stats", h.GetSessionStats).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/users", h.ListSessionUsers).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}", h.GetSession).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}", h.UpdateSession).Methods(http.MethodPatch)
	runtimeRouter.HandleFunc("/sessions/{id}", h.DeleteSession).Methods(http.MethodDelete)
	runtimeRouter.HandleFunc("/sessions/{id}/archive", h.ArchiveSession).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/{id}/activate", h.ActivateSession).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/{id}/close", h.CloseSession).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/{id}/history", h.GetSessionHistory).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/runtime", h.GetSessionRuntimeState).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/runtime/events", h.ListSessionRuntimeEvents).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/runtime/tools", h.ListSessionRuntimeTools).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/runtime/tool-receipts", h.ListSessionToolReceipts).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/runtime/stream", h.StreamSessionRuntimeEvents).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/runtime/commands", h.SubmitSessionRuntimeCommand).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/{id}/agent-control/mailbox", h.ListSessionAgentControlMailbox).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/agents", h.SpawnSessionAgent).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/{id}/agents/wait", h.WaitSessionAgents).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/{id}/agents/events", h.ListSessionAgentEvents).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/agents/{agent_id}", h.GetSessionAgentStatus).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/agents/{agent_id}/input", h.SendSessionAgentInput).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/{id}/agents/{agent_id}/events", h.ListSessionAgentEvents).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/agents/{agent_id}/close", h.CloseSessionAgent).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/{id}/agents/{agent_id}/resume", h.ResumeSessionAgent).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/{id}/generated-images/{name}", h.GetSessionGeneratedImage).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/checkpoints", h.ListSessionCheckpoints).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/checkpoints/{checkpoint_id}/files", h.GetCheckpointFiles).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/sessions/{id}/checkpoints/{checkpoint_id}/preview", h.PreviewSessionCheckpoint).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/{id}/checkpoints/{checkpoint_id}/restore", h.RestoreSessionCheckpoint).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/sessions/{id}/history", h.ClearSessionHistory).Methods(http.MethodDelete)

	// Teams
	runtimeRouter.HandleFunc("/agent-control/agents", h.ListAgentControlAgents).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/agent-control/mailbox", h.ListAgentControlMailbox).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/agent-control/tasks", h.ListAgentControlTasks).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/agent-control/tasks", h.CreateAgentControlTask).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/agent-control/tasks/events", h.ListAgentControlTaskGraphEvents).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/agent-control/tasks/{task_id}", h.UpdateAgentControlTask).Methods(http.MethodPatch)
	runtimeRouter.HandleFunc("/agent-control/tasks/{task_id}/status", h.UpdateAgentControlTaskStatus).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/agent-control/tasks/{task_id}/claim", h.ClaimAgentControlTask).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/agent-control/tasks/{task_id}/lease", h.RenewAgentControlTaskLease).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/agent-control/tasks/{task_id}/release", h.ReleaseAgentControlTask).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/agent-control/tasks/{task_id}/terminal", h.UpdateAgentControlTaskTerminal).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/agent-control/tasks/{task_id}/block", h.BlockAgentControlTask).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/agent-control/tasks/{task_id}/dependencies", h.ListAgentControlTaskDependencies).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/agent-control/tasks/{task_id}/dependencies", h.CreateAgentControlTaskDependency).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams", h.ListTeams).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams", h.CreateTeam).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/summary", h.ListTeamSummaries).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}", h.GetTeam).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}", h.UpdateTeam).Methods(http.MethodPatch)
	runtimeRouter.HandleFunc("/teams/{id}", h.DeleteTeam).Methods(http.MethodDelete)
	runtimeRouter.HandleFunc("/teams/{id}/events", h.ListTeamEvents).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}/summary", h.GetTeamSummary).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}/summary/final", h.GetTeamFinalSummary).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}/teammates", h.ListTeammates).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}/teammates", h.UpsertTeammate).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/teammates/{teammate_id}", h.UpdateTeammate).Methods(http.MethodPatch)
	runtimeRouter.HandleFunc("/teams/{id}/teammates/{teammate_id}/heartbeat", h.UpdateTeammateHeartbeat).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/tasks", h.ListTasks).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}/plan", h.PlanTeamTasks).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/graph", h.GetTaskGraph).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}/tasks", h.CreateTask).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/{task_id}", h.GetTask).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/{task_id}", h.UpdateTask).Methods(http.MethodPatch)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/{task_id}/dependencies", h.ListTaskDependencies).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/{task_id}/dependencies", h.AddTaskDependency).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/{task_id}/dependents", h.ListTaskDependents).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/{task_id}/replan", h.ReplanTask).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/{task_id}/lease", h.RenewTaskLease).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/{task_id}/release", h.ReleaseTaskLease).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/{task_id}/retry", h.RetryTask).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/{task_id}/outcome", h.withRouteHeaders(h.ReportTaskOutcome, routeHeaderOptions{
		canonicalResolver: requestPath,
		mode:              "canonical",
	})).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/ready", h.MarkReadyTasks).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/claim", h.ClaimReadyTasks).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/tasks/reclaim", h.ReclaimExpiredTasks).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/mailbox", h.ListMailbox).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}/mailbox", h.SendMailboxMessage).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/mailbox/{message_id}/ack", h.AckMailboxMessage).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/path-claims", h.ListPathClaims).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/teams/{id}/path-claims/check", h.CheckPathClaims).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/path-claims/prune", h.PrunePathClaims).Methods(http.MethodPost)
	runtimeRouter.HandleFunc("/teams/{id}/teammates/sweep", h.SweepTeammates).Methods(http.MethodPost)

	runtimeRouter.HandleFunc("/skills/{name}", h.GetSkill).Methods(http.MethodGet)
	runtimeRouter.HandleFunc("/skills/{name}", h.UpdateSkill).Methods(http.MethodPut)
	runtimeRouter.HandleFunc("/skills/{name}", h.DeleteSkill).Methods(http.MethodDelete)
	runtimeRouter.HandleFunc("/skills/{name}/execute", h.withRouteHeaders(h.ExecuteSkill, routeHeaderOptions{
		canonicalResolver: requestPath,
		mode:              "admin-debug",
		warningResolver: func(r *http.Request) string {
			return adminDebugRouteWarning(requestPath(r), canonicalAgentChatEntrypoint)
		},
	})).Methods(http.MethodPost)

	return runtimeRouter
}

// ListSkills 列出所有 Skills
func (h *Handler) ListSkills(w http.ResponseWriter, r *http.Request) {
	layer, dir := parseSkillSourceFilters(r)
	skills := filterSkillsBySource(h.skillRegistry.List(), layer, dir)
	hydratedSkills, err := h.hydrateSkillsForResponse(skills)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	response := map[string]interface{}{
		"skills": hydratedSkills,
		"count":  len(hydratedSkills),
	}

	h.writeJSON(w, http.StatusOK, response)
}

// GetSkill 获取单个 Skill
func (h *Handler) GetSkill(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	skillItem, exists := h.skillRegistry.Get(name)
	if !exists {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrSkillNotFound,
			fmt.Sprintf("skill not found: %s", name)))
		return
	}

	hydratedSkill, err := h.hydrateSkillForResponse(skillItem)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, hydratedSkill)
}

// CreateSkill 创建新 Skill
func (h *Handler) CreateSkill(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSkillMutation(r); err != nil {
		h.auditSkillMutation(r, skillMutationActionCreate, "forbidden", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforceMutationActionPolicy(skillMutationActionCreate, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionCreate, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforcePersistPolicy(nil, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionCreate, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	var newSkill skill.Skill
	if err := json.NewDecoder(r.Body).Decode(&newSkill); err != nil {
		h.auditSkillMutation(r, skillMutationActionCreate, "invalid_request")
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}
	newSkill.SetSource("", "", skill.SkillSourceLayerRuntime)

	if err := h.skillRegistry.Register(&newSkill); err != nil {
		h.auditSkillMutation(r, skillMutationActionCreate, "failed", logger.Err(err))
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	// 尝试更新 embedding 索引（如果有）
	h.updateEmbeddingIndex(&newSkill)

	if shouldPersistSkill(r) {
		if err := h.persistSkill(&newSkill, nil, r); err != nil {
			h.auditSkillMutation(r, skillMutationActionCreate, "failed", logger.Err(err))
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	skillChange := skillChangePayloadFromSkill(&newSkill, handlerSkillDirs(h.skillLoader))
	skillChange["action"] = skillMutationActionCreate
	skillChange["status"] = "success"
	skillChange["affected_count"] = 1
	skillChange["count"] = h.currentSkillCount()
	h.publishSkillsChangedEvent(r, skillChange)
	h.auditSkillMutation(r, skillMutationActionCreate, "success",
		logger.String("skill", newSkill.Name),
		logger.String("source_layer", newSkill.Source.Layer))

	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"message": "skill created successfully",
		"skill":   newSkill.Name,
		"source":  newSkill.Source,
	})
}

// UpdateSkill 更新 Skill
func (h *Handler) UpdateSkill(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSkillMutation(r); err != nil {
		h.auditSkillMutation(r, skillMutationActionUpdate, "forbidden", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforceMutationActionPolicy(skillMutationActionUpdate, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionUpdate, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	vars := mux.Vars(r)
	name := vars["name"]

	// 检查 Skill 是否存在
	existingSkill, exists := h.skillRegistry.Get(name)
	if !exists {
		h.auditSkillMutation(r, skillMutationActionUpdate, "not_found", logger.String("skill", name))
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrSkillNotFound,
			fmt.Sprintf("skill not found: %s", name)))
		return
	}
	if err := h.enforcePersistPolicy(existingSkill, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionUpdate, "disabled", logger.String("skill", name), logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	var updatedSkill skill.Skill
	if err := json.NewDecoder(r.Body).Decode(&updatedSkill); err != nil {
		h.auditSkillMutation(r, skillMutationActionUpdate, "invalid_request", logger.String("skill", name))
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}
	updatedSkill.SetSource("", "", skill.SkillSourceLayerRuntime)

	// 注销旧的 Skill
	h.skillRegistry.Unregister(name)

	// 注册新的 Skill
	if err := h.skillRegistry.Register(&updatedSkill); err != nil {
		h.auditSkillMutation(r, skillMutationActionUpdate, "failed", logger.String("skill", name), logger.Err(err))
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	h.removeEmbeddingIndex(existingSkill)
	h.updateEmbeddingIndex(&updatedSkill)

	if shouldPersistUpdatedSkill(existingSkill, r) {
		if err := h.persistSkill(&updatedSkill, existingSkill.Source, r); err != nil {
			h.auditSkillMutation(r, skillMutationActionUpdate, "failed", logger.String("skill", name), logger.Err(err))
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	skillChange := skillChangePayloadFromSkill(&updatedSkill, handlerSkillDirs(h.skillLoader))
	skillChange["action"] = skillMutationActionUpdate
	skillChange["status"] = "success"
	skillChange["affected_count"] = 1
	skillChange["count"] = h.currentSkillCount()
	h.publishSkillsChangedEvent(r, skillChange)
	h.auditSkillMutation(r, skillMutationActionUpdate, "success",
		logger.String("skill", updatedSkill.Name),
		logger.String("source_layer", updatedSkill.Source.Layer))

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "skill updated successfully",
		"skill":   updatedSkill.Name,
		"source":  updatedSkill.Source,
	})
}

// DeleteSkill 删除 Skill
func (h *Handler) DeleteSkill(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSkillMutation(r); err != nil {
		h.auditSkillMutation(r, skillMutationActionDelete, "forbidden", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforceMutationActionPolicy(skillMutationActionDelete, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionDelete, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	vars := mux.Vars(r)
	name := vars["name"]

	// 检查 Skill 是否存在
	skillItem, exists := h.skillRegistry.Get(name)
	if !exists {
		h.auditSkillMutation(r, skillMutationActionDelete, "not_found", logger.String("skill", name))
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrSkillNotFound,
			fmt.Sprintf("skill not found: %s", name)))
		return
	}
	if err := h.enforceDeleteFilePolicy(r); err != nil {
		h.auditSkillMutation(r, skillMutationActionDelete, "disabled", logger.String("skill", name), logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	fileDeleted := false
	if shouldDeleteSkillFile(r) {
		if err := h.deletePersistedSkillFile(skillItem); err != nil {
			h.auditSkillMutation(r, skillMutationActionDelete, "failed", logger.String("skill", name), logger.Err(err))
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		fileDeleted = true
	}

	h.removeEmbeddingIndex(skillItem)
	h.skillRegistry.Unregister(name)
	skillChange := skillChangePayloadFromSkill(skillItem, handlerSkillDirs(h.skillLoader))
	skillChange["action"] = skillMutationActionDelete
	skillChange["status"] = "success"
	skillChange["affected_count"] = 1
	skillChange["count"] = h.currentSkillCount()
	h.publishSkillsChangedEvent(r, skillChange)
	h.auditSkillMutation(r, skillMutationActionDelete, "success", logger.String("skill", name))

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":      "skill deleted successfully",
		"skill":        name,
		"file_deleted": fileDeleted,
	})
}

// ExecuteSkill 执行 Skill
func (h *Handler) ExecuteSkill(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	skillItem, exists := h.skillRegistry.Get(name)
	if !exists {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrSkillNotFound,
			fmt.Sprintf("skill not found: %s", name)))
		return
	}

	executeReq, err := h.decodeExecuteSkillRequest(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse execution parameters"))
		return
	}

	ctx := r.Context()
	usageScope := h.resolveUsageScope(r, executeReq.TenantID, executeReq.ProjectID, executeReq.UserID)
	session, err := h.getOrCreateSession(ctx, usageScope.UserID, executeReq.SessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	runtimeReq := types.NewRequest(executeReq.Prompt)
	runtimeReq.History = h.normalizeMessages(executeReq.History)
	runtimeReq.Context = h.mergeContext(executeReq.Context, executeReq.Params)
	runtimeReq.Options = make(map[string]interface{}, len(executeReq.Options))
	for key, value := range executeReq.Options {
		runtimeReq.Options[key] = value
	}
	runtimeReq.ReasoningEffort = types.ResolveReasoningEffort(executeReq.ReasoningEffort, executeReq.Options)
	runtimeReq.Thinking = types.ResolveThinkingConfig(executeReq.Thinking, executeReq.Options)
	if grantedPermissions := h.resolveGrantedSkillPermissions(r); len(grantedPermissions) > 0 {
		runtimeReq.Metadata.Set("permissions", grantedPermissions)
	}

	if session != nil && len(runtimeReq.History) == 0 {
		runtimeReq.History = append(runtimeReq.History, session.GetMessages()...)
	}
	estimatedPromptTokens := h.estimateRequestTokens(executeReq.Prompt, runtimeReq.History)
	if err := h.enforceUsageQuota(usageScope, estimatedPromptTokens, "execute"); err != nil {
		h.writeError(w, http.StatusTooManyRequests, err)
		return
	}

	executor := skill.NewExecutor(h.skillRegistry, h.mcpManager, h.llmRuntime)
	result, err := executor.Execute(ctx, skillItem, runtimeReq)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	if session != nil {
		_ = h.persistChatTurn(ctx, session, executeReq.Prompt, result.Output, nil)
	}
	h.recordUsage(usageScope, "execute", skillItem.Name, result.Success, estimatedPromptTokens, result.Usage, result.Output)

	response := map[string]interface{}{
		"skill":      skillItem.Name,
		"status":     executionStatus(result.Success),
		"result":     result,
		"session_id": sessionID(session),
	}

	h.writeJSON(w, http.StatusOK, response)
}

// BatchCreateSkills 批量创建 Skills
func (h *Handler) BatchCreateSkills(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSkillMutation(r); err != nil {
		h.auditSkillMutation(r, skillMutationActionBatchCreate, "forbidden", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforceMutationActionPolicy(skillMutationActionBatchCreate, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionBatchCreate, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforcePersistPolicy(nil, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionBatchCreate, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	var skills []struct {
		Content string                 `json:"content"` // YAML 内容
		Params  map[string]interface{} `json:"params,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&skills); err != nil {
		h.auditSkillMutation(r, skillMutationActionBatchCreate, "invalid_request")
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	results := make([]map[string]interface{}, 0, len(skills))
	var errorsList []error
	successCount := 0
	var firstChangedSkill *skill.Skill

	for i, skillData := range skills {
		// 解析 YAML
		skillItem, err := skill.NewManifestParser().ParseBytes([]byte(skillData.Content))
		if err != nil {
			errorsList = append(errorsList, err)
			results = append(results, map[string]interface{}{
				"index":   i,
				"success": false,
				"error":   err.Error(),
			})
			continue
		}
		skillItem.SetSource("", "", skill.SkillSourceLayerRuntime)

		// 注册 Skill
		if err := h.skillRegistry.Register(skillItem); err != nil {
			errorsList = append(errorsList, err)
			results = append(results, map[string]interface{}{
				"index":   i,
				"success": false,
				"error":   err.Error(),
			})
			continue
		}

		h.updateEmbeddingIndex(skillItem)

		if shouldPersistSkill(r) {
			if err := h.persistSkill(skillItem, nil, r); err != nil {
				errorsList = append(errorsList, err)
				results = append(results, map[string]interface{}{
					"index":   i,
					"success": false,
					"skill":   skillItem.Name,
					"error":   err.Error(),
				})
				continue
			}
		}
		if firstChangedSkill == nil {
			firstChangedSkill = skillItem
		}
		successCount++

		results = append(results, map[string]interface{}{
			"index":   i,
			"success": true,
			"skill":   skillItem.Name,
		})
	}

	response := map[string]interface{}{
		"results": results,
		"total":   len(skills),
		"success": len(skills) - len(errorsList),
		"failed":  len(errorsList),
	}

	statusCode := http.StatusOK
	if len(errorsList) > 0 {
		statusCode = http.StatusMultiStatus
	}
	outcome := "success"
	if len(errorsList) > 0 {
		outcome = "partial_success"
	}
	if successCount > 0 {
		skillChange := skillChangePayloadFromSkill(firstChangedSkill, handlerSkillDirs(h.skillLoader))
		skillChange["action"] = skillMutationActionBatchCreate
		skillChange["status"] = outcome
		skillChange["affected_count"] = successCount
		skillChange["failed_count"] = len(errorsList)
		skillChange["count"] = h.currentSkillCount()
		h.publishSkillsChangedEvent(r, skillChange)
	}
	h.auditSkillMutation(r, skillMutationActionBatchCreate, outcome,
		logger.Int("total", len(skills)),
		logger.Int("failed", len(errorsList)))

	h.writeJSON(w, statusCode, response)
}

// SearchSkills 搜索 Skills
func (h *Handler) SearchSkills(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	limitStr := r.URL.Query().Get("limit")
	category := r.URL.Query().Get("category")
	mode := parseSearchMode(r)
	layer, dir := parseSkillSourceFilters(r)

	if query == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"query parameter 'q' is required"))
		return
	}

	// 应用 limit
	limit := 20
	if limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err == nil && l > 0 {
			limit = l
		}
	}
	matches, resolvedMode := h.searchSkillMatches(r.Context(), query, category, mode)
	matches = filterRouteResultsBySource(matches, layer, dir)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	hydratedMatches, err := h.hydrateRouteResultsForResponse(matches)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	skills := extractSkillsFromMatches(hydratedMatches)

	response := map[string]interface{}{
		"query":          query,
		"results":        skills,
		"matches":        serializeSearchMatches(hydratedMatches),
		"count":          len(skills),
		"limit":          limit,
		"requested_mode": string(mode),
		"resolved_mode":  string(resolvedMode),
		"used_embedding": searchUsesEmbedding(hydratedMatches),
	}
	h.recordSearchTelemetry(query, mode, resolvedMode, len(hydratedMatches), response["used_embedding"].(bool))

	h.writeJSON(w, http.StatusOK, response)
}

// ListCapabilities 列出统一能力描述
func (h *Handler) ListCapabilities(w http.ResponseWriter, r *http.Request) {
	if h.skillRegistry == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"skill registry not configured"))
		return
	}

	descriptors := h.skillRegistry.CapabilityDescriptors()
	agentDescriptor := h.agentCapabilityDescriptor()
	if agentDescriptor != nil {
		descriptors = append([]*capability.Descriptor{agentDescriptor}, descriptors...)
	}

	response := map[string]interface{}{
		"capabilities": descriptors,
		"count":        len(descriptors),
	}
	h.writeJSON(w, http.StatusOK, response)
}

// GetRuntimeModels 列出前端可用的聊天 provider / model 目录。
func (h *Handler) GetRuntimeModels(w http.ResponseWriter, r *http.Request) {
	payload := runtimeModelsSnapshot(h.llmRuntime)
	if err := h.attachProfileMetadata(r, payload); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, payload)
}

// AgentChat Agent 对话接口
func (h *Handler) AgentChat(w http.ResponseWriter, r *http.Request) {
	httpTraceID, requestID := runtimeTraceIDForRequest(r)
	if requestID == "" {
		requestID = httpTraceID
	}
	if requestID != "" {
		w.Header().Set("X-Request-ID", requestID)
	}

	var req struct {
		Messages                   []map[string]string   `json:"messages"`
		Profile                    string                `json:"profile,omitempty"`
		Agent                      string                `json:"agent,omitempty"`
		Provider                   string                `json:"provider,omitempty"`
		Model                      string                `json:"model,omitempty"`
		ReasoningEffort            string                `json:"reasoning_effort,omitempty"`
		Thinking                   *types.ThinkingConfig `json:"thinking,omitempty"`
		SessionID                  string                `json:"session_id,omitempty"`
		TeamID                     string                `json:"team_id,omitempty"`
		TaskID                     string                `json:"task_id,omitempty"`
		UserID                     string                `json:"user_id,omitempty"`
		TenantID                   string                `json:"tenant_id,omitempty"`
		ProjectID                  string                `json:"project_id,omitempty"`
		WorkspacePath              string                `json:"workspace_path,omitempty"`
		MaxSteps                   int                   `json:"max_steps,omitempty"`
		EnableRoute                bool                  `json:"enable_routing,omitempty"`
		EnableReAct                bool                  `json:"enable_react,omitempty"`
		PlanningMode               string                `json:"planning_mode,omitempty"`
		ExecutePlannedSubagents    bool                  `json:"execute_planned_subagents,omitempty"`
		AllowWritePlannedSubagents bool                  `json:"allow_write_planned_subagents,omitempty"`
		PatchDecisionPolicy        string                `json:"patch_decision_policy,omitempty"`
		ApproveBlockedPatches      bool                  `json:"approve_blocked_patches,omitempty"`
		PatchApprovalNote          string                `json:"patch_approval_note,omitempty"`
		PatchApproval              *agent.PatchApproval  `json:"patch_approval,omitempty"`
		Stream                     bool                  `json:"stream,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	if len(req.Messages) == 0 {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"messages is required"))
		return
	}

	ctx := r.Context()
	if requestID != "" {
		ctx = logger.WithRequestID(ctx, requestID)
	}
	usageScope := h.resolveUsageScope(r, req.TenantID, req.ProjectID, req.UserID)
	effectiveProfile := strings.TrimSpace(req.Profile)
	if effectiveProfile == "" && isAutoProfileRef(h.profileDefaultRef) {
		effectiveProfile = h.profileDefaultRef
	}
	if isAutoProfileRef(effectiveProfile) {
		prompt := extractLastUserPrompt(req.Messages)
		effectiveProfile = routeProfileForPrompt(prompt)
	}
	workspacePath := strings.TrimSpace(req.WorkspacePath)
	profileState, profileCleanup, err := h.resolveProfileRuntimeState(ctx, effectiveProfile, req.Agent, usageScope, workspacePath)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	if profileCleanup != nil {
		defer profileCleanup()
	}
	session, err := h.getOrCreateSession(ctx, usageScope.UserID, req.SessionID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if session != nil {
		leaseScope := requestID
		if leaseScope == "" {
			leaseScope = uuid.NewString()
		}
		leaseHandle, leaseErr := h.acquireSessionLease(ctx, session.ID, agentChatLeaseOwnerKind, leaseScope)
		if leaseErr != nil {
			if h.writeSessionLeaseConflict(w, leaseErr) {
				return
			}
			h.writeError(w, http.StatusInternalServerError, leaseErr)
			return
		}
		if leaseHandle != nil {
			defer func() {
				_ = leaseHandle.Release(context.Background())
			}()
		}
	}

	selectedConfig := h.resolveRuntimeConfig(usageScope)
	if profileState != nil && profileState.RuntimeConfig != nil {
		selectedConfig = profileState.RuntimeConfig
	}
	requestedProvider := strings.TrimSpace(req.Provider)
	instructionProvider := requestedProvider
	if instructionProvider == "" {
		instructionProvider = resolveAgentProvider(profileState, selectedConfig, h.llmRuntime)
	}
	instructionMessages := buildRuntimeInstructionMessages(profileState, workspacePath, instructionProvider)

	if session != nil {
		sessionUpdated := false
		if ensureSessionInstructionMessages(session, instructionMessages) {
			sessionUpdated = true
		}
		if h.applyProfileSessionContext(session, profileState) {
			sessionUpdated = true
		}
		if sessionUpdated && h.sessionManager != nil {
			_ = h.sessionManager.Update(ctx, session)
		}
	}

	chatMessages, lastMessage := h.buildChatMessages(req.Messages, session)
	chatMessages = injectInstructionMessages(chatMessages, instructionMessages)
	if lastMessage == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"at least one user message is required"))
		return
	}
	workspaceCtx, workspaceErr := h.buildWorkspaceContext(workspacePath, lastMessage, selectedConfig)
	if workspaceErr != nil {
		h.writeError(w, http.StatusBadRequest, workspaceErr)
		return
	}
	requestTraceID := ""
	if !req.EnableReAct {
		requestTraceID = "trace_" + uuid.NewString()
	}
	contextPack := h.buildContextPack(ctx, session, buildProfileContextPack(profileState), workspaceCtx, chatMessages, lastMessage, workspacePath, req.TeamID, req.TaskID, requestTraceID, selectedConfig)
	agentContext := map[string]interface{}{
		"workspace_path":   workspacePath,
		"context_pack":     contextPack,
		"session_id":       sessionID(session),
		"reasoning_effort": types.ResolveReasoningEffort(req.ReasoningEffort),
	}
	for key, value := range runtimeprompt.CurrentEnvironmentValues() {
		agentContext[key] = value
	}
	if grantedPermissions := h.resolveGrantedSkillPermissions(r); len(grantedPermissions) > 0 {
		agentContext["permissions"] = grantedPermissions
	}
	if profileState != nil && len(profileState.ContextValues) > 0 {
		mergeProfileContextInto(agentContext, profileState.ContextValues)
	}
	estimatedPromptTokens := h.estimateMessagesTokens(chatMessages)
	if err := h.enforceUsageQuota(usageScope, estimatedPromptTokens, "agent_chat"); err != nil {
		h.writeError(w, http.StatusTooManyRequests, err)
		return
	}

	runtimeRegistry := h.skillRegistry
	runtimeEmbedding := h.embeddingRouter
	runtimeMCP := h.mcpManager
	runtimeLLM := h.llmRuntime
	if profileState != nil {
		if profileState.Registry != nil {
			runtimeRegistry = profileState.Registry
		}
		if profileState.Embedding != nil {
			runtimeEmbedding = profileState.Embedding
		}
		if profileState.MCPAdapter != nil {
			runtimeMCP = profileState.MCPAdapter
		}
	}

	// 创建 Agent 配置
	agentProvider := resolveAgentProvider(profileState, selectedConfig, runtimeLLM)
	agentModel := resolveAgentModel(profileState, selectedConfig, runtimeLLM)
	if requestedProvider != "" {
		agentProvider = requestedProvider
	}
	if model := strings.TrimSpace(req.Model); model != "" {
		agentModel = model
	}
	if agentProvider != "" {
		ctx = logger.WithProvider(ctx, agentProvider)
	}
	if agentModel != "" {
		ctx = logger.WithModel(ctx, agentModel)
	}
	ctx = llm.WithHTTPDebugReporter(ctx, runtimeHTTPDebugReporter(ctx))
	agentConfig := &agent.Config{
		Name:     "api-agent",
		Provider: agentProvider,
		Model:    agentModel,
		MaxSteps: req.MaxSteps,
	}
	if systemPrompt := primarySystemInstructionContent(instructionMessages); systemPrompt != "" {
		agentConfig.SystemPrompt = systemPrompt
	} else if profileState != nil && strings.TrimSpace(profileState.PromptText) != "" {
		agentConfig.SystemPrompt = strings.TrimSpace(profileState.PromptText)
	}
	if selectedConfig != nil {
		agentConfig.Options = contextOptionsFromRuntimeConfig(selectedConfig)
	}
	if strings.TrimSpace(req.TeamID) != "" || strings.TrimSpace(req.TaskID) != "" {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		if value := strings.TrimSpace(req.TeamID); value != "" {
			agentConfig.Options["team_id"] = value
		}
		if value := strings.TrimSpace(req.TaskID); value != "" {
			agentConfig.Options["task_id"] = value
		}
	}
	if workspacePath != "" {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		agentConfig.Options["workspace_path"] = workspacePath
	}
	if profilePack := buildProfileContextPack(profileState); len(profilePack) > 0 {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		agentConfig.Options["profile_context"] = cloneProfileContextValues(profilePack)
	}
	if agentConfig.MaxSteps < 0 {
		agentConfig.MaxSteps = 0
	} else if agentConfig.MaxSteps == 0 && selectedConfig != nil {
		agentConfig.MaxSteps = agent.NormalizeMaxSteps(selectedConfig.Agent.MaxMaxSteps)
	}

	a := h.newAPIAgentWithRuntime(agentConfig, &agentRuntimeComponents{
		registry:        runtimeRegistry,
		embeddingRouter: runtimeEmbedding,
		mcpManager:      runtimeMCP,
		llmRuntime:      runtimeLLM,
	})
	defer func() {
		_ = a.Close()
	}()
	h.applyAgentExecutionPolicy(a, workspacePath, selectedConfig, profileStateToolPolicy(profileState))
	h.applyAgentHooks(a, selectedConfig)
	h.applyAgentRuntimeServices(a, selectedConfig)
	usesSessionHistory := session != nil && (len(req.Messages) == 0 || (len(req.Messages) == 1 && strings.EqualFold(strings.TrimSpace(req.Messages[0]["role"]), "user")))
	if usesSessionHistory {
		h.maybeAutoCompactSessionHistory(ctx, session, a, agentProvider, agentModel, requestTraceID, req.TaskID)
		chatMessages, lastMessage = h.buildChatMessages(req.Messages, session)
		chatMessages = injectInstructionMessages(chatMessages, instructionMessages)
	}
	historyForAgent := trimLatestUserMessage(chatMessages)
	streamingRequested := req.Stream || wantsEventStream(r)
	effectivePlanningMode := h.resolvePlanningMode(req.PlanningMode, selectedConfig)
	plannerPreferred := strings.EqualFold(strings.TrimSpace(effectivePlanningMode), string(agent.OrchestrationPlannerPreferred))
	routeAttempted := req.EnableRoute || plannerPreferred || strings.EqualFold(strings.TrimSpace(effectivePlanningMode), string(agent.OrchestrationRoutePreferred))
	var routeCandidates []*skill.RouteResult
	if routeAttempted {
		routeCandidates = routeCandidatesWithRuntime(ctx, lastMessage, runtimeRegistry, runtimeEmbedding)
	}
	if streamingRequested {
		if req.EnableReAct && h.llmRuntime != nil {
			execSession := session
			if execSession == nil {
				execSession = chat.NewSession(usageScope.UserID)
			}
			execSession = execSession.Clone()
			execSession.ReplaceHistory(prependContextMessages(historyForAgent, buildAgentContextMessages(agentContext, workspaceCtx)))
			execSession.AddMessage(*types.NewUserMessage(lastMessage))

			reactResult, reactErr := a.RunReActWithSession(ctx, h.llmRuntime, lastMessage, execSession, &agent.LoopReActConfig{
				MaxSteps:            agentConfig.MaxSteps,
				EnableThought:       true,
				EnableToolCalls:     true,
				EnableParallelTools: selectedConfig != nil && selectedConfig.Agent.EnableParallelTools,
				MaxParallelToolCalls: func() int {
					if selectedConfig != nil && selectedConfig.Agent.MaxParallelToolCalls > 0 {
						return selectedConfig.Agent.MaxParallelToolCalls
					}
					return 1
				}(),
				ReasoningEffort: types.ResolveReasoningEffort(req.ReasoningEffort),
				Thinking:        types.ResolveThinkingConfig(req.Thinking),
				Temperature:     0.7,
			})
			if reactErr != nil {
				h.writeAgentChatExecutionError(ctx, w, http.StatusInternalServerError, reactErr, session, requestTraceID)
				return
			}

			if session != nil {
				session.ReplaceHistory(execSession.GetMessages())
				_ = h.sessionManager.Update(ctx, session)
			}

			resultPayload := buildAgentResultPayload("agent_react", reactResult)
			resultPayload["orchestration"] = buildOrchestrationPayload(
				"agent_react",
				routeAttempted,
				routeCandidates,
				reactResult,
				nil,
				"",
			)
			h.recordUsage(usageScope, "agent_chat", reactResult.Skill, reactResult.Success, estimatedPromptTokens, reactResult.Usage, reactResult.Output)

			h.streamStaticResult(w, session, a.GetConfig().Name, resultPayload)
			return
		}

		if req.ExecutePlannedSubagents {
			orchestrationMode := agent.OrchestrationAgentOnly
			if strings.EqualFold(strings.TrimSpace(effectivePlanningMode), string(agent.OrchestrationPlannerPreferred)) {
				orchestrationMode = agent.OrchestrationPlannerPreferred
			} else if req.EnableRoute || strings.EqualFold(strings.TrimSpace(effectivePlanningMode), string(agent.OrchestrationRoutePreferred)) {
				orchestrationMode = agent.OrchestrationRoutePreferred
			} else if h.llmRuntime != nil {
				orchestrationMode = agent.OrchestrationLLMOnly
			}

			orchResult, orchErr := a.Orchestrate(ctx, &agent.OrchestrationRequest{
				Prompt:                     lastMessage,
				History:                    historyForAgent,
				Mode:                       orchestrationMode,
				Provider:                   agentProvider,
				Model:                      agentModel,
				MaxTokens:                  4096,
				Temperature:                0.7,
				Context:                    agentContext,
				Workspace:                  workspaceCtx,
				ExecutePlannedSubagents:    req.ExecutePlannedSubagents,
				AllowWritePlannedSubagents: req.AllowWritePlannedSubagents,
				PatchDecisionPolicy:        req.PatchDecisionPolicy,
				ApproveBlockedPatches:      req.ApproveBlockedPatches,
				PatchApprovalNote:          req.PatchApprovalNote,
				PatchApproval:              req.PatchApproval,
			})
			if orchErr != nil {
				h.writeAgentChatExecutionError(ctx, w, http.StatusInternalServerError, orchErr, session, requestTraceID)
				return
			}

			if orchResult.RouteAttempted && len(orchResult.RouteCandidates) > 0 {
				routeCandidates = orchResult.RouteCandidates
			}

			publicSource := orchResult.Source
			if publicSource == "llm_direct" {
				publicSource = "llm_fallback"
			}

			var resultPayload map[string]interface{}
			switch orchResult.Source {
			case "agent_route", "agent_direct", "agent_planned_subagents":
				resultPayload = buildAgentResultPayload(publicSource, orchResult.AgentResult)
				resultPayload["orchestration"] = buildOrchestrationPayload(
					publicSource,
					orchResult.RouteAttempted,
					routeCandidates,
					orchResult.AgentResult,
					nil,
					orchResult.FallbackReason,
				)
				if orchResult.AgentResult != nil {
					h.recordUsage(usageScope, "agent_chat", orchResult.AgentResult.Skill, orchResult.AgentResult.Success, estimatedPromptTokens, orchResult.AgentResult.Usage, orchResult.AgentResult.Output)
				}
			default:
				resultPayload = buildLLMResultPayload(publicSource, orchResult.LLMResponse)
				resultPayload["orchestration"] = buildOrchestrationPayload(
					publicSource,
					orchResult.RouteAttempted,
					routeCandidates,
					nil,
					orchResult.LLMResponse,
					orchResult.FallbackReason,
				)
				if orchResult.LLMResponse != nil {
					h.recordUsage(usageScope, "agent_chat", "", true, estimatedPromptTokens, orchResult.LLMResponse.Usage, orchResult.LLMResponse.Content)
				}
			}

			if planningPayload := buildPlanningPayload(orchResult); planningPayload != nil {
				resultPayload["planning"] = planningPayload
				if orchestration, ok := resultPayload["orchestration"].(map[string]interface{}); ok {
					orchestration["planning_attempted"] = orchResult.PlanningAttempted
					orchestration["planning_source"] = orchResult.PlanningSource
					orchestration["plan_step_count"] = planningPayload["step_count"]
					orchestration["subagent_task_count"] = planningPayload["subagent_task_count"]
					orchestration["subagent_execution_requested"] = planningPayload["subagent_execution_requested"]
					orchestration["subagent_execution_eligible"] = planningPayload["subagent_execution_eligible"]
					orchestration["subagent_execution_blocked_reason"] = planningPayload["subagent_execution_blocked_reason"]
					orchestration["subagent_execution_attempted"] = planningPayload["subagent_execution_attempted"]
					orchestration["patch_decision"] = planningPayload["patch_decision"]
					orchestration["patch_decision_reason"] = planningPayload["patch_decision_reason"]
					orchestration["patch_decision_required"] = planningPayload["patch_decision_required"]
					if orchResult.PlanningError != "" {
						orchestration["planning_error"] = orchResult.PlanningError
					}
				}
			}

			switch orchResult.Source {
			case "agent_route", "agent_direct", "agent_planned_subagents":
				if session != nil && orchResult.AgentResult != nil {
					_ = h.persistChatTurn(
						ctx,
						session,
						lastMessage,
						orchResult.AgentResult.Output,
						buildWorkspaceEvidenceMetadata(resultPayload),
					)
				}
			default:
				if session != nil && orchResult.LLMResponse != nil {
					_ = h.persistChatTurn(
						ctx,
						session,
						lastMessage,
						orchResult.LLMResponse.Content,
						buildWorkspaceEvidenceMetadata(resultPayload),
					)
				}
			}

			h.streamStaticResult(w, session, a.GetConfig().Name, resultPayload)
			return
		}

		var planningPayload map[string]interface{}
		if plannerPreferred {
			plan, planningSource, planningError := a.PreviewPlan(ctx, &agent.OrchestrationRequest{
				Prompt:                     lastMessage,
				History:                    historyForAgent,
				Mode:                       agent.OrchestrationPlannerPreferred,
				Provider:                   agentProvider,
				Model:                      agentModel,
				MaxTokens:                  4096,
				Temperature:                0.7,
				Context:                    agentContext,
				Workspace:                  workspaceCtx,
				ExecutePlannedSubagents:    req.ExecutePlannedSubagents,
				AllowWritePlannedSubagents: req.AllowWritePlannedSubagents,
				PatchDecisionPolicy:        req.PatchDecisionPolicy,
				ApproveBlockedPatches:      req.ApproveBlockedPatches,
				PatchApprovalNote:          req.PatchApprovalNote,
				PatchApproval:              req.PatchApproval,
			}, routeCandidates)
			planningPayload = buildPlanningPayload(&agent.OrchestrationResult{
				Mode:              agent.OrchestrationPlannerPreferred,
				Plan:              plan,
				SubagentTasks:     agent.BuildSubagentTasksFromPlan(plan),
				PlanningAttempted: true,
				PlanningSource:    planningSource,
				PlanningError:     planningError,
			})
		}

		if req.EnableRoute || plannerPreferred || strings.EqualFold(strings.TrimSpace(effectivePlanningMode), string(agent.OrchestrationRoutePreferred)) {
			result, routeErr := a.RunWithHistoryAndContext(ctx, lastMessage, historyForAgent, agentContext)
			if routeErr != nil {
				h.writeAgentChatExecutionError(ctx, w, http.StatusInternalServerError, routeErr, session, requestTraceID)
				return
			}
			if shouldUseAgentResult(result, h.llmRuntime) {
				h.recordUsage(usageScope, "agent_chat", result.Skill, result.Success, estimatedPromptTokens, result.Usage, result.Output)
				payload := buildAgentResultPayload("agent_route", result)
				payload["orchestration"] = buildOrchestrationPayload("agent_route", routeAttempted, routeCandidates, result, nil, "")
				if planningPayload != nil {
					payload["planning"] = planningPayload
					if orchestration, ok := payload["orchestration"].(map[string]interface{}); ok {
						orchestration["planning_attempted"] = planningPayload["attempted"]
						orchestration["planning_source"] = planningPayload["planning_source"]
						orchestration["plan_step_count"] = planningPayload["step_count"]
						orchestration["subagent_task_count"] = planningPayload["subagent_task_count"]
						orchestration["subagent_execution_requested"] = planningPayload["subagent_execution_requested"]
						orchestration["subagent_execution_eligible"] = planningPayload["subagent_execution_eligible"]
						orchestration["subagent_execution_blocked_reason"] = planningPayload["subagent_execution_blocked_reason"]
						orchestration["subagent_execution_attempted"] = planningPayload["subagent_execution_attempted"]
						orchestration["patch_decision"] = planningPayload["patch_decision"]
						orchestration["patch_decision_reason"] = planningPayload["patch_decision_reason"]
						orchestration["patch_decision_required"] = planningPayload["patch_decision_required"]
						if planningError, ok := planningPayload["planning_error"].(string); ok && planningError != "" {
							orchestration["planning_error"] = planningError
						}
					}
				}
				if session != nil {
					_ = h.persistChatTurn(
						ctx,
						session,
						lastMessage,
						result.Output,
						buildWorkspaceEvidenceMetadata(payload),
					)
				}
				h.streamStaticResult(w, session, a.GetConfig().Name, payload)
				return
			}
		}

		if h.llmRuntime == nil {
			h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
				"LLM runtime not configured for streaming"))
			return
		}

		fallbackReason := "llm_runtime_required"
		if routeAttempted {
			fallbackReason = "no_matching_skill"
		}
		if err := h.streamLLMChat(ctx, w, session, a.GetConfig().Name, agentModel, lastMessage, chatMessages, types.ResolveReasoningEffort(req.ReasoningEffort), types.ResolveThinkingConfig(req.Thinking), routeAttempted, routeCandidates, fallbackReason, usageScope, estimatedPromptTokens, requestTraceID, planningPayload); err != nil {
			return
		}
		return
	}

	if req.EnableReAct && h.llmRuntime != nil {
		execResult, reactErr := runtimechatcore.ExecuteNonStream(ctx, runtimechatcore.ExecuteRequest{
			Agent:           a,
			LLMRuntime:      h.llmRuntime,
			Session:         session,
			SessionUserID:   usageScope.UserID,
			Prompt:          lastMessage,
			PreparedHistory: prependContextMessages(historyForAgent, buildAgentContextMessages(agentContext, workspaceCtx)),
			EnableReAct:     true,
			ReActConfig: &agent.LoopReActConfig{
				MaxSteps:            agentConfig.MaxSteps,
				EnableThought:       true,
				EnableToolCalls:     true,
				EnableParallelTools: selectedConfig != nil && selectedConfig.Agent.EnableParallelTools,
				MaxParallelToolCalls: func() int {
					if selectedConfig != nil && selectedConfig.Agent.MaxParallelToolCalls > 0 {
						return selectedConfig.Agent.MaxParallelToolCalls
					}
					return 1
				}(),
				ReasoningEffort: types.ResolveReasoningEffort(req.ReasoningEffort),
				Thinking:        types.ResolveThinkingConfig(req.Thinking),
				Temperature:     0.7,
			},
		})
		if reactErr != nil {
			h.writeAgentChatExecutionError(ctx, w, http.StatusInternalServerError, reactErr, session, requestTraceID)
			return
		}
		reactResult := execResult.ReactResult

		if session != nil && execResult.UpdatedSession != nil {
			session.ReplaceHistory(execResult.UpdatedSession.GetMessages())
			_ = h.sessionManager.Update(ctx, session)
		}

		responseResult := buildAgentResultPayload("agent_react", reactResult)
		responseResult["orchestration"] = buildOrchestrationPayload(
			"agent_react",
			routeAttempted,
			routeCandidates,
			reactResult,
			nil,
			"",
		)
		h.recordUsage(usageScope, "agent_chat", reactResult.Skill, reactResult.Success, estimatedPromptTokens, reactResult.Usage, reactResult.Output)

		response := map[string]interface{}{
			"session_id": sessionID(session),
			"agent_id":   a.GetConfig().Name,
			"result":     responseResult,
			"source":     responseResultSource(responseResult),
			"status":     finalResultStatus(responseResult),
		}
		h.writeJSON(w, http.StatusOK, response)
		return
	}

	orchestrationMode := agent.OrchestrationAgentOnly
	if strings.EqualFold(strings.TrimSpace(effectivePlanningMode), string(agent.OrchestrationPlannerPreferred)) {
		orchestrationMode = agent.OrchestrationPlannerPreferred
	} else if req.EnableRoute || strings.EqualFold(strings.TrimSpace(effectivePlanningMode), string(agent.OrchestrationRoutePreferred)) {
		orchestrationMode = agent.OrchestrationRoutePreferred
	} else if h.llmRuntime != nil {
		orchestrationMode = agent.OrchestrationLLMOnly
	}

	execResult, orchErr := runtimechatcore.ExecuteNonStream(ctx, runtimechatcore.ExecuteRequest{
		Agent:  a,
		Prompt: lastMessage,
		OrchestrationRequest: &agent.OrchestrationRequest{
			Prompt:                     lastMessage,
			History:                    historyForAgent,
			Mode:                       orchestrationMode,
			Provider:                   agentProvider,
			Model:                      agentModel,
			ReasoningEffort:            types.ResolveReasoningEffort(req.ReasoningEffort),
			Thinking:                   types.ResolveThinkingConfig(req.Thinking),
			MaxTokens:                  4096,
			Temperature:                0.7,
			Context:                    agentContext,
			Workspace:                  workspaceCtx,
			ExecutePlannedSubagents:    req.ExecutePlannedSubagents,
			AllowWritePlannedSubagents: req.AllowWritePlannedSubagents,
			PatchDecisionPolicy:        req.PatchDecisionPolicy,
			ApproveBlockedPatches:      req.ApproveBlockedPatches,
			PatchApprovalNote:          req.PatchApprovalNote,
			PatchApproval:              req.PatchApproval,
		},
	})
	if orchErr != nil {
		h.writeAgentChatExecutionError(ctx, w, http.StatusInternalServerError, orchErr, session, requestTraceID)
		return
	}
	orchResult := execResult.OrchestrationResult

	if orchResult.RouteAttempted && len(orchResult.RouteCandidates) > 0 {
		routeCandidates = orchResult.RouteCandidates
	}

	var responseResult interface{}
	publicSource := orchResult.Source
	if publicSource == "llm_direct" {
		publicSource = "llm_fallback"
	}
	switch orchResult.Source {
	case "agent_route", "agent_direct", "agent_planned_subagents":
		responseResult = buildAgentResultPayload(publicSource, orchResult.AgentResult)
		responseResult.(map[string]interface{})["orchestration"] = buildOrchestrationPayload(
			publicSource,
			orchResult.RouteAttempted,
			routeCandidates,
			orchResult.AgentResult,
			nil,
			orchResult.FallbackReason,
		)
		if orchResult.AgentResult != nil {
			h.recordUsage(usageScope, "agent_chat", orchResult.AgentResult.Skill, orchResult.AgentResult.Success, estimatedPromptTokens, orchResult.AgentResult.Usage, orchResult.AgentResult.Output)
		}
	default:
		responseResult = buildLLMResultPayload(publicSource, orchResult.LLMResponse)
		responseResult.(map[string]interface{})["orchestration"] = buildOrchestrationPayload(
			publicSource,
			orchResult.RouteAttempted,
			routeCandidates,
			nil,
			orchResult.LLMResponse,
			orchResult.FallbackReason,
		)
		if orchResult.LLMResponse != nil {
			h.recordUsage(usageScope, "agent_chat", "", true, estimatedPromptTokens, orchResult.LLMResponse.Usage, orchResult.LLMResponse.Content)
		}
	}
	if planningPayload := buildPlanningPayload(orchResult); planningPayload != nil {
		responseResult.(map[string]interface{})["planning"] = planningPayload
		if orchestration, ok := responseResult.(map[string]interface{})["orchestration"].(map[string]interface{}); ok {
			orchestration["planning_attempted"] = orchResult.PlanningAttempted
			orchestration["planning_source"] = orchResult.PlanningSource
			orchestration["plan_step_count"] = planningPayload["step_count"]
			orchestration["subagent_task_count"] = planningPayload["subagent_task_count"]
			orchestration["subagent_execution_requested"] = planningPayload["subagent_execution_requested"]
			orchestration["subagent_execution_eligible"] = planningPayload["subagent_execution_eligible"]
			orchestration["subagent_execution_blocked_reason"] = planningPayload["subagent_execution_blocked_reason"]
			orchestration["subagent_execution_attempted"] = planningPayload["subagent_execution_attempted"]
			orchestration["patch_decision"] = planningPayload["patch_decision"]
			orchestration["patch_decision_reason"] = planningPayload["patch_decision_reason"]
			orchestration["patch_decision_required"] = planningPayload["patch_decision_required"]
			if orchResult.PlanningError != "" {
				orchestration["planning_error"] = orchResult.PlanningError
			}
		}
	}
	switch orchResult.Source {
	case "agent_route", "agent_direct", "agent_planned_subagents":
		if session != nil && orchResult.AgentResult != nil {
			_ = h.persistChatTurn(
				ctx,
				session,
				lastMessage,
				orchResult.AgentResult.Output,
				buildWorkspaceEvidenceMetadata(responseResult.(map[string]interface{})),
			)
		}
	default:
		if session != nil && orchResult.LLMResponse != nil {
			_ = h.persistChatTurn(
				ctx,
				session,
				lastMessage,
				orchResult.LLMResponse.Content,
				buildWorkspaceEvidenceMetadata(responseResult.(map[string]interface{})),
			)
		}
	}

	response := map[string]interface{}{
		"session_id": sessionID(session),
		"agent_id":   a.GetConfig().Name,
		"result":     responseResult,
		"source":     responseResultSource(responseResult),
		"status":     finalResultStatus(responseResult),
	}
	responseTraceID := requestTraceID
	if resultMap, ok := responseResult.(map[string]interface{}); ok {
		if traceValue, ok := resultMap["trace_id"].(string); ok && strings.TrimSpace(traceValue) != "" {
			responseTraceID = strings.TrimSpace(traceValue)
		}
	}
	if responseTraceID != "" {
		response["trace_id"] = responseTraceID
	}

	h.writeJSON(w, http.StatusOK, response)
}

// CreateSession 创建会话
func (h *Handler) CreateSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	var req struct {
		UserID string `json:"user_id,omitempty"`
		Title  string `json:"title,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	if req.UserID == "" {
		req.UserID = r.URL.Query().Get("user_id")
	}
	req.UserID = h.resolveServerSessionUserID(req.UserID)

	session, err := h.sessionManager.CreateSession(r.Context(), req.UserID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if req.Title != "" {
		session.UpdateTitle(req.Title)
		_ = h.sessionManager.Update(r.Context(), session)
	}

	h.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"session": session,
	})
}

// ListSessions 列出会话
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	userID := h.resolveServerSessionUserID(r.URL.Query().Get("user_id"))

	sessions, err := h.sessionManager.List(r.Context(), userID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": sessions,
		"count":    len(sessions),
		"user_id":  userID,
	})
}

// GetSession 获取会话详情
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	session, err := h.sessionManager.GetSession(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		h.writeError(w, http.StatusNotFound, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": session,
	})
}

// DeleteSession 删除会话
func (h *Handler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	if err := h.sessionManager.Delete(r.Context(), mux.Vars(r)["id"]); err != nil {
		h.writeError(w, http.StatusNotFound, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": true,
		"id":      mux.Vars(r)["id"],
	})
}

// GetSessionHistory 获取会话历史
func (h *Handler) GetSessionHistory(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	session, err := h.sessionManager.GetSession(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		h.writeError(w, http.StatusNotFound, err)
		return
	}

	history := session.GetMessages()
	if history == nil {
		history = []types.Message{}
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": session.ID,
		"history":    history,
		"count":      len(history),
	})
}

// GetSessionStats 获取会话统计
func (h *Handler) GetSessionStats(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	userID := h.resolveServerSessionUserID(r.URL.Query().Get("user_id"))

	stats, err := h.sessionManager.GetStatistics(r.Context(), userID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"user_id": userID,
		"stats":   stats,
	})
}

// SearchSessions 搜索会话
func (h *Handler) SearchSessions(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	var req struct {
		UserID string   `json:"user_id,omitempty"`
		Tags   []string `json:"tags,omitempty"`
		State  string   `json:"state,omitempty"`
		Limit  int      `json:"limit,omitempty"`
		Offset int      `json:"offset,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	if req.UserID == "" {
		req.UserID = r.URL.Query().Get("user_id")
	}
	if req.State == "" {
		req.State = r.URL.Query().Get("state")
	}
	if req.Limit == 0 {
		if limit, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
			req.Limit = limit
		}
	}
	if req.Offset == 0 {
		if offset, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil {
			req.Offset = offset
		}
	}

	searchOpts := &chat.SessionSearchOptions{
		UserID: req.UserID,
		Tags:   append([]string(nil), req.Tags...),
		Limit:  req.Limit,
		Offset: req.Offset,
	}
	if req.State != "" {
		searchOpts.State = chat.SessionState(req.State)
	}

	sessions, err := h.sessionManager.SearchSessions(r.Context(), searchOpts)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"sessions": sessions,
		"count":    len(sessions),
		"filters":  searchOpts,
	})
}

// UpdateSession 更新会话元数据与状态
func (h *Handler) UpdateSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	var req struct {
		Title      *string                `json:"title,omitempty"`
		State      *string                `json:"state,omitempty"`
		TagsAdd    []string               `json:"tags_add,omitempty"`
		TagsRemove []string               `json:"tags_remove,omitempty"`
		Context    map[string]interface{} `json:"context,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	sessionID := mux.Vars(r)["id"]
	session, err := h.sessionManager.GetSession(r.Context(), sessionID)
	if err != nil {
		h.writeError(w, http.StatusNotFound, err)
		return
	}

	if req.Title != nil {
		session.UpdateTitle(*req.Title)
	}
	for _, tag := range req.TagsAdd {
		session.AddTag(tag)
	}
	for _, tag := range req.TagsRemove {
		session.RemoveTag(tag)
	}
	for key, value := range req.Context {
		session.SetContext(key, value)
	}
	if req.State != nil {
		switch chat.SessionState(*req.State) {
		case chat.StateActive, chat.StateIdle, chat.StateClosed, chat.StateArchived:
			session.UpdateState(chat.SessionState(*req.State))
		default:
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
				"invalid session state"))
			return
		}
	}

	if err := h.sessionManager.Update(r.Context(), session); err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": session,
	})
}

// ArchiveSession 归档会话
func (h *Handler) ArchiveSession(w http.ResponseWriter, r *http.Request) {
	h.changeSessionState(w, r, func(ctx context.Context, sessionID string) error {
		return h.sessionManager.ArchiveSession(ctx, sessionID)
	}, "archived")
}

// ActivateSession 激活会话
func (h *Handler) ActivateSession(w http.ResponseWriter, r *http.Request) {
	h.changeSessionState(w, r, func(ctx context.Context, sessionID string) error {
		return h.sessionManager.Activate(ctx, sessionID)
	}, "active")
}

// CloseSession 关闭会话
func (h *Handler) CloseSession(w http.ResponseWriter, r *http.Request) {
	h.changeSessionState(w, r, func(ctx context.Context, sessionID string) error {
		return h.sessionManager.Close(ctx, sessionID)
	}, "closed")
}

// ClearSessionHistory 清空会话历史
func (h *Handler) ClearSessionHistory(w http.ResponseWriter, r *http.Request) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	sessionID := mux.Vars(r)["id"]
	if err := h.sessionManager.ClearHistory(r.Context(), sessionID); err != nil {
		h.writeError(w, http.StatusNotFound, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session_id": sessionID,
		"cleared":    true,
	})
}

// BatchDeleteSessions 批量删除会话
func (h *Handler) BatchDeleteSessions(w http.ResponseWriter, r *http.Request) {
	h.batchSessionAction(w, r, func(ctx context.Context, sessionID string) error {
		return h.sessionManager.Delete(ctx, sessionID)
	}, "deleted")
}

// BatchArchiveSessions 批量归档会话
func (h *Handler) BatchArchiveSessions(w http.ResponseWriter, r *http.Request) {
	h.batchSessionAction(w, r, func(ctx context.Context, sessionID string) error {
		return h.sessionManager.ArchiveSession(ctx, sessionID)
	}, "archived")
}

func (h *Handler) changeSessionState(w http.ResponseWriter, r *http.Request, action func(context.Context, string) error, state string) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	sessionID := mux.Vars(r)["id"]
	if err := action(r.Context(), sessionID); err != nil {
		h.writeError(w, http.StatusNotFound, err)
		return
	}

	session, err := h.sessionManager.GetSession(r.Context(), sessionID)
	if err != nil {
		h.writeError(w, http.StatusNotFound, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"session": session,
		"state":   state,
	})
}

func (h *Handler) batchSessionAction(w http.ResponseWriter, r *http.Request, action func(context.Context, string) error, actionName string) {
	if h.sessionManager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"session manager not configured"))
		return
	}

	var req struct {
		SessionIDs []string `json:"session_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}
	if len(req.SessionIDs) == 0 {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"session_ids is required"))
		return
	}

	processed := make([]string, 0, len(req.SessionIDs))
	failures := make(map[string]string)
	for _, sessionID := range req.SessionIDs {
		if err := action(r.Context(), sessionID); err != nil {
			failures[sessionID] = err.Error()
			continue
		}
		processed = append(processed, sessionID)
	}

	statusCode := http.StatusOK
	if len(failures) > 0 && len(processed) == 0 {
		statusCode = http.StatusNotFound
	} else if len(failures) > 0 {
		statusCode = http.StatusMultiStatus
	}

	h.writeJSON(w, statusCode, map[string]interface{}{
		"action":    actionName,
		"processed": processed,
		"count":     len(processed),
		"failures":  failures,
	})
}

// StartHotReload 启动热加载
func (h *Handler) StartHotReload(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSkillMutation(r); err != nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadStart, "forbidden", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforceMutationActionPolicy(skillMutationActionHotReloadStart, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadStart, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	hotReload, err := h.ensureHotReload()
	if err != nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadStart, "failed", logger.Err(err))
		h.writeError(w, http.StatusServiceUnavailable, err)
		return
	}

	var req struct {
		Dir        string   `json:"dir,omitempty"`
		Dirs       []string `json:"dirs,omitempty"`
		DebounceMS int      `json:"debounce_ms,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.auditSkillMutation(r, skillMutationActionHotReloadStart, "invalid_request")
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	skillDirs := h.resolveRequestedSkillDirs(req.Dir, req.Dirs, r)
	if len(skillDirs) == 0 {
		h.auditSkillMutation(r, skillMutationActionHotReloadStart, "invalid_request")
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"skill directory is required"))
		return
	}

	if req.DebounceMS > 0 {
		hotReload.SetDebounceTime(time.Duration(req.DebounceMS) * time.Millisecond)
	}

	if err := hotReload.StartMany(skillDirs); err != nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadStart, "failed", logger.Err(err))
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := hotReload.Reload(); err != nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadStart, "failed", logger.Err(err))
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.rebuildEmbeddingIndex()
	h.publishSkillsChangedEvent(r, map[string]interface{}{
		"action":         skillMutationActionHotReloadStart,
		"status":         "success",
		"affected_count": len(skillDirs),
		"count":          h.currentSkillCount(),
		"watching":       true,
		"skill_dirs":     append([]string{}, skillDirs...),
	})
	h.auditSkillMutation(r, skillMutationActionHotReloadStart, "success", logger.Int("dir_count", len(skillDirs)))

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"started": true,
		"dir":     skillDirs[0],
		"dirs":    skillDirs,
		"stats":   hotReload.GetStats(),
	})
}

// StopHotReload 停止热加载
func (h *Handler) StopHotReload(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSkillMutation(r); err != nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadStop, "forbidden", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforceMutationActionPolicy(skillMutationActionHotReloadStop, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadStop, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	if h.hotReload == nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadStop, "failed", logger.String("reason", "not_configured"))
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"hot reload not configured"))
		return
	}

	if err := h.hotReload.Stop(); err != nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadStop, "failed", logger.Err(err))
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.publishSkillsChangedEvent(r, map[string]interface{}{
		"action":         skillMutationActionHotReloadStop,
		"status":         "success",
		"affected_count": 0,
		"count":          h.currentSkillCount(),
		"watching":       false,
		"skill_dirs":     append([]string{}, handlerSkillDirs(h.skillLoader)...),
	})
	h.auditSkillMutation(r, skillMutationActionHotReloadStop, "success")

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"stopped": true,
		"stats":   h.hotReload.GetStats(),
	})
}

// ReloadHotReload 手动触发热重载
func (h *Handler) ReloadHotReload(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSkillMutation(r); err != nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadRun, "forbidden", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforceMutationActionPolicy(skillMutationActionHotReloadRun, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadRun, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	if h.hotReload == nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadRun, "failed", logger.String("reason", "not_configured"))
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"hot reload not configured"))
		return
	}

	if err := h.hotReload.Reload(); err != nil {
		h.auditSkillMutation(r, skillMutationActionHotReloadRun, "failed", logger.Err(err))
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.rebuildEmbeddingIndex()
	if skills := h.skillRegistry.List(); len(skills) > 0 {
		skillChange := skillChangePayloadFromSkill(skills[0], handlerSkillDirs(h.skillLoader))
		skillChange["action"] = skillMutationActionHotReloadRun
		skillChange["status"] = "success"
		skillChange["affected_count"] = h.currentSkillCount()
		skillChange["count"] = h.currentSkillCount()
		h.publishSkillsChangedEvent(r, skillChange)
	} else {
		h.publishSkillsChangedEvent(r, map[string]interface{}{
			"action":         skillMutationActionHotReloadRun,
			"status":         "success",
			"affected_count": h.currentSkillCount(),
			"count":          h.currentSkillCount(),
			"skill_dirs":     append([]string(nil), handlerSkillDirs(h.skillLoader)...),
		})
	}
	h.auditSkillMutation(r, skillMutationActionHotReloadRun, "success")

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"reloaded": true,
		"stats":    h.hotReload.GetStats(),
	})
}

// GetHotReloadStats 获取热加载状态
func (h *Handler) GetHotReloadStats(w http.ResponseWriter, r *http.Request) {
	if h.hotReload == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"hot reload not configured"))
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"stats": h.hotReload.GetStats(),
	})
}

// updateEmbeddingIndex 更新 Embedding 索引（如果有）
func (h *Handler) updateEmbeddingIndex(skillItem *skill.Skill) {
	if h.embeddingRouter == nil || skillItem == nil {
		return
	}
	_ = h.embeddingRouter.IncrementalIndex(skillItem)
}

func (h *Handler) removeEmbeddingIndex(skillItem *skill.Skill) {
	if h.embeddingRouter == nil || skillItem == nil {
		return
	}
	_ = h.embeddingRouter.RemoveIndex(skillItem)
}

func (h *Handler) rebuildEmbeddingIndex() {
	if h.embeddingRouter == nil {
		return
	}
	_ = h.embeddingRouter.RebuildIndex()
}

type executeSkillRequest struct {
	Prompt          string                 `json:"prompt,omitempty"`
	Params          map[string]interface{} `json:"params,omitempty"`
	Context         map[string]interface{} `json:"context,omitempty"`
	History         []map[string]string    `json:"history,omitempty"`
	Options         map[string]interface{} `json:"options,omitempty"`
	ReasoningEffort string                 `json:"reasoning_effort,omitempty"`
	Thinking        *types.ThinkingConfig  `json:"thinking,omitempty"`
	SessionID       string                 `json:"session_id,omitempty"`
	UserID          string                 `json:"user_id,omitempty"`
	TenantID        string                 `json:"tenant_id,omitempty"`
	ProjectID       string                 `json:"project_id,omitempty"`
}

func (h *Handler) decodeExecuteSkillRequest(r *http.Request) (*executeSkillRequest, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	if len(body) == 0 {
		return &executeSkillRequest{
			Params:  map[string]interface{}{},
			Context: map[string]interface{}{},
			Options: map[string]interface{}{},
		}, nil
	}

	req := &executeSkillRequest{}
	if err := json.Unmarshal(body, req); err == nil {
		if req.Params == nil {
			req.Params = map[string]interface{}{}
		}
		if req.Context == nil {
			req.Context = map[string]interface{}{}
		}
		if req.Options == nil {
			req.Options = map[string]interface{}{}
		}
		if req.Prompt != "" || req.SessionID != "" || req.UserID != "" || len(req.Params) > 0 || len(req.Context) > 0 || len(req.History) > 0 || len(req.Options) > 0 {
			return req, nil
		}
	}

	legacyParams := map[string]interface{}{}
	if err := json.Unmarshal(body, &legacyParams); err != nil {
		return nil, err
	}

	prompt, _ := legacyParams["prompt"].(string)
	return &executeSkillRequest{
		Prompt:  prompt,
		Params:  legacyParams,
		Context: map[string]interface{}{},
		Options: map[string]interface{}{},
	}, nil
}

func (h *Handler) getOrCreateSession(ctx context.Context, userID, requestedSessionID string) (*chat.Session, error) {
	if h.sessionManager == nil {
		return nil, nil
	}
	userID = h.resolveServerSessionUserID(userID)
	return h.sessionManager.GetOrCreate(ctx, userID, requestedSessionID)
}

func (h *Handler) resolveServerSessionUserID(userID string) string {
	var config *runtimecfg.RuntimeConfig
	if h != nil {
		config = h.runtimeConfig
	}
	return sessionruntime.ResolveSessionUserID(sessionruntime.IdentitySource{
		ExplicitUserID: userID,
		Config:         config,
		ServerFallback: true,
	})
}

func (h *Handler) acquireSessionLease(ctx context.Context, sessionID, ownerKind, ownerScope string) (*chat.SessionLeaseHandle, error) {
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || sessionID == "" {
		return nil, nil
	}
	store := h.getSessionRuntimeStore()
	leaseStore, ok := store.(chat.SessionLeaseStore)
	if !ok || leaseStore == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerKind = strings.TrimSpace(ownerKind)
	if ownerKind == "" {
		ownerKind = sessionruntime.DefaultSessionRuntimeOwner()
	}
	return chat.AcquireSessionLease(ctx, leaseStore, chat.LeaseRequest{
		SessionID: sessionID,
		OwnerID:   sessionLeaseOwnerID(ownerKind, ownerScope),
		OwnerKind: ownerKind,
		PID:       os.Getpid(),
		Hostname:  currentHostname(),
	})
}

func (h *Handler) writeSessionLeaseConflict(w http.ResponseWriter, err error) bool {
	var conflict *chat.LeaseConflictError
	if !stderrors.As(err, &conflict) {
		return false
	}
	runtimeErr := errors.New(errors.ErrValidationFailed, "session runtime lease conflict")
	if conflict != nil && conflict.Lease != nil {
		runtimeErr = runtimeErr.WithContext("lease", conflict.Lease)
	}
	h.writeError(w, http.StatusConflict, runtimeErr)
	return true
}

func sessionLeaseOwnerID(ownerKind, scope string) string {
	parts := []string{sanitizeLeaseOwnerPart(ownerKind)}
	if hostname := currentHostname(); hostname != "" {
		parts = append(parts, sanitizeLeaseOwnerPart(hostname))
	}
	parts = append(parts, strconv.Itoa(os.Getpid()))
	if scope = strings.TrimSpace(scope); scope != "" {
		parts = append(parts, sanitizeLeaseOwnerPart(scope))
	}
	return strings.Join(parts, ":")
}

func currentHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(hostname)
}

func sanitizeLeaseOwnerPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	value = strings.ReplaceAll(value, ":", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func (h *Handler) persistChatTurn(ctx context.Context, session *chat.Session, userPrompt, assistantReply string, assistantMetadata types.Metadata) error {
	if h.sessionManager == nil || session == nil {
		return nil
	}
	if userPrompt != "" {
		if err := h.sessionManager.AddMessage(ctx, session.ID, *types.NewUserMessage(userPrompt)); err != nil {
			return err
		}
	}
	if assistantReply != "" {
		assistantMessage := types.NewAssistantMessage(assistantReply)
		if len(assistantMetadata) > 0 {
			assistantMessage.Metadata = assistantMetadata.Clone()
		}
		if err := h.sessionManager.AddMessage(ctx, session.ID, *assistantMessage); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) normalizeMessages(messages []map[string]string) []types.Message {
	result := make([]types.Message, 0, len(messages))
	for _, message := range messages {
		role := message["role"]
		content := message["content"]
		if role == "" && content == "" {
			continue
		}
		result = append(result, types.Message{
			Role:     role,
			Content:  content,
			Metadata: types.NewMetadata(),
		})
	}
	return result
}

func (h *Handler) mergeContext(contextMap, params map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{}, len(contextMap)+len(params))
	for key, value := range params {
		merged[key] = value
	}
	for key, value := range contextMap {
		merged[key] = value
	}
	return merged
}

func (h *Handler) buildChatMessages(rawMessages []map[string]string, session *chat.Session) ([]types.Message, string) {
	requestMessages := h.normalizeMessages(rawMessages)
	messages := requestMessages
	if session != nil && len(requestMessages) == 1 && requestMessages[0].Role == "user" {
		messages = append(messages[:0:0], session.GetMessages()...)
		messages = append(messages, requestMessages[0])
	} else if session != nil && len(requestMessages) == 0 {
		messages = append(messages, session.GetMessages()...)
	}

	lastUserMessage := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserMessage = messages[i].Content
			break
		}
	}
	return messages, lastUserMessage
}

func (h *Handler) ensureSessionSystemPrompt(ctx context.Context, session *chat.Session, prompt string) bool {
	if session == nil {
		return false
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return false
	}
	history := session.GetMessages()
	if len(history) == 0 {
		session.ReplaceHistory([]types.Message{*types.NewSystemMessage(prompt)})
		return true
	}
	if history[0].Role == "system" {
		if strings.TrimSpace(history[0].Content) == prompt {
			return false
		}
		history[0].Content = prompt
		session.ReplaceHistory(history)
		return true
	}
	newHistory := append([]types.Message{*types.NewSystemMessage(prompt)}, history...)
	session.ReplaceHistory(newHistory)
	return true
}

func (h *Handler) applyProfileSessionContext(session *chat.Session, state *profileRuntimeState) bool {
	if session == nil || state == nil || state.Resolved == nil {
		return false
	}
	changed := false
	setValue := func(key, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if existing := sessionmeta.String(session.Metadata.Context, key); existing == value {
			return
		}
		if session.Metadata.Context == nil {
			session.Metadata.Context = make(map[string]interface{})
		}
		switch key {
		case apiProfileContextReference:
			sessionmeta.Set(session.Metadata.Context, sessionmeta.ProfileRef, value, apiProfileContextReference)
		case apiProfileContextName:
			sessionmeta.Set(session.Metadata.Context, sessionmeta.ProfileName, value)
		case apiProfileContextAgent:
			sessionmeta.Set(session.Metadata.Context, sessionmeta.ProfileAgent, value)
		case apiProfileContextRoot:
			sessionmeta.Set(session.Metadata.Context, sessionmeta.ProfileRoot, value)
		default:
			session.SetContext(key, value)
		}
		changed = true
	}

	setValue(apiProfileContextReference, state.Reference)
	setValue(apiProfileContextName, state.Resolved.ProfileName)
	setValue(apiProfileContextAgent, state.Resolved.AgentID)
	setValue(apiProfileContextRoot, state.Resolved.ProfileRoot)
	return changed
}

func (h *Handler) maybeAutoCompactSessionHistory(ctx context.Context, session *chat.Session, apiAgent *agent.Agent, provider, model, traceID, taskID string) {
	if h == nil || session == nil || apiAgent == nil || h.llmRuntime == nil {
		return
	}

	manager := apiAgent.GetContextManager()
	keepRecent := 0
	if manager != nil {
		keepRecent = manager.Budget.KeepRecentMessages
	}

	runtime := compactruntime.New(h.llmRuntime, manager)
	result, status, err := runtime.MaybeCompact(ctx, compactruntime.Request{
		SessionID:          session.ID,
		TaskID:             firstNonEmptyString(taskID, session.ID),
		Provider:           provider,
		Model:              model,
		History:            session.GetMessages(),
		KeepRecentMessages: keepRecent,
		Phase:              compactruntime.PhasePreTurn,
		CountTokens:        h.llmRuntime.CountMessagesTokens,
	})

	payload := map[string]interface{}{
		"session_id":          session.ID,
		"phase":               compactruntime.PhasePreTurn,
		"mode":                status.Mode,
		"reason":              status.Reason,
		"token_before":        status.TokenBefore,
		"trigger_token_limit": status.TriggerTokenLimit,
		"max_context_tokens":  status.MaxContextTokens,
		"provider":            status.ResolvedProvider,
		"model":               status.ResolvedModel,
	}
	if status.TriggerTokenLimit > 0 && status.TokenBefore > status.TriggerTokenLimit {
		h.publishSessionRuntimeEvent(chat.EventSessionCompactStarted, traceID, session.ID, cloneAnyMap(payload))
	}

	if err != nil {
		payload["error"] = err.Error()
		h.publishSessionRuntimeEvent(chat.EventSessionCompactFailed, traceID, session.ID, payload)
		return
	}
	if result == nil {
		h.publishSessionRuntimeEvent(chat.EventSessionCompactSkipped, traceID, session.ID, payload)
		return
	}

	originalHistory := session.GetMessages()
	session.ReplaceHistory(result.ReplacementHistory)
	if h.sessionManager != nil {
		if updateErr := h.sessionManager.Update(ctx, session); updateErr != nil {
			session.ReplaceHistory(originalHistory)
			payload["error"] = updateErr.Error()
			h.publishSessionRuntimeEvent(chat.EventSessionCompactFailed, traceID, session.ID, payload)
			return
		}
	}

	payload["token_after"] = result.TokenAfter
	payload["compacted_messages"] = result.CompactedMessages
	payload["message_count_after"] = len(result.ReplacementHistory)
	if len(result.CheckpointIDs) > 0 {
		payload["checkpoint_ids"] = append([]string(nil), result.CheckpointIDs...)
		payload["checkpoint_id"] = result.CheckpointIDs[len(result.CheckpointIDs)-1]
	}
	h.publishSessionRuntimeEvent(chat.EventSessionCompactCompleted, traceID, session.ID, payload)
}

func injectSystemPrompt(messages []types.Message, prompt string) []types.Message {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return messages
	}
	if len(messages) == 0 {
		return []types.Message{*types.NewSystemMessage(prompt)}
	}
	if messages[0].Role == "system" {
		if strings.TrimSpace(messages[0].Content) == prompt {
			return messages
		}
		cloned := append([]types.Message(nil), messages...)
		cloned[0].Content = prompt
		return cloned
	}
	return append([]types.Message{*types.NewSystemMessage(prompt)}, messages...)
}

func resolveAgentProvider(profileState *profileRuntimeState, config *runtimecfg.RuntimeConfig, runtime *llm.LLMRuntime) string {
	if profileState != nil && profileState.Resolved != nil {
		if provider := strings.TrimSpace(profileState.Resolved.Provider); provider != "" {
			return provider
		}
	}
	if config != nil {
		if provider := strings.TrimSpace(config.Agent.DefaultProvider); provider != "" {
			return provider
		}
	}
	if runtime != nil {
		if provider := strings.TrimSpace(runtime.DefaultProvider()); provider != "" {
			return provider
		}
	}
	return ""
}

func resolveAgentModel(profileState *profileRuntimeState, config *runtimecfg.RuntimeConfig, runtime *llm.LLMRuntime) string {
	if profileState != nil && profileState.Resolved != nil {
		if model := strings.TrimSpace(profileState.Resolved.Model); model != "" {
			return model
		}
	}
	if config != nil {
		if model := strings.TrimSpace(config.Agent.DefaultModel); model != "" {
			return model
		}
	}
	return defaultAgentModel(runtime)
}

func profileStateToolPolicy(state *profileRuntimeState) *runtimepolicy.ToolExecutionPolicy {
	if state == nil {
		return nil
	}
	return state.ToolPolicy
}

func mergeProfileContextInto(target map[string]interface{}, values map[string]interface{}) {
	if target == nil || len(values) == 0 {
		return
	}
	for key, value := range values {
		target[key] = cloneProfileContextValue(value)
	}
}

func buildProfileContextPack(state *profileRuntimeState) map[string]interface{} {
	if state == nil {
		return nil
	}
	pack := map[string]interface{}{}
	if reference := strings.TrimSpace(state.Reference); reference != "" {
		pack["reference"] = reference
	}
	if state.Resolved != nil {
		if name := strings.TrimSpace(state.Resolved.ProfileName); name != "" {
			pack["name"] = name
		}
		if agentID := strings.TrimSpace(state.Resolved.AgentID); agentID != "" {
			pack["agent"] = agentID
		}
		if root := strings.TrimSpace(state.Resolved.ProfileRoot); root != "" {
			pack["root"] = root
		}
	}
	if state.ContextValues != nil {
		if memoryPath, ok := state.ContextValues["profile_memory_path"].(string); ok && strings.TrimSpace(memoryPath) != "" {
			pack["memory_path"] = strings.TrimSpace(memoryPath)
		}
		if notesPath, ok := state.ContextValues["profile_notes_path"].(string); ok && strings.TrimSpace(notesPath) != "" {
			pack["notes_path"] = strings.TrimSpace(notesPath)
		}
		if resources, ok := state.ContextValues["profile_resources"].(map[string]interface{}); ok && len(resources) > 0 {
			pack["resources"] = cloneProfileContextValues(resources)
		}
	}
	if len(pack) == 0 {
		return nil
	}
	return pack
}

func (h *Handler) newAPIAgent(cfg *agent.Config) *agent.Agent {
	return h.newAPIAgentWithRuntime(cfg, nil)
}

type agentRuntimeComponents struct {
	registry        *skill.Registry
	embeddingRouter *skill.SemanticEmbeddingRouter
	mcpManager      skill.MCPManager
	llmRuntime      *llm.LLMRuntime
}

func (h *Handler) newAPIAgentWithRuntime(cfg *agent.Config, runtime *agentRuntimeComponents) *agent.Agent {
	registry := h.skillRegistry
	embeddingRouter := h.embeddingRouter
	mcpManager := h.mcpManager
	llmRuntime := h.llmRuntime

	if runtime != nil {
		if runtime.registry != nil {
			registry = runtime.registry
		}
		if runtime.embeddingRouter != nil {
			embeddingRouter = runtime.embeddingRouter
		}
		if runtime.mcpManager != nil {
			mcpManager = runtime.mcpManager
		}
		if runtime.llmRuntime != nil {
			llmRuntime = runtime.llmRuntime
		}
	}

	var apiAgent *agent.Agent
	if llmRuntime != nil {
		apiAgent = agent.NewAgentWithLLM(cfg, mcpManager, llmRuntime)
	} else {
		apiAgent = agent.NewAgent(cfg, mcpManager)
	}

	if registry != nil {
		for _, summary := range registry.ListSummaries() {
			if summary == nil {
				continue
			}
			_ = apiAgent.RegisterSkill(summary.ToSkillStub())
		}
	}
	if embeddingRouter != nil {
		if agentEmbeddingRouter, err := embeddingRouter.CloneForRegistry(apiAgent.GetSkillRouter().Registry()); err == nil {
			apiAgent.GetSkillRouter().SetEmbeddingRouter(agentEmbeddingRouter)
		}
	}
	apiAgent.SetEventBus(h.getRuntimeEventBus())
	if h.teamStore != nil {
		if ctxMgr := apiAgent.GetContextManager(); ctxMgr != nil {
			ctxMgr.TeamContext = team.NewContextBuilder(h.teamStore)
		}
	}
	if runtime == nil {
		if gateway := h.getRuntimeToolCatalogGateway(); gateway != nil {
			gateway.Refresh()
			apiAgent.SetToolCatalog(gateway.Catalog())
		}
	}

	return apiAgent
}

func (h *Handler) getRuntimeEventBus() *runtimeevents.Bus {
	if h.runtimeEventBus == nil {
		h.runtimeEventBus = runtimeevents.NewBusWithRetention(2048)
	}
	h.attachRuntimeEventBridge()
	return h.runtimeEventBus
}

func (h *Handler) attachRuntimeEventBridge() {
	if h == nil {
		return
	}
	h.runtimeEventBridgeOnce.Do(func() {
		bus := h.runtimeEventBus
		if bus == nil {
			return
		}
		bus.Subscribe("", func(event runtimeevents.Event) {
			if !shouldPersistRuntimeSessionEvent(event) {
				return
			}
			store := h.getSessionEventStore()
			if store == nil {
				return
			}
			mapped := mapRuntimeEventToSession(event)
			_, _ = store.AppendEvent(context.Background(), mapped)
		})
	})
}

func shouldPersistRuntimeSessionEvent(event runtimeevents.Event) bool {
	if strings.TrimSpace(event.SessionID) == "" {
		return false
	}
	switch strings.TrimSpace(event.Type) {
	case "tool.requested", "tool.completed", "context.profile.injected", "recall.performed", "checkpoint_created":
		return true
	default:
		return false
	}
}

func mapRuntimeEventToSession(event runtimeevents.Event) runtimeevents.Event {
	mapped := event
	switch strings.TrimSpace(event.Type) {
	case "tool.requested":
		mapped.Type = chat.EventToolStarted
	case "tool.completed":
		mapped.Type = chat.EventToolFinished
	}
	return mapped
}

func (h *Handler) getRuntimeToolCatalogGateway() *mcpcatalog.Gateway {
	if h == nil || h.mcpManager == nil {
		return nil
	}
	backend, resolvedPath, configKey := h.runtimeToolCatalogConfig()
	if h.runtimeToolCatalog != nil && h.runtimeToolCatalogConfigKey == configKey {
		h.attachRuntimeMCPLifecycleBridge()
		return h.runtimeToolCatalog
	}
	store := runtimeToolCatalogSnapshotStore(backend, resolvedPath)
	if manager := h.runtimeMCPManager(); manager != nil {
		h.runtimeToolCatalog = mcpcatalog.NewManagerGatewayWithStore(manager, store)
		h.runtimeToolCatalogConfigKey = configKey
		h.attachRuntimeMCPLifecycleBridge()
		return h.runtimeToolCatalog
	}
	h.runtimeToolCatalog = mcpcatalog.NewGatewayWithStore(h.mcpManager, store)
	h.runtimeToolCatalogConfigKey = configKey
	h.attachRuntimeMCPLifecycleBridge()
	return h.runtimeToolCatalog
}

func (h *Handler) runtimeToolCatalogConfig() (string, string, string) {
	backend := "memory"
	snapshotPath := ""
	if h != nil && h.runtimeConfig != nil {
		if value := strings.TrimSpace(h.runtimeConfig.Catalog.Backend); value != "" {
			backend = strings.ToLower(value)
		}
		snapshotPath = strings.TrimSpace(h.runtimeConfig.Catalog.SnapshotPath)
	}
	resolvedPath := resolveRuntimeCatalogSnapshotPath(h.runtimeConfigFile, snapshotPath)
	return backend, resolvedPath, backend + ":" + resolvedPath
}

func runtimeToolCatalogSnapshotStore(backend, resolvedPath string) mcpcatalog.SnapshotStore {
	switch backend {
	case "file":
		return mcpcatalog.NewFileSnapshotStore(resolvedPath)
	case "sqlite":
		if store, err := mcpcatalog.NewSQLiteSnapshotStore(resolvedPath); err == nil {
			return store
		}
	}
	return nil
}

func resolveRuntimeCatalogSnapshotPath(configFile, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) || strings.TrimSpace(configFile) == "" {
		return path
	}
	baseDir := filepath.Dir(strings.TrimSpace(configFile))
	if baseDir == "" || baseDir == "." {
		return path
	}
	return filepath.Join(baseDir, path)
}

func resolveRuntimeTeamStorePath(configFile, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) || strings.TrimSpace(configFile) == "" {
		return path
	}
	baseDir := filepath.Dir(strings.TrimSpace(configFile))
	if baseDir == "" || baseDir == "." {
		return path
	}
	return filepath.Join(baseDir, path)
}

func resolveRuntimeAgentControlMailboxStorePath(configFile, path string) string {
	return resolveRuntimeTeamStorePath(configFile, path)
}

func resolveRuntimeAgentControlAgentStorePath(configFile, path string) string {
	return resolveRuntimeTeamStorePath(configFile, path)
}

func resolveRuntimeAgentControlStorePath(configFile, path string) string {
	return resolveRuntimeTeamStorePath(configFile, path)
}

func resolveRuntimeBackgroundStorePath(configFile, path string) string {
	return resolveRuntimeTeamStorePath(configFile, path)
}

func resolveRuntimeBackgroundLogDir(configFile, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) || strings.TrimSpace(configFile) == "" {
		return path
	}
	baseDir := filepath.Dir(strings.TrimSpace(configFile))
	if baseDir == "" || baseDir == "." {
		return path
	}
	return filepath.Join(baseDir, path)
}

func resolveRuntimeSessionRuntimeStorePath(configFile, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) || strings.TrimSpace(configFile) == "" {
		return path
	}
	baseDir := filepath.Dir(strings.TrimSpace(configFile))
	if baseDir == "" || baseDir == "." {
		return path
	}
	return filepath.Join(baseDir, path)
}

func splitTeamStoreKey(key string) (string, string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", ""
	}
	parts := strings.SplitN(key, "|", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func buildTeamReloadAuditPayload(r *http.Request, scope UsageScope, current, desired map[string]interface{}) map[string]interface{} {
	payload := map[string]interface{}{
		"request_ip": requestRemoteIP(r),
		"user_agent": requestUserAgent(r),
		"current":    current,
		"desired":    desired,
	}
	if requestID := requestIDFromRequest(r); requestID != "" {
		payload["request_id"] = requestID
	}
	if scope.TenantID != "" {
		payload["tenant_id"] = scope.TenantID
	}
	if scope.ProjectID != "" {
		payload["project_id"] = scope.ProjectID
	}
	if scope.UserID != "" {
		payload["user_id"] = scope.UserID
	}
	if scope.ScopeKey != "" {
		payload["scope_key"] = scope.ScopeKey
	}
	return payload
}

func requestUserAgent(r *http.Request) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.UserAgent())
}

func requestIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, key := range []string{"X-Request-ID", "X-Request-Id", "X-Correlation-ID"} {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func (h *Handler) refreshTeamStore(config *runtimecfg.RuntimeConfig, configFile, traceID, requestID string) (bool, error) {
	if h == nil || config == nil {
		return false, nil
	}
	storePath := resolveRuntimeTeamStorePath(configFile, config.Team.StorePath)
	storeDSN := strings.TrimSpace(config.Team.StoreDSN)
	configKey := storePath + "|" + storeDSN

	h.teamStoreMu.RLock()
	currentKey := h.teamStoreConfigKey
	currentStore := h.teamStore
	h.teamStoreMu.RUnlock()

	if currentKey == "" && configKey == "|" && currentStore != nil {
		h.teamStoreMu.Lock()
		if h.teamStoreConfigKey == "" {
			h.teamStoreConfigKey = configKey
		}
		h.teamStoreMu.Unlock()
		return false, nil
	}
	if configKey == currentKey {
		return false, nil
	}

	store, err := team.NewSQLiteStore(&team.StoreConfig{
		Path: strings.TrimSpace(storePath),
		DSN:  storeDSN,
	})
	if err != nil {
		payload := map[string]interface{}{
			"store_path": storePath,
			"uses_dsn":   storeDSN != "",
			"error":      err.Error(),
		}
		if strings.TrimSpace(requestID) != "" {
			payload["request_id"] = strings.TrimSpace(requestID)
		}
		h.publishRuntimeEvent("team.store.reload_failed", traceID, payload)
		return false, err
	}

	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		lifecycle.StopAllLoops()
	}
	h.teamStoreMu.Lock()
	oldStore := h.teamStore
	h.teamStore = store
	h.teamStoreConfigKey = configKey
	h.teamClaimsManager = nil
	h.teamOrchestrator = nil
	h.teamStoreMu.Unlock()

	if oldStore != nil {
		_ = oldStore.Close()
	}
	h.configureMailboxWriteThrough(h.getAgentControlMailboxStore())
	payload := map[string]interface{}{
		"store_path": storePath,
		"uses_dsn":   storeDSN != "",
	}
	if strings.TrimSpace(requestID) != "" {
		payload["request_id"] = strings.TrimSpace(requestID)
	}
	h.publishRuntimeEvent("team.store.reloaded", traceID, payload)
	return true, nil
}

func (h *Handler) refreshAgentControlMailboxStore(config *runtimecfg.RuntimeConfig, configFile string) (bool, error) {
	if h == nil || config == nil {
		return false, nil
	}
	storePath := resolveRuntimeAgentControlMailboxStorePath(configFile, firstNonEmptyString(config.AgentControl.MailboxStorePath, config.AgentControl.StorePath))
	storeDSN := strings.TrimSpace(firstNonEmptyString(config.AgentControl.MailboxStoreDSN, config.AgentControl.StoreDSN))
	configKey := storePath + "|" + storeDSN

	h.agentControlMu.RLock()
	currentKey := h.agentControlMailboxStoreKey
	currentStore := h.agentControlMailboxStore
	autoManaged := h.agentControlMailboxStoreAuto
	h.agentControlMu.RUnlock()

	if configKey == "|" {
		if currentStore != nil && autoManaged {
			h.agentControlMu.Lock()
			if h.agentControlMailboxStore == currentStore && h.agentControlMailboxStoreAuto {
				h.agentControlMailboxStore = nil
				h.agentControlMailboxStoreKey = ""
				h.agentControlMailboxStoreAuto = false
			}
			h.agentControlMu.Unlock()
			_ = currentStore.Close()
			h.configureMailboxWriteThrough(nil)
			return true, nil
		}
		return false, nil
	}
	if currentStore != nil && !autoManaged {
		return false, nil
	}
	if configKey == currentKey && currentStore != nil {
		return false, nil
	}

	store, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: strings.TrimSpace(storePath),
		DSN:  storeDSN,
	})
	if err != nil {
		h.publishRuntimeEvent("agent_control.mailbox.store.reload_failed", "", map[string]interface{}{
			"store_path": storePath,
			"uses_dsn":   storeDSN != "",
			"error":      err.Error(),
		})
		return false, err
	}

	h.agentControlMu.Lock()
	oldStore := h.agentControlMailboxStore
	oldAutoManaged := h.agentControlMailboxStoreAuto
	h.agentControlMailboxStore = store
	h.agentControlMailboxStoreKey = configKey
	h.agentControlMailboxStoreAuto = true
	h.agentControlMu.Unlock()
	h.configureMailboxWriteThrough(store)

	if oldStore != nil && oldAutoManaged {
		_ = oldStore.Close()
	}
	h.publishRuntimeEvent("agent_control.mailbox.store.reloaded", "", map[string]interface{}{
		"store_path": storePath,
		"uses_dsn":   storeDSN != "",
	})
	return true, nil
}

func (h *Handler) refreshAgentControlRegistryService(config *runtimecfg.RuntimeConfig, configFile string) (bool, error) {
	if h == nil || config == nil {
		return false, nil
	}
	cfg := agentcontrol.RegistryServiceConfig{
		StorePath:        resolveRuntimeAgentControlStorePath(configFile, config.AgentControl.StorePath),
		StoreDSN:         strings.TrimSpace(config.AgentControl.StoreDSN),
		MailboxStorePath: resolveRuntimeAgentControlMailboxStorePath(configFile, config.AgentControl.MailboxStorePath),
		MailboxStoreDSN:  strings.TrimSpace(config.AgentControl.MailboxStoreDSN),
		AgentStorePath:   resolveRuntimeAgentControlAgentStorePath(configFile, config.AgentControl.AgentStorePath),
		AgentStoreDSN:    strings.TrimSpace(config.AgentControl.AgentStoreDSN),
	}
	configKey := cfg.Key()

	h.agentControlMu.RLock()
	currentKey := h.agentControlRegistryStoreKey
	currentService := h.agentControlRegistryService
	currentMailboxStore := h.agentControlMailboxStore
	currentAgentStore := h.agentControlAgentStore
	mailboxAuto := h.agentControlMailboxStoreAuto
	agentAuto := h.agentControlAgentStoreAuto
	h.agentControlMu.RUnlock()

	if cfg.Empty() {
		if currentService != nil && mailboxAuto && agentAuto {
			h.agentControlMu.Lock()
			if h.agentControlRegistryService == currentService {
				h.agentControlRegistryService = nil
				h.agentControlRegistryStoreKey = ""
				h.agentControlMailboxStore = nil
				h.agentControlMailboxStoreKey = ""
				h.agentControlMailboxStoreAuto = false
				h.agentControlAgentStore = nil
				h.agentControlAgentStoreKey = ""
				h.agentControlAgentStoreAuto = false
			}
			h.agentControlMu.Unlock()
			h.configureMailboxWriteThrough(nil)
			_ = currentService.Close()
			return true, nil
		}
		return false, nil
	}
	if (currentMailboxStore != nil && !mailboxAuto) || (currentAgentStore != nil && !agentAuto) {
		return false, nil
	}
	if currentService != nil && currentKey == configKey {
		return false, nil
	}

	service, err := agentcontrol.NewRegistryService(context.Background(), cfg)
	if err != nil {
		h.publishRuntimeEvent("agent_control.registry.store.reload_failed", "", map[string]interface{}{
			"store_path":         cfg.Normalize().StorePath,
			"mailbox_store_path": cfg.Normalize().MailboxStorePath,
			"agent_store_path":   cfg.Normalize().AgentStorePath,
			"uses_dsn":           cfg.Normalize().StoreDSN != "" || cfg.Normalize().MailboxStoreDSN != "" || cfg.Normalize().AgentStoreDSN != "",
			"error":              err.Error(),
		})
		return false, err
	}

	h.agentControlMu.Lock()
	oldService := h.agentControlRegistryService
	h.agentControlRegistryService = service
	h.agentControlRegistryStoreKey = configKey
	h.agentControlMailboxStore = service.MailboxStore
	h.agentControlMailboxStoreKey = "registry|" + configKey
	h.agentControlMailboxStoreAuto = true
	h.agentControlAgentStore = service.AgentStore
	h.agentControlAgentStoreKey = "registry|" + configKey
	h.agentControlAgentStoreAuto = true
	h.agentControlMu.Unlock()
	h.configureMailboxWriteThrough(service.MailboxStore)

	if oldService != nil {
		_ = oldService.Close()
	}
	normalized := cfg.Normalize()
	h.publishRuntimeEvent("agent_control.registry.store.reloaded", "", map[string]interface{}{
		"store_path":         normalized.StorePath,
		"mailbox_store_path": normalized.MailboxStorePath,
		"agent_store_path":   normalized.AgentStorePath,
		"uses_dsn":           normalized.StoreDSN != "" || normalized.MailboxStoreDSN != "" || normalized.AgentStoreDSN != "",
	})
	return true, nil
}

func (h *Handler) refreshAgentControlAgentStore(config *runtimecfg.RuntimeConfig, configFile string) (bool, error) {
	if h == nil || config == nil {
		return false, nil
	}
	storePath := resolveRuntimeAgentControlAgentStorePath(configFile, firstNonEmptyString(config.AgentControl.AgentStorePath, config.AgentControl.StorePath))
	storeDSN := strings.TrimSpace(firstNonEmptyString(config.AgentControl.AgentStoreDSN, config.AgentControl.StoreDSN))
	configKey := storePath + "|" + storeDSN

	h.agentControlMu.RLock()
	currentKey := h.agentControlAgentStoreKey
	currentStore := h.agentControlAgentStore
	autoManaged := h.agentControlAgentStoreAuto
	h.agentControlMu.RUnlock()

	if configKey == "|" {
		if currentStore != nil && autoManaged {
			h.agentControlMu.Lock()
			if h.agentControlAgentStore == currentStore && h.agentControlAgentStoreAuto {
				h.agentControlAgentStore = nil
				h.agentControlAgentStoreKey = ""
				h.agentControlAgentStoreAuto = false
			}
			h.agentControlMu.Unlock()
			_ = currentStore.Close()
			return true, nil
		}
		return false, nil
	}
	if currentStore != nil && !autoManaged {
		return false, nil
	}
	if configKey == currentKey && currentStore != nil {
		return false, nil
	}

	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: strings.TrimSpace(storePath),
		DSN:  storeDSN,
	})
	if err != nil {
		h.publishRuntimeEvent("agent_control.agent.store.reload_failed", "", map[string]interface{}{
			"store_path": storePath,
			"uses_dsn":   storeDSN != "",
			"error":      err.Error(),
		})
		return false, err
	}

	h.agentControlMu.Lock()
	oldStore := h.agentControlAgentStore
	oldAutoManaged := h.agentControlAgentStoreAuto
	h.agentControlAgentStore = store
	h.agentControlAgentStoreKey = configKey
	h.agentControlAgentStoreAuto = true
	h.agentControlMu.Unlock()

	if oldStore != nil && oldAutoManaged {
		_ = oldStore.Close()
	}
	h.publishRuntimeEvent("agent_control.agent.store.reloaded", "", map[string]interface{}{
		"store_path": storePath,
		"uses_dsn":   storeDSN != "",
	})
	return true, nil
}

func (h *Handler) refreshSessionRuntimeStore(config *runtimecfg.RuntimeConfig, configFile string) (bool, error) {
	if h == nil || config == nil {
		return false, nil
	}

	storePath := resolveRuntimeSessionRuntimeStorePath(configFile, config.SessionRuntime.StorePath)
	storeDSN := strings.TrimSpace(config.SessionRuntime.StoreDSN)
	configKey := storePath + "|" + storeDSN

	h.sessionRuntimeMu.RLock()
	currentKey := h.sessionRuntimeStoreKey
	currentStore := h.sessionRuntimeStore
	h.sessionRuntimeMu.RUnlock()

	if currentKey == "" && configKey == "|" && currentStore != nil {
		h.sessionRuntimeMu.Lock()
		if h.sessionRuntimeStoreKey == "" {
			h.sessionRuntimeStoreKey = configKey
		}
		h.sessionRuntimeMu.Unlock()
		return false, nil
	}
	if configKey == currentKey && currentStore != nil {
		return false, nil
	}

	var (
		stateStore chat.RuntimeStateStore
		eventStore chat.EventStore
	)
	if storePath != "" || storeDSN != "" {
		store, err := chat.NewSQLiteRuntimeStore(&chat.RuntimeStoreConfig{
			Path: strings.TrimSpace(storePath),
			DSN:  storeDSN,
		})
		if err != nil {
			h.publishRuntimeEvent("session.runtime.store.reload_failed", "", map[string]interface{}{
				"store_path": storePath,
				"uses_dsn":   storeDSN != "",
				"error":      err.Error(),
			})
			return false, err
		}
		stateStore = store
		eventStore = store
	} else {
		memoryStore := chat.NewInMemoryRuntimeStore(2048)
		stateStore = memoryStore
		eventStore = memoryStore
	}

	h.sessionRuntimeMu.Lock()
	oldStore := h.sessionRuntimeStore
	oldEventStore := h.sessionEventStore
	oldHub := h.sessionHub
	h.sessionRuntimeStore = stateStore
	h.sessionEventStore = eventStore
	h.sessionRuntimeStoreKey = configKey
	h.sessionHub = nil
	h.sessionRuntimeMu.Unlock()

	if oldHub != nil {
		oldHub.StopAll()
	}
	closeRuntimeStore(oldStore, oldEventStore)
	h.configureMailboxWriteThrough(h.getAgentControlMailboxStore())

	h.publishRuntimeEvent("session.runtime.store.reloaded", "", map[string]interface{}{
		"store_path": storePath,
		"uses_dsn":   storeDSN != "",
	})
	return true, nil
}

func closeRuntimeStore(store chat.RuntimeStateStore, eventStore chat.EventStore) {
	seen := map[interface{}]struct{}{}
	closeStore := func(value interface{}) {
		if value == nil {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		if closer, ok := value.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	closeStore(store)
	closeStore(eventStore)
}

func (h *Handler) publishRuntimeEvent(eventType, traceID string, payload map[string]interface{}) {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	if traceID != "" {
		payload["trace_id"] = traceID
	}
	h.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      eventType,
		TraceID:   traceID,
		AgentName: "runtime-admin",
		Payload:   payload,
	})
}

func (h *Handler) publishSessionRuntimeEvent(eventType, traceID, sessionID string, payload map[string]interface{}) {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	if traceID != "" {
		payload["trace_id"] = traceID
	}
	h.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      eventType,
		TraceID:   traceID,
		SessionID: strings.TrimSpace(sessionID),
		AgentName: "runtime-admin",
		Payload:   payload,
	})
}

func (h *Handler) attachRuntimeMCPLifecycleBridge() {
	if h == nil {
		return
	}
	manager := h.runtimeMCPManager()
	if manager == nil {
		return
	}
	observable, ok := manager.(mcpmanager.ObservableManager)
	if !ok || observable == nil {
		return
	}

	h.runtimeMCPBridgeOnce.Do(func() {
		observable.AddLifecycleObserver(func(event mcpmanager.LifecycleEvent) {
			payload := make(map[string]interface{}, len(event.Payload)+1)
			for key, value := range event.Payload {
				payload[key] = value
			}
			payload["mcp_name"] = event.MCPName

			switch event.Type {
			case "mcp.connected", "mcp.tools.loaded", "mcp.reconnected", "mcp.disabled", "mcp.stopped":
				if gateway := h.runtimeToolCatalog; gateway != nil {
					gateway.Refresh()
				}
			}

			h.getRuntimeEventBus().Publish(runtimeevents.Event{
				Type:      event.Type,
				TraceID:   event.TraceID,
				AgentName: "mcp-manager",
				Payload:   payload,
				Timestamp: event.Timestamp,
			})
		})
	})
}

func (h *Handler) applyAgentExecutionPolicy(a *agent.Agent, workspacePath string, runtimeConfig *runtimecfg.RuntimeConfig, profilePolicy *runtimepolicy.ToolExecutionPolicy) {
	if a == nil {
		return
	}

	mutationPolicy := h.getMutationPolicy()
	readOnly := mutationPolicy.ReadOnly
	if profilePolicy != nil && profilePolicy.ReadOnly {
		readOnly = true
	}
	allowlist := []string(nil)
	if profilePolicy != nil && profilePolicy.AllowlistEnabled {
		allowlist = profilePolicy.AllowedToolNames()
	}
	toolPolicy := agent.NewToolExecutionPolicy(allowlist, readOnly)
	if profilePolicy != nil && len(profilePolicy.DeniedTools) > 0 {
		toolPolicy.DeniedTools = make(map[string]bool, len(profilePolicy.DeniedTools))
		for name, denied := range profilePolicy.DeniedTools {
			if denied {
				toolPolicy.DeniedTools[name] = true
			}
		}
	}
	sandboxCfg := executor.SandboxConfig{}
	if runtimeConfig != nil {
		sandboxCfg = executor.CloneSandboxConfig(runtimeConfig.Sandbox)
	}
	if profilePolicy != nil && profilePolicy.Sandbox != nil {
		executor.OverlaySandboxConfig(&sandboxCfg, profilePolicy.Sandbox.Config())
	}

	if readOnly {
		sandboxCfg.DeniedCommands = appendUniqueStrings(sandboxCfg.DeniedCommands, defaultReadOnlyDeniedCommands()...)
	}
	if strings.TrimSpace(workspacePath) != "" {
		sandboxCfg.AllowedPaths = appendUniqueStrings(sandboxCfg.AllowedPaths, workspacePath)
		if readOnly {
			sandboxCfg.ReadOnlyPaths = appendUniqueStrings(sandboxCfg.ReadOnlyPaths, workspacePath)
		}
	}
	if executor.SandboxConfigActive(sandboxCfg) || strings.TrimSpace(workspacePath) != "" || readOnly {
		sandboxCfg.Enabled = true
		toolPolicy.Sandbox = executor.NewSandbox(&sandboxCfg)
	}

	a.SetToolExecutionPolicy(toolPolicy)
}

func (h *Handler) applyAgentHooks(a *agent.Agent, runtimeConfig *runtimecfg.RuntimeConfig) {
	if a == nil {
		return
	}
	config := runtimeConfig
	if config == nil {
		config = h.runtimeConfig
	}
	if config == nil || len(config.Hooks) == 0 {
		return
	}
	a.SetHookManager(runtimehooks.NewManager(config.Hooks))
}

func (h *Handler) applyAgentRuntimeServices(a *agent.Agent, runtimeConfig *runtimecfg.RuntimeConfig) {
	if a == nil {
		return
	}
	config := runtimeConfig
	if config == nil {
		config = h.runtimeConfig
	}

	if h.getSessionHub() != nil && h.sessionManager != nil {
		broker := a.GetToolBroker()
		if broker == nil {
			broker = &toolbroker.Broker{}
			a.SetToolBroker(broker)
		}
		if broker.SessionContextStore == nil {
			broker.SessionContextStore = toolbrokersessionctx.New(h.sessionManager.GetStorage())
		}
		broker.AgentSessions = &sessionAgentController{handler: h}
	}

	if store := h.getTeamStore(); store != nil {
		broker := a.GetToolBroker()
		if broker == nil {
			broker = &toolbroker.Broker{}
			a.SetToolBroker(broker)
		}
		if broker.SessionContextStore == nil && h.sessionManager != nil {
			broker.SessionContextStore = toolbrokersessionctx.New(h.sessionManager.GetStorage())
		}
		broker.AgentSessions = &sessionAgentController{handler: h}
		broker.TeamStore = store
		broker.TeamClaims = h.getTeamClaimsManager()
		broker.TeamDispatcher = h
		broker.TeamLifecycleChanged = func() {
			if lifecycle := h.teamLifecycleService(); lifecycle != nil {
				lifecycle.SyncLoops()
			}
		}
		if orchestrator := h.getTeamOrchestrator(); orchestrator != nil {
			broker.TeamEvents = orchestrator.Events
		}
		if hub := h.getSessionHub(); hub != nil {
			broker.TeamPlanner = &team.LeadPlanner{
				Sessions:    &sessionActorClient{hub: hub},
				Store:       store,
				Mailbox:     team.NewMailboxService(store),
				AutoPersist: true,
			}
		}
	}

	if config == nil {
		return
	}

	if config.Checkpoint.Enabled {
		if manager := a.GetCheckpointManager(); manager != nil {
			if config.Checkpoint.MaxFileBytes > 0 {
				manager.MaxFileBytes = config.Checkpoint.MaxFileBytes
			}
		}
	} else {
		a.SetCheckpointManager(nil)
	}

	if bgManager := h.getBackgroundManager(config); bgManager != nil {
		broker := a.GetToolBroker()
		if broker == nil {
			broker = &toolbroker.Broker{}
			a.SetToolBroker(broker)
		}
		if broker.SessionContextStore == nil && h.sessionManager != nil {
			broker.SessionContextStore = toolbrokersessionctx.New(h.sessionManager.GetStorage())
		}
		if broker.AgentSessions == nil {
			broker.AgentSessions = &sessionAgentController{handler: h}
		}
		broker.Background = bgManager
	}
}

func (h *Handler) getBackgroundManager(config *runtimecfg.RuntimeConfig) *background.Manager {
	if h == nil {
		return nil
	}
	cfg := config
	if cfg == nil {
		cfg = h.runtimeConfig
	}
	if cfg == nil {
		return nil
	}
	bgCfg := cfg.Background
	storePath := resolveRuntimeBackgroundStorePath(h.runtimeConfigFile, bgCfg.StorePath)
	storeDSN := strings.TrimSpace(bgCfg.StoreDSN)
	logDir := resolveRuntimeBackgroundLogDir(h.runtimeConfigFile, bgCfg.LogDir)
	key := fmt.Sprintf("%s|%s|%s|%d|%d|%s", storePath, storeDSN, logDir, bgCfg.MaxOutputBytes, bgCfg.MaxConcurrentJobs, bgCfg.DefaultTimeout)

	h.backgroundMu.Lock()
	defer h.backgroundMu.Unlock()
	if h.backgroundManager != nil && h.backgroundConfigKey == key {
		return h.backgroundManager
	}
	manager := background.NewManager(background.Config{
		MaxOutputBytes:    bgCfg.MaxOutputBytes,
		DefaultTimeout:    bgCfg.DefaultTimeout,
		StorePath:         storePath,
		StoreDSN:          storeDSN,
		LogDir:            logDir,
		MaxConcurrentJobs: bgCfg.MaxConcurrentJobs,
		EventHandler:      h.handleBackgroundEvent,
	})
	h.backgroundManager = manager
	h.backgroundConfigKey = key
	return manager
}

func (h *Handler) handleBackgroundEvent(event background.JobEvent) {
	if h == nil {
		return
	}
	eventType := mapBackgroundEventType(event.Type)
	payload := map[string]interface{}{}
	for key, value := range event.Payload {
		payload[key] = value
	}
	if event.JobID != "" {
		payload["job_id"] = event.JobID
	}
	sessionID := ""
	if value, ok := payload["session_id"].(string); ok {
		sessionID = strings.TrimSpace(value)
	}
	if sessionID != "" && event.JobID != "" {
		h.updateSessionActiveJobs(sessionID, event.JobID, event.Type)
	}
	runtimeEvent := runtimeevents.Event{
		Type:      eventType,
		AgentName: "background-manager",
		SessionID: sessionID,
		Payload:   payload,
		Timestamp: event.CreatedAt,
	}
	if store := h.getSessionEventStore(); store != nil && sessionID != "" && eventType != "job_output" {
		_, _ = store.AppendEvent(context.Background(), runtimeEvent)
	}
	h.getRuntimeEventBus().Publish(runtimeEvent)
}

func (h *Handler) updateSessionActiveJobs(sessionID, jobID, eventType string) {
	eventType = strings.ToLower(strings.TrimSpace(eventType))
	switch eventType {
	case "queued", "running", "completed", "failed", "cancelled":
		// handled below
	default:
		return
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(jobID) == "" {
		return
	}
	store := h.getSessionRuntimeStore()
	if store == nil {
		return
	}
	ctx := context.Background()
	state, err := store.LoadState(ctx, sessionID)
	if err != nil || state == nil {
		return
	}

	changed := false
	switch eventType {
	case "queued", "running":
		if !stringSliceContains(state.ActiveJobIDs, jobID) {
			state.ActiveJobIDs = append(state.ActiveJobIDs, jobID)
			changed = true
		}
	case "completed", "failed", "cancelled":
		if filtered, removed := stringSliceRemove(state.ActiveJobIDs, jobID); removed {
			state.ActiveJobIDs = filtered
			changed = true
		}
	}
	if !changed {
		return
	}
	state.UpdatedAt = time.Now().UTC()
	_ = store.SaveState(ctx, state)
}

func stringSliceContains(values []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func stringSliceRemove(values []string, target string) ([]string, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return values, false
	}
	filtered := make([]string, 0, len(values))
	removed := false
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			removed = true
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered, removed
}

func mapBackgroundEventType(eventType string) string {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "queued":
		return "job_queued"
	case "running":
		return "job_started"
	case "output":
		return "job_output"
	case "cancelled":
		return "job_cancelled"
	case "completed", "failed":
		return "job_finished"
	default:
		if strings.TrimSpace(eventType) == "" {
			return "job_event"
		}
		return "job_" + strings.ToLower(strings.TrimSpace(eventType))
	}
}

func defaultReadOnlyDeniedCommands() []string {
	return []string{"sh", "bash", "zsh", "fish", "cmd", "powershell", "pwsh", "python", "python3", "node"}
}

func appendUniqueStrings(existing []string, values ...string) []string {
	seen := make(map[string]bool, len(existing)+len(values))
	result := make([]string, 0, len(existing)+len(values))
	for _, item := range existing {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	for _, item := range values {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	return result
}

func trimLatestUserMessage(messages []types.Message) []types.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			trimmed := make([]types.Message, 0, len(messages)-1)
			trimmed = append(trimmed, messages[:i]...)
			trimmed = append(trimmed, messages[i+1:]...)
			return trimmed
		}
	}
	return messages
}

func shouldUseAgentResult(result *agent.Result, runtime *llm.LLMRuntime) bool {
	if result == nil {
		return false
	}
	if runtime == nil {
		return true
	}
	return result.Skill != "" || result.Success || result.Output != "No matching skill found for the request"
}

func buildAgentResultPayload(source string, result *agent.Result) map[string]interface{} {
	if result == nil {
		return map[string]interface{}{
			"kind":    "agent",
			"source":  source,
			"success": false,
		}
	}

	payload := map[string]interface{}{
		"kind":         "agent",
		"source":       source,
		"success":      result.Success,
		"output":       result.Output,
		"skill":        result.Skill,
		"trace_id":     result.TraceID,
		"steps":        result.Steps,
		"observations": result.Observations,
		"state":        result.State,
		"usage":        result.Usage,
		"duration":     result.Duration,
		"error":        result.Error,
	}
	if toolCalls := observedToolCalls(result.Observations); len(toolCalls) > 0 {
		payload["tool_calls"] = toolCalls
	}
	if subagentSummary := summarizeSubagents(result.Observations); subagentSummary != nil {
		payload["subagent_summary"] = subagentSummary
	}
	if subagentResults := collectSubagentResults(result.Observations); len(subagentResults) > 0 {
		payload["subagent_results"] = subagentResults
	}
	return payload
}

func buildLLMResultPayload(source string, response *llm.LLMResponse) map[string]interface{} {
	if response == nil {
		return map[string]interface{}{
			"kind":    "llm",
			"source":  source,
			"success": false,
		}
	}

	result := map[string]interface{}{
		"kind":       "llm",
		"source":     source,
		"success":    true,
		"output":     response.Content,
		"usage":      response.Usage,
		"model":      response.Model,
		"tool_calls": response.ToolCalls,
		"reasoning":  response.Reasoning,
		"metadata":   response.Metadata,
	}
	if response.ReasoningBlock != nil {
		result["reasoning_block"] = response.ReasoningBlock.ToMap()
	}
	return result
}

func responseResultSource(result interface{}) string {
	if payload, ok := result.(map[string]interface{}); ok {
		if source, ok := payload["source"].(string); ok {
			return source
		}
	}
	return ""
}

func buildOrchestrationPayload(source string, routeAttempted bool, routeCandidates []*skill.RouteResult, agentResult *agent.Result, llmResponse *llm.LLMResponse, fallback string) map[string]interface{} {
	observations := []types.Observation(nil)
	skillName := ""
	steps := 0
	success := false
	model := ""
	toolCalls := 0
	output := ""

	if agentResult != nil {
		observations = agentResult.Observations
		skillName = agentResult.Skill
		steps = agentResult.Steps
		success = agentResult.Success
		output = agentResult.Output
		toolCalls = countObservedToolCalls(observations)
	}
	if skillName == "" && source == "agent_planned_subagents" && len(routeCandidates) > 0 && routeCandidates[0] != nil && routeCandidates[0].Skill != nil {
		skillName = routeCandidates[0].Skill.Name
	}
	if llmResponse != nil {
		model = llmResponse.Model
		toolCalls = len(llmResponse.ToolCalls)
		success = true
		output = llmResponse.Content
	}

	capabilityCandidates := skill.RouteResultsToCapabilityCandidates(routeCandidates)
	selectedCapability := selectCapabilityDescriptor(capabilityCandidates, skillName)

	return map[string]interface{}{
		"source":                source,
		"route_attempted":       routeAttempted,
		"route_matched":         skillName != "",
		"candidate_count":       len(routeCandidates),
		"route_candidates":      summarizeRouteCandidates(routeCandidates, skillName, source),
		"capability_candidates": capabilityCandidates,
		"capability":            selectedCapability,
		"fallback_reason":       fallback,
		"skill":                 skillName,
		"model":                 model,
		"success":               success,
		"steps":                 steps,
		"tool_call_count":       toolCalls,
		"observation_summary":   summarizeObservations(observations),
		"output_preview":        previewText(output, 120),
	}
}

func countObservedToolCalls(observations []types.Observation) int {
	if len(observations) == 0 {
		return 0
	}
	count := 0
	for _, observation := range observations {
		if strings.TrimSpace(observation.Tool) == "" {
			continue
		}
		count++
	}
	return count
}

func observedToolCalls(observations []types.Observation) []map[string]interface{} {
	if len(observations) == 0 {
		return nil
	}

	calls := make([]map[string]interface{}, 0, len(observations))
	for index, observation := range observations {
		toolName := strings.TrimSpace(observation.Tool)
		if toolName == "" {
			continue
		}

		call := map[string]interface{}{
			"id":   observationToolCallID(observation, index),
			"name": toolName,
		}
		if args := cloneObservedToolArguments(observation.Input); len(args) > 0 {
			call["arguments"] = args
		}
		calls = append(calls, call)
	}

	if len(calls) == 0 {
		return nil
	}
	return calls
}

func observationToolCallID(observation types.Observation, index int) string {
	step := strings.TrimSpace(observation.Step)
	if step == "" {
		return fmt.Sprintf("observation_tool_%d", index+1)
	}

	replacer := strings.NewReplacer(" ", "_", ":", "_", "/", "_", "\\", "_")
	return "observation_" + replacer.Replace(step)
}

func cloneObservedToolArguments(input interface{}) map[string]interface{} {
	args, ok := input.(map[string]interface{})
	if !ok || len(args) == 0 {
		return nil
	}

	cloned := make(map[string]interface{}, len(args))
	for key, value := range args {
		cloned[key] = value
	}
	return cloned
}

func selectCapabilityDescriptor(candidates []*capability.Candidate, skillName string) *capability.Descriptor {
	if skillName == "" {
		return nil
	}
	for _, candidate := range candidates {
		if candidate == nil || candidate.Descriptor == nil {
			continue
		}
		if candidate.Descriptor.Name == skillName || candidate.Descriptor.ID == skillName {
			return candidate.Descriptor
		}
	}
	return &capability.Descriptor{
		ID:   skillName,
		Name: skillName,
		Kind: capability.KindSkill,
	}
}

func summarizeRouteCandidates(candidates []*skill.RouteResult, selectedSkill string, source string) []map[string]interface{} {
	if len(candidates) == 0 {
		return []map[string]interface{}{}
	}

	summary := make([]map[string]interface{}, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil || candidate.Skill == nil {
			continue
		}
		chosen := selectedSkill != "" && candidate.Skill.Name == selectedSkill
		selectionReason := "not_selected"
		if chosen {
			selectionReason = "selected"
		} else if source == "llm_fallback" {
			selectionReason = "fallback_to_llm"
		}
		summary = append(summary, map[string]interface{}{
			"skill":            candidate.Skill.Name,
			"score":            candidate.Score,
			"matched_by":       candidate.MatchedBy,
			"details":          candidate.Details,
			"chosen":           chosen,
			"selection_reason": selectionReason,
		})
	}
	return summary
}

func summarizeObservations(observations []types.Observation) map[string]interface{} {
	summary := map[string]interface{}{
		"count":                             len(observations),
		"successful":                        0,
		"failed":                            0,
		"tools":                             []string{},
		"failed_tools":                      []string{},
		"failed_details":                    []map[string]interface{}{},
		"step_durations_ms":                 map[string]int64{},
		"total_duration_ms":                 int64(0),
		"max_duration_ms":                   int64(0),
		"average_duration_ms":               int64(0),
		"subagent_batches":                  0,
		"subagent_count":                    0,
		"subagent_successful":               0,
		"subagent_failed":                   0,
		"subagent_roles":                    []string{},
		"subagent_patch_count":              0,
		"subagent_applied_patch_count":      0,
		"subagent_verified_patch_count":     0,
		"subagent_needs_review_patch_count": 0,
		"subagent_unverified_patch_count":   0,
		"subagent_patch_paths":              []string{},
	}
	if len(observations) == 0 {
		return summary
	}

	tools := make([]string, 0, len(observations))
	failedTools := make([]string, 0)
	failedDetails := make([]map[string]interface{}, 0)
	stepDurations := make(map[string]int64, len(observations))
	subagentRoles := make([]string, 0)
	subagentPatchPaths := make([]string, 0)
	seenRoles := make(map[string]bool)
	seenPatchPaths := make(map[string]bool)
	var totalDuration int64
	var maxDuration int64
	for _, observation := range observations {
		tools = append(tools, observation.Tool)
		durationMS := observation.Duration.GetDuration().Milliseconds()
		stepKey := observation.Step
		if stepKey == "" {
			stepKey = observation.Tool
		}
		stepDurations[stepKey] = durationMS
		totalDuration += durationMS
		if durationMS > maxDuration {
			maxDuration = durationMS
		}
		if observation.Success {
			summary["successful"] = summary["successful"].(int) + 1
		} else {
			summary["failed"] = summary["failed"].(int) + 1
			failedTools = append(failedTools, observation.Tool)
			failedDetails = append(failedDetails, map[string]interface{}{
				"step":        observation.Step,
				"tool":        observation.Tool,
				"error":       observation.Error,
				"duration_ms": durationMS,
			})
		}

		for _, report := range observationSubagentReports(observation) {
			summary["subagent_count"] = summary["subagent_count"].(int) + 1
			if report.Success {
				summary["subagent_successful"] = summary["subagent_successful"].(int) + 1
			} else {
				summary["subagent_failed"] = summary["subagent_failed"].(int) + 1
			}
			if report.Role != "" && !seenRoles[report.Role] {
				seenRoles[report.Role] = true
				subagentRoles = append(subagentRoles, report.Role)
			}
			for _, patch := range report.Patches {
				summary["subagent_patch_count"] = summary["subagent_patch_count"].(int) + 1
				if patchApplyStatus(patch) == "applied" {
					summary["subagent_applied_patch_count"] = summary["subagent_applied_patch_count"].(int) + 1
				}
				switch strings.TrimSpace(patch.VerificationStatus) {
				case "verified":
					summary["subagent_verified_patch_count"] = summary["subagent_verified_patch_count"].(int) + 1
				case "needs_review":
					summary["subagent_needs_review_patch_count"] = summary["subagent_needs_review_patch_count"].(int) + 1
				default:
					summary["subagent_unverified_patch_count"] = summary["subagent_unverified_patch_count"].(int) + 1
				}
				if patch.Path != "" && !seenPatchPaths[patch.Path] {
					seenPatchPaths[patch.Path] = true
					subagentPatchPaths = append(subagentPatchPaths, patch.Path)
				}
			}
		}
		if _, ok := observation.GetMetric("subagent_reports"); ok {
			summary["subagent_batches"] = summary["subagent_batches"].(int) + 1
		}
	}
	summary["tools"] = tools
	summary["failed_tools"] = failedTools
	summary["failed_details"] = failedDetails
	summary["step_durations_ms"] = stepDurations
	summary["total_duration_ms"] = totalDuration
	summary["max_duration_ms"] = maxDuration
	summary["average_duration_ms"] = totalDuration / int64(len(observations))
	summary["subagent_roles"] = subagentRoles
	summary["subagent_patch_paths"] = subagentPatchPaths
	return summary
}

func summarizeSubagents(observations []types.Observation) map[string]interface{} {
	observationSummary := summarizeObservations(observations)
	count, _ := observationSummary["subagent_count"].(int)
	if count == 0 {
		return nil
	}
	return map[string]interface{}{
		"batches":                  observationSummary["subagent_batches"],
		"count":                    observationSummary["subagent_count"],
		"successful":               observationSummary["subagent_successful"],
		"failed":                   observationSummary["subagent_failed"],
		"roles":                    observationSummary["subagent_roles"],
		"patch_count":              observationSummary["subagent_patch_count"],
		"applied_patch_count":      observationSummary["subagent_applied_patch_count"],
		"verified_patch_count":     observationSummary["subagent_verified_patch_count"],
		"needs_review_patch_count": observationSummary["subagent_needs_review_patch_count"],
		"unverified_patch_count":   observationSummary["subagent_unverified_patch_count"],
		"patch_paths":              observationSummary["subagent_patch_paths"],
	}
}

func collectSubagentResults(observations []types.Observation) []agent.SubagentResult {
	results := make([]agent.SubagentResult, 0)
	seen := make(map[string]bool)
	for _, observation := range observations {
		for _, report := range observationSubagentReports(observation) {
			key := firstNonEmptyString(report.SessionID, report.ID)
			if key != "" && seen[key] {
				continue
			}
			if key != "" {
				seen[key] = true
			}
			results = append(results, report)
		}
	}
	return results
}

func observationSubagentReports(observation types.Observation) []agent.SubagentResult {
	value, ok := observation.GetMetric("subagent_reports")
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []agent.SubagentResult:
		return typed
	case []interface{}:
		reports := make([]agent.SubagentResult, 0, len(typed))
		for _, item := range typed {
			reportMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			reports = append(reports, agent.SubagentResult{
				ID:      stringMapValueAny(reportMap, "id"),
				Role:    stringMapValueAny(reportMap, "role"),
				Success: boolMapValueAny(reportMap, "success"),
				Patches: filePatchesFromAny(reportMap["patches"]),
				Error:   stringMapValueAny(reportMap, "error"),
			})
		}
		return reports
	default:
		return nil
	}
}

func stringMapValueAny(values map[string]interface{}, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func boolMapValueAny(values map[string]interface{}, key string) bool {
	if len(values) == 0 {
		return false
	}
	value, _ := values[key].(bool)
	return value
}

func intMapValueAny(values map[string]interface{}, key string) int {
	if len(values) == 0 {
		return 0
	}
	switch value := values[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func stringSliceValueAny(value interface{}) []string {
	switch items := value.(type) {
	case []string:
		values := make([]string, 0, len(items))
		for _, item := range items {
			text := strings.TrimSpace(item)
			if text == "" {
				continue
			}
			values = append(values, text)
		}
		return values
	case []interface{}:
		values := make([]string, 0, len(items))
		for _, item := range items {
			text, _ := item.(string)
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			values = append(values, text)
		}
		return values
	default:
		return nil
	}
}

func filePatchesFromAny(value interface{}) []agent.FilePatch {
	items, ok := value.([]interface{})
	if !ok {
		return nil
	}
	patches := make([]agent.FilePatch, 0, len(items))
	for _, item := range items {
		patchMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		patch := agent.FilePatch{
			Path:               stringMapValueAny(patchMap, "path"),
			Summary:            stringMapValueAny(patchMap, "summary"),
			Diff:               stringMapValueAny(patchMap, "diff"),
			ApplyStatus:        stringMapValueAny(patchMap, "apply_status"),
			AppliedBy:          stringSliceValueAny(patchMap["applied_by"]),
			ArtifactRefs:       stringSliceValueAny(patchMap["artifact_refs"]),
			VerificationStatus: stringMapValueAny(patchMap, "verification_status"),
			VerifiedBy:         stringSliceValueAny(patchMap["verified_by"]),
		}
		patches = append(patches, patch)
	}
	return patches
}

func patchApplyStatus(patch agent.FilePatch) string {
	status := strings.TrimSpace(patch.ApplyStatus)
	if status == "" {
		return "applied"
	}
	return status
}

func previewText(text string, maxLen int) string {
	if maxLen <= 0 || len(text) <= maxLen {
		return text
	}
	if maxLen <= 3 {
		return text[:maxLen]
	}
	return text[:maxLen-3] + "..."
}

func fallbackReason(routeAttempted bool) string {
	if routeAttempted {
		return "no_matching_skill"
	}
	return "route_disabled"
}

func buildPlanningPayload(result *agent.OrchestrationResult) map[string]interface{} {
	if result == nil || (!result.PlanningAttempted && result.Plan == nil) {
		return nil
	}

	payload := map[string]interface{}{
		"mode":                              string(result.Mode),
		"attempted":                         result.PlanningAttempted,
		"planning_source":                   result.PlanningSource,
		"planning_error":                    result.PlanningError,
		"step_count":                        0,
		"subagent_task_count":               0,
		"subagent_execution_requested":      result.SubagentExecutionRequested,
		"subagent_execution_eligible":       result.SubagentExecutionEligible,
		"subagent_execution_blocked_reason": result.SubagentExecutionBlockedReason,
		"subagent_execution_attempted":      result.SubagentExecutionAttempted,
		"subagent_execution_error":          result.SubagentExecutionError,
		"patch_decision":                    result.PatchDecision,
		"patch_decision_reason":             result.PatchDecisionReason,
		"patch_decision_required":           result.PatchDecisionRequired,
		"patch_decision_policy":             result.PatchDecisionPolicy,
		"patch_decision_override_applied":   result.PatchDecisionOverrideApplied,
		"patch_approval":                    result.PatchApproval,
		"subagent_result_count":             len(result.SubagentResults),
		"subagent_patch_count":              0,
		"subagent_applied_patch_count":      0,
		"subagent_verified_patch_count":     0,
		"subagent_needs_review_patch_count": 0,
		"subagent_unverified_patch_count":   0,
		"goal":                              "",
		"steps":                             []map[string]interface{}{},
		"subagent_tasks":                    []agent.SubagentTask{},
	}
	if patchCount, appliedCount, verifiedCount, needsReviewCount, unverifiedCount := subagentPatchStats(result.SubagentResults); patchCount > 0 {
		payload["subagent_patch_count"] = patchCount
		payload["subagent_applied_patch_count"] = appliedCount
		payload["subagent_verified_patch_count"] = verifiedCount
		payload["subagent_needs_review_patch_count"] = needsReviewCount
		payload["subagent_unverified_patch_count"] = unverifiedCount
	}
	if result.Plan == nil {
		if len(result.SubagentTasks) > 0 {
			payload["subagent_tasks"] = result.SubagentTasks
			payload["subagent_task_count"] = len(result.SubagentTasks)
		}
		return payload
	}

	payload["goal"] = result.Plan.Goal
	payload["step_count"] = len(result.Plan.Steps)
	steps := make([]map[string]interface{}, 0, len(result.Plan.Steps))
	for _, step := range result.Plan.Steps {
		steps = append(steps, map[string]interface{}{
			"id":          step.ID,
			"description": step.Description,
			"tool":        step.Tool,
			"depends_on":  step.DependsOn,
			"priority":    step.Priority,
		})
	}
	payload["steps"] = steps
	if len(result.SubagentTasks) > 0 {
		payload["subagent_tasks"] = result.SubagentTasks
		payload["subagent_task_count"] = len(result.SubagentTasks)
	}
	return payload
}

func subagentPatchStats(results []agent.SubagentResult) (int, int, int, int, int) {
	total := 0
	applied := 0
	verified := 0
	needsReview := 0
	unverified := 0
	for _, result := range results {
		for _, patch := range result.Patches {
			total++
			if patchApplyStatus(patch) == "applied" {
				applied++
			}
			switch strings.TrimSpace(patch.VerificationStatus) {
			case "verified":
				verified++
			case "needs_review":
				needsReview++
			default:
				unverified++
			}
		}
	}
	return total, applied, verified, needsReview, unverified
}

func (h *Handler) buildWorkspaceContext(path, query string, config *runtimecfg.RuntimeConfig) (*workspace.WorkspaceContext, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}

	scanner := workspace.NewScanner(workspaceConfigFromRuntime(config))
	scan, err := scanner.Scan(path)
	if err != nil {
		return nil, errors.New(errors.ErrValidationFailed,
			fmt.Sprintf("failed to scan workspace path: %s", path))
	}

	builder := workspace.NewContextBuilder(scan, nil)
	return builder.Build(query), nil
}

func workspaceConfigFromRuntime(config *runtimecfg.RuntimeConfig) *workspace.WorkspaceConfig {
	if config == nil {
		return nil
	}
	ws := config.Workspace
	cfg := workspace.DefaultWorkspaceConfig()
	if ws.MaxFileSize > 0 {
		cfg.MaxFileSize = ws.MaxFileSize
	}
	if ws.MaxChunkSize > 0 {
		cfg.MaxChunkSize = ws.MaxChunkSize
	}
	if ws.ChunkOverlap > 0 {
		cfg.ChunkOverlap = ws.ChunkOverlap
	}
	if len(ws.Include) > 0 {
		cfg.IncludePatterns = append([]string(nil), ws.Include...)
	}
	if len(ws.Exclude) > 0 {
		cfg.ExcludePatterns = append([]string(nil), ws.Exclude...)
	}
	return cfg
}

func (h *Handler) routeCandidates(ctx context.Context, prompt string) []*skill.RouteResult {
	return routeCandidatesWithRuntime(ctx, prompt, h.skillRegistry, h.embeddingRouter)
}

func routeCandidatesWithRuntime(ctx context.Context, prompt string, registry *skill.Registry, embeddingRouter *skill.SemanticEmbeddingRouter) []*skill.RouteResult {
	if registry == nil {
		return nil
	}
	router := skill.NewRouter(registry)
	if embeddingRouter != nil {
		if handlerEmbeddingRouter, err := embeddingRouter.CloneForRegistry(registry); err == nil {
			router.SetEmbeddingRouter(handlerEmbeddingRouter)
		}
	}
	return router.Route(ctx, prompt)
}

func (h *Handler) searchSkillMatches(ctx context.Context, query, category string, mode searchMode) ([]*skill.RouteResult, searchMode) {
	lexicalMatches := h.lexicalSearchMatches(ctx, query, category)
	semanticMatches := h.semanticSearchMatches(ctx, query, category)

	switch mode {
	case searchModeLexical:
		return lexicalMatches, searchModeLexical
	case searchModeSemantic:
		return semanticMatches, searchModeSemantic
	case searchModeHybrid:
		return mergeSearchMatches(lexicalMatches, semanticMatches), searchModeHybrid
	case searchModeAuto:
		if len(lexicalMatches) > 0 {
			return lexicalMatches, searchModeLexical
		}
		if len(semanticMatches) > 0 {
			return semanticMatches, searchModeSemantic
		}
		return lexicalMatches, searchModeAuto
	default:
		return lexicalMatches, searchModeLexical
	}
}

func (h *Handler) lexicalSearchMatches(ctx context.Context, query, category string) []*skill.RouteResult {
	if h.skillRegistry == nil {
		return nil
	}

	router := skill.NewRouter(h.skillRegistry)
	return filterRouteResultsByCategory(router.Route(ctx, query), category)
}

func (h *Handler) semanticSearchMatches(ctx context.Context, query, category string) []*skill.RouteResult {
	if h.embeddingRouter == nil {
		return nil
	}

	results, err := h.embeddingRouter.Route(ctx, query)
	if err != nil {
		return nil
	}
	return filterRouteResultsByCategory(results, category)
}

func filterRouteResultsByCategory(results []*skill.RouteResult, category string) []*skill.RouteResult {
	if category == "" {
		return results
	}

	filtered := make([]*skill.RouteResult, 0, len(results))
	for _, result := range results {
		if result == nil || result.Skill == nil {
			continue
		}
		if strings.EqualFold(result.Skill.Category, category) {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func filterRouteResultsBySource(results []*skill.RouteResult, layer, dir string) []*skill.RouteResult {
	if layer == "" && dir == "" {
		return results
	}

	filtered := make([]*skill.RouteResult, 0, len(results))
	for _, result := range results {
		if result == nil || result.Skill == nil {
			continue
		}
		if matchesSkillSource(result.Skill, layer, dir) {
			filtered = append(filtered, result)
		}
	}
	return filtered
}

func mergeSearchMatches(groups ...[]*skill.RouteResult) []*skill.RouteResult {
	bestBySkill := make(map[string]*skill.RouteResult)
	order := make([]string, 0)

	for _, group := range groups {
		for _, match := range group {
			if match == nil || match.Skill == nil {
				continue
			}

			name := match.Skill.Name
			existing, exists := bestBySkill[name]
			if !exists {
				bestBySkill[name] = match
				order = append(order, name)
				continue
			}

			if match.Score > existing.Score || (match.Score == existing.Score && existing.MatchedBy == "embedding" && match.MatchedBy != "embedding") {
				bestBySkill[name] = match
			}
		}
	}

	merged := make([]*skill.RouteResult, 0, len(order))
	for _, name := range order {
		merged = append(merged, bestBySkill[name])
	}
	return merged
}

func extractSkillsFromMatches(matches []*skill.RouteResult) []*skill.Skill {
	results := make([]*skill.Skill, 0, len(matches))
	for _, match := range matches {
		if match == nil || match.Skill == nil {
			continue
		}
		results = append(results, match.Skill)
	}
	return results
}

func serializeSearchMatches(matches []*skill.RouteResult) []map[string]interface{} {
	serialized := make([]map[string]interface{}, 0, len(matches))
	for _, match := range matches {
		if match == nil || match.Skill == nil {
			continue
		}

		serialized = append(serialized, map[string]interface{}{
			"skill":      match.Skill,
			"score":      match.Score,
			"matched_by": match.MatchedBy,
			"details":    match.Details,
		})
	}
	return serialized
}

func searchUsesEmbedding(matches []*skill.RouteResult) bool {
	for _, match := range matches {
		if match != nil && match.MatchedBy == "embedding" {
			return true
		}
	}
	return false
}

func parseSearchMode(r *http.Request) searchMode {
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode == "" {
		if semantic, err := strconv.ParseBool(r.URL.Query().Get("semantic")); err == nil && semantic {
			return searchModeSemantic
		}
		return searchModeAuto
	}

	switch searchMode(mode) {
	case searchModeAuto, searchModeLexical, searchModeSemantic, searchModeHybrid:
		return searchMode(mode)
	default:
		return searchModeAuto
	}
}

func (h *Handler) recordSearchTelemetry(query string, requestedMode, resolvedMode searchMode, resultCount int, usedEmbedding bool) {
	if h.searchTelemetry == nil {
		return
	}

	h.searchTelemetry.mu.Lock()
	defer h.searchTelemetry.mu.Unlock()

	h.searchTelemetry.totalRequests++
	h.searchTelemetry.totalResults += resultCount
	if usedEmbedding {
		h.searchTelemetry.embeddingRequests++
	}
	h.searchTelemetry.lastQuery = query
	h.searchTelemetry.lastRequestedMode = string(requestedMode)
	h.searchTelemetry.lastResolvedMode = string(resolvedMode)
	h.searchTelemetry.lastResultCount = resultCount
	h.searchTelemetry.lastEmbeddingUsed = usedEmbedding
	h.searchTelemetry.requestedModeCount[string(requestedMode)]++
	h.searchTelemetry.resolvedModeCount[string(resolvedMode)]++
}

func (h *Handler) recordSearchReindex(status string) {
	if h.searchTelemetry == nil {
		return
	}

	h.searchTelemetry.mu.Lock()
	defer h.searchTelemetry.mu.Unlock()

	h.searchTelemetry.reindexCount++
	h.searchTelemetry.lastReindexStatus = status
	h.searchTelemetry.lastReindexAt = time.Now()
}

func (h *Handler) authorizeSearchAdmin(r *http.Request) error {
	if h.hasValidSearchAdminToken(r) || h.hasTrustedAdminRole(r) || isLoopbackRequest(r) {
		return nil
	}
	return errors.New(errors.ErrAgentPermission, "search admin endpoints require loopback access, valid admin token, or admin role")
}

func (h *Handler) authorizeUsageAdmin(r *http.Request) error {
	if h.hasValidSearchAdminToken(r) || h.hasTrustedAdminRole(r) || isLoopbackRequest(r) {
		return nil
	}
	return errors.New(errors.ErrAgentPermission, "usage admin endpoints require loopback access, valid admin token, or admin role")
}

func (h *Handler) authorizeSkillMutation(r *http.Request) error {
	if h.hasValidSearchAdminToken(r) || h.hasTrustedAdminRole(r) || isLoopbackRequest(r) {
		return nil
	}
	return errors.New(errors.ErrAgentPermission, "skill mutation endpoints require loopback access, valid admin token, or admin role")
}

func (h *Handler) enforceMutationActionPolicy(action string, r *http.Request) error {
	policy := h.getMutationPolicy()
	if policy.ReadOnly {
		switch action {
		case skillMutationActionCreate, skillMutationActionUpdate, skillMutationActionDelete, skillMutationActionBatchCreate, skillMutationActionImport:
			return errors.New(errors.ErrAgentPermission, "skills runtime is read-only")
		}
	}

	if policy.DisableImport && action == skillMutationActionImport {
		return errors.New(errors.ErrAgentPermission, "skill import is disabled by policy")
	}
	if policy.DisableReloadOps && action == skillMutationActionReload {
		return errors.New(errors.ErrAgentPermission, "skill reload is disabled by policy")
	}
	if policy.DisableHotReload {
		switch action {
		case skillMutationActionHotReloadStart, skillMutationActionHotReloadStop, skillMutationActionHotReloadRun:
			return errors.New(errors.ErrAgentPermission, "skill hot reload is disabled by policy")
		}
	}
	return nil
}

func (h *Handler) enforcePersistPolicy(existingSkill *skill.Skill, r *http.Request) error {
	if !h.getMutationPolicy().DisablePersist {
		return nil
	}
	if shouldPersistSkill(r) || shouldPersistUpdatedSkill(existingSkill, r) {
		return errors.New(errors.ErrAgentPermission, "skill persistence is disabled by policy")
	}
	return nil
}

func (h *Handler) enforceDeleteFilePolicy(r *http.Request) error {
	if !h.getMutationPolicy().DisablePersist {
		return nil
	}
	if shouldDeleteSkillFile(r) {
		return errors.New(errors.ErrAgentPermission, "skill file deletion is disabled by policy")
	}
	return nil
}

func (h *Handler) searchAdminAccessMode(r *http.Request) string {
	switch {
	case h.hasValidSearchAdminToken(r):
		return "token"
	case h.hasTrustedAdminRole(r):
		return "role"
	case isLoopbackRequest(r):
		return "loopback"
	default:
		return "denied"
	}
}

func (h *Handler) skillMutationAccessMode(r *http.Request) string {
	switch {
	case h.hasValidSearchAdminToken(r):
		return "token"
	case h.hasTrustedAdminRole(r):
		return "role"
	case isLoopbackRequest(r):
		return "loopback"
	default:
		return "denied"
	}
}

func (h *Handler) mutationPolicySnapshot() map[string]interface{} {
	policy := h.getMutationPolicy()
	return map[string]interface{}{
		"read_only":          policy.ReadOnly,
		"disable_import":     policy.DisableImport,
		"disable_persist":    policy.DisablePersist,
		"disable_reload_ops": policy.DisableReloadOps,
		"disable_hot_reload": policy.DisableHotReload,
	}
}

func (h *Handler) usagePolicySnapshot() map[string]interface{} {
	policy := h.getUsagePolicy()
	return map[string]interface{}{
		"tracking_enabled":     policy.TrackingEnabled,
		"ledger_enabled":       h.usageLedgerStore != nil,
		"quota_enabled":        policy.QuotaEnabled,
		"default_max_requests": policy.DefaultMaxRequests,
		"default_max_tokens":   policy.DefaultMaxTokens,
		"tenant_quota_count":   len(policy.TenantQuotas),
		"project_quota_count":  len(policy.ProjectQuotas),
		"user_quota_count":     len(policy.UserQuotas),
	}
}

func (h *Handler) usagePolicyDetailedSnapshot() map[string]interface{} {
	policy := h.getUsagePolicy()
	return map[string]interface{}{
		"tracking_enabled":     policy.TrackingEnabled,
		"ledger_enabled":       h.usageLedgerStore != nil,
		"quota_enabled":        policy.QuotaEnabled,
		"default_max_requests": policy.DefaultMaxRequests,
		"default_max_tokens":   policy.DefaultMaxTokens,
		"tenants":              serializeUsageQuotaLimits(policy.TenantQuotas),
		"projects":             serializeUsageQuotaLimits(policy.ProjectQuotas),
		"users":                serializeUsageQuotaLimits(policy.UserQuotas),
	}
}

func (h *Handler) scopeResolverPolicySnapshot() map[string]interface{} {
	return serializeScopeResolverPolicy(h.getScopeResolverConfig())
}

func serializeScopeResolverPolicy(config ScopeResolverConfig) map[string]interface{} {
	return map[string]interface{}{
		"enabled":               config.Enabled,
		"jwt_claims_enabled":    config.JWTClaimsEnabled,
		"jwt_secret_configured": strings.TrimSpace(config.JWTSecret) != "",
		"tenant_headers":        append([]string(nil), config.TenantHeaders...),
		"project_headers":       append([]string(nil), config.ProjectHeaders...),
		"user_headers":          append([]string(nil), config.UserHeaders...),
		"role_headers":          append([]string(nil), config.RoleHeaders...),
		"tenant_claims":         append([]string(nil), config.TenantClaims...),
		"project_claims":        append([]string(nil), config.ProjectClaims...),
		"user_claims":           append([]string(nil), config.UserClaims...),
		"role_claims":           append([]string(nil), config.RoleClaims...),
		"admin_roles":           append([]string(nil), config.AdminRoles...),
		"api_key_scope_count":   len(config.APIKeyScopes),
	}
}

func (h *Handler) getUsagePolicy() UsagePolicy {
	h.usagePolicyMu.RLock()
	defer h.usagePolicyMu.RUnlock()
	return cloneUsagePolicy(h.usagePolicy)
}

func (h *Handler) getMutationPolicy() MutationPolicy {
	h.mutationPolicyMu.RLock()
	defer h.mutationPolicyMu.RUnlock()
	return h.mutationPolicy
}

func (h *Handler) updateMutationPolicy(update mutationPolicyUpdateRequest) MutationPolicy {
	h.mutationPolicyMu.Lock()
	defer h.mutationPolicyMu.Unlock()

	current := h.mutationPolicy
	if update.ReadOnly != nil {
		current.ReadOnly = *update.ReadOnly
	}
	if update.DisableImport != nil {
		current.DisableImport = *update.DisableImport
	}
	if update.DisablePersist != nil {
		current.DisablePersist = *update.DisablePersist
	}
	if update.DisableReloadOps != nil {
		current.DisableReloadOps = *update.DisableReloadOps
	}
	if update.DisableHotReload != nil {
		current.DisableHotReload = *update.DisableHotReload
	}
	h.mutationPolicy = current
	return current
}

func (h *Handler) getScopeResolverConfig() ScopeResolverConfig {
	h.scopeResolverMu.RLock()
	defer h.scopeResolverMu.RUnlock()
	return cloneScopeResolverConfig(h.scopeResolverConfig)
}

func cloneUsagePolicy(policy UsagePolicy) UsagePolicy {
	cloned := policy
	cloned.TenantQuotas = cloneUsageQuotaMap(policy.TenantQuotas)
	cloned.ProjectQuotas = cloneUsageQuotaMap(policy.ProjectQuotas)
	cloned.UserQuotas = cloneUsageQuotaMap(policy.UserQuotas)
	return cloned
}

func cloneUsageQuotaMap(source map[string]UsageQuotaLimit) map[string]UsageQuotaLimit {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]UsageQuotaLimit, len(source))
	for key, value := range source {
		cloned[key] = UsageQuotaLimit{
			MaxRequests: cloneIntPointer(value.MaxRequests),
			MaxTokens:   cloneIntPointer(value.MaxTokens),
		}
	}
	return cloned
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneUsageScopeMap(source map[string]UsageScope) map[string]UsageScope {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]UsageScope, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneScopeResolverConfig(config ScopeResolverConfig) ScopeResolverConfig {
	return ScopeResolverConfig{
		Enabled:          config.Enabled,
		TenantHeaders:    append([]string(nil), config.TenantHeaders...),
		ProjectHeaders:   append([]string(nil), config.ProjectHeaders...),
		UserHeaders:      append([]string(nil), config.UserHeaders...),
		RoleHeaders:      append([]string(nil), config.RoleHeaders...),
		JWTClaimsEnabled: config.JWTClaimsEnabled,
		JWTSecret:        strings.TrimSpace(config.JWTSecret),
		TenantClaims:     append([]string(nil), config.TenantClaims...),
		ProjectClaims:    append([]string(nil), config.ProjectClaims...),
		UserClaims:       append([]string(nil), config.UserClaims...),
		RoleClaims:       append([]string(nil), config.RoleClaims...),
		AdminRoles:       append([]string(nil), config.AdminRoles...),
		APIKeyScopes:     cloneUsageScopeMap(config.APIKeyScopes),
	}
}

func serializeUsageQuotaLimits(source map[string]UsageQuotaLimit) map[string]map[string]interface{} {
	if len(source) == 0 {
		return map[string]map[string]interface{}{}
	}
	serialized := make(map[string]map[string]interface{}, len(source))
	for key, value := range source {
		item := map[string]interface{}{}
		if value.MaxRequests != nil {
			item["max_requests"] = *value.MaxRequests
		}
		if value.MaxTokens != nil {
			item["max_tokens"] = *value.MaxTokens
		}
		serialized[key] = item
	}
	return serialized
}

func normalizeUsageQuotaLimit(limit UsageQuotaLimit) UsageQuotaLimit {
	return UsageQuotaLimit{
		MaxRequests: cloneIntPointer(limit.MaxRequests),
		MaxTokens:   cloneIntPointer(limit.MaxTokens),
	}
}

func (h *Handler) updateUsagePolicy(update usagePolicyUpdateRequest) UsagePolicy {
	h.usagePolicyMu.Lock()
	defer h.usagePolicyMu.Unlock()

	current := cloneUsagePolicy(h.usagePolicy)
	if update.Replace {
		current.TenantQuotas = nil
		current.ProjectQuotas = nil
		current.UserQuotas = nil
	}
	if update.TrackingEnabled != nil {
		current.TrackingEnabled = *update.TrackingEnabled
	}
	if update.QuotaEnabled != nil {
		current.QuotaEnabled = *update.QuotaEnabled
	}
	if update.DefaultMaxRequests != nil {
		current.DefaultMaxRequests = *update.DefaultMaxRequests
	}
	if update.DefaultMaxTokens != nil {
		current.DefaultMaxTokens = *update.DefaultMaxTokens
	}

	mergeUsageQuotaMap(&current.TenantQuotas, update.Tenants, update.Replace)
	mergeUsageQuotaMap(&current.ProjectQuotas, update.Projects, update.Replace)
	mergeUsageQuotaMap(&current.UserQuotas, update.Users, update.Replace)

	if current.QuotaEnabled {
		current.TrackingEnabled = true
	}
	h.usagePolicy = current
	return cloneUsagePolicy(current)
}

func (h *Handler) updateScopeResolverConfig(update authPolicyUpdateRequest) ScopeResolverConfig {
	h.scopeResolverMu.Lock()
	defer h.scopeResolverMu.Unlock()

	current := cloneScopeResolverConfig(h.scopeResolverConfig)
	if update.Replace {
		current.TenantHeaders = nil
		current.ProjectHeaders = nil
		current.UserHeaders = nil
		current.RoleHeaders = nil
		current.TenantClaims = nil
		current.ProjectClaims = nil
		current.UserClaims = nil
		current.RoleClaims = nil
		current.AdminRoles = nil
		current.APIKeyScopes = nil
	}
	if update.Enabled != nil {
		current.Enabled = *update.Enabled
	}
	if update.JWTClaimsEnabled != nil {
		current.JWTClaimsEnabled = *update.JWTClaimsEnabled
	}
	mergeOrReplaceStringList(&current.TenantHeaders, update.TenantHeaders, update.Replace, false)
	mergeOrReplaceStringList(&current.ProjectHeaders, update.ProjectHeaders, update.Replace, false)
	mergeOrReplaceStringList(&current.UserHeaders, update.UserHeaders, update.Replace, false)
	mergeOrReplaceStringList(&current.RoleHeaders, update.RoleHeaders, update.Replace, false)
	mergeOrReplaceStringList(&current.TenantClaims, update.TenantClaims, update.Replace, false)
	mergeOrReplaceStringList(&current.ProjectClaims, update.ProjectClaims, update.Replace, false)
	mergeOrReplaceStringList(&current.UserClaims, update.UserClaims, update.Replace, false)
	mergeOrReplaceStringList(&current.RoleClaims, update.RoleClaims, update.Replace, false)
	mergeOrReplaceStringList(&current.AdminRoles, update.AdminRoles, update.Replace, true)
	mergeOrReplaceScopeBindings(&current.APIKeyScopes, update.APIKeyScopes, update.Replace)

	current.AdminRoles = normalizeAdminRoles(current.AdminRoles)
	h.scopeResolverConfig = current
	return cloneScopeResolverConfig(current)
}

func (h *Handler) deleteAuthPolicyEntry(field, key string) (ScopeResolverConfig, bool) {
	h.scopeResolverMu.Lock()
	defer h.scopeResolverMu.Unlock()

	current := cloneScopeResolverConfig(h.scopeResolverConfig)
	removed := false
	switch field {
	case "api_key_scope":
		if current.APIKeyScopes != nil {
			if _, ok := current.APIKeyScopes[key]; ok {
				delete(current.APIKeyScopes, key)
				removed = true
			}
		}
	case "admin_role":
		target := strings.ToLower(strings.TrimSpace(key))
		filtered := make([]string, 0, len(current.AdminRoles))
		for _, role := range current.AdminRoles {
			if role == target {
				removed = true
				continue
			}
			filtered = append(filtered, role)
		}
		if removed {
			current.AdminRoles = filtered
		}
	}
	if removed {
		h.scopeResolverConfig = current
	}
	return cloneScopeResolverConfig(current), removed
}

func mergeUsageQuotaMap(target *map[string]UsageQuotaLimit, incoming map[string]UsageQuotaLimit, replace bool) {
	if replace && len(incoming) == 0 {
		*target = nil
		return
	}
	if len(incoming) == 0 {
		return
	}
	if *target == nil {
		*target = make(map[string]UsageQuotaLimit)
	}
	for key, value := range incoming {
		(*target)[key] = normalizeUsageQuotaLimit(value)
	}
}

func mergeOrReplaceStringList(target *[]string, incoming []string, replace bool, normalizeRoles bool) {
	if replace && len(incoming) == 0 {
		*target = nil
		return
	}
	if len(incoming) == 0 {
		return
	}
	values := append([]string(nil), incoming...)
	if normalizeRoles {
		values = normalizeRoleValues(values)
	} else {
		values = normalizeStringList(values)
	}
	if replace {
		*target = values
		return
	}
	combined := append(append([]string(nil), *target...), values...)
	if normalizeRoles {
		*target = uniqueStrings(normalizeRoleValues(combined))
		return
	}
	*target = uniqueStrings(normalizeStringList(combined))
}

func mergeOrReplaceScopeBindings(target *map[string]UsageScope, incoming map[string]UsageScope, replace bool) {
	if replace && len(incoming) == 0 {
		*target = nil
		return
	}
	if len(incoming) == 0 {
		return
	}
	if replace || *target == nil {
		*target = make(map[string]UsageScope, len(incoming))
	} else if *target == nil {
		*target = make(map[string]UsageScope)
	}
	for key, value := range incoming {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		(*target)[key] = UsageScope{
			TenantID:  strings.TrimSpace(value.TenantID),
			ProjectID: strings.TrimSpace(value.ProjectID),
			UserID:    strings.TrimSpace(value.UserID),
		}
	}
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (h *Handler) deleteUsagePolicyEntry(level, key string) (UsagePolicy, bool) {
	h.usagePolicyMu.Lock()
	defer h.usagePolicyMu.Unlock()

	removed := false
	switch level {
	case "tenant":
		if h.usagePolicy.TenantQuotas != nil {
			if _, ok := h.usagePolicy.TenantQuotas[key]; ok {
				delete(h.usagePolicy.TenantQuotas, key)
				removed = true
			}
		}
	case "project":
		if h.usagePolicy.ProjectQuotas != nil {
			if _, ok := h.usagePolicy.ProjectQuotas[key]; ok {
				delete(h.usagePolicy.ProjectQuotas, key)
				removed = true
			}
		}
	case "user":
		if h.usagePolicy.UserQuotas != nil {
			if _, ok := h.usagePolicy.UserQuotas[key]; ok {
				delete(h.usagePolicy.UserQuotas, key)
				removed = true
			}
		}
	}

	return cloneUsagePolicy(h.usagePolicy), removed
}

func (h *Handler) resolveUsageScope(r *http.Request, tenantID, projectID, userID string) UsageScope {
	config := h.getScopeResolverConfig()
	jwtScope := h.resolveScopeFromJWTClaims(r)
	apiKeyScope := h.resolveScopeFromAPIKey(r)
	resolvedUserID := h.resolveUsageScopeValue(userID, r, "user_id", config.UserHeaders, jwtScope.UserID, apiKeyScope.UserID)
	return normalizeUsageScope(UsageScope{
		TenantID:  h.resolveUsageScopeValue(tenantID, r, "tenant_id", config.TenantHeaders, jwtScope.TenantID, apiKeyScope.TenantID),
		ProjectID: h.resolveUsageScopeValue(projectID, r, "project_id", config.ProjectHeaders, jwtScope.ProjectID, apiKeyScope.ProjectID),
		UserID:    h.resolveServerSessionUserID(resolvedUserID),
	})
}

func (h *Handler) resolveUsageScopeFilter(r *http.Request, tenantID, projectID, userID string) (UsageScope, bool) {
	config := h.getScopeResolverConfig()
	jwtScope := h.resolveScopeFromJWTClaims(r)
	rawTenant := h.resolveUsageScopeValue(tenantID, r, "tenant_id", config.TenantHeaders, jwtScope.TenantID)
	rawProject := h.resolveUsageScopeValue(projectID, r, "project_id", config.ProjectHeaders, jwtScope.ProjectID)
	rawUser := h.resolveUsageScopeValue(userID, r, "user_id", config.UserHeaders, jwtScope.UserID)
	if strings.TrimSpace(rawTenant) == "" && strings.TrimSpace(rawProject) == "" && strings.TrimSpace(rawUser) == "" {
		return UsageScope{}, false
	}
	return normalizeUsageScope(UsageScope{
		TenantID:  rawTenant,
		ProjectID: rawProject,
		UserID:    rawUser,
	}), true
}

func (h *Handler) resolveUsageScopeValue(primary string, r *http.Request, queryKey string, headerKeys []string, fallbacks ...string) string {
	if value := strings.TrimSpace(primary); value != "" {
		return value
	}
	if r == nil {
		return firstNonEmptyString(fallbacks...)
	}
	if value := strings.TrimSpace(r.URL.Query().Get(queryKey)); value != "" {
		return value
	}
	if h.getScopeResolverConfig().Enabled {
		for _, headerKey := range headerKeys {
			if value := strings.TrimSpace(r.Header.Get(headerKey)); value != "" {
				return value
			}
		}
	}
	return firstNonEmptyString(fallbacks...)
}

func (h *Handler) resolveScopeFromAPIKey(r *http.Request) UsageScope {
	config := h.getScopeResolverConfig()
	if !config.Enabled || r == nil || len(config.APIKeyScopes) == 0 {
		return UsageScope{}
	}
	if apiKey := presentedAPIKey(r); apiKey != "" {
		if scope, ok := config.APIKeyScopes[apiKey]; ok {
			return scope
		}
	}
	return UsageScope{}
}

func (h *Handler) resolveScopeFromJWTClaims(r *http.Request) UsageScope {
	config := h.getScopeResolverConfig()
	if !config.Enabled || !config.JWTClaimsEnabled || r == nil {
		return UsageScope{}
	}
	claims, ok := h.parseJWTClaims(r)
	if !ok {
		return UsageScope{}
	}
	return UsageScope{
		TenantID:  firstClaimValue(claims, config.TenantClaims...),
		ProjectID: firstClaimValue(claims, config.ProjectClaims...),
		UserID:    firstClaimValue(claims, config.UserClaims...),
	}
}

func (h *Handler) hasTrustedAdminRole(r *http.Request) bool {
	config := h.getScopeResolverConfig()
	if !config.Enabled || len(config.AdminRoles) == 0 {
		return false
	}
	for _, role := range h.resolveRequestRoles(r) {
		if containsNormalizedRole(config.AdminRoles, role) {
			return true
		}
	}
	return false
}

func (h *Handler) resolveRequestRoles(r *http.Request) []string {
	config := h.getScopeResolverConfig()
	roles := headerRoles(r, config.RoleHeaders)
	if len(roles) > 0 {
		return roles
	}
	if !config.JWTClaimsEnabled {
		return nil
	}
	claims, ok := h.parseJWTClaims(r)
	if !ok {
		return nil
	}
	return normalizeRoleValues(claimStringValues(claims, config.RoleClaims...))
}

func (h *Handler) parseJWTClaims(r *http.Request) (jwt.MapClaims, bool) {
	config := h.getScopeResolverConfig()
	tokenString := presentedBearerToken(r)
	if tokenString == "" || strings.Count(tokenString, ".") != 2 {
		return nil, false
	}
	secret := strings.TrimSpace(config.JWTSecret)
	if secret == "" {
		return nil, false
	}

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unsupported signing method: %s", token.Method.Alg())
		}
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{
		jwt.SigningMethodHS256.Alg(),
		jwt.SigningMethodHS384.Alg(),
		jwt.SigningMethodHS512.Alg(),
	}))
	if err != nil || token == nil || !token.Valid {
		return nil, false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, false
	}
	return claims, true
}

func presentedAPIKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	if apiKey := strings.TrimSpace(r.Header.Get("x-api-key")); apiKey != "" {
		return apiKey
	}
	return presentedBearerToken(r)
}

func presentedBearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return ""
}

func firstClaimValue(claims jwt.MapClaims, keys ...string) string {
	values := claimStringValues(claims, keys...)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func claimStringValues(claims jwt.MapClaims, keys ...string) []string {
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if value, ok := claims[key]; ok {
			switch typed := value.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					values = append(values, strings.TrimSpace(typed))
				}
			case fmt.Stringer:
				if strings.TrimSpace(typed.String()) != "" {
					values = append(values, strings.TrimSpace(typed.String()))
				}
			case []string:
				for _, item := range typed {
					if strings.TrimSpace(item) != "" {
						values = append(values, strings.TrimSpace(item))
					}
				}
			case []interface{}:
				for _, item := range typed {
					if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
						values = append(values, text)
					}
				}
			default:
				if text := strings.TrimSpace(fmt.Sprint(typed)); text != "" && text != "<nil>" {
					values = append(values, text)
				}
			}
		}
	}
	return values
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func cloneAnyMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return map[string]interface{}{}
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func normalizeAdminRoles(roles []string) []string {
	return normalizeRoleValues(roles)
}

func normalizeRoleValues(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range splitRoleValues(value) {
			if normalized := strings.ToLower(strings.TrimSpace(part)); normalized != "" {
				result = append(result, normalized)
			}
		}
	}
	return result
}

func splitRoleValues(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';'
	})
}

func headerRoles(r *http.Request, headerKeys []string) []string {
	if r == nil {
		return nil
	}
	values := make([]string, 0, len(headerKeys))
	for _, key := range headerKeys {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			values = append(values, value)
		}
	}
	return normalizeRoleValues(values)
}

func containsNormalizedRole(roles []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	for _, role := range roles {
		if role == target {
			return true
		}
	}
	return false
}

func requestChangedBy(r *http.Request) string {
	if r == nil {
		return "api"
	}
	if value := strings.TrimSpace(r.Header.Get("X-Changed-By")); value != "" {
		return value
	}
	return "api"
}

func normalizeUsageScope(scope UsageScope) UsageScope {
	scope.TenantID = strings.TrimSpace(scope.TenantID)
	if scope.TenantID == "" {
		scope.TenantID = "default"
	}
	scope.ProjectID = strings.TrimSpace(scope.ProjectID)
	if scope.ProjectID == "" {
		scope.ProjectID = "default"
	}
	scope.UserID = strings.TrimSpace(scope.UserID)
	if scope.UserID == "" {
		scope.UserID = "anonymous"
	}
	scope.ScopeKey = fmt.Sprintf("%s/%s/%s", scope.TenantID, scope.ProjectID, scope.UserID)
	return scope
}

func (h *Handler) estimateRequestTokens(prompt string, history []types.Message) int {
	total := h.estimateTextTokens(prompt)
	total += h.estimateMessagesTokens(history)
	return total
}

func (h *Handler) estimateMessagesTokens(messages []types.Message) int {
	if len(messages) == 0 {
		return 0
	}
	if h.llmRuntime != nil {
		return h.llmRuntime.CountMessagesTokens(messages)
	}
	total := 0
	for _, message := range messages {
		total += h.estimateTextTokens(message.Content)
	}
	return total
}

func (h *Handler) estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	if h.llmRuntime != nil {
		return h.llmRuntime.CountTokens(text)
	}
	return len(strings.Fields(text)) + len(text)/10
}

func (h *Handler) enforceUsageQuota(scope UsageScope, estimatedPromptTokens int, entrypoint string) error {
	if !h.usagePolicy.QuotaEnabled || h.usageTracker == nil {
		return nil
	}

	snapshot := h.usageTracker.snapshot(scope)
	quota := h.resolveQuotaForScope(scope)
	if maxRequests := quota.MaxRequests; maxRequests > 0 && snapshot.RequestCount >= maxRequests {
		h.recordUsageQuotaMetric(entrypoint, "requests")
		return errors.New(errors.ErrAPIRateLimit, "skills request quota exceeded").
			WithContext("quota_type", "requests").
			WithContext("entrypoint", entrypoint).
			WithContext("scope_key", scope.ScopeKey).
			WithContext("resolved_from", quota.ResolvedFrom).
			WithContext("max_requests", maxRequests)
	}
	if maxTokens := quota.MaxTokens; maxTokens > 0 && snapshot.TotalTokens+estimatedPromptTokens > maxTokens {
		h.recordUsageQuotaMetric(entrypoint, "tokens")
		return errors.New(errors.ErrAPIRateLimit, "skills token quota exceeded").
			WithContext("quota_type", "tokens").
			WithContext("entrypoint", entrypoint).
			WithContext("scope_key", scope.ScopeKey).
			WithContext("resolved_from", quota.ResolvedFrom).
			WithContext("max_tokens", maxTokens)
	}
	return nil
}

func (h *Handler) recordUsage(scope UsageScope, entrypoint, skillName string, success bool, estimatedPromptTokens int, usage *types.TokenUsage, output string) {
	if !h.usagePolicy.TrackingEnabled || h.usageTracker == nil {
		return
	}

	recorded := usage
	if recorded == nil {
		recorded = &types.TokenUsage{
			PromptTokens:     estimatedPromptTokens,
			CompletionTokens: h.estimateTextTokens(output),
		}
		recorded.TotalTokens = recorded.PromptTokens + recorded.CompletionTokens
	} else {
		recorded = recorded.Clone()
		if recorded.PromptTokens == 0 {
			recorded.PromptTokens = estimatedPromptTokens
		}
		if recorded.TotalTokens == 0 {
			recorded.TotalTokens = recorded.PromptTokens + recorded.CompletionTokens
		}
	}

	h.usageTracker.record(scope, entrypoint, skillName, success, recorded)
	h.recordUsageMetrics(entrypoint, success, recorded)
	h.appendUsageLedger(scope, entrypoint, skillName, success, recorded)
}

func (h *Handler) usageQuotaSnapshot(scope UsageScope) map[string]interface{} {
	usage := h.usageTracker.snapshot(scope)
	quota := h.resolveQuotaForScope(scope)
	maxRequests := quota.MaxRequests
	maxTokens := quota.MaxTokens

	remainingRequests := -1
	if maxRequests > 0 {
		remainingRequests = maxRequests - usage.RequestCount
		if remainingRequests < 0 {
			remainingRequests = 0
		}
	}

	remainingTokens := -1
	if maxTokens > 0 {
		remainingTokens = maxTokens - usage.TotalTokens
		if remainingTokens < 0 {
			remainingTokens = 0
		}
	}

	return map[string]interface{}{
		"scope_key":          scope.ScopeKey,
		"enabled":            h.usagePolicy.QuotaEnabled,
		"max_requests":       maxRequests,
		"max_tokens":         maxTokens,
		"remaining_requests": remainingRequests,
		"remaining_tokens":   remainingTokens,
		"resolved_from":      quota.ResolvedFrom,
	}
}

type resolvedUsageQuota struct {
	MaxRequests  int
	MaxTokens    int
	ResolvedFrom string
}

func (h *Handler) resolveQuotaForScope(scope UsageScope) resolvedUsageQuota {
	resolved := resolvedUsageQuota{
		MaxRequests:  h.usagePolicy.DefaultMaxRequests,
		MaxTokens:    h.usagePolicy.DefaultMaxTokens,
		ResolvedFrom: "default",
	}

	apply := func(limit UsageQuotaLimit, source string) {
		applied := false
		if limit.MaxRequests != nil {
			resolved.MaxRequests = *limit.MaxRequests
			applied = true
		}
		if limit.MaxTokens != nil {
			resolved.MaxTokens = *limit.MaxTokens
			applied = true
		}
		if applied {
			resolved.ResolvedFrom = source
		}
	}

	if limit, ok := h.usagePolicy.TenantQuotas[scope.TenantID]; ok {
		apply(limit, "tenant")
	}
	for _, key := range []string{scope.TenantID + "/" + scope.ProjectID, scope.ProjectID} {
		if limit, ok := h.usagePolicy.ProjectQuotas[key]; ok {
			apply(limit, "project")
			break
		}
	}
	for _, key := range []string{scope.ScopeKey, scope.UserID} {
		if limit, ok := h.usagePolicy.UserQuotas[key]; ok {
			apply(limit, "user")
			break
		}
	}

	return resolved
}

func (t *usageTracker) record(scope UsageScope, entrypoint, skillName string, success bool, usage *types.TokenUsage) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	snapshot, ok := t.users[scope.ScopeKey]
	if !ok {
		snapshot = &UsageSnapshot{
			TenantID:         scope.TenantID,
			ProjectID:        scope.ProjectID,
			UserID:           scope.UserID,
			ScopeKey:         scope.ScopeKey,
			EntrypointCounts: make(map[string]int),
			SkillCounts:      make(map[string]int),
		}
		t.users[scope.ScopeKey] = snapshot
	}

	snapshot.RequestCount++
	snapshot.LastRequestAt = time.Now()
	snapshot.LastEntrypoint = entrypoint
	if skillName != "" {
		snapshot.LastSkill = skillName
		snapshot.SkillCounts[skillName]++
	}
	snapshot.EntrypointCounts[entrypoint]++
	switch entrypoint {
	case "execute":
		snapshot.ExecuteCount++
	case "agent_chat":
		snapshot.AgentChatCount++
	}
	if success {
		snapshot.SuccessCount++
	} else {
		snapshot.FailureCount++
	}
	if usage != nil {
		snapshot.PromptTokens += usage.PromptTokens
		snapshot.CompletionTokens += usage.CompletionTokens
		snapshot.TotalTokens += usage.TotalTokens
	}
}

func (t *usageTracker) snapshot(scope UsageScope) UsageSnapshot {
	if t == nil {
		return UsageSnapshot{
			TenantID:         scope.TenantID,
			ProjectID:        scope.ProjectID,
			UserID:           scope.UserID,
			ScopeKey:         scope.ScopeKey,
			EntrypointCounts: map[string]int{},
			SkillCounts:      map[string]int{},
		}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	snapshot, ok := t.users[scope.ScopeKey]
	if !ok || snapshot == nil {
		return UsageSnapshot{
			TenantID:         scope.TenantID,
			ProjectID:        scope.ProjectID,
			UserID:           scope.UserID,
			ScopeKey:         scope.ScopeKey,
			EntrypointCounts: map[string]int{},
			SkillCounts:      map[string]int{},
		}
	}

	cloned := *snapshot
	cloned.EntrypointCounts = make(map[string]int, len(snapshot.EntrypointCounts))
	for key, value := range snapshot.EntrypointCounts {
		cloned.EntrypointCounts[key] = value
	}
	cloned.SkillCounts = make(map[string]int, len(snapshot.SkillCounts))
	for key, value := range snapshot.SkillCounts {
		cloned.SkillCounts[key] = value
	}
	return cloned
}

func (t *usageTracker) aggregate() map[string]interface{} {
	if t == nil {
		return map[string]interface{}{
			"scope_count":       0,
			"user_count":        0,
			"request_count":     0,
			"execute_count":     0,
			"agent_chat_count":  0,
			"success_count":     0,
			"failure_count":     0,
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	uniqueUsers := make(map[string]struct{})
	summary := map[string]interface{}{
		"scope_count":       len(t.users),
		"user_count":        0,
		"request_count":     0,
		"execute_count":     0,
		"agent_chat_count":  0,
		"success_count":     0,
		"failure_count":     0,
		"prompt_tokens":     0,
		"completion_tokens": 0,
		"total_tokens":      0,
	}
	for _, snapshot := range t.users {
		if snapshot == nil {
			continue
		}
		uniqueUsers[snapshot.UserID] = struct{}{}
		summary["request_count"] = summary["request_count"].(int) + snapshot.RequestCount
		summary["execute_count"] = summary["execute_count"].(int) + snapshot.ExecuteCount
		summary["agent_chat_count"] = summary["agent_chat_count"].(int) + snapshot.AgentChatCount
		summary["success_count"] = summary["success_count"].(int) + snapshot.SuccessCount
		summary["failure_count"] = summary["failure_count"].(int) + snapshot.FailureCount
		summary["prompt_tokens"] = summary["prompt_tokens"].(int) + snapshot.PromptTokens
		summary["completion_tokens"] = summary["completion_tokens"].(int) + snapshot.CompletionTokens
		summary["total_tokens"] = summary["total_tokens"].(int) + snapshot.TotalTokens
	}
	summary["user_count"] = len(uniqueUsers)
	return summary
}

func (t *usageTracker) reset(scope *UsageScope) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if scope == nil {
		t.users = make(map[string]*UsageSnapshot)
		return
	}
	delete(t.users, scope.ScopeKey)
}

func (t *usageTracker) scopes() []UsageScope {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	scopes := make([]UsageScope, 0, len(t.users))
	for _, snapshot := range t.users {
		if snapshot == nil {
			continue
		}
		scopes = append(scopes, UsageScope{
			TenantID:  snapshot.TenantID,
			ProjectID: snapshot.ProjectID,
			UserID:    snapshot.UserID,
			ScopeKey:  snapshot.ScopeKey,
		})
	}
	sort.Slice(scopes, func(i, j int) bool {
		return scopes[i].ScopeKey < scopes[j].ScopeKey
	})
	return scopes
}

func (h *Handler) appendUsageLedger(scope UsageScope, entrypoint, skillName string, success bool, usage *types.TokenUsage) {
	if h.usageLedgerStore == nil || usage == nil {
		return
	}

	quota := h.resolveQuotaForScope(scope)
	record := &entity.TokenUsageHistory{
		ID:           uuid.NewString(),
		RequestID:    uuid.NewString(),
		ModelID:      uuid.Nil.String(),
		ProviderID:   uuid.Nil.String(),
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
		MessageCount: 0,
		MaxTokens:    quota.MaxTokens,
		Success:      success,
		StatusCode:   http.StatusOK,
		Metadata: entity.JSONMap{
			"subsystem":     "skill_runtime",
			"tenant_id":     scope.TenantID,
			"project_id":    scope.ProjectID,
			"user_id":       scope.UserID,
			"scope_key":     scope.ScopeKey,
			"entrypoint":    entrypoint,
			"skill":         skillName,
			"resolved_from": quota.ResolvedFrom,
		},
	}
	if !success {
		record.StatusCode = http.StatusInternalServerError
	}
	if err := h.usageLedgerStore.Create(record); err != nil {
		logger.Warn("failed to persist skills usage ledger", logger.Err(err))
	}
}

func (h *Handler) recordUsageMetrics(entrypoint string, success bool, usage *types.TokenUsage) {
	labels := map[string]string{
		observability.LabelEntrypoint: entrypoint,
		observability.LabelOutcome:    quotaOutcome(success),
	}
	observability.IncrementCounter(observability.MetricSkillUsageRequests, labels)
	if usage == nil {
		return
	}

	observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSkillUsageTokens, map[string]string{
		observability.LabelEntrypoint: entrypoint,
		observability.LabelTokenType:  "prompt",
	}).IncBy(float64(usage.PromptTokens))
	observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSkillUsageTokens, map[string]string{
		observability.LabelEntrypoint: entrypoint,
		observability.LabelTokenType:  "completion",
	}).IncBy(float64(usage.CompletionTokens))
	observability.GlobalMetrics.GetOrCreateCounter(observability.MetricSkillUsageTokens, map[string]string{
		observability.LabelEntrypoint: entrypoint,
		observability.LabelTokenType:  "total",
	}).IncBy(float64(usage.TotalTokens))
}

func (h *Handler) recordUsageQuotaMetric(entrypoint, quotaType string) {
	observability.IncrementCounter(observability.MetricSkillQuotaDenials, map[string]string{
		observability.LabelEntrypoint: entrypoint,
		observability.LabelQuotaType:  quotaType,
	})
}

func quotaOutcome(success bool) string {
	if success {
		return "success"
	}
	return "failed"
}

func (h *Handler) hasValidSearchAdminToken(r *http.Request) bool {
	expected := strings.TrimSpace(h.searchAdminToken)
	if expected == "" || r == nil {
		return false
	}

	provided := strings.TrimSpace(r.Header.Get("X-Skills-Admin-Token"))
	if provided == "" {
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			provided = strings.TrimSpace(authHeader[7:])
		}
	}
	if provided == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func isLoopbackRequest(r *http.Request) bool {
	if r == nil {
		return false
	}

	host := requestRemoteIP(r)
	if host == "" {
		return false
	}

	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func requestRemoteIP(r *http.Request) string {
	if r == nil {
		return ""
	}

	host := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if host != "" {
		if idx := strings.Index(host, ","); idx >= 0 {
			host = strings.TrimSpace(host[:idx])
		}
		return host
	}

	host = strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if host != "" {
		return host
	}

	host = strings.TrimSpace(r.RemoteAddr)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		return parsedHost
	}
	return host
}

func (h *Handler) reindexRetryAfter() (time.Duration, bool) {
	h.searchReindexMu.Lock()
	defer h.searchReindexMu.Unlock()

	if h.searchReindexCooldown <= 0 || h.lastSearchReindexAt.IsZero() {
		return 0, false
	}

	elapsed := time.Since(h.lastSearchReindexAt)
	if elapsed >= h.searchReindexCooldown {
		return 0, false
	}

	return h.searchReindexCooldown - elapsed, true
}

func (h *Handler) markSearchReindexStart() {
	h.searchReindexMu.Lock()
	defer h.searchReindexMu.Unlock()
	h.lastSearchReindexAt = time.Now()
}

func (h *Handler) searchTelemetrySnapshot() map[string]interface{} {
	if h.searchTelemetry == nil {
		return map[string]interface{}{}
	}

	h.searchTelemetry.mu.RLock()
	defer h.searchTelemetry.mu.RUnlock()

	requestedModeCount := make(map[string]int, len(h.searchTelemetry.requestedModeCount))
	for key, value := range h.searchTelemetry.requestedModeCount {
		requestedModeCount[key] = value
	}

	resolvedModeCount := make(map[string]int, len(h.searchTelemetry.resolvedModeCount))
	for key, value := range h.searchTelemetry.resolvedModeCount {
		resolvedModeCount[key] = value
	}

	avgResults := 0.0
	if h.searchTelemetry.totalRequests > 0 {
		avgResults = float64(h.searchTelemetry.totalResults) / float64(h.searchTelemetry.totalRequests)
	}

	return map[string]interface{}{
		"total_requests":       h.searchTelemetry.totalRequests,
		"total_results":        h.searchTelemetry.totalResults,
		"average_results":      avgResults,
		"embedding_requests":   h.searchTelemetry.embeddingRequests,
		"requested_mode_count": requestedModeCount,
		"resolved_mode_count":  resolvedModeCount,
		"last_query":           h.searchTelemetry.lastQuery,
		"last_requested_mode":  h.searchTelemetry.lastRequestedMode,
		"last_resolved_mode":   h.searchTelemetry.lastResolvedMode,
		"last_result_count":    h.searchTelemetry.lastResultCount,
		"last_used_embedding":  h.searchTelemetry.lastEmbeddingUsed,
		"reindex_count":        h.searchTelemetry.reindexCount,
		"last_reindex_status":  h.searchTelemetry.lastReindexStatus,
		"last_reindex_at":      h.searchTelemetry.lastReindexAt,
	}
}

func (h *Handler) auditSearchAdminAction(r *http.Request, action, outcome string, extraFields ...interface{}) {
	fields := []interface{}{
		logger.String("action", action),
		logger.String("outcome", outcome),
		logger.String("access_mode", h.searchAdminAccessMode(r)),
		logger.String("remote_ip", requestRemoteIP(r)),
		logger.RequestID(logger.GetRequestID(r.Context())),
		logger.Any("search_summary", h.searchTelemetrySnapshot()),
	}
	fields = append(fields, extraFields...)
	h.recordSearchAdminMetric(action, outcome, h.searchAdminAccessMode(r))

	adminLogger := logger.Admin().Named("skills_search")

	switch outcome {
	case "forbidden", "rate_limited":
		adminLogger.Warn("skills search admin action", fieldsToZap(fields)...)
	case "failed":
		adminLogger.Error("skills search admin action", fieldsToZap(fields)...)
	default:
		adminLogger.Info("skills search admin action", fieldsToZap(fields)...)
	}
}

func (h *Handler) auditSkillMutation(r *http.Request, action, outcome string, extraFields ...interface{}) {
	fields := []interface{}{
		logger.String("action", action),
		logger.String("outcome", outcome),
		logger.String("access_mode", h.skillMutationAccessMode(r)),
		logger.String("remote_ip", requestRemoteIP(r)),
		logger.RequestID(logger.GetRequestID(r.Context())),
	}
	fields = append(fields, extraFields...)
	h.recordSkillMutationMetric(action, outcome, h.skillMutationAccessMode(r))

	adminLogger := logger.Admin().Named("skills_mutation")
	switch outcome {
	case "forbidden", "invalid_request":
		adminLogger.Warn("skills mutation action", fieldsToZap(fields)...)
	case "failed":
		adminLogger.Error("skills mutation action", fieldsToZap(fields)...)
	default:
		adminLogger.Info("skills mutation action", fieldsToZap(fields)...)
	}
}

func (h *Handler) recordSearchAdminMetric(action, outcome, accessMode string) {
	labels := map[string]string{
		observability.LabelAction:     action,
		observability.LabelOutcome:    outcome,
		observability.LabelAccessMode: accessMode,
	}
	observability.IncrementCounter(observability.MetricSearchAdminActions, labels)
	if action == "search_reindex" {
		observability.IncrementCounter(observability.MetricSearchReindexRuns, labels)
	}
}

func (h *Handler) recordSkillMutationMetric(action, outcome, accessMode string) {
	labels := map[string]string{
		observability.LabelAction:     action,
		observability.LabelOutcome:    outcome,
		observability.LabelAccessMode: accessMode,
	}
	observability.IncrementCounter(observability.MetricSkillMutationActions, labels)
}

func fieldsToZap(fields []interface{}) []zapcore.Field {
	converted := make([]zapcore.Field, 0, len(fields))
	for _, field := range fields {
		if zapField, ok := field.(zapcore.Field); ok {
			converted = append(converted, zapField)
		}
	}
	return converted
}

func defaultAgentModel(runtime *llm.LLMRuntime) string {
	if runtime == nil {
		return ""
	}
	if model := runtime.DefaultModel(); model != "" {
		return model
	}
	providers := runtime.ListProviders()
	if len(providers) == 0 {
		return ""
	}
	return providers[0]
}

func totalMessageContentChars(messages []types.Message) int {
	total := 0
	for _, msg := range messages {
		total += len(strings.TrimSpace(msg.Content))
	}
	return total
}

func totalMessageContentTokens(messages []types.Message, runtime *llm.LLMRuntime, model string) int {
	if runtime == nil {
		return 0
	}
	provider, err := runtime.GetProvider(model)
	if err != nil || provider == nil {
		return 0
	}
	total := 0
	for _, msg := range messages {
		content := strings.TrimSpace(msg.Content)
		if content != "" {
			total += provider.CountTokens(content)
		}
	}
	return total
}

func defaultAgentProvider(runtime *llm.LLMRuntime) string {
	if runtime == nil {
		return ""
	}
	if provider := strings.TrimSpace(runtime.DefaultProvider()); provider != "" {
		return provider
	}
	return ""
}

func runtimeProviderModels(runtime *llm.LLMRuntime, providerName, defaultProvider, defaultModel string) []string {
	if runtime == nil {
		return []string{}
	}

	models := make([]string, 0)
	for _, alias := range runtime.ProviderAliases(providerName) {
		alias = strings.TrimSpace(alias)
		if alias == "" || alias == providerName {
			continue
		}
		models = append(models, alias)
	}

	if model := strings.TrimSpace(defaultModel); model != "" {
		resolvedDefault := runtime.ResolveProviderName(model)
		if resolvedDefault == providerName || (resolvedDefault == "" && defaultProvider == providerName) {
			models = append(models, model)
		}
	}

	models = uniqueStrings(normalizeStringList(models))
	if len(models) == 0 {
		if defaultProvider == providerName {
			return []string{providerName}
		}
		return []string{}
	}
	sort.Strings(models)
	return models
}

func runtimeProviderDefaultModel(runtime *llm.LLMRuntime, providerName, defaultProvider, defaultModel string, models []string) string {
	if runtime != nil {
		if model := strings.TrimSpace(defaultModel); model != "" {
			resolvedDefault := runtime.ResolveProviderName(model)
			if resolvedDefault == providerName || (resolvedDefault == "" && defaultProvider == providerName) {
				return model
			}
		}
	}
	if len(models) > 0 {
		return models[0]
	}
	return ""
}

func runtimeModelsSnapshot(runtime *llm.LLMRuntime) map[string]interface{} {
	defaultProvider := defaultAgentProvider(runtime)
	defaultModel := defaultAgentModel(runtime)
	providersPayload := make([]map[string]interface{}, 0)
	totalModels := 0

	if runtime != nil {
		providers := runtime.ListProviders()
		sort.Strings(providers)

		for _, name := range providers {
			models := runtimeProviderModels(runtime, name, defaultProvider, defaultModel)
			providerPayload := map[string]interface{}{
				"name":        name,
				"models":      models,
				"model_count": len(models),
			}

			if providerDefault := runtimeProviderDefaultModel(
				runtime,
				name,
				defaultProvider,
				defaultModel,
				models,
			); providerDefault != "" {
				providerPayload["default_model"] = providerDefault
			}

			if caps, err := runtime.GetCapabilities(name); err == nil && caps != nil {
				providerPayload["supports_tools"] = caps.SupportsTools
				providerPayload["supports_streaming"] = caps.SupportsStreaming
				providerPayload["max_context_tokens"] = caps.MaxContextTokens
				providerPayload["max_output_tokens"] = caps.MaxOutputTokens
			}

			if provider, err := runtime.GetProvider(name); err == nil && provider != nil {
				if lister, ok := provider.(interface {
					ListModelCapabilities() map[string]agentconfig.ModelCapabilitySpec
				}); ok {
					if modelCapabilities := lister.ListModelCapabilities(); len(modelCapabilities) > 0 {
						providerPayload["model_capabilities"] = modelCapabilities
					}
				}
			}

			providersPayload = append(providersPayload, providerPayload)
			totalModels += len(models)
		}
	}

	return map[string]interface{}{
		"default_provider": defaultProvider,
		"default_model":    defaultModel,
		"providers":        providersPayload,
		"count":            totalModels,
	}
}

func executionStatus(success bool) string {
	if success {
		return "completed"
	}
	return "failed"
}

func sessionID(session *chat.Session) string {
	if session == nil {
		return ""
	}
	return session.ID
}

func (h *Handler) writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

func (h *Handler) streamLLMChat(ctx context.Context, w http.ResponseWriter, session *chat.Session, agentID, modelName, userPrompt string, messages []types.Message, reasoningEffort string, thinking *types.ThinkingConfig, routeAttempted bool, routeCandidates []*skill.RouteResult, fallback string, usageScope UsageScope, estimatedPromptTokens int, traceID string, planningPayload map[string]interface{}) error {
	model := strings.TrimSpace(modelName)
	if model == "" {
		model = defaultAgentModel(h.llmRuntime)
	}
	metadata := map[string]interface{}{
		"session_id": sessionID(session),
	}
	promptLayoutPreview := ""
	promptLayoutLength := 0
	instructionTokens := 0
	var promptLayoutLayers []string
	var promptLayoutSources []string
	if layout := runtimeprompt.RenderInstructionMessagesLayout(messages); layout != "" {
		metadata["prompt_layout"] = layout
	}
	var tokenCountFunc func(string) int
	if provider, pErr := h.llmRuntime.GetProvider(model); pErr == nil && provider != nil {
		tokenCountFunc = provider.CountTokens
	}
	layoutInfo := runtimeprompt.SummarizeInstructionMessagesWithTokens(messages, tokenCountFunc)
	if layoutInfo.Summary != "" {
		promptLayoutPreview = layoutInfo.Summary
		promptLayoutLength = layoutInfo.InstructionChars
		instructionTokens = layoutInfo.InstructionTokens
		promptLayoutLayers = append([]string(nil), layoutInfo.Layers...)
		promptLayoutSources = append([]string(nil), layoutInfo.Sources...)
	}
	requestPayload := map[string]interface{}{
		"session_id":    sessionID(session),
		"agent_id":      agentID,
		"model":         model,
		"message_count": len(messages),
	}
	if promptLayoutPreview != "" {
		requestPayload["prompt_layout_summary"] = promptLayoutPreview
	}
	if promptLayoutLength > 0 {
		requestPayload["prompt_layout_length"] = promptLayoutLength
	}
	if totalMessageChars := totalMessageContentChars(messages); totalMessageChars > 0 {
		requestPayload["total_message_chars"] = totalMessageChars
	}
	if totalMessageTokens := totalMessageContentTokens(messages, h.llmRuntime, model); totalMessageTokens > 0 {
		requestPayload["total_tokens"] = totalMessageTokens
	}
	if instructionTokens > 0 {
		requestPayload["instruction_tokens"] = instructionTokens
	}
	if len(promptLayoutLayers) > 0 {
		requestPayload["prompt_layers"] = promptLayoutLayers
	}
	if len(promptLayoutSources) > 0 {
		requestPayload["prompt_sources"] = promptLayoutSources
	}
	ctx = llm.WithRetryEventReporter(ctx, h.runtimeRetryEventReporter(traceID, sessionID(session)))
	h.publishSessionRuntimeEvent("llm.request.started", traceID, sessionID(session), requestPayload)
	stream, err := h.llmRuntime.Stream(ctx, &llm.LLMRequest{
		Model:           model,
		Messages:        messages,
		MaxTokens:       4096,
		Temperature:     0.7,
		ReasoningEffort: reasoningEffort,
		Thinking:        types.CloneThinkingConfig(thinking),
		Stream:          true,
		Metadata:        metadata,
	})
	if err != nil {
		h.publishSessionRuntimeEvent("llm.request.finished", traceID, sessionID(session), map[string]interface{}{
			"model":   model,
			"success": false,
			"error":   err.Error(),
		})
		h.writeError(w, http.StatusInternalServerError, err)
		return err
	}

	h.prepareSSEHeaders(w)
	emitter := newSSEEmitter(w)
	initialOrchestration := buildOrchestrationPayload("llm_stream", routeAttempted, routeCandidates, nil, &llm.LLMResponse{Model: model}, fallback)
	if planningPayload != nil {
		initialOrchestration["planning_attempted"] = planningPayload["attempted"]
		initialOrchestration["planning_source"] = planningPayload["planning_source"]
		initialOrchestration["plan_step_count"] = planningPayload["step_count"]
		initialOrchestration["subagent_task_count"] = planningPayload["subagent_task_count"]
		initialOrchestration["subagent_execution_requested"] = planningPayload["subagent_execution_requested"]
		initialOrchestration["subagent_execution_eligible"] = planningPayload["subagent_execution_eligible"]
		initialOrchestration["subagent_execution_blocked_reason"] = planningPayload["subagent_execution_blocked_reason"]
		initialOrchestration["subagent_execution_attempted"] = planningPayload["subagent_execution_attempted"]
		initialOrchestration["patch_decision"] = planningPayload["patch_decision"]
		initialOrchestration["patch_decision_reason"] = planningPayload["patch_decision_reason"]
		initialOrchestration["patch_decision_required"] = planningPayload["patch_decision_required"]
		if planningError, ok := planningPayload["planning_error"].(string); ok && planningError != "" {
			initialOrchestration["planning_error"] = planningError
		}
	}
	emitter.Emit("meta", map[string]interface{}{
		"session_id":    sessionID(session),
		"agent_id":      agentID,
		"source":        "llm_stream",
		"kind":          "llm",
		"model":         model,
		"orchestration": initialOrchestration,
		"planning":      planningPayload,
		"status":        "streaming",
	})
	if planningPayload != nil {
		emitter.Emit("planning", planningPayload)
	}

	var builder strings.Builder
	var reasoningBuilder strings.Builder
	toolEvents := make([]map[string]interface{}, 0)
	chunkIndex := 0
	for chunk := range stream {
		chunkIndex++
		switch chunk.Type {
		case llm.EventTypeText:
			if chunk.Content != "" {
				builder.WriteString(chunk.Content)
				emitter.Emit("chunk", buildStreamChunkPayload(chunk, chunkIndex, builder.Len()))
			}
		case llm.EventTypeReasoning, llm.EventTypeToolCall, llm.EventTypeToolStart, llm.EventTypeToolEnd:
			payload := buildStreamChunkPayload(chunk, chunkIndex, builder.Len())
			if chunk.Type == llm.EventTypeReasoning && chunk.Content != "" {
				reasoningBuilder.WriteString(chunk.Content)
			}
			if chunk.ToolCall != nil || chunk.Delta != nil || chunk.Content != "" {
				toolEvents = append(toolEvents, payload)
			}
			emitter.Emit(streamEventName(chunk.Type), payload)
			emitter.Emit("chunk", payload)
		case llm.EventTypeImage:
			payload := buildStreamChunkPayload(chunk, chunkIndex, builder.Len())
			emitter.Emit("chunk", payload)
		case llm.EventTypeError:
			h.publishSessionRuntimeEvent("llm.request.finished", traceID, sessionID(session), map[string]interface{}{
				"model":   model,
				"success": false,
				"error":   chunk.Error,
			})
			emitter.Emit("error", map[string]interface{}{
				"index":   chunkIndex,
				"message": chunk.Error,
				"source":  "llm_stream",
			})
			return fmt.Errorf("%s", chunk.Error)
		case llm.EventTypeDone:
			// handled after loop
		}
		if chunk.Done {
			break
		}
	}

	fullContent := builder.String()
	h.recordUsage(usageScope, "agent_chat", "", true, estimatedPromptTokens, nil, fullContent)
	resultPayload := map[string]interface{}{
		"kind":        "llm",
		"source":      "llm_stream",
		"success":     true,
		"output":      fullContent,
		"model":       model,
		"reasoning":   reasoningBuilder.String(),
		"tool_events": toolEvents,
	}
	resultPayload["orchestration"] = buildOrchestrationPayload("llm_stream", routeAttempted, routeCandidates, nil, &llm.LLMResponse{Model: model, Content: fullContent}, fallback)
	if planningPayload != nil {
		resultPayload["planning"] = planningPayload
		if orchestration, ok := resultPayload["orchestration"].(map[string]interface{}); ok {
			orchestration["planning_attempted"] = planningPayload["attempted"]
			orchestration["planning_source"] = planningPayload["planning_source"]
			orchestration["plan_step_count"] = planningPayload["step_count"]
			orchestration["subagent_task_count"] = planningPayload["subagent_task_count"]
			orchestration["subagent_execution_requested"] = planningPayload["subagent_execution_requested"]
			orchestration["subagent_execution_eligible"] = planningPayload["subagent_execution_eligible"]
			orchestration["subagent_execution_blocked_reason"] = planningPayload["subagent_execution_blocked_reason"]
			orchestration["subagent_execution_attempted"] = planningPayload["subagent_execution_attempted"]
			orchestration["patch_decision"] = planningPayload["patch_decision"]
			orchestration["patch_decision_reason"] = planningPayload["patch_decision_reason"]
			orchestration["patch_decision_required"] = planningPayload["patch_decision_required"]
			if planningError, ok := planningPayload["planning_error"].(string); ok && planningError != "" {
				orchestration["planning_error"] = planningError
			}
		}
	}
	if session != nil {
		_ = h.persistChatTurn(
			ctx,
			session,
			userPrompt,
			fullContent,
			buildWorkspaceEvidenceMetadata(resultPayload),
		)
	}
	h.publishSessionRuntimeEvent("llm.request.finished", traceID, sessionID(session), map[string]interface{}{
		"model":   model,
		"success": true,
	})
	emitter.Emit("orchestration", resultPayload["orchestration"])
	if planningPayload != nil {
		emitter.Emit("planning", planningPayload)
	}
	emitter.Emit("result", resultPayload)

	emitter.Emit("done", map[string]interface{}{
		"session_id": sessionID(session),
		"agent_id":   agentID,
		"source":     "llm_stream",
		"status":     "completed",
		"content":    fullContent,
		"result":     resultPayload,
	})
	return nil
}

func (h *Handler) streamStaticResult(w http.ResponseWriter, session *chat.Session, agentID string, resultPayload map[string]interface{}) {
	output, _ := resultPayload["output"].(string)
	h.prepareSSEHeaders(w)
	emitter := newSSEEmitter(w)
	emitter.Emit("meta", map[string]interface{}{
		"session_id":    sessionID(session),
		"agent_id":      agentID,
		"source":        responseResultSource(resultPayload),
		"kind":          resultPayload["kind"],
		"orchestration": resultPayload["orchestration"],
		"status":        "streaming",
	})
	if planningPayload, ok := resultPayload["planning"]; ok && planningPayload != nil {
		emitter.Emit("planning", planningPayload)
	}
	emitter.Emit("orchestration", resultPayload["orchestration"])
	if routePayload, ok := buildAgentRouteEventPayload(resultPayload); ok {
		emitter.Emit("route", routePayload)
	}
	for _, toolEvent := range buildObservedToolEventPayloads(resultPayload) {
		emitter.Emit(toolEvent.Event, toolEvent.Payload)
	}
	for _, observationPayload := range buildObservationEventPayloads(resultPayload) {
		emitter.Emit("observation", observationPayload)
	}
	for _, subagentPayload := range buildSubagentEventPayloads(resultPayload) {
		emitter.Emit("subagent", subagentPayload)
	}
	emitter.Emit("result", resultPayload)
	emitter.Emit("chunk", map[string]interface{}{
		"type":    "text",
		"content": output,
	})
	emitter.Emit("done", map[string]interface{}{
		"session_id": sessionID(session),
		"agent_id":   agentID,
		"source":     responseResultSource(resultPayload),
		"status":     finalResultStatus(resultPayload),
		"content":    output,
		"result":     resultPayload,
	})
}

func streamEventName(eventType llm.StreamEventType) string {
	switch eventType {
	case llm.EventTypeReasoning:
		return "reasoning"
	case llm.EventTypeToolCall:
		return "tool_call"
	case llm.EventTypeToolStart:
		return "tool_start"
	case llm.EventTypeToolEnd:
		return "tool_end"
	default:
		return "chunk"
	}
}

func buildStreamChunkPayload(chunk llm.StreamChunk, index int, totalChars int) map[string]interface{} {
	payload := map[string]interface{}{
		"index":    index,
		"type":     string(chunk.Type),
		"content":  chunk.Content,
		"metadata": chunk.Metadata,
	}

	switch chunk.Type {
	case llm.EventTypeText:
		payload["total_chars"] = totalChars
		payload["text"] = map[string]interface{}{
			"content":     chunk.Content,
			"total_chars": totalChars,
		}
	case llm.EventTypeReasoning:
		payload["reasoning"] = map[string]interface{}{
			"content": chunk.Content,
			"delta":   chunk.Content,
			"length":  len(chunk.Content),
		}
	case llm.EventTypeToolCall, llm.EventTypeToolStart, llm.EventTypeToolEnd:
		payload["tool_call"] = chunk.ToolCall
		payload["delta"] = chunk.Delta
		payload["tool"] = buildToolEventPayload(chunk)
	}

	return payload
}

func buildToolEventPayload(chunk llm.StreamChunk) map[string]interface{} {
	var toolID string
	var toolName string
	var toolArgs map[string]interface{}

	if chunk.ToolCall != nil {
		toolID = chunk.ToolCall.ID
		toolName = chunk.ToolCall.Name
		toolArgs = chunk.ToolCall.Args
	}
	if chunk.Delta != nil {
		if toolID == "" {
			toolID = chunk.Delta.ID
		}
		if toolName == "" {
			toolName = chunk.Delta.Name
		}
		if len(toolArgs) == 0 {
			toolArgs = chunk.Delta.Args
		}
	}

	return map[string]interface{}{
		"id":      toolID,
		"name":    toolName,
		"args":    toolArgs,
		"status":  string(chunk.Type),
		"content": chunk.Content,
	}
}

type staticToolEvent struct {
	Event   string
	Payload map[string]interface{}
}

func buildObservedToolEventPayloads(resultPayload map[string]interface{}) []staticToolEvent {
	observations := observationsFromResultPayload(resultPayload)
	if len(observations) == 0 {
		return nil
	}

	events := make([]staticToolEvent, 0, len(observations)*3)
	for index, observation := range observations {
		toolName := strings.TrimSpace(observation.Tool)
		if toolName == "" {
			continue
		}

		toolCallID := observationToolCallID(observation, index)
		toolCall := map[string]interface{}{
			"id":   toolCallID,
			"name": toolName,
		}
		if args := cloneObservedToolArguments(observation.Input); len(args) > 0 {
			toolCall["arguments"] = args
		}

		metadata := map[string]interface{}{
			"step":        observation.Step,
			"success":     observation.Success,
			"duration_ms": observation.Duration.GetDuration().Milliseconds(),
		}
		if strings.TrimSpace(observation.Error) != "" {
			metadata["error"] = observation.Error
		}
		if len(observation.Metrics) > 0 {
			metadata["metrics"] = observation.Metrics
		}

		events = append(events,
			buildStaticToolEventPayload(index, llm.EventTypeToolCall, toolCall, metadata, ""),
			buildStaticToolEventPayload(index, llm.EventTypeToolStart, toolCall, metadata, ""),
			buildStaticToolEventPayload(index, llm.EventTypeToolEnd, toolCall, metadata, observationOutputText(observation)),
		)
	}

	if len(events) == 0 {
		return nil
	}
	return events
}

func buildStaticToolEventPayload(index int, eventType llm.StreamEventType, toolCall map[string]interface{}, metadata map[string]interface{}, content string) staticToolEvent {
	toolPayload := map[string]interface{}{
		"id":      toolCall["id"],
		"name":    toolCall["name"],
		"args":    toolCall["arguments"],
		"status":  string(eventType),
		"content": content,
	}

	payload := map[string]interface{}{
		"index":     index + 1,
		"type":      string(eventType),
		"content":   content,
		"metadata":  metadata,
		"tool_call": toolCall,
		"tool":      toolPayload,
	}
	if eventType == llm.EventTypeToolCall {
		payload["delta"] = toolCall
	}

	return staticToolEvent{
		Event:   streamEventName(eventType),
		Payload: payload,
	}
}

func observationOutputText(observation types.Observation) string {
	if strings.TrimSpace(observation.Error) != "" {
		return observation.Error
	}

	switch value := observation.Output.(type) {
	case nil:
		return ""
	case string:
		return value
	case []byte:
		return string(value)
	default:
		encoded, err := json.Marshal(value)
		if err == nil {
			return string(encoded)
		}
		return fmt.Sprintf("%v", value)
	}
}

func buildAgentRouteEventPayload(resultPayload map[string]interface{}) (map[string]interface{}, bool) {
	if resultPayload == nil {
		return nil, false
	}
	orchestration, ok := resultPayload["orchestration"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	return map[string]interface{}{
		"source":           responseResultSource(resultPayload),
		"skill":            resultPayload["skill"],
		"route_attempted":  orchestration["route_attempted"],
		"route_matched":    orchestration["route_matched"],
		"candidate_count":  orchestration["candidate_count"],
		"route_candidates": orchestration["route_candidates"],
	}, true
}

func buildObservationEventPayloads(resultPayload map[string]interface{}) []map[string]interface{} {
	observations := observationsFromResultPayload(resultPayload)
	if len(observations) == 0 {
		return nil
	}

	payloads := make([]map[string]interface{}, 0, len(observations))
	for idx, observation := range observations {
		payloads = append(payloads, map[string]interface{}{
			"index":       idx + 1,
			"step":        observation.Step,
			"tool":        observation.Tool,
			"success":     observation.Success,
			"error":       observation.Error,
			"duration_ms": observation.Duration.GetDuration().Milliseconds(),
			"input":       observation.Input,
			"output":      observation.Output,
			"metrics":     observation.Metrics,
		})
	}
	return payloads
}

func observationsFromResultPayload(resultPayload map[string]interface{}) []types.Observation {
	if resultPayload == nil {
		return nil
	}

	rawObservations, ok := resultPayload["observations"]
	if !ok {
		return nil
	}

	switch value := rawObservations.(type) {
	case []types.Observation:
		return value
	case []interface{}:
		observations := make([]types.Observation, 0, len(value))
		for _, item := range value {
			switch observation := item.(type) {
			case types.Observation:
				observations = append(observations, observation)
			case map[string]interface{}:
				decoded, ok := observationFromMap(observation)
				if ok {
					observations = append(observations, decoded)
				}
			}
		}
		return observations
	default:
		return nil
	}
}

func observationFromMap(value map[string]interface{}) (types.Observation, bool) {
	if len(value) == 0 {
		return types.Observation{}, false
	}

	observation := types.Observation{
		Step:    stringMapValueAny(value, "step"),
		Tool:    stringMapValueAny(value, "tool"),
		Input:   value["input"],
		Output:  value["output"],
		Success: boolMapValueAny(value, "success"),
		Error:   stringMapValueAny(value, "error"),
		Metrics: mapMapValueAny(value["metrics"]),
	}
	if timestamp, ok := value["timestamp"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
			observation.Timestamp = parsed
		}
	}
	if durationMap, ok := value["duration"].(map[string]interface{}); ok {
		if start, ok := durationMap["start"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, start); err == nil {
				observation.Duration.Start = parsed
			}
		}
		if end, ok := durationMap["end"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, end); err == nil {
				observation.Duration.End = parsed
			}
		}
	}
	return observation, strings.TrimSpace(observation.Tool) != ""
}

func mapMapValueAny(value interface{}) map[string]interface{} {
	typed, ok := value.(map[string]interface{})
	if !ok || len(typed) == 0 {
		return nil
	}

	cloned := make(map[string]interface{}, len(typed))
	for key, item := range typed {
		cloned[key] = item
	}
	return cloned
}

func buildSubagentEventPayloads(resultPayload map[string]interface{}) []map[string]interface{} {
	if resultPayload == nil {
		return nil
	}
	rawResults, ok := resultPayload["subagent_results"]
	if !ok {
		return nil
	}

	var results []agent.SubagentResult
	switch typed := rawResults.(type) {
	case []agent.SubagentResult:
		results = typed
	case []interface{}:
		results = make([]agent.SubagentResult, 0, len(typed))
		for _, item := range typed {
			reportMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			results = append(results, agent.SubagentResult{
				ID:           stringMapValueAny(reportMap, "id"),
				Role:         stringMapValueAny(reportMap, "role"),
				SessionID:    stringMapValueAny(reportMap, "session_id"),
				ReadOnly:     boolMapValueAny(reportMap, "read_only"),
				BudgetTokens: intMapValueAny(reportMap, "budget_tokens"),
				Success:      boolMapValueAny(reportMap, "success"),
				Summary:      stringMapValueAny(reportMap, "summary"),
				Findings:     stringSliceValueAny(reportMap["findings"]),
				Patches:      filePatchesFromAny(reportMap["patches"]),
				Error:        stringMapValueAny(reportMap, "error"),
			})
		}
	default:
		return nil
	}

	payloads := make([]map[string]interface{}, 0, len(results))
	for idx, report := range results {
		payloads = append(payloads, map[string]interface{}{
			"index":         idx + 1,
			"id":            report.ID,
			"role":          report.Role,
			"session_id":    report.SessionID,
			"read_only":     report.ReadOnly,
			"budget_tokens": report.BudgetTokens,
			"success":       report.Success,
			"summary":       report.Summary,
			"findings":      report.Findings,
			"patches":       report.Patches,
			"error":         report.Error,
		})
	}
	return payloads
}

func buildWorkspaceEvidenceMetadata(resultPayload map[string]interface{}) types.Metadata {
	if len(resultPayload) == 0 {
		return nil
	}

	evidence := buildWorkspaceEvidenceEntries(resultPayload)
	if len(evidence) == 0 {
		return nil
	}

	artifactIDs := make([]string, 0, len(evidence))
	for _, item := range evidence {
		id, _ := item["id"].(string)
		if strings.TrimSpace(id) == "" {
			continue
		}
		artifactIDs = append(artifactIDs, id)
	}
	if len(artifactIDs) == 0 {
		return nil
	}

	metadata := types.NewMetadata()
	metadata["workspace_related_artifact_ids"] = artifactIDs
	metadata["workspace_related_artifacts"] = evidence
	return metadata
}

func buildWorkspaceEvidenceEntries(resultPayload map[string]interface{}) []map[string]interface{} {
	entries := make([]map[string]interface{}, 0, 8)
	source := strings.TrimSpace(responseResultSource(resultPayload))

	entries = append(entries, buildWorkspaceEvidenceEntry(
		"agent-chat-response",
		"Final response payload persisted with the assistant history.",
		source,
		map[string]interface{}{
			"source": resultPayload["source"],
			"kind":   resultPayload["kind"],
			"status": finalResultStatus(resultPayload),
			"result": resultPayload,
		},
	))

	if planning, ok := resultPayload["planning"].(map[string]interface{}); ok && len(planning) > 0 {
		entries = append(entries, buildWorkspaceEvidenceEntry(
			"planning",
			"Planning payload emitted by /api/agent/chat.",
			source,
			planning,
		))
	}

	if orchestration, ok := resultPayload["orchestration"].(map[string]interface{}); ok && len(orchestration) > 0 {
		entries = append(entries, buildWorkspaceEvidenceEntry(
			"orchestration",
			"Structured orchestration summary emitted by /api/agent/chat.",
			source,
			orchestration,
		))
	}

	if route, ok := buildAgentRouteEventPayload(resultPayload); ok && len(route) > 0 {
		entries = append(entries, buildWorkspaceEvidenceEntry(
			"route",
			"Route metadata emitted by static agent execution.",
			source,
			route,
		))
	}

	if toolEvents := buildObservedToolEventPayloads(resultPayload); len(toolEvents) > 0 {
		payloads := make([]map[string]interface{}, 0, len(toolEvents))
		for _, item := range toolEvents {
			if len(item.Payload) == 0 {
				continue
			}
			payloads = append(payloads, item.Payload)
		}
		if len(payloads) > 0 {
			entries = append(entries, buildWorkspaceEvidenceEntry(
				"tool-events",
				"Tool events observed during agent execution.",
				source,
				payloads,
			))
		}
	}

	if observations := buildObservationEventPayloads(resultPayload); len(observations) > 0 {
		entries = append(entries, buildWorkspaceEvidenceEntry(
			"observations",
			"Observation events emitted by static agent execution.",
			source,
			observations,
		))
	}

	if subagents := buildSubagentEventPayloads(resultPayload); len(subagents) > 0 {
		entries = append(entries, buildWorkspaceEvidenceEntry(
			"subagents",
			"Subagent events emitted by static agent execution.",
			source,
			subagents,
		))
	}

	return entries
}

func buildWorkspaceEvidenceEntry(kind, summary, source string, payload interface{}) map[string]interface{} {
	if strings.TrimSpace(kind) == "" {
		return nil
	}
	idSource := strings.TrimSpace(source)
	if idSource == "" {
		idSource = "runtime"
	}
	filename := kind + "-" + idSource + ".json"
	return map[string]interface{}{
		"id":       "persisted-" + kind + "-" + idSource,
		"name":     filename,
		"path":     "runtime/" + filename,
		"summary":  summary,
		"kind":     "json",
		"language": "json",
		"content":  payload,
	}
}

func finalResultStatus(result interface{}) string {
	payload, ok := result.(map[string]interface{})
	if !ok || len(payload) == 0 {
		return "completed"
	}
	if planning, ok := payload["planning"].(map[string]interface{}); ok {
		if decision, ok := planning["patch_decision"].(string); ok && strings.TrimSpace(decision) == "blocked" {
			policy, _ := planning["patch_decision_policy"].(string)
			overrideApplied, _ := planning["patch_decision_override_applied"].(bool)
			if strings.TrimSpace(policy) == agent.PatchDecisionPolicyWarn || overrideApplied {
				return "completed"
			}
			return "blocked"
		}
	}
	if success, ok := payload["success"].(bool); ok && !success {
		return "failed"
	}
	return "completed"
}

type sseEmitter struct {
	w        http.ResponseWriter
	sequence int
}

func newSSEEmitter(w http.ResponseWriter) *sseEmitter {
	return &sseEmitter{w: w}
}

func (e *sseEmitter) Emit(event string, data interface{}) {
	e.sequence++
	writeSSEEventWithEnvelope(e.w, event, data, e.sequence)
}

func (h *Handler) prepareSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func (h *Handler) writeSSEEvent(w http.ResponseWriter, event string, data interface{}) {
	writeSSEEventWithEnvelope(w, event, data, 0)
}

func writeSSEEventWithEnvelope(w http.ResponseWriter, event string, data interface{}, sequence int) {
	if event != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
	}
	encoded, err := json.Marshal(wrapSSEData(event, data, sequence))
	if err != nil {
		encoded = []byte(`{"error":"failed to marshal event"}`)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func wrapSSEData(event string, data interface{}, sequence int) interface{} {
	eventMeta := map[string]interface{}{
		"name":           event,
		"schema_version": "skill_runtime.sse.v1",
		"timestamp":      time.Now().Format(time.RFC3339Nano),
	}
	if sequence > 0 {
		eventMeta["sequence"] = sequence
	}

	if payload, ok := data.(map[string]interface{}); ok {
		cloned := make(map[string]interface{}, len(payload)+1)
		for key, value := range payload {
			cloned[key] = value
		}
		cloned["_event"] = eventMeta
		return cloned
	}

	return map[string]interface{}{
		"data":   data,
		"_event": eventMeta,
	}
}

func wantsEventStream(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}

func (h *Handler) ensureHotReload() (*skill.HotReload, error) {
	if h.hotReload != nil {
		return h.hotReload, nil
	}
	if h.skillLoader == nil {
		return nil, errors.New(errors.ErrConfigInvalid, "skill loader not configured")
	}
	if h.skillRegistry == nil {
		return nil, errors.New(errors.ErrConfigInvalid, "skill registry not configured")
	}

	hotReload, err := skill.NewHotReload(h.skillLoader, h.skillRegistry)
	if err != nil {
		return nil, err
	}
	h.hotReload = hotReload
	h.attachEmbeddingHotReloadSync()
	return hotReload, nil
}

func (h *Handler) attachEmbeddingHotReloadSync() {
	if h.hotReload == nil || h.embeddingRouter == nil || h.embeddingHotReloadSyncAttached {
		return
	}

	h.hotReload.AddCallback(func(event *skill.ReloadEvent) {
		if event == nil {
			return
		}

		switch event.Type {
		case skill.ReloadEventSkillAdded, skill.ReloadEventSkillUpdated:
			if registeredSkill, ok := h.skillRegistry.Get(event.SkillName); ok {
				_ = h.embeddingRouter.IncrementalIndex(registeredSkill)
			}
			h.publishHotReloadSkillChangedEvent(event)
		case skill.ReloadEventSkillRemoved:
			_ = h.embeddingRouter.RemoveIndex(&skill.Skill{Name: event.SkillName})
			h.publishHotReloadSkillChangedEvent(event)
		case skill.ReloadEventReloadDone:
			_ = h.embeddingRouter.RebuildIndex()
		}
	})
	h.embeddingHotReloadSyncAttached = true
}

func (h *Handler) publishSkillsChangedEvent(r *http.Request, payload map[string]interface{}) {
	if h == nil {
		return
	}
	h.invalidateCodexSkillsListCache()
	if payload == nil {
		payload = map[string]interface{}{}
	}
	if _, ok := payload["skill_dirs"]; !ok {
		skillDirs := handlerSkillDirs(h.skillLoader)
		if skillDirs == nil {
			skillDirs = []string{}
		}
		payload["skill_dirs"] = append([]string{}, skillDirs...)
	}
	if _, ok := payload["count"]; !ok {
		payload["count"] = h.currentSkillCount()
	}
	if _, ok := payload["codex_list_cache_version"]; !ok {
		payload["codex_list_cache_version"] = h.currentCodexSkillsListCacheVersion()
	}

	traceID := ""
	if r != nil {
		traceID = strings.TrimSpace(logger.GetRequestID(r.Context()))
		if traceID != "" {
			payload["trace_id"] = traceID
		}
	}

	h.getRuntimeEventBus().Publish(runtimeevents.Event{
		Type:      skillsChangedEventType,
		TraceID:   traceID,
		AgentName: "skills-runtime",
		Payload:   payload,
	})
}

func (h *Handler) publishHotReloadSkillChangedEvent(event *skill.ReloadEvent) {
	if h == nil || event == nil {
		return
	}

	action := hotReloadActionForEventType(event.Type)
	if action == "" {
		return
	}

	payload := map[string]interface{}{
		"action":         action,
		"status":         "success",
		"affected_count": 1,
	}
	if name := strings.TrimSpace(event.SkillName); name != "" {
		payload["skill_name"] = name
	}
	if path := strings.TrimSpace(event.FilePath); path != "" {
		payload["skill_path"] = path
		if layer := h.skillSourceLayerForPath(path); layer != skill.SkillSourceLayerUnknown {
			payload["source_layer"] = layer
		}
	}
	if h.skillRegistry != nil {
		if registeredSkill, ok := h.skillRegistry.Get(event.SkillName); ok && registeredSkill != nil {
			if name := strings.TrimSpace(registeredSkill.Name); name != "" {
				payload["skill_name"] = name
			}
			if registeredSkill.Source != nil {
				if path := strings.TrimSpace(registeredSkill.Source.Path); path != "" {
					payload["skill_path"] = path
				}
				if layer := strings.TrimSpace(registeredSkill.Source.Layer); layer != "" {
					payload["source_layer"] = layer
				}
			}
		}
	}

	h.publishSkillsChangedEvent(nil, payload)
}

func hotReloadActionForEventType(eventType skill.ReloadEventType) string {
	switch eventType {
	case skill.ReloadEventSkillAdded:
		return skillHotReloadActionAdded
	case skill.ReloadEventSkillUpdated:
		return skillHotReloadActionUpdated
	case skill.ReloadEventSkillRemoved:
		return skillHotReloadActionRemoved
	default:
		return ""
	}
}

func (h *Handler) skillSourceLayerForPath(path string) string {
	skillDirs := handlerSkillDirs(h.skillLoader)
	if len(skillDirs) == 0 {
		return skill.SkillSourceLayerUnknown
	}

	normalizedPath := filepath.Clean(strings.TrimSpace(path))
	if normalizedPath == "." || normalizedPath == "" {
		return skill.SkillSourceLayerUnknown
	}

	for index, dir := range skillDirs {
		normalizedDir := filepath.Clean(strings.TrimSpace(dir))
		if normalizedDir == "." || normalizedDir == "" {
			continue
		}
		if normalizedPath == normalizedDir || strings.HasPrefix(normalizedPath, normalizedDir+string(os.PathSeparator)) {
			if index == 0 {
				return skill.SkillSourceLayerSystem
			}
			return skill.SkillSourceLayerExternal
		}
	}

	return skill.SkillSourceLayerUnknown
}

func (h *Handler) currentSkillCount() int {
	if h == nil || h.skillRegistry == nil {
		return 0
	}
	return h.skillRegistry.Count()
}

func skillChangePayloadFromSkill(skillItem *skill.Skill, skillDirs []string) map[string]interface{} {
	payload := make(map[string]interface{})
	if skillItem == nil {
		return payload
	}
	if name := strings.TrimSpace(skillItem.Name); name != "" {
		payload["skill_name"] = name
	}
	if skillItem.Source != nil {
		if path := strings.TrimSpace(skillItem.Source.Path); path != "" {
			payload["skill_path"] = path
		}
		if layer := strings.TrimSpace(skillItem.Source.Layer); layer != "" {
			payload["source_layer"] = layer
		}
	}
	if _, ok := payload["source_layer"]; !ok {
		if path, _ := payload["skill_path"].(string); path != "" {
			if layer := skillSourceLayerForPath(path, skillDirs); layer != skill.SkillSourceLayerUnknown {
				payload["source_layer"] = layer
			}
		}
	}
	return payload
}

func skillSourceLayerForPath(path string, skillDirs []string) string {
	normalizedPath := filepath.Clean(strings.TrimSpace(path))
	if normalizedPath == "." || normalizedPath == "" {
		return skill.SkillSourceLayerUnknown
	}
	for index, dir := range skillDirs {
		normalizedDir := filepath.Clean(strings.TrimSpace(dir))
		if normalizedDir == "." || normalizedDir == "" {
			continue
		}
		if normalizedPath == normalizedDir || strings.HasPrefix(normalizedPath, normalizedDir+string(os.PathSeparator)) {
			if index == 0 {
				return skill.SkillSourceLayerSystem
			}
			return skill.SkillSourceLayerExternal
		}
	}
	return skill.SkillSourceLayerUnknown
}

func (h *Handler) writeError(w http.ResponseWriter, statusCode int, err error) {
	response := map[string]interface{}{
		"error": err.Error(),
	}
	if requestID := strings.TrimSpace(w.Header().Get("X-Request-ID")); requestID != "" {
		response["request_id"] = requestID
	}
	if traceID := strings.TrimSpace(w.Header().Get("X-Trace-ID")); traceID != "" {
		response["trace_id"] = traceID
	}

	var runtimeErr *errors.RuntimeError
	if stderrors.As(err, &runtimeErr) {
		response["code"] = runtimeErr.Code
		response["context"] = runtimeErr.GetContext()
	}
	if preflightErr, ok := agent.AsPromptPreflightError(err); ok && preflightErr != nil {
		response["error_type"] = "prompt_preflight"
		for key, value := range preflightErr.Metadata() {
			response[key] = value
		}
	}

	h.writeJSON(w, statusCode, response)
}

func (h *Handler) writeAgentChatExecutionError(ctx context.Context, w http.ResponseWriter, statusCode int, err error, session *chat.Session, traceID string) {
	if err == nil {
		return
	}
	preparedErr, resolvedTraceID := h.prepareAgentChatExecutionError(ctx, err, session, traceID)
	if resolvedTraceID != "" {
		w.Header().Set("X-Trace-ID", resolvedTraceID)
	}
	h.writeError(w, statusCode, preparedErr)
}

func (h *Handler) prepareAgentChatExecutionError(ctx context.Context, err error, session *chat.Session, traceID string) (error, string) {
	resolvedTraceID := strings.TrimSpace(traceID)
	preflightErr, ok := agent.AsPromptPreflightError(err)
	if !ok || preflightErr == nil {
		return err, resolvedTraceID
	}

	preflightErr.ReplacementHistoryApplied = false
	replacement := preflightErr.CloneReplacementHistory()
	if resolvedTraceID == "" {
		resolvedTraceID = "trace_" + uuid.NewString()
	}
	if len(replacement) == 0 || h == nil || session == nil || h.sessionManager == nil {
		h.publishAgentChatPromptPreflightEvent(resolvedTraceID, session, err)
		return err, resolvedTraceID
	}

	originalHistory := session.GetMessages()
	session.ReplaceHistory(replacement)
	if updateErr := h.sessionManager.Update(ctx, session); updateErr != nil {
		session.ReplaceHistory(originalHistory)
		wrappedErr := fmt.Errorf("%w: failed to persist prompt preflight recovery history: %v", err, updateErr)
		h.publishAgentChatPromptPreflightEvent(resolvedTraceID, session, wrappedErr)
		return wrappedErr, resolvedTraceID
	}

	preflightErr.ReplacementHistoryApplied = true
	h.publishAgentChatPromptPreflightEvent(resolvedTraceID, session, err)
	return err, resolvedTraceID
}

func (h *Handler) publishAgentChatPromptPreflightEvent(traceID string, session *chat.Session, err error) {
	if h == nil || strings.TrimSpace(traceID) == "" || err == nil {
		return
	}
	preflightErr, ok := agent.AsPromptPreflightError(err)
	if !ok || preflightErr == nil {
		return
	}
	payload := map[string]interface{}{
		"success":     false,
		"error":       err.Error(),
		"error_type":  "prompt_preflight",
		"source":      "agent_chat",
		"entrypoint":  canonicalAgentChatEntrypoint,
		"session_id":  sessionID(session),
		"request_end": true,
	}
	for key, value := range preflightErr.Metadata() {
		payload[key] = value
	}
	h.publishSessionRuntimeEvent(chat.EventSessionEnd, traceID, sessionID(session), payload)
}

func (h *Handler) runtimeRetryEventReporter(traceID, sessionID string) llm.RetryEventReporter {
	if h == nil {
		return nil
	}
	traceID = strings.TrimSpace(traceID)
	sessionID = strings.TrimSpace(sessionID)
	return func(event llm.RetryEvent) {
		payload := map[string]interface{}{
			"source": strings.TrimSpace(event.Source),
		}
		if provider := strings.TrimSpace(event.Provider); provider != "" {
			payload["provider"] = provider
		}
		if protocol := strings.TrimSpace(event.Protocol); protocol != "" {
			payload["protocol"] = protocol
		}
		if model := strings.TrimSpace(event.Model); model != "" {
			payload["model"] = model
		}
		if event.Attempt > 0 {
			payload["attempt"] = event.Attempt
		}
		if event.MaxAttempts > 0 {
			payload["max_attempts"] = event.MaxAttempts
		}
		if reason := strings.TrimSpace(event.RetryReason); reason != "" {
			payload["retry_reason"] = reason
		}
		if event.RetryDelayMS > 0 {
			payload["retry_delay_ms"] = event.RetryDelayMS
		}
		if errText := strings.TrimSpace(event.Error); errText != "" {
			payload["error"] = errText
		}
		if sessionID != "" {
			h.publishSessionRuntimeEvent("llm.retry", traceID, sessionID, payload)
			return
		}
		h.publishRuntimeEvent("llm.retry", traceID, payload)
	}
}

func runtimeHTTPDebugReporter(ctx context.Context) llm.HTTPDebugReporter {
	return func(event llm.HTTPDebugEvent) {
		fields := make([]zapcore.Field, 0, 12)
		if value := strings.TrimSpace(event.Source); value != "" {
			fields = append(fields, logger.String("http_debug_source", value))
		}
		if value := strings.TrimSpace(event.Phase); value != "" {
			fields = append(fields, logger.String("http_debug_phase", value))
		}
		if value := strings.TrimSpace(event.Provider); value != "" {
			fields = append(fields, logger.String("upstream_provider", value))
		}
		if value := strings.TrimSpace(event.Protocol); value != "" {
			fields = append(fields, logger.String("upstream_protocol", value))
		}
		if value := strings.TrimSpace(event.Model); value != "" {
			fields = append(fields, logger.String("upstream_model", value))
		}
		if value := strings.TrimSpace(event.Method); value != "" {
			fields = append(fields, logger.Method(value))
		}
		if value := strings.TrimSpace(event.URL); value != "" {
			fields = append(fields, logger.URL(value))
		}
		if event.Attempt > 0 {
			fields = append(fields, logger.Int("attempt", event.Attempt))
		}
		if event.MaxAttempts > 0 {
			fields = append(fields, logger.Int("max_attempts", event.MaxAttempts))
		}
		if value := strings.TrimSpace(event.RetryReason); value != "" {
			fields = append(fields, logger.String("retry_reason", value))
		}
		if event.RetryDelayMS > 0 {
			fields = append(fields, logger.Int64("retry_delay_ms", event.RetryDelayMS))
		}
		if event.RequestBodyBytes > 0 {
			fields = append(fields, logger.Int("request_body_bytes", event.RequestBodyBytes))
		}
		if debug := llm.HTTPDebugRequestDiagnostics(event.RequestMetadata); len(debug) > 0 {
			for _, key := range []string{"request_sha256", "cache_surface_sha256", "input_sha256", "tools_sha256", "prompt_layout_sha256"} {
				if value := strings.TrimSpace(stringMapValueAny(debug, key)); value != "" {
					fields = append(fields, logger.String(key, value))
				}
			}
			for _, key := range []string{"message_count", "input_count", "tool_count", "instructions_length", "prompt_layout_length"} {
				if value := intMapValueAny(debug, key); value > 0 {
					fields = append(fields, logger.Int(key, value))
				}
			}
			if value := strings.TrimSpace(stringMapValueAny(debug, "prompt_cache_key")); value != "" {
				fields = append(fields, logger.String("prompt_cache_key", value))
			}
		}
		if event.ResponseStatusCode > 0 {
			fields = append(fields, logger.Int("response_status_code", event.ResponseStatusCode))
		}
		if event.ResponseBodyBytes > 0 {
			fields = append(fields, logger.Int("response_body_bytes", event.ResponseBodyBytes))
		}
		if value := strings.TrimSpace(event.ResponseBodyPreview); value != "" {
			fields = append(fields, logger.String("response_body_preview", value))
		}
		if value := strings.TrimSpace(event.Error); value != "" {
			fields = append(fields, logger.String("upstream_error", value))
		}

		switch {
		case strings.TrimSpace(event.Error) != "" || event.ResponseStatusCode >= http.StatusBadRequest:
			logger.CtxError(ctx, "LLM upstream request failed", fields...)
		case logger.L().Core().Enabled(zapcore.DebugLevel):
			message := "LLM upstream response"
			if strings.EqualFold(strings.TrimSpace(event.Phase), "request") {
				message = "LLM upstream request"
			}
			logger.CtxDebug(ctx, message, fields...)
		}
	}
}

// SkillStats Skill 统计信息
type SkillStats struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	CallCount   int    `json:"call_count"`
	SuccessRate int    `json:"success_rate"`
	AvgDuration int    `json:"avg_duration_ms"`
	SourceDir   string `json:"source_dir,omitempty"`
	SourcePath  string `json:"source_path,omitempty"`
	SourceLayer string `json:"source_layer,omitempty"`
}

type mcpStatusReader interface {
	ListMCPs() []*mcpconfig.MCPStatus
}

type mcpAdapterReader interface {
	GetManager() mcpmanager.Manager
}

func (h *Handler) runtimeStatusSnapshot(ctx context.Context, mode llm.HealthCheckMode) map[string]interface{} {
	providers := make([]map[string]interface{}, 0)
	if h.llmRuntime != nil {
		healthCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		if mode == "" {
			mode = llm.HealthCheckModeStale
		}
		h.llmRuntime.CheckHealthWithMode(healthCtx, mode)
		healthMap := h.llmRuntime.ProviderHealthSnapshot()
		for _, name := range h.llmRuntime.ListProviders() {
			item := map[string]interface{}{
				"name": name,
			}
			if caps, err := h.llmRuntime.GetCapabilities(name); err == nil && caps != nil {
				item["supports_tools"] = caps.SupportsTools
				item["supports_streaming"] = caps.SupportsStreaming
				item["max_context_tokens"] = caps.MaxContextTokens
				item["max_output_tokens"] = caps.MaxOutputTokens
			}
			if health, ok := healthMap[name]; ok {
				item["status"] = string(health.Status)
				item["healthy"] = health.Status == llm.HealthStatusHealthy
				item["consecutive_failures"] = health.ConsecutiveFailures
				item["consecutive_successes"] = health.ConsecutiveSuccesses
				if !health.LastCheckTime.IsZero() {
					item["last_check"] = health.LastCheckTime
				}
				if !health.LastSuccessTime.IsZero() {
					item["last_success"] = health.LastSuccessTime
				}
				if !health.LastFailureTime.IsZero() {
					item["last_failure"] = health.LastFailureTime
				}
				if health.LastError != "" {
					item["error"] = health.LastError
				}
			} else {
				item["status"] = string(llm.HealthStatusUnknown)
				item["healthy"] = false
			}
			providers = append(providers, item)
		}
		sort.Slice(providers, func(i, j int) bool {
			return providers[i]["name"].(string) < providers[j]["name"].(string)
		})
	}

	mcps := make([]map[string]interface{}, 0)
	var statusReader mcpStatusReader
	if reader, ok := h.mcpManager.(mcpStatusReader); ok && reader != nil {
		statusReader = reader
	} else if manager := h.runtimeMCPManager(); manager != nil {
		if reader, ok := manager.(mcpStatusReader); ok {
			statusReader = reader
		}
	}
	if statusReader != nil {
		for _, status := range statusReader.ListMCPs() {
			if status == nil {
				continue
			}
			mcps = append(mcps, map[string]interface{}{
				"name":           status.Name,
				"type":           status.Type,
				"trust_level":    status.TrustLevel,
				"execution_mode": status.ExecutionMode,
				"enabled":        status.Enabled,
				"connected":      status.Connected,
				"tool_count":     status.ToolCount,
				"last_error":     status.LastError,
				"last_connect":   status.LastConnect,
				"health_check":   status.HealthCheck,
			})
		}
		sort.Slice(mcps, func(i, j int) bool {
			return mcps[i]["name"].(string) < mcps[j]["name"].(string)
		})
	}

	patchGovernance := map[string]interface{}{
		"decisions":             0,
		"blocked":               0,
		"approved":              0,
		"approved_override":     0,
		"approvals_with_ticket": 0,
		"policies":              map[string]int{},
		"trace_count":           0,
		"latest_trace_ids":      []string{},
	}
	provenance := map[string]interface{}{
		"profile_context_injected": 0,
		"recall_with_source_refs":  0,
		"profile_resource_refs":    []string{},
		"profile_resource_kinds":   map[string]int{},
		"profile_resource_count":   0,
		"profile_memory_count":     0,
		"profile_notes_count":      0,
		"profile_resource_labels":  []string{},
		"trace_count":              0,
		"latest_trace_ids":         []string{},
	}
	if bus := h.getRuntimeEventBus(); bus != nil {
		traceStats := bus.TraceStats(runtimeevents.TraceFilter{Limit: 50})
		patchGovernance = buildPatchGovernanceSummaryFromView(traceStats.Governance)
		patchGovernance["trace_count"] = traceStats.TraceCount
		patchGovernance["latest_trace_ids"] = traceStats.LatestTraceIDs
		provenance = buildProvenanceSummaryFromView(traceStats.Provenance)
		provenance["trace_count"] = traceStats.TraceCount
		provenance["latest_trace_ids"] = traceStats.LatestTraceIDs
	}

	contextSnapshot := map[string]interface{}{
		"profile": "balanced",
	}
	if h.runtimeConfig != nil {
		contextSnapshot = contextSnapshotFromRuntimeConfig(h.runtimeConfig)
	}

	toolCatalog := map[string]interface{}{
		"backend":       "memory",
		"snapshot_path": "",
		"tool_count":    0,
		"added":         0,
		"removed":       0,
		"updated":       0,
	}
	backend, resolvedPath, _ := h.runtimeToolCatalogConfig()
	if backend == "file" {
		toolCatalog["backend"] = "file"
		toolCatalog["snapshot_path"] = resolvedPath
	} else if backend == "sqlite" {
		toolCatalog["backend"] = "sqlite"
		toolCatalog["snapshot_path"] = resolvedPath
	}
	if gateway := h.getRuntimeToolCatalogGateway(); gateway != nil {
		stats := gateway.RefreshStats()
		toolCatalog = map[string]interface{}{
			"backend":         toolCatalog["backend"],
			"snapshot_path":   toolCatalog["snapshot_path"],
			"tool_count":      stats.ToolCount,
			"added":           stats.Added,
			"removed":         stats.Removed,
			"updated":         stats.Updated,
			"last_refresh_at": stats.LastRefreshAt,
		}
	}

	return map[string]interface{}{
		"default_provider":    defaultAgentProvider(h.llmRuntime),
		"default_model":       defaultAgentModel(h.llmRuntime),
		"providers":           providers,
		"provider_count":      len(providers),
		"mcps":                mcps,
		"mcp_count":           len(mcps),
		"context":             contextSnapshot,
		"session_persistence": h.sessionPersistenceSnapshot(),
		"tool_catalog":        toolCatalog,
		"patch_governance":    patchGovernance,
		"provenance":          provenance,
	}
}

func (h *Handler) sessionPersistenceSnapshot() map[string]interface{} {
	if h == nil {
		return map[string]interface{}{}
	}
	paths := sessionruntime.ResolvePaths(sessionruntime.ResolveOptions{
		Config:     h.runtimeConfig,
		ConfigFile: h.runtimeConfigFile,
		Mode:       sessionruntime.ModeServer,
	})
	result := map[string]interface{}{
		"config_file":                  h.runtimeConfigFile,
		"session_dir":                  paths.SessionDir,
		"runtime_dir":                  paths.RuntimeDir,
		"session_runtime_store_path":   paths.SessionRuntimeStorePath,
		"legacy_runtime_store_path":    paths.LegacySessionRuntimeStorePath,
		"team_store_path":              paths.TeamStorePath,
		"agent_control_store_path":     paths.AgentControlStorePath,
		"artifact_store_path":          paths.ArtifactStorePath,
		"background_store_path":        paths.BackgroundStorePath,
		"background_log_dir":           paths.BackgroundLogDir,
		"default_persistence":          paths.DefaultPersistence,
		"file_defaults_enabled":        paths.FileDefaultsEnabled,
		"session_runtime_store_active": h.sessionRuntimeStoreKey,
		"team_store_active":            h.teamStoreConfigKey,
		"background_active":            h.backgroundConfigKey,
	}
	if paths.AgentControlMailboxStorePath != "" {
		result["agent_control_mailbox_store_path"] = paths.AgentControlMailboxStorePath
	}
	if paths.AgentControlAgentStorePath != "" {
		result["agent_control_agent_store_path"] = paths.AgentControlAgentStorePath
	}
	if h.sessionManager != nil && h.sessionManager.GetStorage() != nil {
		if dirReader, ok := h.sessionManager.GetStorage().(interface{ Dir() string }); ok {
			result["session_store_dir"] = dirReader.Dir()
		}
	}
	if h.runtimeConfig != nil {
		result["checkpoint_enabled"] = h.runtimeConfig.Checkpoint.Enabled
	}
	return result
}

// RuntimeStatusSnapshot 导出 runtime 状态快照
func (h *Handler) RuntimeStatusSnapshot(ctx context.Context, mode llm.HealthCheckMode) map[string]interface{} {
	return h.runtimeStatusSnapshot(ctx, mode)
}

// RuntimeValidationSnapshot 导出 runtime 校验快照
func (h *Handler) RuntimeValidationSnapshot() map[string]interface{} {
	return h.runtimeValidationSnapshot()
}

// RuntimeHealthSummary 导出 runtime 健康摘要
func (h *Handler) RuntimeHealthSummary(runtime map[string]interface{}) map[string]interface{} {
	return runtimeHealthSummary(runtime)
}

func runtimeHealthSummary(runtime map[string]interface{}) map[string]interface{} {
	if runtime == nil {
		return map[string]interface{}{
			"healthy": false,
			"issues":  []string{"runtime status unavailable"},
		}
	}

	issues := make([]string, 0)
	healthyProviders := 0
	degradedProviders := 0
	unhealthyProviders := 0
	unknownProviders := 0
	if providers, ok := runtime["providers"].([]map[string]interface{}); ok {
		for _, provider := range providers {
			name, _ := provider["name"].(string)
			status, _ := provider["status"].(string)
			if status == "" {
				if healthy, ok := provider["healthy"].(bool); ok {
					if healthy {
						status = string(llm.HealthStatusHealthy)
					} else {
						status = string(llm.HealthStatusUnhealthy)
					}
				} else {
					status = string(llm.HealthStatusUnknown)
				}
			}

			switch status {
			case string(llm.HealthStatusHealthy):
				healthyProviders++
			case string(llm.HealthStatusDegraded):
				degradedProviders++
				unhealthyProviders++
			case string(llm.HealthStatusUnhealthy):
				unhealthyProviders++
			default:
				unknownProviders++
			}

			if status != string(llm.HealthStatusHealthy) {
				errText, _ := provider["error"].(string)
				statusText := status
				if statusText == string(llm.HealthStatusUnknown) {
					statusText = "status unknown"
				}
				if errText != "" {
					issues = append(issues, fmt.Sprintf("provider %s %s: %s", name, statusText, errText))
				} else {
					issues = append(issues, fmt.Sprintf("provider %s %s", name, statusText))
				}
			}
		}
	}

	connectedMCPs := 0
	disconnectedMCPs := 0
	if mcps, ok := runtime["mcps"].([]map[string]interface{}); ok {
		for _, mcp := range mcps {
			enabled, _ := mcp["enabled"].(bool)
			connected, _ := mcp["connected"].(bool)
			name, _ := mcp["name"].(string)
			if enabled && connected {
				connectedMCPs++
			} else if enabled {
				disconnectedMCPs++
				issues = append(issues, fmt.Sprintf("mcp %s is enabled but not connected", name))
			}
		}
	}

	return map[string]interface{}{
		"healthy":             len(issues) == 0,
		"healthy_providers":   healthyProviders,
		"degraded_providers":  degradedProviders,
		"unhealthy_providers": unhealthyProviders,
		"unknown_providers":   unknownProviders,
		"connected_mcps":      connectedMCPs,
		"disconnected_mcps":   disconnectedMCPs,
		"issues":              issues,
	}
}

func (h *Handler) runtimeMCPManager() mcpmanager.Manager {
	if h == nil || h.mcpManager == nil {
		return nil
	}
	if adapter, ok := h.mcpManager.(mcpAdapterReader); ok && adapter != nil {
		return adapter.GetManager()
	}
	return nil
}

func (h *Handler) runtimeValidationSnapshot() map[string]interface{} {
	issues := make([]string, 0)
	warnings := make([]string, 0)
	configIssues := make([]string, 0)
	configWarnings := make([]string, 0)

	if h.skillRegistry == nil {
		issues = append(issues, "skill registry is not configured")
	}

	skillCount := 0
	if h.skillRegistry != nil {
		skillCount = len(h.skillRegistry.List())
		if skillCount == 0 {
			warnings = append(warnings, "no skills loaded")
		}
	}

	skillDirs := handlerSkillDirs(h.skillLoader)
	if len(skillDirs) == 0 {
		warnings = append(warnings, "no skill directories configured")
	} else {
		for _, dir := range skillDirs {
			if dir == "" {
				continue
			}
			if _, err := os.Stat(dir); err != nil {
				if os.IsNotExist(err) {
					warnings = append(warnings, buildMissingPathMessage("skill directory not found", dir))
				} else {
					warnings = append(warnings, fmt.Sprintf("skill directory not accessible: %s", dir))
				}
			}
		}
	}

	if h.llmRuntime == nil {
		warnings = append(warnings, "llm runtime is not configured")
	} else {
		providers := h.llmRuntime.ListProviders()
		if len(providers) == 0 {
			issues = append(issues, "no llm providers registered")
		}
		defaultModel := defaultAgentModel(h.llmRuntime)
		if defaultModel == "" {
			warnings = append(warnings, "default model is empty")
		} else if _, err := h.llmRuntime.GetProvider(defaultModel); err != nil {
			issues = append(issues, fmt.Sprintf("default model/provider not registered: %s", defaultModel))
		}
	}

	hasEmbeddingSkill := false
	hasToolBackedSkill := false
	if h.skillRegistry != nil {
		for _, skillItem := range h.skillRegistry.List() {
			if skillItem == nil {
				continue
			}
			if !hasToolBackedSkill && (len(skillItem.Tools) > 0 || skillItem.HasWorkflow()) {
				hasToolBackedSkill = true
			}
			if !hasEmbeddingSkill {
				for _, trigger := range skillItem.Triggers {
					if trigger.Type == "embedding" {
						hasEmbeddingSkill = true
						break
					}
				}
			}
		}
	}
	if hasEmbeddingSkill && h.embeddingRouter == nil {
		warnings = append(warnings, "embedding-triggered skills exist but embedding router is not configured")
	}
	if hasToolBackedSkill && h.mcpManager == nil {
		warnings = append(warnings, "tool-backed skills exist but mcp manager is not configured")
	}
	if h.sessionManager == nil {
		warnings = append(warnings, "session manager is not configured")
	}

	if h.runtimeConfigFile != "" {
		info, err := os.Stat(h.runtimeConfigFile)
		if err != nil {
			if os.IsNotExist(err) {
				configWarnings = append(configWarnings, buildMissingPathMessage("runtime config file not found", h.runtimeConfigFile))
			} else {
				configIssues = append(configIssues, fmt.Sprintf("runtime config file not accessible: %s", h.runtimeConfigFile))
			}
		} else if info.IsDir() {
			configIssues = append(configIssues, fmt.Sprintf("runtime config path is a directory: %s", h.runtimeConfigFile))
		}
	}

	if h.runtimeConfig != nil {
		if err := runtimecfg.ValidateRuntimeConfig(h.runtimeConfig); err != nil {
			configIssues = append(configIssues, fmt.Sprintf("runtime config invalid: %s", err.Error()))
		}
		if h.runtimeConfig.Router.EnableEmbedding && !h.runtimeConfig.Embedding.Enabled {
			configWarnings = append(configWarnings, "embedding router enabled but embedding is disabled")
		}
		if h.runtimeConfig.Embedding.Enabled && !h.runtimeConfig.Router.EnableEmbedding {
			configWarnings = append(configWarnings, "embedding enabled but router embedding is disabled")
		}
		if h.runtimeConfig.HotReload.Enabled && len(handlerSkillDirs(h.skillLoader)) == 0 {
			configWarnings = append(configWarnings, "hot reload enabled but no skill directories configured")
		}
		workspaceRoot := strings.TrimSpace(h.runtimeConfig.Workspace.Root)
		if workspaceRoot != "" {
			if _, err := os.Stat(workspaceRoot); err != nil {
				if os.IsNotExist(err) {
					configWarnings = append(configWarnings, buildMissingPathMessage("workspace root not found", workspaceRoot))
				} else {
					configWarnings = append(configWarnings, fmt.Sprintf("workspace root not accessible: %s", workspaceRoot))
				}
			}
		}
		if h.runtimeConfig.Rollout.Enabled {
			candidateFile := strings.TrimSpace(h.runtimeConfig.Rollout.CandidateFile)
			if candidateFile != "" {
				if _, err := os.Stat(candidateFile); err != nil {
					if os.IsNotExist(err) {
						configWarnings = append(configWarnings, buildMissingPathMessage("rollout candidate file not found", candidateFile))
					} else {
						configWarnings = append(configWarnings, fmt.Sprintf("rollout candidate file not accessible: %s", candidateFile))
					}
				}
			}
		}
	}

	if len(configIssues) > 0 {
		issues = append(issues, configIssues...)
	}
	if len(configWarnings) > 0 {
		warnings = append(warnings, configWarnings...)
	}

	return map[string]interface{}{
		"healthy":       len(issues) == 0,
		"issue_count":   len(issues),
		"warning_count": len(warnings),
		"issues":        issues,
		"warnings":      warnings,
		"skill_count":   skillCount,
		"skill_dirs":    skillDirs,
		"default_model": defaultAgentModel(h.llmRuntime),
		"config": map[string]interface{}{
			"file":     h.runtimeConfigFile,
			"valid":    len(configIssues) == 0,
			"issues":   configIssues,
			"warnings": configWarnings,
			"version":  runtimeConfigVersion(h.runtimeConfig),
			"rollout":  runtimeConfigRollout(h.runtimeConfig),
		},
	}
}

func runtimeConfigVersion(config *runtimecfg.RuntimeConfig) string {
	if config == nil {
		return ""
	}
	return config.Version
}

func runtimeConfigRollout(config *runtimecfg.RuntimeConfig) interface{} {
	if config == nil {
		return nil
	}
	return config.Rollout
}

func (h *Handler) agentCapabilityDescriptor() *capability.Descriptor {
	cfg := runtimecfg.AgentConfig{}
	if h.runtimeConfig != nil {
		cfg = h.runtimeConfig.Agent
	}
	model := cfg.DefaultModel
	if model == "" {
		model = defaultAgentModel(h.llmRuntime)
	}
	if model == "" {
		return nil
	}
	return &capability.Descriptor{
		ID:           "api-agent",
		Name:         "api-agent",
		Kind:         capability.KindAgent,
		Description:  "Unified skill orchestration entry point",
		Capabilities: []string{"route", "execute", "orchestrate"},
		Metadata: map[string]interface{}{
			"model":           model,
			"max_steps":       cfg.MaxMaxSteps,
			"timeout":         cfg.Timeout,
			"enable_memory":   cfg.EnableMemory,
			"enable_planning": cfg.EnablePlanning,
		},
	}
}

// GetStats 获取统计信息
func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	layer, dir := parseSkillSourceFilters(r)
	skills := filterSkillsBySource(h.skillRegistry.List(), layer, dir)
	stats := make([]SkillStats, 0, len(skills))

	for _, s := range skills {
		stat := SkillStats{
			Name:        s.Name,
			Category:    s.Category,
			CallCount:   0,
			SuccessRate: 0,
		}
		if s.Source != nil {
			stat.SourceDir = s.Source.Dir
			stat.SourcePath = s.Source.Path
			stat.SourceLayer = s.Source.Layer
		}
		stats = append(stats, stat)
	}

	response := map[string]interface{}{
		"stats":                 stats,
		"total_skills":          len(skills),
		"skill_dirs":            handlerSkillDirs(h.skillLoader),
		"source_summary":        buildSkillSourceSummary(skills),
		"mutation_policy":       h.mutationPolicySnapshot(),
		"usage_policy":          h.usagePolicySnapshot(),
		"scope_resolver_policy": h.scopeResolverPolicySnapshot(),
		"search":                h.searchTelemetrySnapshot(),
		"embedding": map[string]interface{}{
			"enabled": h.embeddingRouter != nil,
			"stats":   embeddingRouterStats(h.embeddingRouter),
		},
		"runtime":    h.runtimeStatusSnapshot(r.Context(), llm.HealthCheckModeStale),
		"validation": h.runtimeValidationSnapshot(),
	}
	if err := h.attachProfileMetadata(r, response); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}

	h.writeJSON(w, http.StatusOK, response)
}

// GetRuntimeStatus 获取 provider / MCP 运行时状态
func (h *Handler) GetRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	mode := parseHealthRecheckMode(r)
	runtime := h.runtimeStatusSnapshot(r.Context(), mode)
	payload := map[string]interface{}{
		"runtime": runtime,
	}
	if err := h.attachProfileMetadata(r, payload); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, payload)
}

// GetRuntimeHealth 获取 provider / MCP 健康摘要
func (h *Handler) GetRuntimeHealth(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	mode := parseHealthRecheckMode(r)
	runtime := h.runtimeStatusSnapshot(r.Context(), mode)
	payload := map[string]interface{}{
		"runtime": runtime,
		"health":  runtimeHealthSummary(runtime),
	}
	if err := h.attachProfileMetadata(r, payload); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, payload)
}

// GetRuntimeTrace 获取指定 trace 的 runtime 审计事件。
func (h *Handler) GetRuntimeTraces(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	limit, err := parseRuntimeTraceLimit(r, 20, 200)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	filters := runtimeevents.TraceFilter{
		TraceIDPrefix:       strings.TrimSpace(r.URL.Query().Get("trace_prefix")),
		SessionID:           strings.TrimSpace(r.URL.Query().Get("session_id")),
		AgentName:           strings.TrimSpace(r.URL.Query().Get("agent_name")),
		ToolName:            strings.TrimSpace(r.URL.Query().Get("tool_name")),
		EventType:           strings.TrimSpace(r.URL.Query().Get("event_type")),
		TeamID:              strings.TrimSpace(r.URL.Query().Get("team_id")),
		ProfileResourceKind: strings.TrimSpace(r.URL.Query().Get("profile_resource_kind")),
		Limit:               limit,
	}

	traces := h.getRuntimeEventBus().RecentTraces(filters)
	rawTeamIDLimit := strings.TrimSpace(r.URL.Query().Get("team_id_limit"))
	teamIDLimitSource := "default"
	if rawTeamIDLimit != "" {
		teamIDLimitSource = "query"
	} else if h.runtimeTeamIDLimit() > 0 {
		teamIDLimitSource = "config"
	}
	teamIDLimit, err := parseRuntimeTeamIDLimit(r, h.runtimeTeamIDLimit(), 50)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("X-AI-Gateway-Team-ID-Limit", strconv.Itoa(teamIDLimit))
	w.Header().Set("X-AI-Gateway-Team-ID-Limit-Source", teamIDLimitSource)
	teamIDTruncated := false
	if teamIDLimit > 0 {
		for _, trace := range traces {
			if len(trace.TeamIDs) > teamIDLimit {
				teamIDTruncated = true
				break
			}
		}
	}
	if teamIDLimit > 0 {
		traces = limitTraceTeamIDs(traces, teamIDLimit)
	}
	payload := map[string]interface{}{
		"count":                len(traces),
		"traces":               traces,
		"recovery":             buildRecoverySummaryFromTraceSummaries(traces),
		"team_count":           countTraceTeams(traces),
		"team_id_limit":        teamIDLimit,
		"team_id_limit_source": teamIDLimitSource,
		"team_id_truncated":    teamIDTruncated,
		"filters": map[string]interface{}{
			"trace_prefix":          filters.TraceIDPrefix,
			"session_id":            filters.SessionID,
			"agent_name":            filters.AgentName,
			"tool_name":             filters.ToolName,
			"event_type":            filters.EventType,
			"team_id":               filters.TeamID,
			"profile_resource_kind": filters.ProfileResourceKind,
			"limit":                 filters.Limit,
			"team_id_limit":         teamIDLimit,
			"team_id_limit_source":  teamIDLimitSource,
			"team_id_truncated":     teamIDTruncated,
		},
	}
	if err := h.attachProfileMetadata(r, payload); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, payload)
}

// GetRuntimeTraceStats 获取最近 traces 的聚合统计。
func (h *Handler) GetRuntimeTraceStats(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	limit, err := parseRuntimeTraceLimit(r, 50, 500)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	teamLimit, teamLimitSource := h.runtimeTeamIDLimitMeta()
	w.Header().Set("X-AI-Gateway-Team-ID-Limit", strconv.Itoa(teamLimit))
	w.Header().Set("X-AI-Gateway-Team-ID-Limit-Source", teamLimitSource)

	filters := runtimeevents.TraceFilter{
		TraceIDPrefix:       strings.TrimSpace(r.URL.Query().Get("trace_prefix")),
		SessionID:           strings.TrimSpace(r.URL.Query().Get("session_id")),
		AgentName:           strings.TrimSpace(r.URL.Query().Get("agent_name")),
		ToolName:            strings.TrimSpace(r.URL.Query().Get("tool_name")),
		EventType:           strings.TrimSpace(r.URL.Query().Get("event_type")),
		TeamID:              strings.TrimSpace(r.URL.Query().Get("team_id")),
		ProfileResourceKind: strings.TrimSpace(r.URL.Query().Get("profile_resource_kind")),
		Limit:               limit,
	}

	stats := h.getRuntimeEventBus().TraceStats(filters)
	payload := map[string]interface{}{
		"stats":                stats,
		"patch_governance":     buildPatchGovernanceSummaryFromView(stats.Governance),
		"recovery":             buildRecoverySummaryFromView(stats.Recovery),
		"team_count":           stats.TeamCount,
		"team_id_limit":        teamLimit,
		"team_id_limit_source": teamLimitSource,
		"filters": map[string]interface{}{
			"trace_prefix":          filters.TraceIDPrefix,
			"session_id":            filters.SessionID,
			"agent_name":            filters.AgentName,
			"tool_name":             filters.ToolName,
			"event_type":            filters.EventType,
			"team_id":               filters.TeamID,
			"profile_resource_kind": filters.ProfileResourceKind,
			"limit":                 filters.Limit,
		},
	}
	if err := h.attachProfileMetadata(r, payload); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, payload)
}

// GetRuntimeTraceGovernance 获取最近 traces 的治理导出视图。
func (h *Handler) GetRuntimeTraceGovernance(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	limit, err := parseRuntimeTraceLimit(r, 50, 500)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	teamLimit, teamLimitSource := h.runtimeTeamIDLimitMeta()
	w.Header().Set("X-AI-Gateway-Team-ID-Limit", strconv.Itoa(teamLimit))
	w.Header().Set("X-AI-Gateway-Team-ID-Limit-Source", teamLimitSource)

	filters := runtimeevents.TraceFilter{
		TraceIDPrefix:       strings.TrimSpace(r.URL.Query().Get("trace_prefix")),
		SessionID:           strings.TrimSpace(r.URL.Query().Get("session_id")),
		AgentName:           strings.TrimSpace(r.URL.Query().Get("agent_name")),
		ToolName:            strings.TrimSpace(r.URL.Query().Get("tool_name")),
		EventType:           strings.TrimSpace(r.URL.Query().Get("event_type")),
		TeamID:              strings.TrimSpace(r.URL.Query().Get("team_id")),
		ProfileResourceKind: strings.TrimSpace(r.URL.Query().Get("profile_resource_kind")),
		Limit:               limit,
	}

	allTraces := h.getRuntimeEventBus().RecentTraces(filters)
	deniedTraces := make([]runtimeevents.TraceSummary, 0, len(allTraces))
	for _, trace := range allTraces {
		if trace.Governance.DeniedEvents > 0 || trace.Governance.PatchDecisions > 0 {
			deniedTraces = append(deniedTraces, trace)
		}
	}

	governanceStats := h.getRuntimeEventBus().GovernanceStats(filters)
	payload := map[string]interface{}{
		"stats":                governanceStats,
		"patch_governance":     buildPatchGovernanceSummaryFromStats(governanceStats),
		"provenance":           buildProvenanceSummaryFromView(governanceStats.Provenance),
		"traces":               deniedTraces,
		"count":                len(deniedTraces),
		"team_count":           governanceStats.TeamCount,
		"team_id_limit":        teamLimit,
		"team_id_limit_source": teamLimitSource,
		"filters": map[string]interface{}{
			"trace_prefix":          filters.TraceIDPrefix,
			"session_id":            filters.SessionID,
			"agent_name":            filters.AgentName,
			"tool_name":             filters.ToolName,
			"event_type":            filters.EventType,
			"team_id":               filters.TeamID,
			"profile_resource_kind": filters.ProfileResourceKind,
			"limit":                 filters.Limit,
		},
	}
	if err := h.attachProfileMetadata(r, payload); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, payload)
}

// GetRuntimeTrace 获取指定 trace 的 runtime 审计事件。
func (h *Handler) GetRuntimeTrace(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	traceID := strings.TrimSpace(mux.Vars(r)["trace_id"])
	if traceID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "trace_id is required"))
		return
	}

	limit, err := parseRuntimeTraceLimit(r, 200, 1000)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}

	bus := h.getRuntimeEventBus()
	events := bus.Query(runtimeevents.QueryFilter{
		TraceID:   traceID,
		SessionID: strings.TrimSpace(r.URL.Query().Get("session_id")),
		AgentName: strings.TrimSpace(r.URL.Query().Get("agent_name")),
		ToolName:  strings.TrimSpace(r.URL.Query().Get("tool_name")),
		EventType: strings.TrimSpace(r.URL.Query().Get("event_type")),
		TeamID:    strings.TrimSpace(r.URL.Query().Get("team_id")),
		Limit:     limit,
	})
	summary := summarizeRuntimeTrace(events)
	patchGovernance := map[string]interface{}{
		"decisions":             0,
		"blocked":               0,
		"approved":              0,
		"approved_override":     0,
		"approvals_with_ticket": 0,
		"policies":              map[string]int{},
	}
	promptSummary := map[string]interface{}{
		"layouts_observed":   0,
		"instruction_chars":  0,
		"total_chars":        0,
		"instruction_tokens": 0,
		"total_tokens":       0,
		"layers":             map[string]int{},
		"sources":            []string{},
		"source_count":       0,
	}
	recoverySummary := map[string]interface{}{
		"prompt_preflight_events":        0,
		"prompt_preflight_by_event_type": map[string]int{},
		"prompt_preflight_failure_codes": map[string]int{},
		"replacement_history_available":  0,
		"replacement_history_applied":    0,
		"summary_failure_events":         0,
		"summary_failure_reasons":        map[string]int{},
		"summary_fallbacks":              0,
		"summary_fallback_reasons":       map[string]int{},
	}
	if summaryGovernance, ok := summary["governance"].(map[string]interface{}); ok {
		patchGovernance = patchGovernanceSummaryFromMap(summaryGovernance)
	}
	if summaryPrompt, ok := summary["prompt"].(map[string]interface{}); ok {
		promptSummary = promptSummaryFromMap(summaryPrompt)
	}
	if summaryRecovery, ok := summary["recovery"].(map[string]interface{}); ok {
		recoverySummary = recoverySummaryFromMap(summaryRecovery)
	}
	payload := map[string]interface{}{
		"trace_id":         traceID,
		"count":            len(events),
		"events":           events,
		"summary":          summary,
		"patch_governance": patchGovernance,
		"prompt":           promptSummary,
		"recovery":         recoverySummary,
		"team_count":       countTeamIDsFromSummary(summary),
		"filters": map[string]interface{}{
			"session_id": strings.TrimSpace(r.URL.Query().Get("session_id")),
			"agent_name": strings.TrimSpace(r.URL.Query().Get("agent_name")),
			"tool_name":  strings.TrimSpace(r.URL.Query().Get("tool_name")),
			"event_type": strings.TrimSpace(r.URL.Query().Get("event_type")),
			"team_id":    strings.TrimSpace(r.URL.Query().Get("team_id")),
			"limit":      limit,
		},
	}
	if err := h.attachProfileMetadata(r, payload); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, payload)
}

func parseRuntimeTraceLimit(r *http.Request, defaultLimit, maxLimit int) (int, error) {
	limit := defaultLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed <= 0 {
			return 0, errors.New(errors.ErrValidationFailed, "invalid limit value")
		}
		limit = parsed
	}
	if maxLimit > 0 && limit > maxLimit {
		limit = maxLimit
	}
	return limit, nil
}

func countTraceTeams(traces []runtimeevents.TraceSummary) int {
	teams := make(map[string]bool)
	for _, trace := range traces {
		for _, teamID := range trace.TeamIDs {
			if strings.TrimSpace(teamID) == "" {
				continue
			}
			teams[strings.TrimSpace(teamID)] = true
		}
	}
	return len(teams)
}

func countTeamIDsFromSummary(summary map[string]interface{}) int {
	if len(summary) == 0 {
		return 0
	}
	if teamIDs, ok := summary["team_ids"].([]string); ok {
		return len(teamIDs)
	}
	if raw, ok := summary["team_ids"].([]interface{}); ok {
		seen := make(map[string]bool)
		for _, item := range raw {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				seen[strings.TrimSpace(text)] = true
			}
		}
		return len(seen)
	}
	return 0
}

func (h *Handler) runtimeTeamIDLimit() int {
	if h == nil {
		return 0
	}
	if h.runtimeConfig != nil && h.runtimeConfig.Trace.TeamIDLimit > 0 {
		return h.runtimeConfig.Trace.TeamIDLimit
	}
	return 0
}

func (h *Handler) runtimeTeamIDLimitMeta() (int, string) {
	limit := h.runtimeTeamIDLimit()
	source := "default"
	if limit > 0 {
		source = "config"
	}
	return limit, source
}

func limitTraceTeamIDs(traces []runtimeevents.TraceSummary, limit int) []runtimeevents.TraceSummary {
	if limit <= 0 || len(traces) == 0 {
		return traces
	}
	limited := make([]runtimeevents.TraceSummary, len(traces))
	for i, trace := range traces {
		limited[i] = trace
		if len(trace.TeamIDs) > limit {
			limited[i].TeamIDs = append([]string(nil), trace.TeamIDs[:limit]...)
		} else if len(trace.TeamIDs) > 0 {
			limited[i].TeamIDs = append([]string(nil), trace.TeamIDs...)
		}
	}
	return limited
}

func parseRuntimeTeamIDLimit(r *http.Request, defaultLimit, maxLimit int) (int, error) {
	limit := defaultLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("team_id_limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 0 {
			return 0, errors.New(errors.ErrValidationFailed, "invalid team_id_limit value")
		}
		limit = parsed
	}
	if maxLimit > 0 && limit > maxLimit {
		limit = maxLimit
	}
	return limit, nil
}

func traceEventTeamID(event runtimeevents.Event) string {
	if event.Payload == nil {
		return ""
	}
	if value, ok := event.Payload["team_id"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	if value, ok := event.Payload["teamID"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return ""
}

// ReloadRuntimeMCPs 重新加载并重连 MCP runtime
func (h *Handler) ReloadRuntimeMCPs(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	manager := h.runtimeMCPManager()
	if manager == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"MCP runtime manager not available"))
		return
	}

	traceID := "trace_" + uuid.NewString()
	_ = h.getRuntimeToolCatalogGateway()
	h.publishRuntimeEvent("mcp.reload.started", traceID, map[string]interface{}{
		"mcp_count": len(manager.ListMCPs()),
	})

	if err := manager.ReloadConfig(); err != nil {
		h.publishRuntimeEvent("mcp.reload.completed", traceID, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to reload MCP config", err))
		return
	}

	reloadCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := manager.Start(mcpmanager.WithTraceID(reloadCtx, traceID)); err != nil {
		h.publishRuntimeEvent("mcp.reload.completed", traceID, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to restart MCP runtime", err))
		return
	}

	catalogToolCount := 0
	catalogStats := mcpcatalog.RefreshStats{}
	if gateway := h.getRuntimeToolCatalogGateway(); gateway != nil {
		catalogStats = gateway.Refresh()
		catalogToolCount = gateway.Catalog().Count()
		h.publishRuntimeEvent("mcp.catalog.refreshed", traceID, map[string]interface{}{
			"tool_count":      catalogToolCount,
			"added":           catalogStats.Added,
			"removed":         catalogStats.Removed,
			"updated":         catalogStats.Updated,
			"last_refresh_at": catalogStats.LastRefreshAt,
		})
	}
	h.publishRuntimeEvent("mcp.reload.completed", traceID, map[string]interface{}{
		"success":    true,
		"mcp_count":  len(manager.ListMCPs()),
		"tool_count": catalogToolCount,
	})

	runtime := h.runtimeStatusSnapshot(r.Context(), llm.HealthCheckModeAll)
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"reloaded": true,
		"trace_id": traceID,
		"catalog": map[string]interface{}{
			"tool_count":      catalogToolCount,
			"added":           catalogStats.Added,
			"removed":         catalogStats.Removed,
			"updated":         catalogStats.Updated,
			"last_refresh_at": catalogStats.LastRefreshAt,
		},
		"runtime": runtime,
		"health":  runtimeHealthSummary(runtime),
	})
}

// ReloadRuntimeTeams reloads the team store based on the current runtime config.
func (h *Handler) ReloadRuntimeTeams(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if h.runtimeConfig == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "runtime config not configured"))
		return
	}

	var req struct {
		DryRun bool `json:"dry_run,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "failed to parse request body"))
		return
	}
	dryRun := req.DryRun
	if raw := strings.TrimSpace(r.URL.Query().Get("dry_run")); raw != "" {
		dryRun = parseOptionalBool(raw)
	}
	forceReload := false
	if raw := strings.TrimSpace(r.URL.Query().Get("force")); raw != "" {
		forceReload = parseOptionalBool(raw)
	}
	if force := strings.TrimSpace(r.URL.Query().Get("reload")); force != "" {
		forceReload = forceReload || parseOptionalBool(force)
	}

	storePath := resolveRuntimeTeamStorePath(h.runtimeConfigFile, h.runtimeConfig.Team.StorePath)
	storeDSN := strings.TrimSpace(h.runtimeConfig.Team.StoreDSN)
	desiredKey := storePath + "|" + storeDSN

	h.teamStoreMu.RLock()
	currentKey := h.teamStoreConfigKey
	currentStore := h.teamStore
	h.teamStoreMu.RUnlock()

	shouldReload := desiredKey != currentKey || forceReload
	if currentKey == "" && desiredKey == "|" && currentStore != nil {
		shouldReload = false
	}

	beforePath, beforeDSN := splitTeamStoreKey(currentKey)
	before := map[string]interface{}{
		"config_key": currentKey,
		"store_path": beforePath,
		"store_dsn":  beforeDSN,
		"uses_dsn":   strings.TrimSpace(beforeDSN) != "",
	}
	after := map[string]interface{}{
		"config_key": desiredKey,
		"store_path": storePath,
		"store_dsn":  storeDSN,
		"uses_dsn":   storeDSN != "",
	}

	if dryRun {
		h.writeJSON(w, http.StatusOK, map[string]interface{}{
			"dry_run":      true,
			"would_reload": shouldReload,
			"force":        forceReload,
			"current":      before,
			"desired":      after,
		})
		return
	}

	traceID := "trace_" + uuid.NewString()
	requestID := requestIDFromRequest(r)
	startPayload := map[string]interface{}{
		"store_path": storePath,
		"uses_dsn":   storeDSN != "",
		"current":    before,
		"desired":    after,
	}
	startPayload["request_id"] = strings.TrimSpace(requestID)
	startPayload["request_ip"] = requestRemoteIP(r)
	startPayload["user_agent"] = requestUserAgent(r)
	scope := h.resolveUsageScope(r, "", "", "")
	if scope.TenantID != "" {
		startPayload["tenant_id"] = scope.TenantID
	}
	if scope.ProjectID != "" {
		startPayload["project_id"] = scope.ProjectID
	}
	if scope.UserID != "" {
		startPayload["user_id"] = scope.UserID
	}
	if scope.ScopeKey != "" {
		startPayload["scope_key"] = scope.ScopeKey
	}
	h.publishRuntimeEvent("team.store.reload.started", traceID, startPayload)
	if forceReload {
		h.publishRuntimeEvent("team.store.reload.forced", traceID, buildTeamReloadAuditPayload(r, h.resolveUsageScope(r, "", "", ""), before, after))
	}

	reloaded, err := h.refreshTeamStore(h.runtimeConfig, h.runtimeConfigFile, traceID, requestID)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !reloaded && forceReload {
		store, err := team.NewSQLiteStore(&team.StoreConfig{
			Path: strings.TrimSpace(storePath),
			DSN:  storeDSN,
		})
		if err != nil {
			h.publishRuntimeEvent("team.store.reload_failed", traceID, map[string]interface{}{
				"store_path": storePath,
				"uses_dsn":   storeDSN != "",
				"error":      err.Error(),
				"force":      true,
				"request_id": requestID,
			})
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
		if lifecycle := h.teamLifecycleService(); lifecycle != nil {
			lifecycle.StopAllLoops()
		}
		h.teamStoreMu.Lock()
		oldStore := h.teamStore
		h.teamStore = store
		h.teamStoreConfigKey = desiredKey
		h.teamClaimsManager = nil
		h.teamOrchestrator = nil
		h.teamStoreMu.Unlock()
		if oldStore != nil {
			_ = oldStore.Close()
		}
		reloaded = true
		h.publishRuntimeEvent("team.store.reloaded", traceID, map[string]interface{}{
			"store_path": storePath,
			"uses_dsn":   storeDSN != "",
			"force":      true,
			"request_id": requestID,
		})
	}

	payload := map[string]interface{}{
		"trace_id":   traceID,
		"reloaded":   reloaded,
		"store_path": storePath,
		"uses_dsn":   storeDSN != "",
		"force":      forceReload,
		"current":    before,
		"desired":    after,
	}
	h.writeJSON(w, http.StatusOK, payload)
}

func summarizeRuntimeTrace(events []runtimeevents.Event) map[string]interface{} {
	summary := map[string]interface{}{
		"event_types": map[string]int{},
		"agents":      []string{},
		"sessions":    []string{},
		"team_ids":    []string{},
		"execution": map[string]interface{}{
			"tool_requested":           0,
			"tool_completed":           0,
			"tool_reduced":             0,
			"artifact_refs":            0,
			"reducers":                 map[string]int{},
			"subagent_batches":         0,
			"subagent_batch_completed": 0,
			"subagent_started":         0,
			"subagent_completed":       0,
			"subagent_roles":           map[string]int{},
			"patch_applied":            0,
			"applied_by":               map[string]int{},
		},
		"governance": map[string]interface{}{
			"denied_events":               0,
			"tool_denied":                 0,
			"subagent_denied":             0,
			"patch_decisions":             0,
			"patch_blocked":               0,
			"patch_approved":              0,
			"patch_approved_override":     0,
			"patch_approvals_with_ticket": 0,
			"policies":                    map[string]int{},
			"reasons":                     map[string]int{},
			"patch_policies":              map[string]int{},
		},
		"provenance": map[string]interface{}{
			"profile_context_injected": 0,
			"recall_with_source_refs":  0,
			"profile_resource_refs":    []string{},
			"profile_resource_kinds":   map[string]int{},
			"profile_resource_count":   0,
			"profile_memory_count":     0,
			"profile_notes_count":      0,
			"profile_resource_labels":  []string{},
		},
		"prompt": map[string]interface{}{
			"layouts_observed":   0,
			"instruction_chars":  0,
			"total_chars":        0,
			"instruction_tokens": 0,
			"total_tokens":       0,
			"layers":             map[string]int{},
			"sources":            []string{},
			"source_count":       0,
		},
		"recovery": map[string]interface{}{
			"prompt_preflight_events":        0,
			"prompt_preflight_by_event_type": map[string]int{},
			"prompt_preflight_failure_codes": map[string]int{},
			"replacement_history_available":  0,
			"replacement_history_applied":    0,
			"summary_failure_events":         0,
			"summary_failure_reasons":        map[string]int{},
			"summary_fallbacks":              0,
			"summary_fallback_reasons":       map[string]int{},
		},
		"patch_approval_tickets": []string{},
		"started_at":             nil,
		"ended_at":               nil,
	}
	if len(events) == 0 {
		return summary
	}

	eventTypes := make(map[string]int)
	agents := make(map[string]bool)
	sessions := make(map[string]bool)
	teams := make(map[string]bool)
	tickets := make(map[string]bool)
	execution := map[string]interface{}{
		"tool_requested":           0,
		"tool_completed":           0,
		"tool_reduced":             0,
		"artifact_refs":            0,
		"reducers":                 map[string]int{},
		"subagent_batches":         0,
		"subagent_batch_completed": 0,
		"subagent_started":         0,
		"subagent_completed":       0,
		"subagent_roles":           map[string]int{},
		"patch_applied":            0,
		"applied_by":               map[string]int{},
	}
	governance := map[string]interface{}{
		"denied_events":               0,
		"tool_denied":                 0,
		"subagent_denied":             0,
		"patch_decisions":             0,
		"patch_blocked":               0,
		"patch_approved":              0,
		"patch_approved_override":     0,
		"patch_approvals_with_ticket": 0,
		"policies":                    map[string]int{},
		"reasons":                     map[string]int{},
		"patch_policies":              map[string]int{},
	}
	provenance := map[string]interface{}{
		"profile_context_injected": 0,
		"recall_with_source_refs":  0,
		"profile_resource_refs":    []string{},
		"profile_resource_kinds":   map[string]int{},
		"profile_resource_count":   0,
		"profile_memory_count":     0,
		"profile_notes_count":      0,
		"profile_resource_labels":  []string{},
	}
	prompt := map[string]interface{}{
		"layouts_observed":   0,
		"instruction_chars":  0,
		"total_chars":        0,
		"instruction_tokens": 0,
		"total_tokens":       0,
		"layers":             map[string]int{},
		"sources":            []string{},
		"source_count":       0,
	}
	recovery := map[string]interface{}{
		"prompt_preflight_events":        0,
		"prompt_preflight_by_event_type": map[string]int{},
		"prompt_preflight_failure_codes": map[string]int{},
		"replacement_history_available":  0,
		"replacement_history_applied":    0,
		"summary_failure_events":         0,
		"summary_failure_reasons":        map[string]int{},
		"summary_fallbacks":              0,
		"summary_fallback_reasons":       map[string]int{},
	}
	startedAt := events[0].Timestamp
	endedAt := events[len(events)-1].Timestamp

	for _, event := range events {
		eventTypes[event.Type]++
		if event.AgentName != "" {
			agents[event.AgentName] = true
		}
		if event.SessionID != "" {
			sessions[event.SessionID] = true
		}
		if teamID := traceEventTeamID(event); teamID != "" {
			teams[teamID] = true
		}
		if !event.Timestamp.IsZero() && event.Timestamp.Before(startedAt) {
			startedAt = event.Timestamp
		}
		if event.Timestamp.After(endedAt) {
			endedAt = event.Timestamp
		}
		applyTraceGovernanceEvent(governance, tickets, event)
		applyTraceExecutionEvent(execution, event)
		applyTraceProvenanceEvent(provenance, event)
		applyTracePromptEvent(prompt, event)
		applyTraceRecoveryEvent(recovery, event)
	}

	summary["event_types"] = eventTypes
	summary["agents"] = sortedStringKeys(agents)
	summary["sessions"] = sortedStringKeys(sessions)
	summary["team_ids"] = sortedStringKeys(teams)
	summary["execution"] = execution
	summary["governance"] = governance
	summary["provenance"] = provenance
	summary["prompt"] = prompt
	summary["recovery"] = recovery
	summary["patch_approval_tickets"] = sortedStringKeys(tickets)
	summary["started_at"] = startedAt
	summary["ended_at"] = endedAt
	return summary
}

func applyTraceGovernanceEvent(governance map[string]interface{}, tickets map[string]bool, event runtimeevents.Event) {
	if len(governance) == 0 {
		return
	}
	policies := governance["policies"].(map[string]int)
	reasons := governance["reasons"].(map[string]int)
	patchPolicies := governance["patch_policies"].(map[string]int)

	switch event.Type {
	case "tool.denied":
		governance["denied_events"] = governance["denied_events"].(int) + 1
		governance["tool_denied"] = governance["tool_denied"].(int) + 1
		if policy, ok := event.Payload["policy"].(string); ok && strings.TrimSpace(policy) != "" {
			policies[strings.TrimSpace(policy)]++
		}
		if reason, ok := event.Payload["reason"].(string); ok && strings.TrimSpace(reason) != "" {
			reasons[strings.TrimSpace(reason)]++
		}
	case "subagent.denied":
		governance["denied_events"] = governance["denied_events"].(int) + 1
		governance["subagent_denied"] = governance["subagent_denied"].(int) + 1
		if policy, ok := event.Payload["policy"].(string); ok && strings.TrimSpace(policy) != "" {
			policies[strings.TrimSpace(policy)]++
		}
		if reason, ok := event.Payload["reason"].(string); ok && strings.TrimSpace(reason) != "" {
			reasons[strings.TrimSpace(reason)]++
		}
	case "patch.decision":
		governance["patch_decisions"] = governance["patch_decisions"].(int) + 1
		if policy, ok := event.Payload["patch_decision_policy"].(string); ok && strings.TrimSpace(policy) != "" {
			patchPolicies[strings.TrimSpace(policy)]++
		}
		if decision, ok := event.Payload["patch_decision"].(string); ok {
			switch strings.TrimSpace(decision) {
			case "blocked":
				governance["patch_blocked"] = governance["patch_blocked"].(int) + 1
			case "approved":
				governance["patch_approved"] = governance["patch_approved"].(int) + 1
			case "approved_override":
				governance["patch_approved"] = governance["patch_approved"].(int) + 1
				governance["patch_approved_override"] = governance["patch_approved_override"].(int) + 1
			}
		}
		if approval, ok := event.Payload["patch_approval"].(map[string]interface{}); ok {
			if ticketID, ok := approval["ticket_id"].(string); ok && strings.TrimSpace(ticketID) != "" {
				if tickets != nil {
					tickets[strings.TrimSpace(ticketID)] = true
				}
				governance["patch_approvals_with_ticket"] = governance["patch_approvals_with_ticket"].(int) + 1
			}
		}
	}
}

func applyTraceExecutionEvent(execution map[string]interface{}, event runtimeevents.Event) {
	if len(execution) == 0 {
		return
	}
	reducers := execution["reducers"].(map[string]int)
	subagentRoles := execution["subagent_roles"].(map[string]int)
	appliedBy := execution["applied_by"].(map[string]int)

	switch event.Type {
	case "tool.requested":
		execution["tool_requested"] = execution["tool_requested"].(int) + 1
	case "tool.completed":
		execution["tool_completed"] = execution["tool_completed"].(int) + 1
	case "tool.reduced":
		execution["tool_reduced"] = execution["tool_reduced"].(int) + 1
		execution["artifact_refs"] = execution["artifact_refs"].(int) + intMapValueAny(event.Payload, "artifact_ref_count")
		if reducer, ok := event.Payload["reducer"].(string); ok && strings.TrimSpace(reducer) != "" {
			reducers[strings.TrimSpace(reducer)]++
		}
	case "subagent.batch.started":
		execution["subagent_batches"] = execution["subagent_batches"].(int) + 1
	case "subagent.batch.completed":
		execution["subagent_batch_completed"] = execution["subagent_batch_completed"].(int) + 1
	case "subagent.started":
		execution["subagent_started"] = execution["subagent_started"].(int) + 1
		if role, ok := event.Payload["role"].(string); ok && strings.TrimSpace(role) != "" {
			subagentRoles[strings.TrimSpace(role)]++
		}
	case "subagent.completed":
		execution["subagent_completed"] = execution["subagent_completed"].(int) + 1
	case "patch.applied":
		execution["patch_applied"] = execution["patch_applied"].(int) + 1
		execution["artifact_refs"] = execution["artifact_refs"].(int) + intMapValueAny(event.Payload, "artifact_ref_count")
		for _, actor := range stringSliceValueAny(event.Payload["applied_by"]) {
			appliedBy[actor]++
		}
	}
}

func applyTraceProvenanceEvent(provenance map[string]interface{}, event runtimeevents.Event) {
	if len(provenance) == 0 {
		return
	}
	kinds := provenance["profile_resource_kinds"].(map[string]int)
	refs := stringSliceValueAny(event.Payload["source_refs"])
	if len(refs) == 0 {
		refs = stringSliceValueAny(event.Payload["profile_source_refs"])
	}
	switch event.Type {
	case "context.profile.injected":
		provenance["profile_context_injected"] = provenance["profile_context_injected"].(int) + 1
	case "recall.performed":
		if len(refs) > 0 {
			provenance["recall_with_source_refs"] = provenance["recall_with_source_refs"].(int) + 1
		}
	}
	if len(refs) == 0 {
		return
	}
	provenance["profile_resource_refs"] = mergeStringSlicesAny(provenance["profile_resource_refs"], refs)
	for _, ref := range refs {
		switch {
		case strings.HasPrefix(ref, "profile-resource:memory:"):
			kinds["memory"]++
		case strings.HasPrefix(ref, "profile-resource:notes:"):
			kinds["notes"]++
		}
	}
	refreshTraceProvenanceDisplay(provenance)
}

func applyTracePromptEvent(prompt map[string]interface{}, event runtimeevents.Event) {
	if len(prompt) == 0 {
		return
	}
	layers := map[string]int{}
	switch typed := prompt["layers"].(type) {
	case map[string]int:
		layers = cloneIntMap(typed)
	case map[string]interface{}:
		layers = make(map[string]int, len(typed))
		for key, value := range typed {
			switch count := value.(type) {
			case int:
				layers[key] = count
			case float64:
				layers[key] = int(count)
			}
		}
	}
	summary := runtimeevents.PromptView{
		LayoutsObserved:   intMapValueAny(prompt, "layouts_observed"),
		InstructionChars:  intMapValueAny(prompt, "instruction_chars"),
		TotalChars:        intMapValueAny(prompt, "total_chars"),
		InstructionTokens: intMapValueAny(prompt, "instruction_tokens"),
		TotalTokens:       intMapValueAny(prompt, "total_tokens"),
		Layers:            layers,
		Sources:           stringSliceValueAny(prompt["sources"]),
		SourceCount:       intMapValueAny(prompt, "source_count"),
	}
	runtimeevents.ApplyPromptEventForAPI(&summary, event)
	built := buildPromptSummaryFromView(summary)
	for key, value := range built {
		prompt[key] = value
	}
}

func applyTraceRecoveryEvent(recovery map[string]interface{}, event runtimeevents.Event) {
	if len(recovery) == 0 {
		return
	}
	summary := runtimeevents.RecoveryView{
		PromptPreflightEvents:       intMapValueAny(recovery, "prompt_preflight_events"),
		PromptPreflightByEventType:  cloneIntMapAny(recovery["prompt_preflight_by_event_type"]),
		PromptPreflightFailureCodes: cloneIntMapAny(recovery["prompt_preflight_failure_codes"]),
		ReplacementHistoryAvailable: intMapValueAny(recovery, "replacement_history_available"),
		ReplacementHistoryApplied:   intMapValueAny(recovery, "replacement_history_applied"),
		SummaryFailureEvents:        intMapValueAny(recovery, "summary_failure_events"),
		SummaryFailureReasons:       cloneIntMapAny(recovery["summary_failure_reasons"]),
		SummaryFallbacks:            intMapValueAny(recovery, "summary_fallbacks"),
		SummaryFallbackReasons:      cloneIntMapAny(recovery["summary_fallback_reasons"]),
	}
	runtimeevents.ApplyRecoveryEventForAPI(&summary, event)
	built := buildRecoverySummaryFromView(summary)
	for key, value := range built {
		recovery[key] = value
	}
}

func sortedStringKeys(values map[string]bool) []string {
	if len(values) == 0 {
		return []string{}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func buildPatchGovernanceSummaryFromView(view runtimeevents.GovernanceView) map[string]interface{} {
	return map[string]interface{}{
		"decisions":             view.PatchDecisions,
		"blocked":               view.PatchBlocked,
		"approved":              view.PatchApproved,
		"approved_override":     view.PatchApprovedOverride,
		"approvals_with_ticket": view.PatchApprovalsWithTicket,
		"policies":              cloneIntMap(view.PatchPolicies),
	}
}

func buildPatchGovernanceSummaryFromStats(stats runtimeevents.GovernanceStats) map[string]interface{} {
	return map[string]interface{}{
		"decisions":             stats.PatchDecisions,
		"blocked":               stats.PatchBlocked,
		"approved":              stats.PatchApproved,
		"approved_override":     stats.PatchApprovedOverride,
		"approvals_with_ticket": stats.PatchApprovalsWithTicket,
		"policies":              cloneIntMap(stats.PatchPolicies),
	}
}

func cloneIntMap(values map[string]int) map[string]int {
	if len(values) == 0 {
		return map[string]int{}
	}
	cloned := make(map[string]int, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneIntMapAny(value interface{}) map[string]int {
	switch typed := value.(type) {
	case map[string]int:
		return cloneIntMap(typed)
	case map[string]interface{}:
		cloned := make(map[string]int, len(typed))
		for key, raw := range typed {
			switch count := raw.(type) {
			case int:
				cloned[key] = count
			case int32:
				cloned[key] = int(count)
			case int64:
				cloned[key] = int(count)
			case float64:
				cloned[key] = int(count)
			}
		}
		return cloned
	default:
		return map[string]int{}
	}
}

func buildProvenanceSummaryFromView(view runtimeevents.ProvenanceView) map[string]interface{} {
	return map[string]interface{}{
		"profile_context_injected": view.ProfileContextInjected,
		"recall_with_source_refs":  view.RecallWithSourceRefs,
		"profile_resource_refs":    append([]string(nil), view.ProfileResourceRefs...),
		"profile_resource_kinds":   cloneIntMap(view.ProfileResourceKinds),
		"profile_resource_count":   view.ProfileResourceCount,
		"profile_memory_count":     view.ProfileMemoryCount,
		"profile_notes_count":      view.ProfileNotesCount,
		"profile_resource_labels":  append([]string(nil), view.ProfileResourceLabels...),
	}
}

func buildPromptSummaryFromView(view runtimeevents.PromptView) map[string]interface{} {
	return map[string]interface{}{
		"layouts_observed":   view.LayoutsObserved,
		"instruction_chars":  view.InstructionChars,
		"total_chars":        view.TotalChars,
		"instruction_tokens": view.InstructionTokens,
		"total_tokens":       view.TotalTokens,
		"layers":             cloneIntMap(view.Layers),
		"sources":            append([]string(nil), view.Sources...),
		"source_count":       view.SourceCount,
	}
}

func buildRecoverySummaryFromView(view runtimeevents.RecoveryView) map[string]interface{} {
	return map[string]interface{}{
		"prompt_preflight_events":        view.PromptPreflightEvents,
		"prompt_preflight_by_event_type": cloneIntMap(view.PromptPreflightByEventType),
		"prompt_preflight_failure_codes": cloneIntMap(view.PromptPreflightFailureCodes),
		"replacement_history_available":  view.ReplacementHistoryAvailable,
		"replacement_history_applied":    view.ReplacementHistoryApplied,
		"summary_failure_events":         view.SummaryFailureEvents,
		"summary_failure_reasons":        cloneIntMap(view.SummaryFailureReasons),
		"summary_fallbacks":              view.SummaryFallbacks,
		"summary_fallback_reasons":       cloneIntMap(view.SummaryFallbackReasons),
	}
}

func summarizeRuntimeEventProvenance(events []runtimeevents.Event) map[string]interface{} {
	summary := runtimeevents.ProvenanceView{
		ProfileResourceKinds: make(map[string]int),
	}
	for _, event := range events {
		runtimeevents.ApplyProvenanceEventForAPI(&summary, event)
	}
	return buildProvenanceSummaryFromView(summary)
}

func refreshTraceProvenanceDisplay(provenance map[string]interface{}) {
	if len(provenance) == 0 {
		return
	}
	refs := stringSliceValueAny(provenance["profile_resource_refs"])
	provenance["profile_resource_count"] = len(refs)
	memoryCount := 0
	notesCount := 0
	labels := make([]string, 0, len(refs))
	for _, ref := range refs {
		switch {
		case strings.HasPrefix(ref, "profile-resource:memory:"):
			memoryCount++
			labels = append(labels, "memory:"+shortTraceProfileResourceName(strings.TrimPrefix(ref, "profile-resource:memory:")))
		case strings.HasPrefix(ref, "profile-resource:notes:"):
			notesCount++
			labels = append(labels, "notes:"+shortTraceProfileResourceName(strings.TrimPrefix(ref, "profile-resource:notes:")))
		}
	}
	provenance["profile_memory_count"] = memoryCount
	provenance["profile_notes_count"] = notesCount
	provenance["profile_resource_labels"] = mergeStringSlicesAny(provenance["profile_resource_labels"], labels)
}

func shortTraceProfileResourceName(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return ""
	}
	if index := strings.LastIndex(value, "/"); index >= 0 && index+1 < len(value) {
		return value[index+1:]
	}
	return value
}

func mergeStringSlicesAny(current interface{}, values []string) []string {
	seen := make(map[string]struct{}, len(values))
	merged := make([]string, 0)
	switch typed := current.(type) {
	case []string:
		for _, value := range typed {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			merged = append(merged, value)
		}
	case []interface{}:
		for _, item := range typed {
			if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
				value = strings.TrimSpace(value)
				if _, ok := seen[value]; ok {
					continue
				}
				seen[value] = struct{}{}
				merged = append(merged, value)
			}
		}
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		merged = append(merged, value)
	}
	sort.Strings(merged)
	return merged
}

func patchGovernanceSummaryFromMap(governance map[string]interface{}) map[string]interface{} {
	if len(governance) == 0 {
		return map[string]interface{}{
			"decisions":             0,
			"blocked":               0,
			"approved":              0,
			"approved_override":     0,
			"approvals_with_ticket": 0,
			"policies":              map[string]int{},
		}
	}
	return map[string]interface{}{
		"decisions":             governance["patch_decisions"],
		"blocked":               governance["patch_blocked"],
		"approved":              governance["patch_approved"],
		"approved_override":     governance["patch_approved_override"],
		"approvals_with_ticket": governance["patch_approvals_with_ticket"],
		"policies":              governance["patch_policies"],
	}
}

func promptSummaryFromMap(prompt map[string]interface{}) map[string]interface{} {
	if len(prompt) == 0 {
		return map[string]interface{}{
			"layouts_observed":   0,
			"instruction_chars":  0,
			"total_chars":        0,
			"instruction_tokens": 0,
			"total_tokens":       0,
			"layers":             map[string]int{},
			"sources":            []string{},
			"source_count":       0,
		}
	}
	return map[string]interface{}{
		"layouts_observed":   intMapValueAny(prompt, "layouts_observed"),
		"instruction_chars":  intMapValueAny(prompt, "instruction_chars"),
		"total_chars":        intMapValueAny(prompt, "total_chars"),
		"instruction_tokens": intMapValueAny(prompt, "instruction_tokens"),
		"total_tokens":       intMapValueAny(prompt, "total_tokens"),
		"layers":             cloneIntMapAny(prompt["layers"]),
		"sources":            stringSliceValueAny(prompt["sources"]),
		"source_count":       intMapValueAny(prompt, "source_count"),
	}
}

func recoverySummaryFromMap(recovery map[string]interface{}) map[string]interface{} {
	if len(recovery) == 0 {
		return map[string]interface{}{
			"prompt_preflight_events":        0,
			"prompt_preflight_by_event_type": map[string]int{},
			"prompt_preflight_failure_codes": map[string]int{},
			"replacement_history_available":  0,
			"replacement_history_applied":    0,
			"summary_failure_events":         0,
			"summary_failure_reasons":        map[string]int{},
			"summary_fallbacks":              0,
			"summary_fallback_reasons":       map[string]int{},
		}
	}
	return map[string]interface{}{
		"prompt_preflight_events":        intMapValueAny(recovery, "prompt_preflight_events"),
		"prompt_preflight_by_event_type": cloneIntMapAny(recovery["prompt_preflight_by_event_type"]),
		"prompt_preflight_failure_codes": cloneIntMapAny(recovery["prompt_preflight_failure_codes"]),
		"replacement_history_available":  intMapValueAny(recovery, "replacement_history_available"),
		"replacement_history_applied":    intMapValueAny(recovery, "replacement_history_applied"),
		"summary_failure_events":         intMapValueAny(recovery, "summary_failure_events"),
		"summary_failure_reasons":        cloneIntMapAny(recovery["summary_failure_reasons"]),
		"summary_fallbacks":              intMapValueAny(recovery, "summary_fallbacks"),
		"summary_fallback_reasons":       cloneIntMapAny(recovery["summary_fallback_reasons"]),
	}
}

func buildRecoverySummaryFromTraceSummaries(traces []runtimeevents.TraceSummary) map[string]interface{} {
	view := runtimeevents.RecoveryView{
		PromptPreflightByEventType:  make(map[string]int),
		PromptPreflightFailureCodes: make(map[string]int),
		SummaryFailureReasons:       make(map[string]int),
		SummaryFallbackReasons:      make(map[string]int),
	}
	for _, trace := range traces {
		view.PromptPreflightEvents += trace.Recovery.PromptPreflightEvents
		view.ReplacementHistoryAvailable += trace.Recovery.ReplacementHistoryAvailable
		view.ReplacementHistoryApplied += trace.Recovery.ReplacementHistoryApplied
		view.SummaryFailureEvents += trace.Recovery.SummaryFailureEvents
		view.SummaryFallbacks += trace.Recovery.SummaryFallbacks
		for eventType, count := range trace.Recovery.PromptPreflightByEventType {
			view.PromptPreflightByEventType[eventType] += count
		}
		for code, count := range trace.Recovery.PromptPreflightFailureCodes {
			view.PromptPreflightFailureCodes[code] += count
		}
		for reason, count := range trace.Recovery.SummaryFailureReasons {
			view.SummaryFailureReasons[reason] += count
		}
		for reason, count := range trace.Recovery.SummaryFallbackReasons {
			view.SummaryFallbackReasons[reason] += count
		}
	}
	return buildRecoverySummaryFromView(view)
}

func contextOptionsFromRuntimeConfig(config *runtimecfg.RuntimeConfig) map[string]interface{} {
	if config == nil {
		return nil
	}
	options := make(map[string]interface{})
	ctxCfg := config.Context
	if strings.TrimSpace(ctxCfg.Profile) != "" {
		options["context_profile"] = strings.TrimSpace(ctxCfg.Profile)
	}
	if strings.TrimSpace(ctxCfg.CompactionMode) != "" {
		options["context_compaction_mode"] = strings.TrimSpace(ctxCfg.CompactionMode)
	}
	if strings.TrimSpace(ctxCfg.RecallMode) != "" {
		options["context_recall_mode"] = strings.TrimSpace(ctxCfg.RecallMode)
	}
	if strings.TrimSpace(ctxCfg.ObservationMode) != "" {
		options["context_observation_mode"] = strings.TrimSpace(ctxCfg.ObservationMode)
	}
	if strings.TrimSpace(ctxCfg.WorkspaceMode) != "" {
		options["context_workspace_mode"] = strings.ToLower(strings.TrimSpace(ctxCfg.WorkspaceMode))
	}
	if ctxCfg.MinCompactionMessages > 0 {
		options["context_min_compaction_messages"] = ctxCfg.MinCompactionMessages
	}
	if ctxCfg.MinRecallQueryLength > 0 {
		options["context_min_recall_query_length"] = ctxCfg.MinRecallQueryLength
	}
	if ctxCfg.LedgerLoadLimit > 0 {
		options["context_ledger_load_limit"] = ctxCfg.LedgerLoadLimit
	}
	if ctxCfg.MaxPromptTokens > 0 {
		options["context_max_prompt_tokens"] = ctxCfg.MaxPromptTokens
	}
	if ctxCfg.FallbackMaxPromptTokens > 0 {
		options["context_fallback_max_prompt_tokens"] = ctxCfg.FallbackMaxPromptTokens
	}
	if ctxCfg.MaxMessages > 0 {
		options["context_max_messages"] = ctxCfg.MaxMessages
	}
	if ctxCfg.KeepRecentMessages > 0 {
		options["context_keep_recent_messages"] = ctxCfg.KeepRecentMessages
	}
	if ctxCfg.MaxRecallResults > 0 {
		options["context_max_recall_results"] = ctxCfg.MaxRecallResults
	}
	if ctxCfg.MaxObservationItems > 0 {
		options["context_max_observation_items"] = ctxCfg.MaxObservationItems
	}
	wsCfg := config.Workspace
	if strings.TrimSpace(ctxCfg.WorkspaceMode) == "" && strings.TrimSpace(wsCfg.Mode) != "" {
		options["context_workspace_mode"] = strings.ToLower(strings.TrimSpace(wsCfg.Mode))
	}
	if wsCfg.MaxFileSize > 0 {
		options["workspace_max_file_size"] = wsCfg.MaxFileSize
	}
	if wsCfg.MaxChunkSize > 0 {
		options["workspace_max_chunk_size"] = wsCfg.MaxChunkSize
	}
	if wsCfg.ChunkOverlap > 0 {
		options["workspace_chunk_overlap"] = wsCfg.ChunkOverlap
	}
	if len(wsCfg.Include) > 0 {
		options["workspace_include"] = append([]string(nil), wsCfg.Include...)
	}
	if len(wsCfg.Exclude) > 0 {
		options["workspace_exclude"] = append([]string(nil), wsCfg.Exclude...)
	}
	if path := strings.TrimSpace(config.Artifact.StorePath); path != "" {
		options["artifact_store_path"] = path
	}
	if dsn := strings.TrimSpace(config.Artifact.StoreDSN); dsn != "" {
		options["artifact_store_dsn"] = dsn
	}
	if len(options) == 0 {
		return nil
	}
	return options
}

func contextSnapshotFromRuntimeConfig(config *runtimecfg.RuntimeConfig) map[string]interface{} {
	if config == nil {
		budget := runtimecontext.ResolveBudget(runtimecontext.BudgetProfileBalanced, runtimecontext.Budget{})
		strategy := runtimecontext.ResolveStrategy(runtimecontext.BudgetProfileBalanced, runtimecontext.Strategy{})
		return map[string]interface{}{
			"profile":                    runtimecontext.BudgetProfileBalanced,
			"resolved_profile":           strategy.Profile,
			"compaction_mode":            strategy.CompactionMode,
			"recall_mode":                strategy.RecallMode,
			"observation_mode":           strategy.ObservationMode,
			"max_prompt_tokens":          budget.MaxPromptTokens,
			"fallback_max_prompt_tokens": runtimecontext.DefaultFallbackMaxPromptTokens,
			"max_messages":               budget.MaxMessages,
			"keep_recent_messages":       budget.KeepRecentMessages,
			"max_recall_results":         budget.MaxRecallResults,
			"max_observation_items":      budget.MaxObservationItems,
			"layers":                     runtimecontext.ResolvedLayerPlan(runtimecontext.BudgetProfileBalanced, budget, strategy),
		}
	}
	ctxCfg := config.Context
	profile := strings.TrimSpace(ctxCfg.Profile)
	if profile == "" {
		profile = runtimecontext.BudgetProfileBalanced
	}
	budget := runtimecontext.ResolveBudget(profile, runtimecontext.Budget{
		MaxPromptTokens:     ctxCfg.MaxPromptTokens,
		MaxMessages:         ctxCfg.MaxMessages,
		KeepRecentMessages:  ctxCfg.KeepRecentMessages,
		MaxRecallResults:    ctxCfg.MaxRecallResults,
		MaxObservationItems: ctxCfg.MaxObservationItems,
	})
	fallbackMaxPromptTokens := ctxCfg.FallbackMaxPromptTokens
	if fallbackMaxPromptTokens <= 0 {
		fallbackMaxPromptTokens = runtimecontext.DefaultFallbackMaxPromptTokens
	}
	strategy := runtimecontext.ResolveStrategy(profile, runtimecontext.Strategy{
		CompactionMode:        ctxCfg.CompactionMode,
		RecallMode:            ctxCfg.RecallMode,
		ObservationMode:       ctxCfg.ObservationMode,
		MinCompactionMessages: ctxCfg.MinCompactionMessages,
		MinRecallQueryLength:  ctxCfg.MinRecallQueryLength,
		LedgerLoadLimit:       ctxCfg.LedgerLoadLimit,
	})
	return map[string]interface{}{
		"profile":                    profile,
		"resolved_profile":           strategy.Profile,
		"compaction_mode":            strategy.CompactionMode,
		"recall_mode":                strategy.RecallMode,
		"observation_mode":           strategy.ObservationMode,
		"min_compaction_messages":    strategy.MinCompactionMessages,
		"min_recall_query_length":    strategy.MinRecallQueryLength,
		"ledger_load_limit":          strategy.LedgerLoadLimit,
		"max_prompt_tokens":          budget.MaxPromptTokens,
		"fallback_max_prompt_tokens": fallbackMaxPromptTokens,
		"max_messages":               budget.MaxMessages,
		"keep_recent_messages":       budget.KeepRecentMessages,
		"max_recall_results":         budget.MaxRecallResults,
		"max_observation_items":      budget.MaxObservationItems,
		"layers":                     runtimecontext.ResolvedLayerPlan(profile, budget, strategy),
	}
}

// ValidateRuntimeConfig 获取 runtime 配置健康校验结果
func (h *Handler) ValidateRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	payload := map[string]interface{}{
		"validation": h.runtimeValidationSnapshot(),
	}
	if err := h.attachProfileMetadata(r, payload); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	h.writeJSON(w, http.StatusOK, payload)
}

func embeddingRouterStats(router *skill.SemanticEmbeddingRouter) interface{} {
	if router == nil {
		return nil
	}
	return router.GetStats()
}

func handlerSkillDirs(loader *skill.Loader) []string {
	if loader == nil {
		return nil
	}
	return loader.GetSkillDirs()
}

func parseSkillSourceFilters(r *http.Request) (string, string) {
	if r == nil {
		return "", ""
	}
	layer := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source_layer")))
	dir := strings.TrimSpace(r.URL.Query().Get("source_dir"))
	return layer, dir
}

func (h *Handler) resolveRuntimeConfig(scope UsageScope) *runtimecfg.RuntimeConfig {
	if h.runtimeConfigResolver != nil {
		if cfg := h.runtimeConfigResolver(scope); cfg != nil {
			return cfg
		}
	}
	return h.runtimeConfig
}

func (h *Handler) generatedImageCacheMaxAge() time.Duration {
	if h == nil {
		return time.Hour
	}
	if cfg := h.resolveRuntimeConfig(UsageScope{}); cfg != nil {
		cacheMaxAge := cfg.Images.CacheMaxAge
		if cacheMaxAge > 0 {
			return cacheMaxAge
		}
		if cacheMaxAge == 0 {
			return 0
		}
	}
	return time.Hour
}

func cacheControlHeader(cacheMaxAge time.Duration) string {
	if cacheMaxAge <= 0 {
		return "no-store"
	}
	return fmt.Sprintf("private, max-age=%d", int(cacheMaxAge.Seconds()))
}

func (h *Handler) resolvePlanningMode(requested string, config *runtimecfg.RuntimeConfig) string {
	mode := strings.TrimSpace(requested)
	if mode != "" {
		return mode
	}
	if config != nil {
		return strings.TrimSpace(config.Agent.DefaultPlanningMode)
	}
	if h.runtimeConfig != nil {
		return strings.TrimSpace(h.runtimeConfig.Agent.DefaultPlanningMode)
	}
	return ""
}

func (h *Handler) buildContextPack(ctx context.Context, session *chat.Session, profileCtx map[string]interface{}, workspaceCtx *workspace.WorkspaceContext, messages []types.Message, prompt, workspacePath, teamID, taskID, traceID string, config *runtimecfg.RuntimeConfig) map[string]interface{} {
	builder := contextpack.NewBuilder()
	if len(profileCtx) > 0 {
		builder.AddProvider(contextpack.NewProfileProvider())
	}
	builder.AddProvider(contextpack.NewWorkspaceProvider())
	maxMessages := h.contextPackMaxMessages(config)
	builder.AddProvider(contextpack.NewSessionProvider(maxMessages))
	if store := h.getTeamStore(); store != nil && (strings.TrimSpace(teamID) != "" || strings.TrimSpace(taskID) != "") {
		builder.AddProvider(contextpack.NewTeamProvider(team.NewContextBuilder(store), 6))
	}

	pack, _ := builder.Build(ctx, &contextpack.Input{
		Prompt:        prompt,
		Messages:      messages,
		Session:       buildContextPackSessionSnapshot(session, maxMessages),
		Profile:       cloneProfileContextValues(profileCtx),
		Workspace:     workspaceCtx,
		WorkspacePath: strings.TrimSpace(workspacePath),
		TeamID:        strings.TrimSpace(teamID),
		TaskID:        strings.TrimSpace(taskID),
	})
	if traceID != "" && pack != nil {
		if teamPack, ok := pack["team"].(map[string]interface{}); ok {
			payload := map[string]interface{}{}
			if value := teamPack["team_id"]; value != nil {
				payload["team_id"] = value
			}
			if value := teamPack["task_id"]; value != nil {
				payload["task_id"] = value
			}
			if value := teamPack["task_count"]; value != nil {
				payload["task_count"] = value
			}
			if value := teamPack["mail_count"]; value != nil {
				payload["mail_count"] = value
			}
			if value := teamPack["mate_count"]; value != nil {
				payload["mate_count"] = value
			}
			if summary, ok := teamPack["summary"].(string); ok && strings.TrimSpace(summary) != "" {
				payload["summary_present"] = true
			}
			if session != nil && strings.TrimSpace(session.ID) != "" {
				payload["session_id"] = session.ID
			}
			h.publishRuntimeEvent("context.team.pack", traceID, payload)
		}
	}
	return pack
}

func buildContextPackSessionSnapshot(session *chat.Session, maxMessages int) *contextpack.SessionSnapshot {
	if session == nil {
		return nil
	}
	recentMessages := session.GetRecentMessages(maxMessages)
	clonedMessages := make([]types.Message, 0, len(recentMessages))
	for index := range recentMessages {
		clonedMessages = append(clonedMessages, *recentMessages[index].Clone())
	}
	return &contextpack.SessionSnapshot{
		ID:             session.ID,
		UserID:         session.UserID,
		State:          string(session.State),
		Tags:           append([]string(nil), session.Metadata.Tags...),
		Context:        cloneProfileContextValues(session.Metadata.Context),
		TotalTurns:     session.Metadata.TotalTurns,
		LastAgent:      session.Metadata.LastAgent,
		LastSkill:      session.Metadata.LastSkill,
		LastModel:      session.Metadata.LastModel,
		RecentMessages: clonedMessages,
		UpdatedAt:      session.UpdatedAt,
	}
}

func (h *Handler) resolveGrantedSkillPermissions(r *http.Request) []string {
	if r == nil {
		return nil
	}
	if h.hasValidSearchAdminToken(r) || h.hasTrustedAdminRole(r) || isLoopbackRequest(r) {
		return []string{"*"}
	}

	seen := make(map[string]struct{})
	granted := make([]string, 0)
	add := func(value string) {
		for _, item := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\t'
		}) {
			item = strings.ToLower(strings.TrimSpace(item))
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			granted = append(granted, item)
		}
	}

	for _, value := range r.Header.Values("X-Skills-Permission") {
		add(value)
	}
	for _, value := range r.Header.Values("X-Skills-Permissions") {
		add(value)
	}
	return granted
}

func (h *Handler) contextPackMaxMessages(config *runtimecfg.RuntimeConfig) int {
	if config != nil && config.Agent.MaxMemorySize > 0 {
		return config.Agent.MaxMemorySize
	}
	if h.runtimeConfig != nil && h.runtimeConfig.Agent.MaxMemorySize > 0 {
		return h.runtimeConfig.Agent.MaxMemorySize
	}
	return 10
}

func parseHealthRecheckMode(r *http.Request) llm.HealthCheckMode {
	if r == nil {
		return llm.HealthCheckModeStale
	}
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("recheck")))
	switch value {
	case "", "false", "0", "off":
		return llm.HealthCheckModeStale
	case "true", "1", "all":
		return llm.HealthCheckModeAll
	case "unhealthy", "degraded":
		return llm.HealthCheckModeUnhealthy
	case "stale":
		return llm.HealthCheckModeStale
	case "none":
		return llm.HealthCheckModeNone
	default:
		return llm.HealthCheckModeStale
	}
}

func (h *Handler) attachProfileMetadata(r *http.Request, target map[string]interface{}) error {
	if h == nil || target == nil {
		return nil
	}
	profileRef, agentID := resolveProfileRequestParams(r)
	resolved, ref, err := h.resolveProfileMetadata(profileRef, agentID)
	if err != nil {
		return err
	}
	if resolved == nil {
		return nil
	}
	target["profile"] = map[string]interface{}{
		"reference": ref,
		"resolved":  resolved,
	}
	return nil
}

func resolveProfileRequestParams(r *http.Request) (string, string) {
	if r == nil || r.URL == nil {
		return "", ""
	}
	query := r.URL.Query()
	return strings.TrimSpace(query.Get("profile")), strings.TrimSpace(query.Get("agent"))
}

func filterSkillsBySource(skills []*skill.Skill, layer, dir string) []*skill.Skill {
	if layer == "" && dir == "" {
		return skills
	}

	filtered := make([]*skill.Skill, 0, len(skills))
	for _, skillItem := range skills {
		if matchesSkillSource(skillItem, layer, dir) {
			filtered = append(filtered, skillItem)
		}
	}
	return filtered
}

func (h *Handler) hydrateSkillForResponse(skillItem *skill.Skill) (*skill.Skill, error) {
	if skillItem == nil {
		return nil, nil
	}
	if h != nil && h.skillRegistry != nil {
		return h.skillRegistry.Hydrate(skillItem)
	}
	return skill.HydrateSkill(skillItem)
}

func (h *Handler) hydrateSkillsForResponse(skills []*skill.Skill) ([]*skill.Skill, error) {
	if len(skills) == 0 {
		return skills, nil
	}
	hydrated := make([]*skill.Skill, 0, len(skills))
	for _, skillItem := range skills {
		item, err := h.hydrateSkillForResponse(skillItem)
		if err != nil {
			return nil, err
		}
		if item != nil {
			hydrated = append(hydrated, item)
		}
	}
	return hydrated, nil
}

func (h *Handler) hydrateRouteResultsForResponse(matches []*skill.RouteResult) ([]*skill.RouteResult, error) {
	if len(matches) == 0 {
		return matches, nil
	}
	hydrated := make([]*skill.RouteResult, 0, len(matches))
	for _, match := range matches {
		if match == nil || match.Skill == nil {
			continue
		}
		item, err := h.hydrateSkillForResponse(match.Skill)
		if err != nil {
			return nil, err
		}
		cloned := *match
		cloned.Skill = item
		hydrated = append(hydrated, &cloned)
	}
	return hydrated, nil
}

func matchesSkillSource(skillItem *skill.Skill, layer, dir string) bool {
	if skillItem == nil {
		return false
	}
	if layer == "" && dir == "" {
		return true
	}
	if skillItem.Source == nil {
		return false
	}
	if layer != "" && !strings.EqualFold(skillItem.Source.Layer, layer) {
		return false
	}
	if dir != "" {
		normalizedFilter := filepath.Clean(dir)
		normalizedSource := filepath.Clean(skillItem.Source.Dir)
		if normalizedSource != normalizedFilter && !strings.HasPrefix(normalizedSource, normalizedFilter+string(filepath.Separator)) {
			return false
		}
	}
	return true
}

func buildSkillSourceSummary(skills []*skill.Skill) map[string]int {
	summary := make(map[string]int)
	for _, skillItem := range skills {
		layer := skill.SkillSourceLayerUnknown
		if skillItem != nil && skillItem.Source != nil && skillItem.Source.Layer != "" {
			layer = skillItem.Source.Layer
		}
		summary[layer]++
	}
	return summary
}

// GetSearchStats 获取搜索与 embedding 观测数据
func (h *Handler) GetSearchStats(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSearchAdmin(r); err != nil {
		h.auditSearchAdminAction(r, "search_stats", "forbidden", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	h.auditSearchAdminAction(r, "search_stats", "success")

	response := map[string]interface{}{
		"search": h.searchTelemetrySnapshot(),
		"embedding": map[string]interface{}{
			"enabled": h.embeddingRouter != nil,
			"stats":   embeddingRouterStats(h.embeddingRouter),
		},
	}

	h.writeJSON(w, http.StatusOK, response)
}

// ReindexSearchIndex 手动重建 embedding 搜索索引
func (h *Handler) ReindexSearchIndex(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSearchAdmin(r); err != nil {
		h.recordSearchReindex("forbidden")
		h.auditSearchAdminAction(r, "search_reindex", "forbidden", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	if h.embeddingRouter == nil {
		h.recordSearchReindex("embedding_disabled")
		h.auditSearchAdminAction(r, "search_reindex", "failed", logger.String("reason", "embedding_disabled"))
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"embedding router not configured"))
		return
	}

	force, _ := strconv.ParseBool(r.URL.Query().Get("force"))
	if !force {
		if retryAfter, limited := h.reindexRetryAfter(); limited {
			h.recordSearchReindex("rate_limited")
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			h.auditSearchAdminAction(r, "search_reindex", "rate_limited",
				logger.Int("retry_after_seconds", int(retryAfter.Seconds())),
				logger.Bool("force", force),
			)
			h.writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
				"error":               "search reindex cooldown active",
				"retry_after_seconds": int(retryAfter.Seconds()),
				"search":              h.searchTelemetrySnapshot(),
			})
			return
		}
	}

	h.markSearchReindexStart()
	if err := h.embeddingRouter.RebuildIndex(); err != nil {
		h.recordSearchReindex("failed")
		h.auditSearchAdminAction(r, "search_reindex", "failed", logger.Err(err), logger.Bool("force", force))
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	h.recordSearchReindex("success")
	h.auditSearchAdminAction(r, "search_reindex", "success", logger.Bool("force", force))
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"reindexed": true,
		"embedding": map[string]interface{}{
			"enabled": true,
			"stats":   embeddingRouterStats(h.embeddingRouter),
		},
		"search": h.searchTelemetrySnapshot(),
	})
}

// GetUsageStats 获取 usage/quota 统计
func (h *Handler) GetUsageStats(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	scope, scoped := h.resolveUsageScopeFilter(r, "", "", "")
	response := map[string]interface{}{
		"tracking_enabled": h.usagePolicy.TrackingEnabled,
		"policy":           h.usagePolicySnapshot(),
	}
	if scoped {
		response["scope"] = scope
		response["quota"] = h.usageQuotaSnapshot(scope)
		response["usage"] = h.usageTracker.snapshot(scope)
	} else {
		response["usage"] = h.usageTracker.aggregate()
		response["scopes"] = h.usageTracker.scopes()
	}

	h.writeJSON(w, http.StatusOK, response)
}

// GetUsageLedger 获取持久化 usage ledger
func (h *Handler) GetUsageLedger(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if h.usageLedgerStore == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "usage ledger not configured"))
		return
	}

	scope, scoped := h.resolveUsageScopeFilter(r, "", "", "")
	entrypoint := strings.TrimSpace(r.URL.Query().Get("entrypoint"))
	skillName := strings.TrimSpace(r.URL.Query().Get("skill"))
	limit := 50
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 200 {
		limit = 200
	}

	since := time.Time{}
	if rawSince := strings.TrimSpace(r.URL.Query().Get("since")); rawSince != "" {
		parsed, err := time.Parse(time.RFC3339, rawSince)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid since value"))
			return
		}
		since = parsed
	}

	var successFilter *bool
	if rawSuccess := strings.TrimSpace(r.URL.Query().Get("success")); rawSuccess != "" {
		parsed, err := strconv.ParseBool(rawSuccess)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid success value"))
			return
		}
		successFilter = &parsed
	}

	fetchLimit := limit * 5
	if fetchLimit < limit {
		fetchLimit = limit
	}
	if fetchLimit > 1000 {
		fetchLimit = 1000
	}

	records, err := h.usageLedgerStore.GetSince(since, fetchLimit)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	filtered := make([]*entity.TokenUsageHistory, 0, limit)
	for _, record := range records {
		if record == nil {
			continue
		}
		if scoped {
			if fmt.Sprint(record.Metadata["scope_key"]) != scope.ScopeKey {
				continue
			}
		}
		if entrypoint != "" && fmt.Sprint(record.Metadata["entrypoint"]) != entrypoint {
			continue
		}
		if skillName != "" && fmt.Sprint(record.Metadata["skill"]) != skillName {
			continue
		}
		if successFilter != nil && record.Success != *successFilter {
			continue
		}
		filtered = append(filtered, record)
		if len(filtered) >= limit {
			break
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"records": filtered,
		"count":   len(filtered),
		"filters": map[string]interface{}{
			"scope": func() interface{} {
				if scoped {
					return scope
				}
				return nil
			}(),
			"entrypoint": entrypoint,
			"skill":      skillName,
			"success":    successFilter,
			"since":      since,
			"limit":      limit,
		},
	})
}

// ResetUsageStats 重置 usage 统计
func (h *Handler) ResetUsageStats(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	var req struct {
		TenantID  string `json:"tenant_id,omitempty"`
		ProjectID string `json:"project_id,omitempty"`
		UserID    string `json:"user_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}
	scope, scoped := h.resolveUsageScopeFilter(r, req.TenantID, req.ProjectID, req.UserID)
	if scoped {
		h.usageTracker.reset(&scope)
	} else {
		h.usageTracker.reset(nil)
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"reset": true,
		"scope": func() interface{} {
			if scoped {
				return scope
			}
			return nil
		}(),
	})
}

// GetUsagePolicy 获取 runtime usage/quota policy
func (h *Handler) GetUsagePolicy(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"policy": h.usagePolicyDetailedSnapshot(),
	})
}

// GetMutationPolicy 获取 runtime mutation policy
func (h *Handler) GetMutationPolicy(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSkillMutation(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"policy": h.mutationPolicySnapshot(),
	})
}

// GetGovernancePolicy 获取统一治理策略视图
func (h *Handler) GetGovernancePolicy(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"mutation_policy": h.mutationPolicySnapshot(),
		"usage_policy":    h.usagePolicyDetailedSnapshot(),
		"auth_policy":     h.scopeResolverPolicySnapshot(),
		"persistence": map[string]interface{}{
			"mutation_policy_enabled": h.mutationPolicyPersister != nil,
			"usage_policy_enabled":    h.usagePolicyPersister != nil,
			"auth_policy_enabled":     h.authPolicyPersister != nil,
			"usage_ledger_enabled":    h.usageLedgerStore != nil,
		},
		"search_admin": map[string]interface{}{
			"admin_token_configured":   strings.TrimSpace(h.searchAdminToken) != "",
			"reindex_cooldown_seconds": int(h.searchReindexCooldown.Seconds()),
		},
	})
}

// UpdateMutationPolicy 更新 runtime mutation policy
func (h *Handler) UpdateMutationPolicy(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSkillMutation(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	var req mutationPolicyUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	previous := h.getMutationPolicy()
	policy := h.updateMutationPolicy(req)
	if h.mutationPolicyPersister != nil {
		changedBy := requestChangedBy(r)
		if err := h.mutationPolicyPersister(policy, changedBy); err != nil {
			h.SetMutationPolicy(previous)
			h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to persist mutation policy", err))
			return
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"updated": true,
		"policy":  h.mutationPolicySnapshot(),
	})
}

// GetAuthPolicy 获取 runtime scope/auth resolver policy
func (h *Handler) GetAuthPolicy(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"policy": h.scopeResolverPolicySnapshot(),
	})
}

// UpdateAuthPolicy 更新 runtime scope/auth resolver policy
func (h *Handler) UpdateAuthPolicy(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	var req authPolicyUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	previous := h.getScopeResolverConfig()
	policy := h.updateScopeResolverConfig(req)
	if h.authPolicyPersister != nil {
		changedBy := requestChangedBy(r)
		if err := h.authPolicyPersister(policy, changedBy); err != nil {
			h.SetScopeResolverConfig(previous)
			h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to persist auth policy", err))
			return
		}
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"updated": true,
		"policy":  serializeScopeResolverPolicy(policy),
	})
}

// DeleteAuthPolicyEntry 删除 runtime auth/scope policy 条目
func (h *Handler) DeleteAuthPolicyEntry(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	var req struct {
		Field string `json:"field"`
		Key   string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}
	req.Field = strings.ToLower(strings.TrimSpace(req.Field))
	req.Key = strings.TrimSpace(req.Key)
	if req.Key == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "policy key is required"))
		return
	}
	switch req.Field {
	case "api_key_scope", "admin_role":
	default:
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "policy field must be api_key_scope or admin_role"))
		return
	}

	previous := h.getScopeResolverConfig()
	policy, removed := h.deleteAuthPolicyEntry(req.Field, req.Key)
	if !removed {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "policy entry not found"))
		return
	}
	if h.authPolicyPersister != nil {
		changedBy := requestChangedBy(r)
		if err := h.authPolicyPersister(policy, changedBy); err != nil {
			h.SetScopeResolverConfig(previous)
			h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to persist auth policy", err))
			return
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": true,
		"field":   req.Field,
		"key":     req.Key,
		"policy":  serializeScopeResolverPolicy(policy),
	})
}

// UpdateUsagePolicy 更新 runtime usage/quota policy
func (h *Handler) UpdateUsagePolicy(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	var req usagePolicyUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	previous := h.getUsagePolicy()
	policy := h.updateUsagePolicy(req)
	if h.usagePolicyPersister != nil {
		changedBy := requestChangedBy(r)
		if err := h.usagePolicyPersister(policy, changedBy); err != nil {
			h.SetUsagePolicy(previous)
			h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to persist usage policy", err))
			return
		}
	}
	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"updated": true,
		"policy": map[string]interface{}{
			"tracking_enabled":     policy.TrackingEnabled,
			"quota_enabled":        policy.QuotaEnabled,
			"default_max_requests": policy.DefaultMaxRequests,
			"default_max_tokens":   policy.DefaultMaxTokens,
			"tenants":              serializeUsageQuotaLimits(policy.TenantQuotas),
			"projects":             serializeUsageQuotaLimits(policy.ProjectQuotas),
			"users":                serializeUsageQuotaLimits(policy.UserQuotas),
		},
	})
}

// DeleteUsagePolicyEntry 删除 runtime usage/quota policy 条目
func (h *Handler) DeleteUsagePolicyEntry(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	var req struct {
		Level string `json:"level"`
		Key   string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}
	req.Level = strings.ToLower(strings.TrimSpace(req.Level))
	req.Key = strings.TrimSpace(req.Key)
	if req.Key == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "policy key is required"))
		return
	}
	switch req.Level {
	case "tenant", "project", "user":
	default:
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "policy level must be tenant, project, or user"))
		return
	}

	previous := h.getUsagePolicy()
	policy, removed := h.deleteUsagePolicyEntry(req.Level, req.Key)
	if !removed {
		h.writeError(w, http.StatusNotFound, errors.New(errors.ErrValidationFailed, "policy entry not found"))
		return
	}
	if h.usagePolicyPersister != nil {
		changedBy := requestChangedBy(r)
		if err := h.usagePolicyPersister(policy, changedBy); err != nil {
			h.SetUsagePolicy(previous)
			h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "failed to persist usage policy", err))
			return
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": true,
		"level":   req.Level,
		"key":     req.Key,
		"policy": map[string]interface{}{
			"tracking_enabled":     policy.TrackingEnabled,
			"quota_enabled":        policy.QuotaEnabled,
			"default_max_requests": policy.DefaultMaxRequests,
			"default_max_tokens":   policy.DefaultMaxTokens,
			"tenants":              serializeUsageQuotaLimits(policy.TenantQuotas),
			"projects":             serializeUsageQuotaLimits(policy.ProjectQuotas),
			"users":                serializeUsageQuotaLimits(policy.UserQuotas),
		},
	})
}

// ReloadSkills 重新加载 Skills
func (h *Handler) ReloadSkills(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSkillMutation(r); err != nil {
		h.auditSkillMutation(r, skillMutationActionReload, "forbidden", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforceMutationActionPolicy(skillMutationActionReload, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionReload, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	if h.skillLoader == nil {
		h.auditSkillMutation(r, skillMutationActionReload, "failed", logger.String("reason", "loader_not_configured"))
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid,
			"skill loader not configured"))
		return
	}

	var req struct {
		Dir  string   `json:"dir,omitempty"`
		Dirs []string `json:"dirs,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		h.auditSkillMutation(r, skillMutationActionReload, "invalid_request")
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	dirs := h.resolveRequestedSkillDirs(req.Dir, req.Dirs, r)
	if len(dirs) == 0 {
		h.auditSkillMutation(r, skillMutationActionReload, "invalid_request")
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"skill directory is required"))
		return
	}

	h.skillRegistry.Clear()
	skill.InvalidateAllHydratedSkills()
	if h.skillRegistry != nil {
		h.skillRegistry.ClearLoadedCache()
	}
	if len(dirs) == 1 {
		h.skillLoader.SetSkillDir(dirs[0])
		if err := h.skillLoader.DiscoverAllWithRegistry([]string{dirs[0]}, h.skillRegistry); err != nil {
			h.auditSkillMutation(r, skillMutationActionReload, "failed", logger.Err(err))
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
	} else {
		h.skillLoader.SetSkillDirs(dirs)
		if err := h.skillLoader.DiscoverAllWithRegistry(dirs, h.skillRegistry); err != nil {
			h.auditSkillMutation(r, skillMutationActionReload, "failed", logger.Err(err))
			h.writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	h.rebuildEmbeddingIndex()
	if skills := h.skillRegistry.List(); len(skills) > 0 {
		skillChange := skillChangePayloadFromSkill(skills[0], dirs)
		skillChange["action"] = skillMutationActionReload
		skillChange["status"] = "success"
		skillChange["affected_count"] = h.currentSkillCount()
		skillChange["count"] = h.currentSkillCount()
		skillChange["skill_dirs"] = append([]string(nil), dirs...)
		h.publishSkillsChangedEvent(r, skillChange)
	} else {
		h.publishSkillsChangedEvent(r, map[string]interface{}{
			"action":         skillMutationActionReload,
			"status":         "success",
			"affected_count": h.currentSkillCount(),
			"count":          h.currentSkillCount(),
			"skill_dirs":     append([]string(nil), dirs...),
		})
	}
	h.auditSkillMutation(r, skillMutationActionReload, "success", logger.Int("dir_count", len(dirs)))

	response := map[string]interface{}{
		"message":      "skills reloaded",
		"status":       "success",
		"skill_dirs":   dirs,
		"total_skills": h.skillRegistry.Count(),
	}

	h.writeJSON(w, http.StatusOK, response)
}

// ValidateSkill 验证 Skill 配置
func (h *Handler) ValidateSkill(w http.ResponseWriter, r *http.Request) {
	var newSkill skill.Skill
	if err := json.NewDecoder(r.Body).Decode(&newSkill); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse request body"))
		return
	}

	// 基本验证
	if newSkill.Name == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"skill name is required"))
		return
	}

	// 验证工具是否存在
	toolsAvailable := true
	if len(newSkill.Tools) > 0 {
		// 这里可以添加工具存在性验证
		// toolsAvailable = h.mcpManager.ValidateTools(newSkill.Tools)
	}

	response := map[string]interface{}{
		"valid": true,
		"skill": newSkill.Name,
		"checks": []string{
			"manifest_valid",
		},
	}

	if toolsAvailable && len(newSkill.Tools) > 0 {
		response["checks"] = append(response["checks"].([]string), "tools_available")
	}

	if len(newSkill.Triggers) > 0 {
		response["checks"] = append(response["checks"].([]string), "triggers_valid")
	}

	h.writeJSON(w, http.StatusOK, response)
}

// ExportSkills 导出 Skills
func (h *Handler) ExportSkills(w http.ResponseWriter, r *http.Request) {
	layer, dir := parseSkillSourceFilters(r)
	skills := filterSkillsBySource(h.skillRegistry.List(), layer, dir)
	hydratedSkills, err := h.hydrateSkillsForResponse(skills)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=skills_export.json")

	json.NewEncoder(w).Encode(map[string]interface{}{
		"skills":         hydratedSkills,
		"count":          len(hydratedSkills),
		"source_summary": buildSkillSourceSummary(hydratedSkills),
	})
}

// ImportSkills 导入 Skills
func (h *Handler) ImportSkills(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeSkillMutation(r); err != nil {
		h.auditSkillMutation(r, skillMutationActionImport, "forbidden", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforceMutationActionPolicy(skillMutationActionImport, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionImport, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	if err := h.enforcePersistPolicy(nil, r); err != nil {
		h.auditSkillMutation(r, skillMutationActionImport, "disabled", logger.Err(err))
		h.writeError(w, http.StatusForbidden, err)
		return
	}

	var importData struct {
		Skills []*skill.Skill `json:"skills"`
	}

	if err := json.NewDecoder(r.Body).Decode(&importData); err != nil {
		h.auditSkillMutation(r, skillMutationActionImport, "invalid_request")
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"failed to parse import data"))
		return
	}

	for _, skillItem := range importData.Skills {
		if skillItem != nil {
			skillItem.SetSource("", "", skill.SkillSourceLayerRuntime)
		}
	}

	errs := make([]error, 0)
	imported := 0
	persisted := 0
	var firstImportedSkill *skill.Skill
	for _, skillItem := range importData.Skills {
		if skillItem == nil {
			errs = append(errs, errors.New(errors.ErrValidationFailed, "skill entry cannot be nil"))
			continue
		}
		if err := h.skillRegistry.Register(skillItem); err != nil {
			errs = append(errs, err)
			continue
		}

		if shouldPersistSkill(r) {
			if err := h.persistSkill(skillItem, nil, r); err != nil {
				h.skillRegistry.Unregister(skillItem.Name)
				errs = append(errs, err)
				continue
			}
			persisted++
		}

		h.updateEmbeddingIndex(skillItem)
		if firstImportedSkill == nil {
			firstImportedSkill = skillItem
		}
		imported++
	}

	response := map[string]interface{}{
		"imported":  imported,
		"persisted": persisted,
		"failed":    len(errs),
		"errors":    errs,
	}

	statusCode := http.StatusOK
	if len(errs) > 0 {
		statusCode = http.StatusMultiStatus
	}
	outcome := "success"
	if len(errs) > 0 {
		outcome = "partial_success"
	}
	if imported > 0 {
		skillChange := skillChangePayloadFromSkill(firstImportedSkill, handlerSkillDirs(h.skillLoader))
		skillChange["action"] = skillMutationActionImport
		skillChange["status"] = outcome
		skillChange["affected_count"] = imported
		skillChange["imported"] = imported
		skillChange["persisted"] = persisted
		skillChange["failed_count"] = len(errs)
		skillChange["count"] = h.currentSkillCount()
		h.publishSkillsChangedEvent(r, skillChange)
	}
	h.auditSkillMutation(r, skillMutationActionImport, outcome,
		logger.Int("imported", imported),
		logger.Int("persisted", persisted),
		logger.Int("failed", len(errs)))

	h.writeJSON(w, statusCode, response)
}

func shouldPersistSkill(r *http.Request) bool {
	persist, _ := queryBoolFlag(r, "persist")
	return persist
}

func shouldDeleteSkillFile(r *http.Request) bool {
	deleteFile, _ := queryBoolFlag(r, "delete_file")
	return deleteFile
}

func shouldPersistUpdatedSkill(existingSkill *skill.Skill, r *http.Request) bool {
	persist, specified := queryBoolFlag(r, "persist")
	if specified {
		return persist
	}
	if existingSkill == nil || existingSkill.Source == nil {
		return false
	}
	return existingSkill.Source.Layer == skill.SkillSourceLayerExternal && strings.TrimSpace(existingSkill.Source.Path) != ""
}

func queryBoolFlag(r *http.Request, name string) (bool, bool) {
	if r == nil {
		return false, false
	}
	rawValues, ok := r.URL.Query()[name]
	if !ok || len(rawValues) == 0 {
		return false, false
	}
	value, err := strconv.ParseBool(strings.TrimSpace(rawValues[0]))
	if err != nil {
		return false, true
	}
	return value, true
}

func (h *Handler) persistSkill(skillItem *skill.Skill, previousSource *skill.SkillSource, r *http.Request) error {
	if h.skillLoader == nil {
		return errors.New(errors.ErrConfigInvalid, "skill loader not configured")
	}
	targetFile, targetLayer, err := h.resolvePersistTargetPath(skillItem, previousSource, r)
	if err != nil {
		return err
	}
	if err := h.skillLoader.SaveToFile(skillItem, targetFile); err != nil {
		return err
	}
	skill.InvalidateHydratedSkill(targetFile)
	if h.skillRegistry != nil {
		h.skillRegistry.InvalidateLoadedSkill(skillItem.Name)
	}
	if previousSource != nil && strings.TrimSpace(previousSource.Path) != "" && filepath.Clean(previousSource.Path) != filepath.Clean(targetFile) {
		skill.InvalidateHydratedSkill(previousSource.Path)
	}
	skillItem.SetSource(targetFile, filepath.Dir(targetFile), targetLayer)
	return nil
}

func (h *Handler) resolvePersistTargetPath(skillItem *skill.Skill, previousSource *skill.SkillSource, r *http.Request) (string, string, error) {
	targetDir := ""
	if r != nil {
		targetDir = strings.TrimSpace(r.URL.Query().Get("target_dir"))
	}

	configuredDirs := handlerSkillDirs(h.skillLoader)
	systemDir := ""
	if len(configuredDirs) > 0 {
		systemDir = filepath.Clean(configuredDirs[0])
	}

	if targetDir != "" {
		targetDir = filepath.Clean(targetDir)
		if systemDir != "" && isSameOrSubdir(targetDir, systemDir) {
			return "", "", errors.New(errors.ErrValidationFailed, "persist target cannot be the system skill directory")
		}
		return filepath.Join(targetDir, skillItem.Name, "skill.yaml"), skill.SkillSourceLayerExternal, nil
	}

	if previousSource != nil && previousSource.Layer == skill.SkillSourceLayerExternal && previousSource.Path != "" {
		return filepath.Clean(previousSource.Path), skill.SkillSourceLayerExternal, nil
	}

	if len(configuredDirs) > 1 {
		targetDir = filepath.Clean(configuredDirs[1])
		return filepath.Join(targetDir, skillItem.Name, "skill.yaml"), skill.SkillSourceLayerExternal, nil
	}

	return "", "", errors.New(errors.ErrValidationFailed, "external skill directory is required; configure extra_skill_dirs or provide target_dir")
}

func (h *Handler) resolveRequestedSkillDirs(primary string, extras []string, r *http.Request) []string {
	seen := make(map[string]struct{})
	resolved := make([]string, 0, 1+len(extras))

	addDir := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		dir = filepath.Clean(dir)
		if _, exists := seen[dir]; exists {
			return
		}
		seen[dir] = struct{}{}
		resolved = append(resolved, dir)
	}

	addDir(primary)
	for _, dir := range extras {
		addDir(dir)
	}

	if len(resolved) == 0 && r != nil {
		addDir(r.URL.Query().Get("dir"))
	}
	if len(resolved) == 0 && h.skillLoader != nil {
		for _, dir := range h.skillLoader.GetSkillDirs() {
			addDir(dir)
		}
	}

	return resolved
}

func isSameOrSubdir(path string, parent string) bool {
	path = filepath.Clean(path)
	parent = filepath.Clean(parent)
	return path == parent || strings.HasPrefix(path, parent+string(filepath.Separator))
}

func (h *Handler) deletePersistedSkillFile(skillItem *skill.Skill) error {
	if skillItem == nil || skillItem.Source == nil || strings.TrimSpace(skillItem.Source.Path) == "" {
		return errors.New(errors.ErrValidationFailed, "skill does not have a persisted source file")
	}
	if skillItem.Source.Layer == skill.SkillSourceLayerSystem {
		return errors.New(errors.ErrValidationFailed, "cannot delete file for system skill")
	}

	filePath := filepath.Clean(skillItem.Source.Path)
	if err := os.Remove(filePath); err != nil {
		return err
	}
	skill.InvalidateHydratedSkill(filePath)
	if h.skillRegistry != nil {
		h.skillRegistry.InvalidateLoadedSkill(skillItem.Name)
	}
	promptPath := strings.TrimSpace(skillItem.Source.PromptPath)
	if promptPath == "" {
		promptPath = filepath.Join(filepath.Dir(filePath), "prompt.md")
	}
	if err := os.Remove(filepath.Clean(promptPath)); err != nil && !os.IsNotExist(err) {
		return err
	}

	dirPath := filepath.Dir(filePath)
	entries, err := os.ReadDir(dirPath)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dirPath)
	}
	return nil
}

func isAutoProfileRef(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "auto")
}

func extractLastUserPrompt(messages []map[string]string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		role := strings.ToLower(strings.TrimSpace(messages[i]["role"]))
		if role == "" || role == "user" {
			if content := strings.TrimSpace(messages[i]["content"]); content != "" {
				return content
			}
		}
	}
	return ""
}

func routeProfileForPrompt(prompt string) string {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return ""
	}
	if containsAny(lower, []string{"write", "implement", "fix", "add", "edit", "refactor", "patch", "update", "change"}) {
		return "executor"
	}
	if containsAny(lower, []string{"plan", "break down", "design", "compare", "proposal", "approach"}) {
		return "planner"
	}
	if containsAny(lower, []string{"search", "inspect", "understand", "locate", "find", "investigate", "look up"}) {
		return "explore"
	}
	return "executor"
}

func containsAny(text string, needles []string) bool {
	if text == "" || len(needles) == 0 {
		return false
	}
	for _, needle := range needles {
		if needle == "" {
			continue
		}
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
