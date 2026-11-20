package schedule

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Template is a persisted, validated preset for the schedule CreateFormState.
// It deliberately stores presentation-neutral strings (amount/atTime/clockTime)
// so clients can apply it without losing the user's local form semantics.
type Template struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Description     string     `json:"description,omitempty"`
	TaskTitle       string     `json:"taskTitle"`
	Category        string     `json:"category,omitempty"`
	Prompt          string     `json:"prompt"`
	Kind            Kind       `json:"kind"`
	Amount          string     `json:"amount,omitempty"`
	AtTime          string     `json:"atTime,omitempty"`
	ClockTime       string     `json:"clockTime,omitempty"`
	TimeZone        string     `json:"timeZone,omitempty"`
	Weekdays        []int      `json:"weekdays,omitempty"`
	Cron            string     `json:"cron,omitempty"`
	Goal            string     `json:"goal,omitempty"`
	Plan            string     `json:"plan,omitempty"`
	TargetKind      TargetKind `json:"targetKind"`
	TargetWorkspace string     `json:"targetWorkspace,omitempty"`
	TargetProfile   string     `json:"targetProfile,omitempty"`
	Provider        string     `json:"provider,omitempty"`
	Model           string     `json:"model,omitempty"`
	ReasoningEffort string     `json:"reasoningEffort,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (t Template) Validate() error {
	if strings.TrimSpace(t.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if strings.TrimSpace(t.Title) == "" || len([]rune(strings.TrimSpace(t.Title))) > MaxTitleLength {
		return fmt.Errorf("title is required and must be at most %d characters", MaxTitleLength)
	}
	if strings.TrimSpace(t.TaskTitle) == "" || len([]rune(strings.TrimSpace(t.TaskTitle))) > MaxTitleLength {
		return fmt.Errorf("taskTitle is required and must be at most %d characters", MaxTitleLength)
	}
	if strings.TrimSpace(t.Prompt) == "" {
		return fmt.Errorf("prompt is required")
	}
	switch t.Kind {
	case KindAfter, KindAt, KindEvery, KindDaily, KindWeekly, KindCron:
	default:
		return fmt.Errorf("kind must be one of after, at, every, daily, weekly, cron")
	}
	tk := NormalizeTargetKind(string(t.TargetKind))
	if tk != t.TargetKind && strings.TrimSpace(string(t.TargetKind)) != "" {
		return fmt.Errorf("targetKind is invalid")
	}
	if tk != TargetSession && strings.TrimSpace(t.Goal) == "" {
		return fmt.Errorf("goal is required for non-session targets")
	}
	if tk == TargetWorkspace && strings.TrimSpace(t.TargetWorkspace) == "" {
		return fmt.Errorf("targetWorkspace is required for workspace targets")
	}
	switch t.Kind {
	case KindAfter, KindEvery:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(t.Amount), "%d", &n); err != nil || n <= 0 || strings.TrimSpace(t.Amount) != fmt.Sprint(n) {
			return fmt.Errorf("amount must be a positive integer")
		}
		if t.Kind == KindEvery && n < 1 {
			return fmt.Errorf("amount must be at least 1")
		}
	case KindAt:
		if strings.TrimSpace(t.AtTime) == "" {
			return fmt.Errorf("atTime is required")
		}
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(t.AtTime)); err != nil {
			if _, err2 := time.Parse("2006-01-02T15:04", strings.TrimSpace(t.AtTime)); err2 != nil {
				return fmt.Errorf("atTime must be a valid datetime")
			}
		}
	case KindDaily, KindWeekly:
		if !validClock(t.ClockTime) {
			return fmt.Errorf("clockTime must be HH:mm[:ss]")
		}
		if strings.TrimSpace(t.TimeZone) == "" {
			return fmt.Errorf("timeZone is required")
		}
		if _, err := time.LoadLocation(strings.TrimSpace(t.TimeZone)); err != nil {
			return fmt.Errorf("timeZone is invalid")
		}
		if t.Kind == KindWeekly {
			if len(t.Weekdays) == 0 {
				return fmt.Errorf("weekdays is required")
			}
			seen := map[int]bool{}
			for _, d := range t.Weekdays {
				if d < 1 || d > 7 || seen[d] {
					return fmt.Errorf("weekdays must contain unique values from 1 through 7")
				}
				seen[d] = true
			}
		}
	case KindCron:
		if strings.TrimSpace(t.Cron) == "" {
			return fmt.Errorf("cron is required")
		}
		if strings.TrimSpace(t.TimeZone) == "" {
			return fmt.Errorf("timeZone is required")
		}
		if _, err := time.LoadLocation(strings.TrimSpace(t.TimeZone)); err != nil {
			return fmt.Errorf("timeZone is invalid")
		}
	}
	return nil
}

func normalizeTemplate(t Template) Template {
	t.ID = strings.TrimSpace(t.ID)
	t.Title = strings.TrimSpace(t.Title)
	t.Description = strings.TrimSpace(t.Description)
	t.TaskTitle = strings.TrimSpace(t.TaskTitle)
	if t.TaskTitle == "" {
		t.TaskTitle = t.Title
	}
	t.Category = strings.TrimSpace(t.Category)
	t.Prompt = strings.TrimSpace(t.Prompt)
	targetKind := strings.TrimSpace(string(t.TargetKind))
	if targetKind == "" {
		targetKind = string(TargetSession)
	}
	t.TargetKind = TargetKind(targetKind)
	return t
}

func validClock(v string) bool {
	_, err := time.Parse("15:04", strings.TrimSpace(v))
	if err == nil {
		return true
	}
	_, err = time.Parse("15:04:05", strings.TrimSpace(v))
	return err == nil
}

func (s *SQLiteStore) ListTemplates() ([]Template, error) {
	rows, err := s.db.Query(`SELECT id,title,description,task_title,category,prompt,kind,amount,at_time,clock_time,time_zone,weekdays_json,cron,goal,plan,target_kind,target_workspace,target_profile,provider,model,reasoning_effort,created_at,updated_at FROM schedule_templates ORDER BY updated_at DESC,id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Template
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetTemplate(id string) (Template, bool, error) {
	row := s.db.QueryRow(`SELECT id,title,description,task_title,category,prompt,kind,amount,at_time,clock_time,time_zone,weekdays_json,cron,goal,plan,target_kind,target_workspace,target_profile,provider,model,reasoning_effort,created_at,updated_at FROM schedule_templates WHERE id=?`, id)
	t, err := scanTemplate(row)
	if err == sql.ErrNoRows {
		return Template{}, false, nil
	}
	if err != nil {
		return Template{}, false, err
	}
	return t, true, nil
}

