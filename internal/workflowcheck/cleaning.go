package workflowcheck

import (
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// cleaningRelease exercises the complete extended cleaning flow: a run
// completes into cleaning, a mandatory checklist snapshot is created, the
// vessel cannot be released until every required check carries an execution
// result while the run stays terminal, duplicate and stale submissions fail,
// and both detail surfaces share the same record without losing history.
func cleaningRelease(api *client) error {
	formula, err := ensureApprovedFormula(api, "Cleaning Formula")
	if err != nil {
		return err
	}
	vessel, err := createCheckVessel(api, "VSL-303", 320)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formula.ID, vessel.ID, "RUN-CLEANING")
	if err != nil {
		return err
	}
	completedPair, err := completeCleaningRun(api, started)
	if err != nil {
		return err
	}
	vessel = completedPair.Vessel

	runDetail, err := readRunCleaningDetail(api, started.Run.ID)
	if err != nil {
		return err
	}
	if len(runDetail.CleaningRecords) != 1 {
		return errInvalidWorkflow("completed run did not expose one cleaning record")
	}
	record := runDetail.CleaningRecords[0]
	if record.State != domain.CleaningInProgress {
		return errInvalidWorkflow("new cleaning record is not in progress")
	}
	if record.RunID != started.Run.ID || record.VesselID != vessel.ID {
		return errInvalidWorkflow("cleaning record is not bound to the run and vessel")
	}
	if record.CompletedAt != nil {
		return errInvalidWorkflow("new cleaning record already carries a completion time")
	}
	if len(record.Checks) != 4 {
		return fmt.Errorf("expected 4 mandatory checks, got %d", len(record.Checks))
	}
	for index, check := range record.Checks {
		if check.Sequence != index+1 || !check.Required {
			return errInvalidWorkflow("cleaning checklist is not the mandatory snapshot")
		}
		if check.Result != nil || check.RecordedAt != nil {
			return errInvalidWorkflow("new cleaning check already carries a result")
		}
	}

	earlyFinish, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, ptrInt64(vessel.Version))
	if err != nil {
		return err
	}
	if err := requireCode(earlyFinish, 409, string(domain.CodeCleaningIncomplete),
		"finish cleaning before checks"); err != nil {
		return err
	}

	// Vessel version stays fixed while checks are recorded; the version is the
	// optimistic guard against stale check submissions.
	checkVersion := vessel.Version
	for index, check := range record.Checks {
		result := string(domain.CheckPassed)
		if index == len(record.Checks)-1 {
			result = string(domain.CheckFailed)
		}
		checkResponse, checkErr := api.call("POST",
			"/v1/cleaning-records/"+record.ID+"/checks",
			map[string]any{
				"sequence": check.Sequence,
				"result":   result,
				"note":     fmt.Sprintf("check %d", check.Sequence),
			}, ptrInt64(checkVersion))
		if checkErr != nil {
			return checkErr
		}
		if err := requireStatus(checkResponse, 200, "record cleaning check"); err != nil {
			return err
		}
		var recorded cleaningCheckPairView
		if err := checkResponse.decode(&recorded); err != nil {
			return err
		}
		record = recorded.CleaningRecord
		if recorded.Vessel.State != domain.VesselCleaning {
			return errInvalidWorkflow("recording a check changed the vessel state")
		}
	}

	pending := 0
	recordedResults := 0
	for _, check := range record.Checks {
		if check.Result == nil {
			pending++
		} else {
			recordedResults++
			if check.RecordedAt == nil {
				return errInvalidWorkflow("recorded check is missing its result timestamp")
			}
		}
	}
	if pending != 0 || recordedResults != 4 {
		return errInvalidWorkflow("checklist did not snapshot all execution results")
	}

	duplicateCheck, err := api.call("POST",
		"/v1/cleaning-records/"+record.ID+"/checks",
		map[string]any{"sequence": 1, "result": string(domain.CheckPassed)},
		ptrInt64(checkVersion))
	if err != nil {
		return err
	}
	if err := requireCode(duplicateCheck, 409, string(domain.CodeCleaningCheckConflict),
		"record the same check twice"); err != nil {
		return err
	}

	staleCheck, err := api.call("POST",
		"/v1/cleaning-records/"+record.ID+"/checks",
		map[string]any{"sequence": 2, "result": string(domain.CheckPassed)},
		ptrInt64(checkVersion-1))
	if err != nil {
		return err
	}
	if err := requireCode(staleCheck, 412, string(domain.CodeRevisionConflict),
		"record check with stale version"); err != nil {
		return err
	}

	unknownCheck, err := api.call("POST",
		"/v1/cleaning-records/"+record.ID+"/checks",
		map[string]any{"sequence": 99, "result": string(domain.CheckPassed)},
		ptrInt64(checkVersion))
	if err != nil {
		return err
	}
	if err := requireCode(unknownCheck, 409, string(domain.CodeCleaningCheckNotFound),
		"record unknown check"); err != nil {
		return err
	}

	finishResponse, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, ptrInt64(checkVersion))
	if err != nil {
		return err
	}
	if err := requireStatus(finishResponse, 200, "finish cleaning"); err != nil {
		return err
	}
	var finished vesselDetailView
	if err := finishResponse.decode(&finished); err != nil {
		return err
	}
	if err := requireVesselState(finished.vesselView, domain.VesselAvailable); err != nil {
		return err
	}
	if finished.ActiveRunID != nil {
		return errInvalidWorkflow("released vessel still references the run")
	}
	if len(finished.CleaningRecords) != 1 {
		return errInvalidWorkflow("released vessel did not retain its cleaning record")
	}
	completedRecord := finished.CleaningRecords[0]
	if completedRecord.State != domain.CleaningCompleted ||
		completedRecord.CompletedAt == nil {
		return errInvalidWorkflow("completed cleaning record lacks its completion snapshot")
	}
	if !completedRecord.CompletedAt.After(completedRecord.StartedAt) {
		return errInvalidWorkflow("cleaning completion time does not follow its start")
	}

	repeatFinish, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, ptrInt64(finished.Version))
	if err != nil {
		return err
	}
	if err := requireCode(repeatFinish, 409, string(domain.CodeVesselState),
		"finish cleaning twice"); err != nil {
		return err
	}

	staleFinish, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, ptrInt64(checkVersion))
	if err != nil {
		return err
	}
	if err := requireCode(staleFinish, 412, string(domain.CodeRevisionConflict),
		"finish cleaning with stale version"); err != nil {
		return err
	}

	recordAfterFinish, err := readCleaningRecord(api, record.ID)
	if err != nil {
		return err
	}
	if recordAfterFinish.State != domain.CleaningCompleted {
		return errInvalidWorkflow("finished cleaning record is not readable as completed")
	}
	completedCheck, err := api.call("POST",
		"/v1/cleaning-records/"+record.ID+"/checks",
		map[string]any{"sequence": 1, "result": string(domain.CheckPassed)},
		ptrInt64(finished.Version))
	if err != nil {
		return err
	}
	if err := requireCode(completedCheck, 409, string(domain.CodeCleaningState),
		"record a check after completion"); err != nil {
		return err
	}

	// Vessel detail and run detail must show the same shared record.
	vesselDetail, err := readVesselCleaningDetail(api, vessel.ID)
	if err != nil {
		return err
	}
	if !sameCleaningRecord(vesselDetail.CleaningRecords[0], recordAfterFinish) {
		return errInvalidWorkflow("vessel detail does not share the run's cleaning record")
	}
	runReadBack, err := readRunCleaningDetail(api, started.Run.ID)
	if err != nil {
		return err
	}
	if !sameCleaningRecord(runReadBack.CleaningRecords[0], recordAfterFinish) {
		return errInvalidWorkflow("run detail does not share the vessel's cleaning record")
	}

	if err := secondCleaningKeepsHistory(api, formula.ID, vessel, started.Run.ID); err != nil {
		return err
	}
	if err := abortedRunRequiresChecks(api, formula.ID); err != nil {
		return err
	}
	return nil
}

