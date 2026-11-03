package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// ============================================================================
// /web/api/schedule/* —— micro web「自动化任务」页签的本地管理面
//
// 数据源与 TUI /schedule、schedule_* 工具完全同源：当前进程的 schedule.Service。
// 这里刻意不请求 runtime-server 的 /api/runtime/schedule/*；aicli --web 是单进程
// 宿主，没有挂载该路径（历史缺陷：页签请求恒 404，创建失败时只剩无明细文案）。
// ============================================================================

type chatWebScheduleCreateRequest struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Prompt    string `json:"prompt"`

	AfterSeconds *int64                `json:"after_seconds,omitempty"`
	At           *string               `json:"at,omitempty"`
	EverySeconds *int64                `json:"every_seconds,omitempty"`
	Daily        *schedule.DailyInput  `json:"daily,omitempty"`
	Weekly       *schedule.WeeklyInput `json:"weekly,omitempty"`
	Cron         *schedule.CronInput   `json:"cron,omitempty"`

	Goal            string   `json:"goal,omitempty"`
	Plan            []string `json:"plan,omitempty"`
	TargetKind      string   `json:"target_kind,omitempty"`
	TargetWorkspace string   `json:"target_workspace,omitempty"`
	TargetProfile   string   `json:"target_profile,omitempty"`
}

type chatWebScheduleDeleteRequest struct {
	SessionID string `json:"session_id"`
	ID        string `json:"id"`
}

type chatWebScheduleControlRequest struct {
	SessionID string `json:"session_id"`
	ID        string `json:"id"`
}

type chatWebScheduleUpdateRequest struct {
	SessionID string          `json:"session_id"`
	ID        string          `json:"id"`
	Expected  schedule.Record `json:"expected"`
	Title     *string         `json:"title,omitempty"`
	Prompt    *string         `json:"prompt,omitempty"`
	Change    *struct {
		Kind         schedule.Kind `json:"kind"`
		At           *string       `json:"at,omitempty"`
		EverySeconds *int64        `json:"every_seconds,omitempty"`
		Daily        *struct {
			Time     string `json:"time"`
			TimeZone string `json:"time_zone"`
		} `json:"daily,omitempty"`
		Weekly *struct {
			Time     string `json:"time"`
			TimeZone string `json:"time_zone"`
			Weekdays []int  `json:"weekdays"`
		} `json:"weekly,omitempty"`
		Cron *struct {
			Expression string `json:"expression"`
			TimeZone   string `json:"time_zone"`
		} `json:"cron,omitempty"`
	} `json:"change,omitempty"`
}

// HandleChatWebAPISchedule 分派 /web/api/schedule 下的子路径。
func HandleChatWebAPISchedule(w http.ResponseWriter, r *http.Request) {
	subpath := strings.Trim(strings.TrimPrefix(r.URL.Path, ChatWebAPISchedulePath), "/")
	switch subpath {
	case "catalog":
		handleChatWebScheduleCatalog(w, r)
	case "create":
		handleChatWebScheduleCreate(w, r)
	case "update":
		handleChatWebScheduleUpdate(w, r)
	case "pause":
		handleChatWebScheduleControl(w, r, "pause")
	case "resume":
		handleChatWebScheduleControl(w, r, "resume")
	case "runs":
		handleChatWebScheduleRuns(w, r)
	case "delete":
		handleChatWebScheduleDelete(w, r)
	default:
		writeChatWebScheduleError(w, http.StatusNotFound, "", "unknown schedule endpoint: "+r.URL.Path)
	}
}

func handleChatWebScheduleCatalog(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodGet) {
		return
	}
	service := chatWebScheduleService()
	if service == nil {
		writeChatWebScheduleError(w, http.StatusServiceUnavailable, "schedule_disabled",
			"automation task service is not configured; schedule.enabled defaults to true, so check that it is not disabled by a global or profile runtime.yaml, and that the schedule store is writable; restart aicli after fixing the override")
		return
	}
	entries, err := service.Catalog()
	if err != nil {
		writeChatWebScheduleError(w, http.StatusInternalServerError, string(schedule.CodeInternal),
			"read schedule catalog failed: "+err.Error())
		return
	}
	writeWebAPIJSON(w, http.StatusOK, map[string]any{"tasks": entries, "count": len(entries)})
}

