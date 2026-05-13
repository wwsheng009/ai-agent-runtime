package commands

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	slashCompletionMaxInputTokenRunes = 64
	slashCompletionMaxPopupRows       = 8
)

type chatSlashCompletionContext struct {
	Active         bool
	Query          string
	TokenStart     int
	TokenEnd       int
	InArguments    bool
	Command        string
	ArgsQuery      string
	ArgsTokenStart int
	ArgsTokenEnd   int
}

type chatSlashCompletionMatchKind int

const (
	chatSlashCompletionMatchKindExact chatSlashCompletionMatchKind = iota
	chatSlashCompletionMatchKindCommandPrefix
	chatSlashCompletionMatchKindShortcutPrefix
	chatSlashCompletionMatchKindAliasPrefix
)

type chatSlashCompletionCandidate struct {
	Command       string
	AliasOf       string
	ShortcutOf    string
	Usage         string
	Summary       string
	Group         string
	Score         int
	AcceptsArgs   bool
	Informational bool
	MatchKind     chatSlashCompletionMatchKind
}

type chatSlashCompletionState struct {
	Active       bool
	Context      chatSlashCompletionContext
	Candidates   []chatSlashCompletionCandidate
	Selected     int
	Warning      string
	Query        string
	CommonPrefix string
	ExactMatch   bool
}

func buildSlashCompletionState(text string, cursor int, previousSelected int) chatSlashCompletionState {
	return buildSlashCompletionStateWithPrevious(text, cursor, previousSelected, "")
}

func buildSlashCompletionStateWithPrevious(text string, cursor int, previousSelected int, previousCommand string) chatSlashCompletionState {
	context := detectSlashCompletionContext(text, cursor)
	state := chatSlashCompletionState{
		Context: context,
		Query:   context.Query,
	}
	if !context.Active {
		return state
	}

	candidates := matchSlashCommandCandidates(chatSlashCommandCatalog(), context.Query)
	state.Candidates = candidates
	state.Active = true
	state.CommonPrefix = longestCommonSlashPrefix(candidates)
	state.Selected = -1

	if len(candidates) == 0 {
		state.Warning = fmt.Sprintf("未找到匹配命令: %s", context.Query)
		return state
	}

	if previousCommand != "" {
		for i, candidate := range candidates {
			if strings.EqualFold(candidate.Command, previousCommand) {
				state.Selected = i
				state.ExactMatch = candidateIsExactMatch(candidate, context.Query)
				return state
			}
		}
	}

	if previousSelected >= 0 && previousSelected < len(candidates) {
		state.Selected = previousSelected
	} else {
		state.Selected = 0
	}
	state.ExactMatch = candidateIsExactMatch(candidates[state.Selected], context.Query)
	return state
}

func detectSlashCompletionContext(text string, cursor int) chatSlashCompletionContext {
	runes := []rune(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if len(runes) == 0 {
		return chatSlashCompletionContext{}
	}

	start := 0
	for start < len(runes) && unicode.IsSpace(runes[start]) {
		start++
	}
	if start >= len(runes) || runes[start] != '/' {
		return chatSlashCompletionContext{}
	}
	if cursor < start {
		return chatSlashCompletionContext{}
	}

	end := start
	for end < len(runes) && !unicode.IsSpace(runes[end]) {
		end++
	}
	if end-start > slashCompletionMaxInputTokenRunes {
		return chatSlashCompletionContext{}
	}

	queryEnd := cursor
	if queryEnd < start {
		queryEnd = start
	}
	if queryEnd > end {
		queryEnd = end
	}

	spec, exact := resolveSlashCommandSpecForToken(string(runes[start:end]))
	if cursor <= end {
		ctx := chatSlashCompletionContext{
			Active:         true,
			Query:          string(runes[start:queryEnd]),
			TokenStart:     start,
			TokenEnd:       end,
			InArguments:    false,
			ArgsTokenStart: cursor,
			ArgsTokenEnd:   cursor,
		}
		if exact {
			ctx.Command = spec.Name
		}
		return ctx
	}

	ctx := chatSlashCompletionContext{
		Active:         false,
		Query:          string(runes[start:end]),
		TokenStart:     start,
		TokenEnd:       end,
		InArguments:    false,
		ArgsTokenStart: cursor,
		ArgsTokenEnd:   cursor,
	}
	if exact && spec.AcceptsArgs {
		argStart, argEnd, argQuery := detectSlashArgumentTokenRange(runes, end, cursor)
		ctx.Command = spec.Name
		ctx.InArguments = true
		ctx.ArgsQuery = argQuery
		ctx.ArgsTokenStart = argStart
		ctx.ArgsTokenEnd = argEnd
	}
	return ctx
}

func resolveSlashCommandSpecForToken(token string) (chatSlashCommandSpec, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return chatSlashCommandSpec{}, false
	}
	spec, ok := chatSlashCommandCatalogMap()[token]
	if !ok {
		return chatSlashCommandSpec{}, false
	}
	return spec, true
}

func detectSlashArgumentTokenRange(runes []rune, commandEnd, cursor int) (int, int, string) {
	if commandEnd < 0 {
		commandEnd = 0
	}
	if commandEnd > len(runes) {
		commandEnd = len(runes)
	}
	if cursor < commandEnd {
		cursor = commandEnd
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}

	for i := commandEnd; i < len(runes); {
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
		if i >= len(runes) {
			break
		}
		tokenStart := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) {
			i++
		}
		tokenEnd := i
		if cursor >= tokenStart && cursor <= tokenEnd {
			return tokenStart, tokenEnd, string(runes[tokenStart:cursor])
		}
		if cursor < tokenStart {
			return cursor, cursor, ""
		}
	}

	return cursor, cursor, ""
}

