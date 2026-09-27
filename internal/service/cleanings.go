package service

import (
	"sort"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// RecordCleaningChecksInput carries one batch of executed checklist results.
type RecordCleaningChecksInput struct {
	ExpectedVersion *int64
	Results         []domain.CleaningCheckInput
}

// VesselDetail is the single-vessel view. It embeds the vessel aggregate and
// surfaces the open cleaning plus the append-only completed cleaning history.
type VesselDetail struct {
	domain.Vessel
	CurrentCleaning *domain.CleaningRecord  `json:"current_cleaning,omitempty"`
	CleaningHistory []domain.CleaningRecord `json:"cleaning_history"`
}

// RunDetail is the single-run view. It embeds the run aggregate and surfaces the
// same cleaning record the vessel detail shows.
type RunDetail struct {
	domain.FermentationRun
	Cleaning *domain.CleaningRecord `json:"cleaning,omitempty"`
}

// startCleaning appends a new open cleaning record for the vessel's current run
// and links it from both the vessel and the run. Every entry into cleaning
// creates a fresh record; previous completed records are left untouched.
func (s *Service) startCleaning(
	snapshot *domain.Snapshot,
	vessel *domain.Vessel,
	runID string,
	now time.Time,
) (string, error) {
	number := 1
	for _, cleaning := range snapshot.Cleanings {
		if cleaning.VesselID == vessel.ID && cleaning.Number >= number {
			number = cleaning.Number + 1
		}
	}
	id, err := generateEntityID("clean")
	if err != nil {
		return "", domain.NewError(domain.CodeStorage, err.Error())
	}
	record := domain.NewCleaningRecord(id, vessel.ID, runID, number, now)
	snapshot.Cleanings[id] = record
	vessel.CurrentCleaningID = &id
	return id, nil
}

func (s *Service) RecordCleaningChecks(
	vesselID string,
	input RecordCleaningChecksInput,
) (domain.CleaningRecord, error) {
	var updated domain.CleaningRecord
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		vessel, ok := snapshot.Vessels[vesselID]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		if vessel.State != domain.VesselCleaning || vessel.CurrentCleaningID == nil {
			return domain.NewError(domain.CodeVesselState,
				"vessel is not waiting for cleaning")
		}
		cleaning, ok := snapshot.Cleanings[*vessel.CurrentCleaningID]
		if !ok {
			return domain.NewError(domain.CodeCleaningNotFound,
				"current cleaning record was not found")
		}
		if err := s.checkExpected(cleaning.Version, input.ExpectedVersion); err != nil {
			return err
		}
		if cleaning.VesselID != vessel.ID {
			return domain.NewError(domain.CodeCleaningState,
				"cleaning record belongs to a different vessel")
		}
		if vessel.ActiveRunID == nil || cleaning.RunID != *vessel.ActiveRunID {
			return domain.NewError(domain.CodeCleaningState,
				"cleaning record belongs to a different run")
		}
		if _, runFound := snapshot.Runs[cleaning.RunID]; !runFound {
			return domain.NewError(domain.CodeRunNotFound, "associated run was not found")
		}
		results := make([]domain.CleaningCheckInput, 0, len(input.Results))
		results = append(results, input.Results...)
		if err := cleaning.RecordResults(results, s.clock.Now()); err != nil {
			return err
		}
		snapshot.Cleanings[cleaning.ID] = cleaning.Clone()
		updated = cleaning.Clone()
		return nil
	})
	if err != nil {
		return domain.CleaningRecord{}, err
	}
	return updated, nil
}

func (s *Service) buildVesselDetail(snapshot domain.Snapshot, vessel domain.Vessel) VesselDetail {
	detail := VesselDetail{
		Vessel:          vessel,
		CleaningHistory: []domain.CleaningRecord{},
	}
	if vessel.CurrentCleaningID != nil {
		if cleaning, ok := snapshot.Cleanings[*vessel.CurrentCleaningID]; ok {
			record := cleaning.Clone()
			detail.CurrentCleaning = &record
		}
	}
	for _, cleaning := range snapshot.Cleanings {
		if cleaning.VesselID == vessel.ID && cleaning.State == domain.CleaningComplete {
			detail.CleaningHistory = append(detail.CleaningHistory, cleaning.Clone())
		}
	}
	sort.Slice(detail.CleaningHistory, func(i, j int) bool {
		if detail.CleaningHistory[i].Number != detail.CleaningHistory[j].Number {
			return detail.CleaningHistory[i].Number < detail.CleaningHistory[j].Number
		}
		return detail.CleaningHistory[i].ID < detail.CleaningHistory[j].ID
	})
	return detail
}

func (s *Service) buildRunDetail(snapshot domain.Snapshot, run domain.FermentationRun) RunDetail {
	detail := RunDetail{FermentationRun: run}
	if run.CleaningID != nil {
		if cleaning, ok := snapshot.Cleanings[*run.CleaningID]; ok {
			record := cleaning.Clone()
			detail.Cleaning = &record
		}
	}
	return detail
}