// abortedRunRequiresChecks verifies that aborting a started run also opens a
// checklist-gated cleaning and that the vessel cannot be released early.
func abortedRunRequiresChecks(api *client, formulaID string) error {
	vessel, err := createCheckVessel(api, "VSL-304", 280)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formulaID, vessel.ID, "RUN-CLEANING-ABORT")
	if err != nil {
		return err
	}
	abortResponse, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/abort",
		nil, ptrInt64(started.Run.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(abortResponse, 200, "abort started run"); err != nil {
		return err
	}
	var aborted pairView
	if err := abortResponse.decode(&aborted); err != nil {
		return err
	}
	if err := requireRunState(aborted.Run, domain.RunAborted); err != nil {
		return err
	}
	if err := requireVesselState(aborted.Vessel, domain.VesselCleaning); err != nil {
		return err
	}
	detail, err := readRunCleaningDetail(api, started.Run.ID)
	if err != nil {
		return err
	}
	if len(detail.CleaningRecords) != 1 ||
		detail.CleaningRecords[0].State != domain.CleaningInProgress {
		return errInvalidWorkflow("aborted run did not open an in-progress cleaning record")
	}
	earlyFinish, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, ptrInt64(aborted.Vessel.Version))
	if err != nil {
		return err
	}
	if err := requireCode(earlyFinish, 409, string(domain.CodeCleaningIncomplete),
		"release vessel of aborted run without checks"); err != nil {
		return err
	}
	record := detail.CleaningRecords[0]
	for _, check := range record.Checks {
		response, checkErr := api.call("POST",
			"/v1/cleaning-records/"+record.ID+"/checks",
			map[string]any{"sequence": check.Sequence, "result": string(domain.CheckPassed)},
			ptrInt64(aborted.Vessel.Version))
		if checkErr != nil {
			return checkErr
		}
		if err := requireStatus(response, 200, "record aborted-run cleaning check"); err != nil {
			return err
		}
	}
	finishResponse, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, ptrInt64(aborted.Vessel.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(finishResponse, 200, "release vessel after aborted-run cleaning"); err != nil {
		return err
	}
	var released vesselDetailView
	if err := finishResponse.decode(&released); err != nil {
		return err
	}
	return requireVesselState(released.vesselView, domain.VesselAvailable)
}

