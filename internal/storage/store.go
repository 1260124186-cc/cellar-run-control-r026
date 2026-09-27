package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

const stateFileName = "state.json"

type Store struct {
	mu       sync.RWMutex
	path     string
	snapshot domain.Snapshot
	now      func() time.Time
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	store := &Store{
		path:     filepath.Join(dataDir, stateFileName),
		snapshot: domain.EmptySnapshot(),
		now:      func() time.Time { return time.Now().UTC() },
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	var loaded domain.Snapshot
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return fmt.Errorf("decode state: %w", err)
	}
	normalized, err := normalizeSnapshot(loaded)
	if err != nil {
		return err
	}
	s.snapshot = normalized
	return nil
}

func normalizeSnapshot(value domain.Snapshot) (domain.Snapshot, error) {
	if value.Revision < 0 {
		return domain.Snapshot{}, fmt.Errorf("state revision cannot be negative")
	}
	if value.Formulas == nil {
		value.Formulas = map[string]domain.Formula{}
	}
	if value.Vessels == nil {
		value.Vessels = map[string]domain.Vessel{}
	}
	if value.Runs == nil {
		value.Runs = map[string]domain.FermentationRun{}
	}
	if value.Cleanings == nil {
		value.Cleanings = map[string]domain.CleaningRecord{}
	}
	if value.NameIndex == nil {
		value.NameIndex = map[string]string{}
	}
	if value.VesselIndex == nil {
		value.VesselIndex = map[string]string{}
	}
	if value.RunIndex == nil {
		value.RunIndex = map[string]string{}
	}
	if err := validateIndexes(value); err != nil {
		return domain.Snapshot{}, err
	}
	return value, nil
}

func validateIndexes(value domain.Snapshot) error {
	expectedNames := make(map[string]string, len(value.Formulas))
	for id, formula := range value.Formulas {
		key := domain.NameKey(formula.Name)
		if key == "" {
			return fmt.Errorf("formula %s has an empty normalized name", id)
		}
		if existing, found := expectedNames[key]; found && existing != id {
			return fmt.Errorf("formula name index contains a duplicate key")
		}
		expectedNames[key] = id
	}
	if err := compareIndexes("formula name", value.NameIndex, expectedNames); err != nil {
		return err
	}

	expectedVessels := make(map[string]string, len(value.Vessels))
	for id, vessel := range value.Vessels {
		if existing, found := expectedVessels[vessel.Code]; found && existing != id {
			return fmt.Errorf("vessel index contains a duplicate code")
		}
		expectedVessels[vessel.Code] = id
	}
	if err := compareIndexes("vessel code", value.VesselIndex, expectedVessels); err != nil {
		return err
	}

	expectedRuns := make(map[string]string, len(value.Runs))
	for id, run := range value.Runs {
		if existing, found := expectedRuns[run.Code]; found && existing != id {
			return fmt.Errorf("run index contains a duplicate code")
		}
		expectedRuns[run.Code] = id
	}
	if err := compareIndexes("run code", value.RunIndex, expectedRuns); err != nil {
		return err
	}
	return validateCleanings(value)
}

