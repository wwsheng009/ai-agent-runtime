package skill

import (
	"reflect"
	"testing"
)

func testSubstitutionContext() SubstitutionContext {
	return SubstitutionContext{
		Enabled:    true,
		Arguments:  []string{"alpha", "beta gamma"},
		Named:      map[string]string{"path": "src/main.go", "mode": "fast"},
		SkillDir:   "/skills/demo",
		ProjectDir: "/workspace",
		SessionID:  "sess-1",
		Effort:     "high",
	}
}

func TestSubstituteSkillText_StandardTokens(t *testing.T) {
	ctx := testSubstitutionContext()
	input := "args=$ARGUMENTS first=$ARGUMENTS[0] second=${1} name=$path mode=${mode}\n" +
		"dir=${SKILL_DIR} project=${PROJECT_DIR} session=${SESSION_ID} effort=${EFFORT}\n" +
		"claude=${CLAUDE_SKILL_DIR} commandcode=${COMMANDCODE_PROJECT_DIR}"

	got, report := SubstituteSkillText(input, ctx)
	want := "args=alpha beta gamma first=alpha second=beta gamma name=src/main.go mode=fast\n" +
		"dir=/skills/demo project=/workspace session=sess-1 effort=high\n" +
		"claude=/skills/demo commandcode=/workspace"
	if got != want {
		t.Fatalf("SubstituteSkillText mismatch:\n got: %q\nwant: %q", got, want)
	}
	if report.Applied != 11 {
		t.Fatalf("applied = %d, want 11", report.Applied)
	}
	if len(report.Unknown) != 0 {
		t.Fatalf("unknown = %v, want none", report.Unknown)
	}
}

func TestSubstituteSkillText_UnknownTokenKeptLiteral(t *testing.T) {
	ctx := testSubstitutionContext()
	got, report := SubstituteSkillText("home=${HOME} notAToken=$NOPE", ctx)
	if got != "home=${HOME} notAToken=$NOPE" {
		t.Fatalf("got %q", got)
	}
	if len(report.Unknown) != 1 || report.Unknown[0] != "HOME" {
		t.Fatalf("unknown = %v, want [HOME]", report.Unknown)
	}
	if report.Changed() {
		t.Fatalf("applied = %d, want 0", report.Applied)
	}
}

func TestSubstituteSkillText_MissingArgumentsBecomeEmpty(t *testing.T) {
	ctx := testSubstitutionContext()
	got, report := SubstituteSkillText("[$ARGUMENTS[9]][$ARGUMENTS[0]]", ctx)
	if got != "[][alpha]" {
		t.Fatalf("got %q", got)
	}
	if len(report.Empty) != 1 || report.Empty[0] != "$ARGUMENTS[9]" {
		t.Fatalf("empty = %v", report.Empty)
	}
}

func TestSubstituteSkillText_SinglePassNoReexpansion(t *testing.T) {
	ctx := testSubstitutionContext()
	ctx.Arguments = []string{"$ARGUMENTS", "${SKILL_DIR}"}
	got, _ := SubstituteSkillText("value=$ARGUMENTS", ctx)
	// 插入内容不再解析：值里的 `$ARGUMENTS` 与 `${SKILL_DIR}` 原样保留。
	if got != "value=$ARGUMENTS ${SKILL_DIR}" {
		t.Fatalf("got %q", got)
	}
}

func TestSubstituteSkillText_DisabledReturnsOriginal(t *testing.T) {
	ctx := testSubstitutionContext()
	ctx.Enabled = false
	got, report := SubstituteSkillText("x=$ARGUMENTS", ctx)
	if got != "x=$ARGUMENTS" || report.Changed() {
		t.Fatalf("got %q report=%+v", got, report)
	}
}

func TestSubstituteSkillText_BareTokensAreNotTouched(t *testing.T) {
	ctx := testSubstitutionContext()
	got, report := SubstituteSkillText("$ARGUMENTS_SUFFIX stays, $unknown stays", ctx)
	if got != "$ARGUMENTS_SUFFIX stays, $unknown stays" {
		t.Fatalf("got %q", got)
	}
	if report.Changed() {
		t.Fatalf("applied = %d, want 0", report.Applied)
	}
}

func TestSplitSkillArguments(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "   ", want: nil},
		{name: "simple", in: "a b c", want: []string{"a", "b", "c"}},
		{name: "double quotes", in: `"a b" c`, want: []string{"a b", "c"}},
		{name: "single quotes", in: `'a b' c`, want: []string{"a b", "c"}},
		{name: "escaped space", in: `a\ b c`, want: []string{"a b", "c"}},
		{name: "empty quoted token", in: `"" x`, want: []string{"", "x"}},
		{name: "unterminated quote", in: `"a b`, want: []string{"a b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SplitSkillArguments(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SplitSkillArguments(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewSubstitutionContext_BindsDeclaredArguments(t *testing.T) {
	item := &Skill{
		Source: &SkillSource{Dir: "/skills/demo"},
		Codex: &CodexSkillMetadata{
			Arguments: []CodexSkillArgument{
				{Name: "path"},
				{Name: "mode", Default: "safe"},
			},
		},
	}
	ctx := NewSubstitutionContext(item, []string{"src/app.go"}, "/ws", "sess", "low", true)
	if ctx.SkillDir != "/skills/demo" || ctx.ProjectDir != "/ws" || ctx.SessionID != "sess" {
		t.Fatalf("context = %+v", ctx)
	}
	if ctx.Named["path"] != "src/app.go" {
		t.Fatalf("named path = %q", ctx.Named["path"])
	}
	if ctx.Named["mode"] != "safe" {
		t.Fatalf("named mode = %q, want declared default", ctx.Named["mode"])
	}

	got, _ := SubstituteSkillText("edit $path in $mode mode", ctx)
	if got != "edit src/app.go in safe mode" {
		t.Fatalf("got %q", got)
	}
}
