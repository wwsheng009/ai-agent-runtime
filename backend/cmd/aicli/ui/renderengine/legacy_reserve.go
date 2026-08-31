package renderengine

// LegacyReserveState is the compatibility state surface retained for the
// immediate-mode reserve fallback. Owned ScreenModel frames do not need it;
// the surface keeps the historical diagnostic fields so tests and callers can
// still observe the retired-path bookkeeping (fields are only ever zeroed by
// production code now that the legacy reserve renderer has been removed).
type LegacyReserveState struct {
	ScrollCompensatedRows int
	PendingScrollDownRows int
	OutputScrollDebtRows  int
	CursorOnBlankRow      bool
}