package service

import (
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

type CreateFormulaInput struct {
	Name                  string
	Style                 string
	TargetOriginalGravity float64
	TargetFinalGravity    float64
	MaxFermentationTempC  float64
	MinimumDays           int
	MaximumDays           int
}

type SetFormulaStepsInput struct {
	ExpectedVersion *int64
	Steps           []domain.FormulaStep
}

type VersionInput struct {
	ExpectedVersion *int64
}

type CreateVesselInput struct {
	Code      string
	CapacityL float64
}

type CreateRunInput struct {
	Code           string
	FormulaID      string
	PlannedVolumeL float64
	Notes          string
}

type ReserveRunInput struct {
	ExpectedVersion *int64
	VesselID        string
}

type ObservationInput struct {
	Sequence     int
	Gravity      float64
	TemperatureC float64
	ObservedAt   time.Time
	Note         string
}

type FinishCleaningInput struct {
	ExpectedVersion *int64
}

// RecordCleaningCheckInput records the execution result of one mandatory
// cleaning checklist item. The expected version is the current vessel version.
type RecordCleaningCheckInput struct {
	ExpectedVersion *int64
	Sequence        int
	Result          domain.CheckResult
	Note            string
}

// VesselDetail is the vessel view with the shared cleaning record history for
// the vessel's active and completed cleanings.
type VesselDetail struct {
	domain.Vessel
	CleaningRecords []domain.CleaningRecord `json:"cleaning_records"`
}

// RunDetail is the run view with the same cleaning records exposed by the
// vessel detail, so both surfaces show one shared record per cleaning.
type RunDetail struct {
	domain.FermentationRun
	CleaningRecords []domain.CleaningRecord `json:"cleaning_records"`
}
