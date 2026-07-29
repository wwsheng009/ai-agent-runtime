// Package style provides semantic palettes, terminal color profiles, and
// theme resolution for the structured render pipeline.
package style

// Role is a semantic UI color role. Components select roles; resolvers map
// them to concrete render styles. Roles must not encode terminal sequences.
type Role string

const (
	RoleTextPrimary   Role = "TextPrimary"
	RoleTextSecondary Role = "TextSecondary"
	RoleTextMuted     Role = "TextMuted"
	RoleAccent        Role = "Accent"
	RoleUser          Role = "User"
	RoleAssistant     Role = "Assistant"
	RoleSystem        Role = "System"
	RoleTool          Role = "Tool"
	RoleReasoning     Role = "Reasoning"
	RoleApproval      Role = "Approval"
	RoleInfo          Role = "Info"
	RoleSuccess       Role = "Success"
	RoleWarning       Role = "Warning"
	RoleError         Role = "Error"
	RoleLink          Role = "Link"
	RoleBorder        Role = "Border"
	RoleSelection     Role = "Selection"
	RoleCodeInline    Role = "CodeInline"
	RoleCommand       Role = "Command"
	RoleMetaLabel     Role = "MetaLabel"
	RoleTimeline      Role = "Timeline"
	RoleProgress      Role = "Progress"
)

// RequiredRoles lists roles every palette must define.
var RequiredRoles = []Role{
	RoleTextPrimary,
	RoleTextSecondary,
	RoleTextMuted,
	RoleAccent,
	RoleUser,
	RoleAssistant,
	RoleSystem,
	RoleTool,
	RoleReasoning,
	RoleApproval,
	RoleInfo,
	RoleSuccess,
	RoleWarning,
	RoleError,
	RoleLink,
	RoleBorder,
	RoleSelection,
	RoleCodeInline,
	RoleCommand,
	RoleMetaLabel,
	RoleTimeline,
	RoleProgress,
}