// validateCleanings keeps cleaning records consistent with the vessel and run
// they belong to and enforces the append-only checklist invariants.
func validateCleanings(value domain.Snapshot) error {
	vesselNumbers := make(map[string]map[int]string)
	for id, cleaning := range value.Cleanings {
		vessel, vesselFound := value.Vessels[cleaning.VesselID]
		if !vesselFound {
			return fmt.Errorf("cleaning %s references a missing vessel", id)
		}
		run, runFound := value.Runs[cleaning.RunID]
		if !runFound {
			return fmt.Errorf("cleaning %s references a missing run", id)
		}
		if vessel.ID != cleaning.VesselID {
			return fmt.Errorf("cleaning %s vessel reference is inconsistent", id)
		}
		if run.VesselID == nil || *run.VesselID != cleaning.VesselID {
			return fmt.Errorf("cleaning %s run is not bound to its vessel", id)
		}
		if len(cleaning.Checks) == 0 {
			return fmt.Errorf("cleaning %s has no mandatory checklist", id)
		}
		keys := make(map[string]struct{}, len(cleaning.Checks))
		for _, check := range cleaning.Checks {
			key := domain.NameKey(check.Key)
			if key == "" {
				return fmt.Errorf("cleaning %s has an empty check key", id)
			}
			if _, repeated := keys[key]; repeated {
				return fmt.Errorf("cleaning %s contains a duplicate check key", id)
			}
			keys[key] = struct{}{}
		}
		if cleaning.Number < 1 {
			return fmt.Errorf("cleaning %s has an invalid sequence number", id)
		}
		numbers, seen := vesselNumbers[cleaning.VesselID]
		if !seen {
			numbers = map[int]string{}
			vesselNumbers[cleaning.VesselID] = numbers
		}
		if existing, duplicated := numbers[cleaning.Number]; duplicated {
			return fmt.Errorf("cleaning number %d for vessel %s is used by %s and %s",
				cleaning.Number, cleaning.VesselID, existing, id)
		}
		numbers[cleaning.Number] = id

		switch cleaning.State {
		case domain.CleaningOpen:
			if cleaning.Completion != nil {
				return fmt.Errorf("open cleaning %s must not carry a completion snapshot", id)
			}
			if vessel.State != domain.VesselCleaning {
				return fmt.Errorf("open cleaning %s vessel is not in the cleaning state", id)
			}
			if vessel.CurrentCleaningID == nil || *vessel.CurrentCleaningID != id {
				return fmt.Errorf("open cleaning %s is not the current vessel cleaning", id)
			}
			if vessel.ActiveRunID == nil || *vessel.ActiveRunID != cleaning.RunID {
				return fmt.Errorf("open cleaning %s is not bound to the active run", id)
			}
		case domain.CleaningComplete:
			if cleaning.Completion == nil {
				return fmt.Errorf("completed cleaning %s is missing its completion snapshot", id)
			}
			if vessel.CurrentCleaningID != nil && *vessel.CurrentCleaningID == id {
				return fmt.Errorf("completed cleaning %s is still marked as current", id)
			}
		default:
			return fmt.Errorf("cleaning %s has an unknown state", id)
		}
	}

	// Vessel and run pointers must reference records that exist and agree.
	for id, vessel := range value.Vessels {
		if vessel.CurrentCleaningID != nil {
			cleaning, found := value.Cleanings[*vessel.CurrentCleaningID]
			if !found {
				return fmt.Errorf("vessel %s references a missing cleaning", id)
			}
			if cleaning.VesselID != id || cleaning.State != domain.CleaningOpen {
				return fmt.Errorf("vessel %s current cleaning is inconsistent", id)
			}
		}
	}
	for id, run := range value.Runs {
		if run.CleaningID != nil {
			cleaning, found := value.Cleanings[*run.CleaningID]
			if !found {
				return fmt.Errorf("run %s references a missing cleaning", id)
			}
			if cleaning.RunID != id {
				return fmt.Errorf("run %s cleaning reference is inconsistent", id)
			}
		}
	}
	return nil
}

func compareIndexes(label string, actual, expected map[string]string) error {
	if len(actual) != len(expected) {
		return fmt.Errorf("%s index does not match its collection", label)
	}
	for key, expectedID := range expected {
		actualID, found := actual[key]
		if !found || actualID != expectedID {
			return fmt.Errorf("%s index does not match its collection", label)
		}
	}
	return nil
}

func (s *Store) Revision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot.Revision
}

func (s *Store) View(visitor func(domain.Snapshot) error) error {
	s.mu.RLock()
	copied := s.snapshot.Clone()
	s.mu.RUnlock()
	return visitor(copied)
}

func (s *Store) Update(expected *int64, mutate func(*domain.Snapshot) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if expected != nil && *expected != s.snapshot.Revision {
		return domain.NewError(domain.CodeRevisionConflict,
			"the supplied revision does not match the current snapshot")
	}
	candidate := s.snapshot.Clone()
	if err := mutate(&candidate); err != nil {
		return err
	}
	candidate.Revision = s.snapshot.Revision + 1
	candidate.UpdatedAt = s.now()
	if err := writeSnapshot(s.path, candidate); err != nil {
		return domain.NewError(domain.CodeStorage, err.Error())
	}
	s.snapshot = candidate
	return nil
}

func (s *Store) Execute(mutator func(*domain.Snapshot) error) error {
	return s.Update(nil, mutator)
}
