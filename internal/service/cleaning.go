package service

import (
	"sort"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// beginCleaning moves a vessel into cleaning and creates the in-progress
// cleaning record with the mandatory checklist snapshot. The record is shared
// state between the vessel and the run and is never replaced later.
func (s *Service) beginCleaning(
	snapshot *domain.Snapshot,
	vessel *domain.Vessel,
	runID string,
) (domain.CleaningRecord, error) {
	now := s.clock.Now()
	if err := vessel.StartCleaning(runID, now); err != nil {
		return domain.CleaningRecord{}, err
	}
	id, err := generateEntityID("clean")
	if err != nil {
		return domain.CleaningRecord{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	record := domain.NewCleaningRecord(id, vessel.ID, runID, now)
	snapshot.CleaningRecords[id] = record
	return record.Clone(), nil
}

// RecordCleaningCheck records the execution result of one mandatory checklist
// item for an in-progress cleaning. The vessel version acts as the optimistic
// concurrency guard and repeated results for the same item are rejected.
func (s *Service) RecordCleaningCheck(
	recordID string,
	input RecordCleaningCheckInput,
) (domain.CleaningRecord, domain.Vessel, error) {
	var updatedRecord domain.CleaningRecord
	var updatedVessel domain.Vessel
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		record, ok := snapshot.CleaningRecords[recordID]
		if !ok {
			return domain.NewError(domain.CodeCleaningNotFound, "cleaning record was not found")
		}
		vessel, ok := snapshot.Vessels[record.VesselID]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "cleaned vessel was not found")
		}
		if err := s.checkExpected(vessel.Version, input.ExpectedVersion); err != nil {
			return err
		}
		if record.State != domain.CleaningInProgress || vessel.State != domain.VesselCleaning {
			return domain.NewError(domain.CodeCleaningState,
				"cleaning record is not awaiting checks")
		}
		if vessel.ActiveRunID == nil || *vessel.ActiveRunID != record.RunID {
			return domain.NewError(domain.CodeCleaningState,
				"cleaning record no longer belongs to the active vessel run")
		}
		run, ok := snapshot.Runs[record.RunID]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "associated run was not found")
		}
		if run.State != domain.RunCompleted && run.State != domain.RunAborted {
			return domain.NewError(domain.CodeRunState,
				"associated run is still open")
		}
		if err := record.RecordCheck(input.Sequence, input.Result, input.Note, s.clock.Now()); err != nil {
			return err
		}
		snapshot.CleaningRecords[recordID] = record
		updatedRecord = record.Clone()
		updatedVessel = vessel
		return nil
	})
	if err != nil {
		return domain.CleaningRecord{}, domain.Vessel{}, err
	}
	return updatedRecord, updatedVessel, nil
}

// FinishCleaning releases the vessel only when every mandatory check is
// recorded, the cleaning record is still in progress, and the associated run
// is still terminal. Repeated submissions fail because the record completes
// once and the vessel leaves the cleaning state.
func (s *Service) FinishCleaning(
	id string,
	input FinishCleaningInput,
) (VesselDetail, error) {
	var updated VesselDetail
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		vessel, ok := snapshot.Vessels[id]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		if err := s.checkExpected(vessel.Version, input.ExpectedVersion); err != nil {
			return err
		}
		if vessel.State != domain.VesselCleaning {
			return domain.NewError(domain.CodeVesselState,
				"vessel is not waiting for cleaning")
		}
		if vessel.ActiveRunID == nil {
			return domain.NewError(domain.CodeVesselState,
				"cleaning vessel has no associated run")
		}
		run, ok := snapshot.Runs[*vessel.ActiveRunID]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound,
				"associated run was not found")
		}
		if run.State != domain.RunCompleted && run.State != domain.RunAborted {
			return domain.NewError(domain.CodeRunState,
				"associated run is still open")
		}
		record, ok := snapshot.ActiveCleaningForRun(run.ID)
		if !ok || record.VesselID != vessel.ID {
			return domain.NewError(domain.CodeCleaningState,
				"no in-progress cleaning record exists for this vessel")
		}
		now := s.clock.Now()
		if err := record.Complete(now); err != nil {
			return err
		}
		if err := vessel.FinishCleaning(now); err != nil {
			return err
		}
		snapshot.CleaningRecords[record.ID] = record
		snapshot.Vessels[id] = vessel
		updated = VesselDetail{
			Vessel:          vessel,
			CleaningRecords: cleaningRecordsForVessel(*snapshot, id),
		}
		return nil
	})
	if err != nil {
		return VesselDetail{}, err
	}
	return updated, nil
}

// GetCleaningRecord reads a single shared cleaning record.
func (s *Service) GetCleaningRecord(id string) (domain.CleaningRecord, error) {
	var found domain.CleaningRecord
	err := s.store.View(func(snapshot domain.Snapshot) error {
		record, ok := snapshot.CleaningRecords[id]
		if !ok {
			return domain.NewError(domain.CodeCleaningNotFound, "cleaning record was not found")
		}
		found = record.Clone()
		return nil
	})
	if err != nil {
		return domain.CleaningRecord{}, err
	}
	return found, nil
}

func cleaningRecordsForVessel(snapshot domain.Snapshot, vesselID string) []domain.CleaningRecord {
	records := make([]domain.CleaningRecord, 0)
	for _, record := range snapshot.CleaningRecords {
		if record.VesselID == vesselID {
			records = append(records, record.Clone())
		}
	}
	sortCleaningRecords(records)
	return records
}

func cleaningRecordsForRun(snapshot domain.Snapshot, runID string) []domain.CleaningRecord {
	records := make([]domain.CleaningRecord, 0)
	for _, record := range snapshot.CleaningRecords {
		if record.RunID == runID {
			records = append(records, record.Clone())
		}
	}
	sortCleaningRecords(records)
	return records
}

func sortCleaningRecords(records []domain.CleaningRecord) {
	sort.Slice(records, func(i, j int) bool {
		if !records[i].StartedAt.Equal(records[j].StartedAt) {
			return records[i].StartedAt.Before(records[j].StartedAt)
		}
		return records[i].ID < records[j].ID
	})
}