// secondCleaningKeepsHistory runs a second cycle on the released vessel and
// verifies the completed history is retained rather than overwritten.
func secondCleaningKeepsHistory(
	api *client,
	formulaID string,
	vessel vesselView,
	firstRunID string,
) error {
	second, err := createStartedRun(api, formulaID, vessel.ID, "RUN-CLEANING-TWO")
	if err != nil {
		return err
	}
	if _, err := completeCleaningRun(api, second); err != nil {
		return err
	}
	detail, err := readRunCleaningDetail(api, second.Run.ID)
	if err != nil {
		return err
	}
	if len(detail.CleaningRecords) != 1 {
		return errInvalidWorkflow("second run did not get exactly one new cleaning record")
	}
	secondRecord := detail.CleaningRecords[0]
	if secondRecord.RunID != second.Run.ID || secondRecord.State != domain.CleaningInProgress {
		return errInvalidWorkflow("second cleaning record is not an independent snapshot")
	}

	vesselDetail, err := readVesselCleaningDetail(api, vessel.ID)
	if err != nil {
		return err
	}
	if len(vesselDetail.CleaningRecords) != 2 {
		return errInvalidWorkflow("vessel history overwrote the first cleaning record")
	}
	first := vesselDetail.CleaningRecords[0]
	latest := vesselDetail.CleaningRecords[1]
	if first.RunID != firstRunID || first.State != domain.CleaningCompleted {
		return errInvalidWorkflow("first cleaning history lost its completed snapshot")
	}
	if latest.ID != secondRecord.ID || latest.RunID != second.Run.ID {
		return errInvalidWorkflow("vessel history did not append the second cleaning record")
	}
	return nil
}