func scanTemplate(s interface{ Scan(...any) error }) (Template, error) {
	var t Template
	var days, created, updated string
	err := s.Scan(&t.ID, &t.Title, &t.Description, &t.TaskTitle, &t.Category, &t.Prompt, &t.Kind, &t.Amount, &t.AtTime, &t.ClockTime, &t.TimeZone, &days, &t.Cron, &t.Goal, &t.Plan, &t.TargetKind, &t.TargetWorkspace, &t.TargetProfile, &t.Provider, &t.Model, &t.ReasoningEffort, &created, &updated)
	if err != nil {
		return t, err
	}
	_ = json.Unmarshal([]byte(days), &t.Weekdays)
	t.CreatedAt, _ = time.Parse(instantLayout, created)
	t.UpdatedAt, _ = time.Parse(instantLayout, updated)
	return t, nil
}

func templateArgs(t Template, now string) []any {
	days, _ := json.Marshal(t.Weekdays)
	return []any{t.ID, t.Title, t.Description, t.TaskTitle, t.Category, t.Prompt, string(t.Kind), t.Amount, t.AtTime, t.ClockTime, t.TimeZone, string(days), t.Cron, t.Goal, t.Plan, string(t.TargetKind), t.TargetWorkspace, t.TargetProfile, t.Provider, t.Model, t.ReasoningEffort, now, now}
}

func (s *SQLiteStore) PutTemplate(t Template) error {
	t = normalizeTemplate(t)
	if err := t.Validate(); err != nil {
		return err
	}
	now := formatInstant(time.Now())
	_, err := s.db.ExecContext(context.Background(), `INSERT INTO schedule_templates (id,title,description,task_title,category,prompt,kind,amount,at_time,clock_time,time_zone,weekdays_json,cron,goal,plan,target_kind,target_workspace,target_profile,provider,model,reasoning_effort,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, templateArgs(t, now)...)
	return err
}

func (s *SQLiteStore) UpdateTemplate(t Template) error {
	t = normalizeTemplate(t)
	if err := t.Validate(); err != nil {
		return err
	}
	now := formatInstant(time.Now())
	_, err := s.db.ExecContext(context.Background(), `UPDATE schedule_templates SET title=?,description=?,task_title=?,category=?,prompt=?,kind=?,amount=?,at_time=?,clock_time=?,time_zone=?,weekdays_json=?,cron=?,goal=?,plan=?,target_kind=?,target_workspace=?,target_profile=?,provider=?,model=?,reasoning_effort=?,updated_at=? WHERE id=?`,
		t.Title, t.Description, t.TaskTitle, t.Category, t.Prompt, string(t.Kind), t.Amount, t.AtTime, t.ClockTime, t.TimeZone, mustJSON(t.Weekdays), t.Cron, t.Goal, t.Plan, string(t.TargetKind), t.TargetWorkspace, t.TargetProfile, t.Provider, t.Model, t.ReasoningEffort, now, t.ID)
	return err
}

func mustJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func (s *SQLiteStore) DeleteTemplate(id string) (bool, error) {
	r, err := s.db.Exec(`DELETE FROM schedule_templates WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}
