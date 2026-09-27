package workflowcheck

import (
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

type formulaView struct {
	ID      string               `json:"id"`
	State   domain.FormulaState  `json:"state"`
	Version int64                `json:"version"`
	Steps   []domain.FormulaStep `json:"steps"`
}

type vesselView struct {
	ID                string             `json:"id"`
	State             domain.VesselState `json:"state"`
	Version           int64              `json:"version"`
	ActiveRunID       *string            `json:"active_run_id"`
	CurrentCleaningID *string            `json:"current_cleaning_id"`
}

type cleaningView struct {
	ID         string               `json:"id"`
	VesselID   string               `json:"vessel_id"`
	RunID      string               `json:"run_id"`
	Number     int                  `json:"number"`
	State      domain.CleaningState `json:"state"`
	Checks     []checkView          `json:"checks"`
	Completion *completionView      `json:"completion"`
	Version    int64                `json:"version"`
}

type checkView struct {
	Key   string            `json:"key"`
	State domain.CheckState `json:"state"`
	Note  string            `json:"note"`
}

type completionView struct {
	ChecksTotal    int             `json:"checks_total"`
	ChecksRecorded int             `json:"checks_recorded"`
	ChecksPassed   int             `json:"checks_passed"`
	ChecksFailed   int             `json:"checks_failed"`
	RunState       domain.RunState `json:"run_state"`
	CompletedAt    time.Time       `json:"completed_at"`
}

type vesselDetailView struct {
	vesselView
	CurrentCleaning *cleaningView  `json:"current_cleaning"`
	CleaningHistory []cleaningView `json:"cleaning_history"`
}

type runView struct {
	ID           string                      `json:"id"`
	State        domain.RunState             `json:"state"`
	Version      int64                       `json:"version"`
	VesselID     *string                     `json:"vessel_id"`
	CleaningID   *string                     `json:"cleaning_id"`
	Observations []domain.GravityObservation `json:"observations"`
	Metrics      *domain.RunMetrics          `json:"metrics"`
}

type runDetailView struct {
	runView
	Cleaning *cleaningView `json:"cleaning"`
}

type pairView struct {
	Run    runView    `json:"run"`
	Vessel vesselView `json:"vessel"`
}

func createFormulaPayload(name string) map[string]any {
	return map[string]any{
		"name":                    name,
		"style":                   "Cellar pilot",
		"target_original_gravity": 1.052,
		"target_final_gravity":    1.012,
		"max_fermentation_temp_c": 22.0,
		"minimum_days":            7,
		"maximum_days":            21,
	}
}

func formulaStepPayload() map[string]any {
	return map[string]any{
		"steps": []map[string]any{
			{
				"sequence":      1,
				"label":         "Heat and combine",
				"temperature_c": 66.0,
				"hold_minutes":  60,
			},
			{
				"sequence":      2,
				"label":         "Transfer and cool",
				"temperature_c": 20.0,
				"hold_minutes":  30,
			},
			{
				"sequence":      3,
				"label":         "Hold for primary activity",
				"temperature_c": 19.0,
				"hold_minutes":  240,
			},
		},
	}
}

func createRunPayload(code, formulaID string) map[string]any {
	return map[string]any{
		"code":                  code,
		"formula_id":            formulaID,
		"planned_volume_liters": 180.0,
		"notes":                 "pilot run",
	}
}

func verifiedAt(base time.Time, sequence int) time.Time {
	return base.Add(time.Duration(sequence) * 10 * time.Second)
}

func requireRunState(run runView, state domain.RunState) error {
	if run.State != state {
		return fmt.Errorf("expected run state %q, got %q", state, run.State)
	}
	return nil
}

func requireVesselState(vessel vesselView, state domain.VesselState) error {
	if vessel.State != state {
		return fmt.Errorf("expected vessel state %q, got %q", state, vessel.State)
	}
	return nil
}
