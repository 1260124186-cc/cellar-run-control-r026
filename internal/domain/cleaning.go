package domain

import "time"

// CleaningState tracks the lifecycle of a single vessel cleaning.
type CleaningState string

const (
	CleaningOpen     CleaningState = "open"
	CleaningComplete CleaningState = "complete"
)

// CheckState records whether a mandatory cleaning checklist item has been
// executed and with what outcome.
type CheckState string

const (
	CheckPending CheckState = "pending"
	CheckPassed  CheckState = "passed"
	CheckFailed  CheckState = "failed"
)

// Checklist item keys are matched case-insensitively against this catalog.
const (
	CheckKeyVisualInspection = "visual-inspection"
	CheckKeySanitizerRinse   = "sanitizer-rinse"
	CheckKeyValveGasket      = "valve-gasket"
)

// CleaningCheck is one mandatory checklist item attached to a cleaning.
type CleaningCheck struct {
	Key        string     `json:"key"`
	Label      string     `json:"label"`
	State      CheckState `json:"state"`
	Note       string     `json:"note,omitempty"`
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
}

// CleaningCompletion is the immutable snapshot captured when a cleaning is
// finished. It freezes the outcome and the fact that the associated run was
// still in a terminal state at release time.
type CleaningCompletion struct {
	ChecksTotal    int       `json:"checks_total"`
	ChecksRecorded int       `json:"checks_recorded"`
	ChecksPassed   int       `json:"checks_passed"`
	ChecksFailed   int       `json:"checks_failed"`
	RunState       RunState  `json:"run_state"`
	CompletedAt    time.Time `json:"completed_at"`
}

