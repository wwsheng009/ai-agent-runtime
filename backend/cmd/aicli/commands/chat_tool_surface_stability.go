package commands

import "strings"

func stableSharedFunctionSelectionForRequest(session *ChatSession, _ string) (*aicliFunctionSelection, *skillExposureDetails) {
	if session == nil || session.DisableTools {
		return nil, nil
	}
	catalog := ensureFunctionCatalog(session)
	if catalog == nil || catalog.Registry() == nil {
		return nil, nil
	}
	sessionID := currentRuntimeSessionID(session)
	if session.stableSharedToolSelection != nil && strings.EqualFold(session.stableSharedToolSessionID, sessionID) {
		return cloneFunctionSelection(session.stableSharedToolSelection), nil
	}

	selection := catalog.SelectStableSessionFunctions(session)
	if selection == nil {
		return nil, nil
	}
	session.stableSharedToolSessionID = sessionID
	session.stableSharedToolSelection = cloneFunctionSelection(selection)
	return cloneFunctionSelection(selection), nil
}

func resetStableSharedToolSurface(session *ChatSession) {
	if session == nil {
		return
	}
	session.stableSharedToolSessionID = ""
	session.stableSharedToolSelection = nil
}

func cloneFunctionSelection(input *aicliFunctionSelection) *aicliFunctionSelection {
	if input == nil {
		return nil
	}
	return &aicliFunctionSelection{
		Mode:               input.Mode,
		IncludeBuiltin:     input.IncludeBuiltin,
		BuiltinFunctions:   append([]string(nil), input.BuiltinFunctions...),
		SkillFunctions:     append([]string(nil), input.SkillFunctions...),
		FinalFunctionNames: append([]string(nil), input.FinalFunctionNames...),
		Schemas:            cloneFunctionSchemas(input.Schemas),
	}
}
