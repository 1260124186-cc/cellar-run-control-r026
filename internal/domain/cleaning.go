package domain

import "time"

// CleaningState is the lifecycle of a single cleaning record. A record stays
// in progress for as long as the vessel is in the cleaning state and is only
// completed when every required check has an execution result and the
// associated run remains terminal.
type CleaningState string

const (
	CleaningInProgress CleaningState = "in_progress"
	CleaningCompleted  CleaningState = "completed"
)

// CheckResult is the recorded execution outcome of a cleaning check.
type CheckResult string

const (
	CheckPassed CheckResult = "passed"
	CheckFailed CheckResult = "failed"
)

// CleaningCheck is a required cleaning check item together with its execution
// result snapshot. An item without a recorded result is still pending.
type CleaningCheck struct {
	Sequence   int          `json:"sequence"`
	Label      string       `json:"label"`
	Required   bool         `json:"required"`
	Result     *CheckResult `json:"result,omitempty"`
	RecordedAt *time.Time   `json:"recorded_at,omitempty"`
	Note       string       `json:"note,omitempty"`
}

// CleaningRecord is the immutable-on-completion snapshot created whenever a
// vessel enters cleaning. It is shared by the vessel detail and the run detail
// and is never overwritten by a later cleaning cycle.
type CleaningRecord struct {
	ID          string          `json:"id"`
	VesselID    string          `json:"vessel_id"`
	RunID       string          `json:"run_id"`
	State       CleaningState   `json:"state"`
	Checks      []CleaningCheck `json:"checks"`
	StartedAt   time.Time       `json:"started_at"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Version     int64           `json:"version"`
}

// standardCleaningChecks is the mandatory checklist created for every
// cleaning. All items are required and each must carry an execution result
// before the vessel can be released.
func standardCleaningChecks() []CleaningCheck {
	labels := []string{
		"Visual inspection of interior surfaces",
		"Caustic circulation wash",
		"Sanitizing rinse",
		"Drain and dryness verification",
	}
	checks := make([]CleaningCheck, 0, len(labels))
	for index, label := range labels {
		checks = append(checks, CleaningCheck{
			Sequence: index + 1,
			Label:    label,
			Required: true,
		})
	}
	return checks
}

// NewCleaningRecord starts an in-progress record for a vessel/run cleaning.
func NewCleaningRecord(id, vesselID, runID string, now time.Time) CleaningRecord {
	return CleaningRecord{
		ID:        id,
		VesselID:  vesselID,
		RunID:     runID,
		State:     CleaningInProgress,
		Checks:    standardCleaningChecks(),
		StartedAt: now.UTC(),
		Version:   1,
	}
}

func (r CleaningRecord) Clone() CleaningRecord {
	cloned := r
	cloned.Checks = append([]CleaningCheck(nil), r.Checks...)
	if r.CompletedAt != nil {
		value := *r.CompletedAt
		cloned.CompletedAt = &value
	}
	for index := range r.Checks {
		if r.Checks[index].Result != nil {
			value := *r.Checks[index].Result
			cloned.Checks[index].Result = &value
		}
		if r.Checks[index].RecordedAt != nil {
			value := *r.Checks[index].RecordedAt
			cloned.Checks[index].RecordedAt = &value
		}
	}
	return cloned
}

// RecordCheck snapshots the execution result of one checklist item.
func (r *CleaningRecord) RecordCheck(
	sequence int,
	result CheckResult,
	note string,
	now time.Time,
) error {
	if r.State != CleaningInProgress {
		return NewError(CodeCleaningState, "cleaning record is already completed")
	}
	if result != CheckPassed && result != CheckFailed {
		return FieldError(CodeInvalidInput, "result",
			"result must be \"passed\" or \"failed\"")
	}
	if len([]rune(note)) > 500 {
		return FieldError(CodeInvalidInput, "note",
			"must not exceed 500 characters")
	}
	for index := range r.Checks {
		check := &r.Checks[index]
		if check.Sequence != sequence {
			continue
		}
		if check.Result != nil {
			return NewError(CodeCleaningCheckConflict,
				"cleaning check already has a recorded result")
		}
		stamp := now.UTC()
		recorded := result
		check.Result = &recorded
		check.RecordedAt = &stamp
		check.Note = note
		r.Updated(stamp)
		return nil
	}
	return NewError(CodeCleaningCheckNotFound, "cleaning check sequence was not found")
}

// AllRequiredChecksRecorded reports whether every mandatory checklist item has
// an execution result.
func (r CleaningRecord) AllRequiredChecksRecorded() bool {
	for _, check := range r.Checks {
		if check.Required && check.Result == nil {
			return false
		}
	}
	return true
}

// Complete snapshots the completion time once the checklist is finished.
func (r *CleaningRecord) Complete(now time.Time) error {
	if r.State == CleaningCompleted {
		return NewError(CodeCleaningState, "cleaning record is already completed")
	}
	if !r.AllRequiredChecksRecorded() {
		return NewError(CodeCleaningIncomplete,
			"all required cleaning checks must be recorded before release")
	}
	stamp := now.UTC()
	r.State = CleaningCompleted
	r.CompletedAt = &stamp
	r.Updated(stamp)
	return nil
}

func (r *CleaningRecord) Updated(stamp time.Time) {
	r.Version++
}