func handleChatWebScheduleCreate(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodPost) {
		return
	}
	service := chatWebScheduleService()
	if service == nil {
		writeChatWebScheduleError(w, http.StatusServiceUnavailable, "schedule_disabled",
			"automation task service is not configured; schedule.enabled defaults to true, so check that it is not disabled by a global or profile runtime.yaml, and that the schedule store is writable; restart aicli after fixing the override")
		return
	}
	var request chatWebScheduleCreateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		writeChatWebScheduleError(w, http.StatusBadRequest, string(schedule.CodeInvalidRule),
			"invalid JSON body: "+err.Error())
		return
	}
	targetKind := schedule.NormalizeTargetKind(request.TargetKind)
	if targetKind == schedule.TargetSession && strings.TrimSpace(request.SessionID) == "" {
		writeChatWebScheduleError(w, http.StatusBadRequest, string(schedule.CodeInvalidRule),
			"session_id is required for target_kind=session")
		return
	}
	// Non-session targets are not bound to the request's session_id, but still
	// record the current session as creator so the management surface can
	// edit/pause/delete the task later without requiring a persistent binding.
	creatorSessionID := strings.TrimSpace(request.SessionID)
	if targetKind != schedule.TargetSession && creatorSessionID == "" {
		if session := chatWebSession(); session != nil {
			creatorSessionID = strings.TrimSpace(currentRuntimeSessionID(session))
		}
	}
	record, err := service.Create(creatorSessionID, schedule.CreateRequest{
		Prompt:          request.Prompt,
		Title:           request.Title,
		AfterSeconds:    request.AfterSeconds,
		At:              request.At,
		EverySeconds:    request.EverySeconds,
		Daily:           request.Daily,
		Weekly:          request.Weekly,
		Cron:            request.Cron,
		Goal:            request.Goal,
		Plan:            request.Plan,
		TargetKind:      targetKind,
		TargetWorkspace: request.TargetWorkspace,
		TargetProfile:   request.TargetProfile,
	}, schedule.OriginManual)
	if err != nil {
		if input, ok := schedule.IsInputError(err); ok {
			writeChatWebScheduleError(w, http.StatusBadRequest, string(input.Code),
				string(input.Code)+": "+input.Message)
			return
		}
		writeChatWebScheduleError(w, http.StatusInternalServerError, string(schedule.CodeInternal),
			"create schedule task failed: "+err.Error())
		return
	}
	writeWebAPIJSON(w, http.StatusOK, map[string]any{
		"record": record,
		"origin": schedule.OriginManual,
		"view":   schedule.BuildView(record, service.Now()),
	})
}

func handleChatWebScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodPost) {
		return
	}
	service := chatWebScheduleService()
	if service == nil {
		writeChatWebScheduleError(w, http.StatusServiceUnavailable, "schedule_disabled",
			"automation task service is not configured; schedule.enabled defaults to true, so check that it is not disabled by a global or profile runtime.yaml, and that the schedule store is writable; restart aicli after fixing the override")
		return
	}
	var request chatWebScheduleUpdateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		writeChatWebScheduleError(w, http.StatusBadRequest, string(schedule.CodeInvalidRule),
			"invalid JSON body: "+err.Error())
		return
	}
	sessionID := strings.TrimSpace(request.SessionID)
	id := strings.TrimSpace(request.ID)
	if sessionID == "" || id == "" {
		writeChatWebScheduleError(w, http.StatusBadRequest, string(schedule.CodeInvalidRule),
			"session_id and id are required")
		return
	}
	var change *schedule.TimingChange
	if request.Change != nil {
		change = &schedule.TimingChange{
			Kind:         request.Change.Kind,
			At:           request.Change.At,
			EverySeconds: request.Change.EverySeconds,
		}
		if request.Change.Daily != nil {
			change.Daily = &schedule.DailyInput{
				Time: request.Change.Daily.Time, TimeZone: request.Change.Daily.TimeZone,
			}
		}
		if request.Change.Weekly != nil {
			change.Weekly = &schedule.WeeklyInput{
				Time: request.Change.Weekly.Time, TimeZone: request.Change.Weekly.TimeZone,
				Weekdays: request.Change.Weekly.Weekdays,
			}
		}
		if request.Change.Cron != nil {
			change.Cron = &schedule.CronInput{
				Expression: request.Change.Cron.Expression, TimeZone: request.Change.Cron.TimeZone,
			}
		}
	}
	result, err := service.Update(sessionID, id, request.Expected,
		schedule.UpdateContent{Title: request.Title, Prompt: request.Prompt}, change)
	if err != nil {
		writeChatWebScheduleError(w, http.StatusInternalServerError, string(schedule.CodeInternal),
			"update schedule task failed: "+err.Error())
		return
	}
	writeWebAPIJSON(w, http.StatusOK, result)
}

