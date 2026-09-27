package service

import (
	"sort"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

func (s *Service) CreateVessel(input CreateVesselInput) (domain.Vessel, error) {
	now := s.clock.Now()
	vessel := domain.Vessel{
		Code:      domain.NormalizeVesselCode(input.Code),
		CapacityL: input.CapacityL,
		State:     domain.VesselAvailable,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := vessel.Validate(); err != nil {
		return domain.Vessel{}, err
	}
	id, err := generateEntityID("vessel")
	if err != nil {
		return domain.Vessel{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	vessel.ID = id

	err = s.store.Execute(func(snapshot *domain.Snapshot) error {
		if _, exists := snapshot.VesselIndex[vessel.Code]; exists {
			return domain.NewError(domain.CodeVesselConflict,
				"a vessel with the same code already exists")
		}
		snapshot.Vessels[vessel.ID] = vessel
		snapshot.VesselIndex[vessel.Code] = vessel.ID
		return nil
	})
	if err != nil {
		return domain.Vessel{}, err
	}
	return vessel, nil
}

func (s *Service) GetVessel(id string) (VesselDetail, error) {
	var found VesselDetail
	err := s.store.View(func(snapshot domain.Snapshot) error {
		vessel, ok := snapshot.Vessels[id]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		found = s.buildVesselDetail(snapshot, vessel)
		return nil
	})
	if err != nil {
		return VesselDetail{}, err
	}
	return found, nil
}

func (s *Service) ListVessels() ([]domain.Vessel, error) {
	var vessels []domain.Vessel
	err := s.store.View(func(snapshot domain.Snapshot) error {
		vessels = make([]domain.Vessel, 0, len(snapshot.Vessels))
		for _, vessel := range snapshot.Vessels {
			vessels = append(vessels, vessel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(vessels, func(i, j int) bool {
		if vessels[i].Code == vessels[j].Code {
			return vessels[i].ID < vessels[j].ID
		}
		return vessels[i].Code < vessels[j].Code
	})
	return vessels, nil
}

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
		if vessel.State != domain.VesselCleaning {
			return domain.NewError(domain.CodeVesselState,
				"vessel is not waiting for cleaning")
		}
		if vessel.ActiveRunID == nil || vessel.CurrentCleaningID == nil {
			return domain.NewError(domain.CodeVesselState,
				"cleaning vessel has no associated run or cleaning record")
		}
		cleaning, ok := snapshot.Cleanings[*vessel.CurrentCleaningID]
		if !ok {
			return domain.NewError(domain.CodeCleaningNotFound,
				"current cleaning record was not found")
		}
		// Optimistic concurrency is guarded by the cleaning record version:
		// concurrent checklist writes must not be released on a stale read.
		if err := s.checkExpected(cleaning.Version, input.ExpectedVersion); err != nil {
			return err
		}
		if cleaning.VesselID != vessel.ID {
			return domain.NewError(domain.CodeCleaningState,
				"cleaning record belongs to a different vessel")
		}
		run, ok := snapshot.Runs[cleaning.RunID]
		if !ok {
			return domain.NewError(domain.CodeRunNotFound, "associated run was not found")
		}
		if cleaning.RunID != *vessel.ActiveRunID {
			return domain.NewError(domain.CodeRunState,
				"cleaning record belongs to a different run")
		}
		now := s.clock.Now()
		// Freeze the completion snapshot only while the run is still terminal.
		// If the run was reopened, this fails and the vessel stays in cleaning.
		if err := cleaning.Complete(run.State, now); err != nil {
			return err
		}
		if err := vessel.FinishCleaning(now); err != nil {
			return err
		}
		snapshot.Cleanings[cleaning.ID] = cleaning.Clone()
		snapshot.Vessels[id] = vessel
		updated = s.buildVesselDetail(*snapshot, vessel)
		return nil
	})
	if err != nil {
		return VesselDetail{}, err
	}
	return updated, nil
}

func (s *Service) RetireVessel(id string, input VersionInput) (domain.Vessel, error) {
	var updated domain.Vessel
	err := s.store.Execute(func(snapshot *domain.Snapshot) error {
		vessel, ok := snapshot.Vessels[id]
		if !ok {
			return domain.NewError(domain.CodeVesselNotFound, "vessel was not found")
		}
		if err := s.checkExpected(vessel.Version, input.ExpectedVersion); err != nil {
			return err
		}
		if err := vessel.Retire(s.clock.Now()); err != nil {
			return err
		}
		snapshot.Vessels[id] = vessel
		updated = vessel
		return nil
	})
	if err != nil {
		return domain.Vessel{}, err
	}
	return updated, nil
}
