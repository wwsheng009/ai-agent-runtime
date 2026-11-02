package contextmgr

// Ledger header text and its historical variants.
//
// The header is the only text the fact layer contributes to the prompt framing,
// and display surfaces (CLI transcript, web thread projection) recognize the
// ledger block by it as a fallback for rows whose metadata was stripped. Every
// label ever shipped must therefore stay listed here, or an old session starts
// leaking an internal prompt layer into the user-visible conversation.
const factLedgerHeader = "Session facts carried across compaction (constraints, decisions, open work; newest first):"

// legacyFactLedgerHeaders returns the current label plus every label stored
// sessions can still carry.
func legacyFactLedgerHeaders() []string {
	return []string{
		factLedgerHeader,
		// Ledger blocks written before the process/conclusion split, when the
		// block claimed precedence over the compaction prose.
		"Verified fact ledger (authoritative over compacted prose):",
		// Intermediate label from the noise-reduction step.
		"Verified session facts (newest first; already checked, do not redo):",
	}
}

// FactLedgerHeader exposes the current label for display filters.
func FactLedgerHeader() string { return factLedgerHeader }

// LegacyFactLedgerHeaders exposes every known label for display filters.
func LegacyFactLedgerHeaders() []string { return legacyFactLedgerHeaders() }
