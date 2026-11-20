package schedule

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
)

func TestScheduleTemplatesDefaultsAndCRUD(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	templates, err := store.ListTemplates()
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	wantIDs := map[string]bool{
		"weekday-digest":   false,
		"weekly-review":    false,
		"project-health":   false,
		"knowledge-garden": false,
	}
	if len(templates) != len(wantIDs) {
		t.Fatalf("got %d default templates, want %d", len(templates), len(wantIDs))
	}
	for _, template := range templates {
		if _, ok := wantIDs[template.ID]; !ok {
			t.Errorf("unexpected default template id %q", template.ID)
			continue
		}
		wantIDs[template.ID] = true
	}
	for id, found := range wantIDs {
		if !found {
			t.Errorf("default template %q was not inserted", id)
		}
	}

	created := Template{
		ID:          "custom-check",
		Title:       "  Custom check  ",
		TaskTitle:   "  Run checks  ",
		Category:    "  custom  ",
		Description: "  Custom description  ",
		Prompt:      "  Check the project  ",
		Kind:        KindEvery,
		Amount:      "300",
		TargetKind:  TargetSession,
	}
	if err := store.PutTemplate(created); err != nil {
		t.Fatalf("PutTemplate: %v", err)
	}
	created, found, err := store.GetTemplate(created.ID)
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if !found {
		t.Fatal("created template was not found")
	}
	if created.Title != "Custom check" || created.TaskTitle != "Run checks" ||
		created.Category != "custom" || created.Description != "Custom description" ||
		created.Prompt != "Check the project" {
		t.Errorf("template fields were not normalized: %+v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Errorf("timestamps were not persisted: %+v", created)
	}

	duplicate := created
	if err := store.PutTemplate(duplicate); err == nil {
		t.Fatal("PutTemplate accepted a duplicate id")
	}

	created.Title = "Updated check"
	created.Amount = "600"
	if err := store.UpdateTemplate(created); err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}
	updated, found, err := store.GetTemplate(created.ID)
	if err != nil {
		t.Fatalf("GetTemplate after update: %v", err)
	}
	if !found || updated.Title != "Updated check" || updated.Amount != "600" {
		t.Errorf("updated template mismatch: found=%v template=%+v", found, updated)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("update changed createdAt: before=%v after=%v", created.CreatedAt, updated.CreatedAt)
	}

	deleted, err := store.DeleteTemplate(created.ID)
	if err != nil || !deleted {
		t.Fatalf("DeleteTemplate = (%v, %v), want (true, nil)", deleted, err)
	}
	deleted, err = store.DeleteTemplate(created.ID)
	if err != nil || deleted {
		t.Fatalf("second DeleteTemplate = (%v, %v), want (false, nil)", deleted, err)
	}
}

func TestScheduleTemplateMigrationPreservesExistingTemplates(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "schedule.sqlite"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	migrations := storeMigrations()
	if err := migrate.Apply(context.Background(), db, migrations[:11]); err != nil {
		t.Fatalf("apply schema through v11: %v", err)
	}
	_, err = db.Exec(`
INSERT INTO schedule_templates (id, title, prompt, kind, created_at, updated_at)
VALUES ('existing-template', 'Keep my template', 'Existing prompt', 'after',
        '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)
	if err != nil {
		t.Fatalf("insert pre-upgrade template: %v", err)
	}
	if err := migrate.Apply(context.Background(), db, storeMigrations()); err != nil {
		t.Fatalf("apply latest schema: %v", err)
	}

	store := &SQLiteStore{db: db}
	templates, err := store.ListTemplates()
	if err != nil {
		t.Fatalf("ListTemplates after migration: %v", err)
	}
	if len(templates) != 5 {
		t.Fatalf("got %d templates after migration, want existing + four defaults", len(templates))
	}
	existing, found, err := store.GetTemplate("existing-template")
	if err != nil {
		t.Fatalf("GetTemplate existing: %v", err)
	}
	if !found || existing.Title != "Keep my template" || existing.Prompt != "Existing prompt" {
		t.Errorf("migration did not preserve existing template: found=%v template=%+v", found, existing)
	}
	if existing.TaskTitle != "" || existing.Category != "" {
		t.Errorf("new fields should use migration defaults for existing rows: %+v", existing)
	}
}

func TestScheduleTemplateValidation(t *testing.T) {
	base := Template{
		ID:         "test",
		Title:      "Test",
		TaskTitle:  "Run test",
		Prompt:     "Check the project",
		Kind:       KindEvery,
		Amount:     "60",
		TargetKind: TargetSession,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}

	invalid := base
	invalid.Amount = "0"
	if err := invalid.Validate(); err == nil {
		t.Fatal("template with non-positive amount was accepted")
	}
	invalid = base
	invalid.TargetKind = TargetWorkspace
	invalid.Goal = "Check project"
	if err := invalid.Validate(); err == nil {
		t.Fatal("workspace template without targetWorkspace was accepted")
	}
	invalid = base
	invalid.TargetKind = TargetKind("unknown")
	if err := normalizeTemplate(invalid).Validate(); err == nil {
		t.Fatal("template with an invalid targetKind was accepted")
	}
}