func handleChatWebScheduleControl(w http.ResponseWriter, r *http.Request, action string) {
	if !chatWebRequireMethod(w, r, http.MethodPost) {
		return
	}
	service := chatWebScheduleService()
	if service == nil {
		writeChatWebScheduleError(w, http.StatusServiceUnavailable, "schedule_disabled",
			"automation task service is not configured; schedule.enabled defaults to true, so check that it is not disabled by a global or profile runtime.yaml, and that the schedule store is writable; restart aicli after fixing the override")
		return
	}
	var request chatWebScheduleControlRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		writeChatWebScheduleError(w, http.StatusBadRequest, string(schedule.CodeInvalidRule),
			"invalid JSON body: "+err.Error())
		return
	}
	sessionID := strings.TrimSpace(request.SessionID)
	id := strings.TrimSpace(request.ID)
	if sessionID == "" || id == "" {
		writeChatWebScheduleError(w, http.StatusBadRequest, string(schedule.CodeInvalidRule),
			"session_id and id are required")
		return
	}
	var (
		result schedule.UpdateResult
		err    error
	)
	switch action {
	case "pause":
		result, err = service.Pause(sessionID, id)
	case "resume":
		result, err = service.Resume(sessionID, id)
	default:
		writeChatWebScheduleError(w, http.StatusNotFound, "", "unknown schedule action: "+action)
		return
	}
	if err != nil {
		writeChatWebScheduleError(w, http.StatusInternalServerError, string(schedule.CodeInternal),
			action+" schedule task failed: "+err.Error())
		return
	}
	writeWebAPIJSON(w, http.StatusOK, result)
}

func handleChatWebScheduleRuns(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodGet) {
		return
	}
	service := chatWebScheduleService()
	if service == nil {
		writeChatWebScheduleError(w, http.StatusServiceUnavailable, "schedule_disabled",
			"automation task service is not configured; schedule.enabled defaults to true, so check that it is not disabled by a global or profile runtime.yaml, and that the schedule store is writable; restart aicli after fixing the override")
		return
	}
	query := r.URL.Query()
	id := strings.TrimSpace(query.Get("id"))
	if id == "" {
		writeChatWebScheduleError(w, http.StatusBadRequest, string(schedule.CodeInvalidRule),
			"id is required")
		return
	}
	limit := 20
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeChatWebScheduleError(w, http.StatusBadRequest, string(schedule.CodeInvalidRule),
				"limit must be an integer from 1 through 100")
			return
		}
		limit = parsed
	}
	runs, err := service.JobRuns(strings.TrimSpace(query.Get("session_id")), id, limit)
	if err != nil {
		if input, ok := schedule.IsInputError(err); ok {
			status := http.StatusBadRequest
			if input.Code == schedule.CodeNotFound {
				status = http.StatusNotFound
			}
			writeChatWebScheduleError(w, status, string(input.Code), input.Message)
			return
		}
		writeChatWebScheduleError(w, http.StatusInternalServerError, string(schedule.CodeInternal),
			"read schedule runs failed: "+err.Error())
		return
	}
	writeWebAPIJSON(w, http.StatusOK, map[string]any{
		"id": id, "runs": runs, "count": len(runs),
	})
}

func handleChatWebScheduleDelete(w http.ResponseWriter, r *http.Request) {
	if !chatWebRequireMethod(w, r, http.MethodPost) {
		return
	}
	service := chatWebScheduleService()
	if service == nil {
		writeChatWebScheduleError(w, http.StatusServiceUnavailable, "schedule_disabled",
			"automation task service is not configured; schedule.enabled defaults to true, so check that it is not disabled by a global or profile runtime.yaml, and that the schedule store is writable; restart aicli after fixing the override")
		return
	}
	var request chatWebScheduleDeleteRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		writeChatWebScheduleError(w, http.StatusBadRequest, string(schedule.CodeInvalidRule),
			"invalid JSON body: "+err.Error())
		return
	}
	sessionID := strings.TrimSpace(request.SessionID)
	id := strings.TrimSpace(request.ID)
	if sessionID == "" || id == "" {
		writeChatWebScheduleError(w, http.StatusBadRequest, string(schedule.CodeInvalidRule),
			"session_id and id are required")
		return
	}
	result, err := service.Delete(sessionID, id)
	if err != nil {
		writeChatWebScheduleError(w, http.StatusInternalServerError, string(schedule.CodeInternal),
			"delete schedule task failed: "+err.Error())
		return
	}
	writeWebAPIJSON(w, http.StatusOK, result)
}

// chatWebScheduleService 解析本进程的调度服务。优先当前会话宿主；能力面异步
// 挂载前拿到的早期会话快照可能没有 host，因此回退到进程级注册位。
func chatWebScheduleService() *schedule.Service {
	if session := chatWebSession(); session != nil &&
		session.LocalRuntimeHost != nil &&
		session.LocalRuntimeHost.Schedule != nil {
		return session.LocalRuntimeHost.Schedule
	}
	return processSchedule()
}

// writeChatWebScheduleError 使用与 runtime API 兼容的扁平错误体
// （error + code），让 micro web、脚本和后续复用方都能读到稳定错误码。
func writeChatWebScheduleError(w http.ResponseWriter, status int, code, message string) {
	body := map[string]any{"error": message}
	if code != "" {
		body["code"] = code
	}
	writeWebAPIJSON(w, status, body)
}