func matchSlashCommandCandidates(specs []chatSlashCommandSpec, query string) []chatSlashCompletionCandidate {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}

	candidates := make([]chatSlashCompletionCandidate, 0, len(specs))
	for _, spec := range specs {
		if spec.Hidden {
			continue
		}
		candidates = append(candidates, matchSlashCommandSpecCandidates(spec, query)...)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
		if left.Score != right.Score {
			return left.Score < right.Score
		}
		leftGroup := slashCommandGroupRank(left.Group)
		rightGroup := slashCommandGroupRank(right.Group)
		if leftGroup != rightGroup {
			return leftGroup < rightGroup
		}
		if left.MatchKind != right.MatchKind {
			return left.MatchKind < right.MatchKind
		}
		if left.Command != right.Command {
			return left.Command < right.Command
		}
		if left.AliasOf != right.AliasOf {
			return left.AliasOf < right.AliasOf
		}
		if left.ShortcutOf != right.ShortcutOf {
			return left.ShortcutOf < right.ShortcutOf
		}
		return left.Usage < right.Usage
	})

	return candidates
}

func matchSlashCommandSpecCandidates(spec chatSlashCommandSpec, query string) []chatSlashCompletionCandidate {
	names := spec.allNames()
	candidates := make([]chatSlashCompletionCandidate, 0, len(names))
	for _, name := range names {
		candidate, ok := matchSlashCommandName(spec, name, query)
		if !ok {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return candidates
}

func matchSlashCommandName(spec chatSlashCommandSpec, name, query string) (chatSlashCompletionCandidate, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return chatSlashCompletionCandidate{}, false
	}
	nameLower := strings.ToLower(name)
	if query == "" {
		return chatSlashCompletionCandidate{}, false
	}

	matchKind := chatSlashCompletionMatchKindCommandPrefix
	score := -1
	switch {
	case nameLower == query:
		score = 0
		matchKind = chatSlashCompletionMatchKindExact
	case strings.HasPrefix(nameLower, query):
		score = 10
		matchKind = chatSlashCompletionMatchKindCommandPrefix
	}

	if score < 0 {
		return chatSlashCompletionCandidate{}, false
	}

	candidate := chatSlashCompletionCandidate{
		Command:     name,
		Usage:       spec.Usage,
		Summary:     spec.Summary,
		Group:       spec.Group,
		AcceptsArgs: spec.AcceptsArgs,
		MatchKind:   matchKind,
	}
	candidate.Score = score + slashCommandSourceScore(spec, name, matchKind)
	if name == spec.Name && spec.ShortcutOf != "" {
		candidate.ShortcutOf = spec.ShortcutOf
	} else if name != spec.Name {
		candidate.AliasOf = spec.Name
	}
	return candidate, true
}

func slashCommandSourceScore(spec chatSlashCommandSpec, name string, matchKind chatSlashCompletionMatchKind) int {
	if name == spec.Name {
		return 0
	}
	if matchKind == chatSlashCompletionMatchKindExact {
		return 0
	}
	return 20
}

func longestCommonSlashPrefix(candidates []chatSlashCompletionCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	prefix := strings.ToLower(candidates[0].Command)
	for _, candidate := range candidates[1:] {
		current := strings.ToLower(candidate.Command)
		for !strings.HasPrefix(current, prefix) {
			if prefix == "" {
				return ""
			}
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

func candidateIsExactMatch(candidate chatSlashCompletionCandidate, query string) bool {
	if candidate.Command == "" {
		return false
	}
	return strings.EqualFold(candidate.Command, strings.TrimSpace(query))
}

func applySlashCommandCompletion(text string, cursor, tokenStart, tokenEnd int, candidate string, acceptsArgs bool) (string, int) {
	runes := []rune(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if tokenStart < 0 {
		tokenStart = 0
	}
	if tokenEnd < tokenStart {
		tokenEnd = tokenStart
	}
	if tokenStart > len(runes) {
		tokenStart = len(runes)
	}
	if tokenEnd > len(runes) {
		tokenEnd = len(runes)
	}

	replacement := []rune(candidate)
	next := make([]rune, 0, len(runes)-tokenEnd+tokenStart+len(replacement)+1)
	next = append(next, runes[:tokenStart]...)
	next = append(next, replacement...)

	insertedCursor := tokenStart + len(replacement)
	if acceptsArgs {
		nextRuneIsSpace := tokenEnd < len(runes) && unicode.IsSpace(runes[tokenEnd])
		if !nextRuneIsSpace {
			next = append(next, ' ')
			insertedCursor++
		}
	}
	next = append(next, runes[tokenEnd:]...)
	return string(next), insertedCursor
}

func slashCommandGroupRank(group string) int {
	switch strings.ToLower(strings.TrimSpace(group)) {
	case string(chatSlashCommandGroupHelp):
		return 0
	case string(chatSlashCommandGroupBasics):
		return 10
	case string(chatSlashCommandGroupSession):
		return 20
	case string(chatSlashCommandGroupModel):
		return 30
	case string(chatSlashCommandGroupContext):
		return 40
	case string(chatSlashCommandGroupPermission):
		return 50
	case string(chatSlashCommandGroupFunctions):
		return 60
	case string(chatSlashCommandGroupShell):
		return 70
	default:
		return 100
	}
}