func completeCleaningRun(api *client, started pairView) (pairView, error) {
	version := started.Run.Version
	base := time.Now().UTC().Add(10 * time.Second)
	for sequence := 1; sequence <= 3; sequence++ {
		response, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/observations",
			map[string]any{
				"sequence":      sequence,
				"gravity":       1.050 - float64(sequence)*0.012,
				"temperature_c": 20.0,
				"observed_at":   verifiedAt(base, sequence),
			}, ptrInt64(version))
		if err != nil {
			return pairView{}, err
		}
		if err := requireStatus(response, 200, "append cleaning observation"); err != nil {
			return pairView{}, err
		}
		var run runView
		if err := response.decode(&run); err != nil {
			return pairView{}, err
		}
		version = run.Version
	}
	conditioningResponse, err := api.call("POST",
		"/v1/runs/"+started.Run.ID+"/conditioning", nil, ptrInt64(version))
	if err != nil {
		return pairView{}, err
	}
	if err := requireStatus(conditioningResponse, 200, "begin conditioning"); err != nil {
		return pairView{}, err
	}
	var conditioning runView
	if err := conditioningResponse.decode(&conditioning); err != nil {
		return pairView{}, err
	}
	completeResponse, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/complete",
		nil, ptrInt64(conditioning.Version))
	if err != nil {
		return pairView{}, err
	}
	if err := requireStatus(completeResponse, 200, "complete run"); err != nil {
		return pairView{}, err
	}
	var pair pairView
	if err := completeResponse.decode(&pair); err != nil {
		return pairView{}, err
	}
	if err := requireVesselState(pair.Vessel, domain.VesselCleaning); err != nil {
		return pairView{}, err
	}
	return pair, nil
}

func readVesselCleaningDetail(api *client, id string) (vesselDetailView, error) {
	response, err := api.call("GET", "/v1/vessels/"+id, nil, nil)
	if err != nil {
		return vesselDetailView{}, err
	}
	if err := requireStatus(response, 200, "read vessel detail"); err != nil {
		return vesselDetailView{}, err
	}
	var detail vesselDetailView
	if err := response.decode(&detail); err != nil {
		return vesselDetailView{}, err
	}
	return detail, nil
}

func readRunCleaningDetail(api *client, id string) (runDetailView, error) {
	response, err := api.call("GET", "/v1/runs/"+id, nil, nil)
	if err != nil {
		return runDetailView{}, err
	}
	if err := requireStatus(response, 200, "read run detail"); err != nil {
		return runDetailView{}, err
	}
	var detail runDetailView
	if err := response.decode(&detail); err != nil {
		return runDetailView{}, err
	}
	return detail, nil
}

func readCleaningRecord(api *client, id string) (cleaningRecordView, error) {
	response, err := api.call("GET", "/v1/cleaning-records/"+id, nil, nil)
	if err != nil {
		return cleaningRecordView{}, err
	}
	if err := requireStatus(response, 200, "read cleaning record"); err != nil {
		return cleaningRecordView{}, err
	}
	var record cleaningRecordView
	if err := response.decode(&record); err != nil {
		return cleaningRecordView{}, err
	}
	return record, nil
}

func sameCleaningRecord(left, right cleaningRecordView) bool {
	return left.ID == right.ID && left.State == right.State &&
		left.VesselID == right.VesselID && left.RunID == right.RunID &&
		len(left.Checks) == len(right.Checks)
}