// CleaningRecord is an append-only record created every time a vessel enters
// cleaning. It owns the mandatory checklist and the completion snapshot.
// Completed records are never overwritten or replaced.
type CleaningRecord struct {
	ID         string              `json:"id"`
	VesselID   string              `json:"vessel_id"`
	RunID      string              `json:"run_id"`
	Number     int                 `json:"number"`
	State      CleaningState       `json:"state"`
	Checks     []CleaningCheck     `json:"checks"`
	Completion *CleaningCompletion `json:"completion,omitempty"`
	Version    int64               `json:"version"`
	StartedAt  time.Time           `json:"started_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

// StandardCleaningChecks returns the mandatory checklist every cleaning starts
// with. All items must be executed before the vessel can be released.
func StandardCleaningChecks() []CleaningCheck {
	definitions := []struct {
		key   string
		label string
	}{
		{CheckKeyVisualInspection, "Visual interior inspection"},
		{CheckKeySanitizerRinse, "Sanitizer rinse completion"},
		{CheckKeyValveGasket, "Valve and gasket inspection"},
	}
	checks := make([]CleaningCheck, 0, len(definitions))
	for _, definition := range definitions {
		checks = append(checks, CleaningCheck{
			Key:   definition.key,
			Label: definition.label,
			State: CheckPending,
		})
	}
	return checks
}

// NewCleaningRecord starts an open cleaning with the mandatory checklist.
func NewCleaningRecord(
	id string,
	vesselID string,
	runID string,
	number int,
	now time.Time,
) CleaningRecord {
	stamp := now.UTC()
	return CleaningRecord{
		ID:        id,
		VesselID:  vesselID,
		RunID:     runID,
		Number:    number,
		State:     CleaningOpen,
		Checks:    StandardCleaningChecks(),
		Version:   1,
		StartedAt: stamp,
		UpdatedAt: stamp,
	}
}

// CleaningCheckInput is one executed checklist result submitted by an operator.
type CleaningCheckInput struct {
	Key    string
	Passed bool
	Note   string
}

func normalizeCheckKey(value string) string {
	return toFold(value)
}

// RecordResults marks pending checklist items with their executed outcome.
// Unknown items, items that were already recorded, and duplicate items in the
// same batch are rejected without partial writes.
func (c *CleaningRecord) RecordResults(results []CleaningCheckInput, now time.Time) error {
	if c.State != CleaningOpen {
		return NewError(CodeCleaningState, "cleaning is already complete")
	}
	if len(results) == 0 {
		return FieldError(CodeInvalidInput, "results",
			"at least one checklist result is required")
	}
	indexByKey := make(map[string]int, len(c.Checks))
	for position, check := range c.Checks {
		indexByKey[normalizeCheckKey(check.Key)] = position
	}
	seen := make(map[string]struct{}, len(results))
	type recorded struct {
		position int
		passed   bool
		note     string
	}
	updates := make([]recorded, 0, len(results))
	for _, result := range results {
		key := normalizeCheckKey(result.Key)
		if key == "" {
			return FieldError(CodeInvalidInput, "check_key",
				"checklist item key is required")
		}
		if _, repeated := seen[key]; repeated {
			return FieldError(CodeCleaningCheckConflict, "check_key",
				"checklist item is submitted more than once")
		}
		seen[key] = struct{}{}
		position, known := indexByKey[key]
		if !known {
			return FieldError(CodeInvalidInput, "check_key",
				"unknown cleaning checklist item")
		}
		if c.Checks[position].State != CheckPending {
			return NewError(CodeCleaningCheckConflict,
				"checklist item has already been recorded")
		}
		if len([]rune(result.Note)) > 500 {
			return FieldError(CodeInvalidInput, "note",
				"must not exceed 500 characters")
		}
		updates = append(updates, recorded{
			position: position,
			passed:   result.Passed,
			note:     result.Note,
		})
	}
	stamp := now.UTC()
	for _, update := range updates {
		state := CheckFailed
		if update.passed {
			state = CheckPassed
		}
		recordedAt := stamp
		check := c.Checks[update.position]
		check.State = state
		check.Note = update.note
		check.RecordedAt = &recordedAt
		c.Checks[update.position] = check
	}
	c.UpdatedAt = stamp
	c.Version++
	return nil
}

// AllChecksRecorded reports whether every mandatory checklist item has an
// executed result.
func (c CleaningRecord) AllChecksRecorded() bool {
	if len(c.Checks) == 0 {
		return false
	}
	for _, check := range c.Checks {
		if check.State == CheckPending {
			return false
		}
	}
	return true
}

// Complete closes the cleaning and freezes the completion snapshot. It can only
// succeed when every mandatory checklist item has been executed and the
// associated run is still in a terminal state.
func (c *CleaningRecord) Complete(runState RunState, now time.Time) error {
	if c.State != CleaningOpen {
		return NewError(CodeCleaningState, "cleaning is already complete")
	}
	if !c.AllChecksRecorded() {
		return NewError(CodeCleaningCheckIncomplete,
			"all mandatory cleaning checklist items must be recorded")
	}
	if runState != RunCompleted && runState != RunAborted {
		return NewError(CodeRunState, "associated run is no longer in a terminal state")
	}
	stamp := now.UTC()
	completion := CleaningCompletion{
		ChecksTotal: len(c.Checks),
		RunState:    runState,
		CompletedAt: stamp,
	}
	for _, check := range c.Checks {
		completion.ChecksRecorded++
		switch check.State {
		case CheckPassed:
			completion.ChecksPassed++
		case CheckFailed:
			completion.ChecksFailed++
		}
	}
	c.Completion = &completion
	c.State = CleaningComplete
	c.UpdatedAt = stamp
	c.Version++
	return nil
}

// Clone returns an independent deep copy of the record.
func (c CleaningRecord) Clone() CleaningRecord {
	cloned := c
	cloned.Checks = make([]CleaningCheck, len(c.Checks))
	for i, check := range c.Checks {
		copiedCheck := check
		if check.RecordedAt != nil {
			recordedAt := *check.RecordedAt
			copiedCheck.RecordedAt = &recordedAt
		}
		cloned.Checks[i] = copiedCheck
	}
	if c.Completion != nil {
		completion := *c.Completion
		cloned.Completion = &completion
	}
	return cloned
}
