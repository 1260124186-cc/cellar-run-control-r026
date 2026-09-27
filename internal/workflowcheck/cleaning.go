package workflowcheck

import (
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// cleaningRelease exercises the mandatory checklist, the completion snapshot,
// version guarding, and the append-only history shared by vessel and run views.
func cleaningRelease(api *client) error {
	formula, err := ensureApprovedFormula(api, "Cleaning Formula")
	if err != nil {
		return err
	}
	vessel, err := createCheckVessel(api, "VSL-303", 400)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formula.ID, vessel.ID, "RUN-CLEANING")
	if err != nil {
		return err
	}
	if err := appendStandardObservations(api, started.Run.ID, started.Run.Version); err != nil {
		return err
	}
	conditioningResponse, err := api.call("POST",
		"/v1/runs/"+started.Run.ID+"/conditioning", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(conditioningResponse, 200, "begin conditioning"); err != nil {
		return err
	}
	var conditioning runView
	if err := conditioningResponse.decode(&conditioning); err != nil {
		return err
	}

	completeResponse, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/complete",
		nil, ptrInt64(conditioning.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(completeResponse, 200, "complete run"); err != nil {
		return err
	}
	var completed pairView
	if err := completeResponse.decode(&completed); err != nil {
		return err
	}
	if err := requireVesselState(completed.Vessel, domain.VesselCleaning); err != nil {
		return err
	}
	if completed.Vessel.CurrentCleaningID == nil {
		return errInvalidWorkflow("cleaning vessel has no cleaning record link")
	}
	if completed.Run.CleaningID == nil ||
		*completed.Run.CleaningID != *completed.Vessel.CurrentCleaningID {
		return errInvalidWorkflow("run and vessel do not share the same cleaning record")
	}
	cleaningID := *completed.Vessel.CurrentCleaningID

	// Vessel and run details must expose the identical cleaning record.
	vesselDetail, err := getVesselDetail(api, vessel.ID)
	if err != nil {
		return err
	}
	runDetail, err := getRunDetail(api, started.Run.ID)
	if err != nil {
		return err
	}
	if vesselDetail.CurrentCleaning == nil ||
		vesselDetail.CurrentCleaning.ID != cleaningID {
		return errInvalidWorkflow("vessel detail is missing the current cleaning")
	}
	if runDetail.Cleaning == nil || runDetail.Cleaning.ID != cleaningID {
		return errInvalidWorkflow("run detail is missing the shared cleaning")
	}
	if len(runDetail.Cleaning.Checks) != 3 {
		return errInvalidWorkflow("cleaning must start with three mandatory checks")
	}
	for _, check := range runDetail.Cleaning.Checks {
		if check.State != domain.CheckPending {
			return errInvalidWorkflow("new cleaning checks must be pending")
		}
	}
	cleaningVersion := runDetail.Cleaning.Version

	// Release must fail before the mandatory checklist is complete.
	premature, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, ptrInt64(cleaningVersion))
	if err != nil {
		return err
	}
	if err := requireCode(premature, 409, string(domain.CodeCleaningCheckIncomplete),
		"finish cleaning without checks"); err != nil {
		return err
	}

	// A single batch can record several checks.
	recordResponse, err := api.call("POST",
		"/v1/vessels/"+vessel.ID+"/cleaning-checks",
		map[string]any{"results": []map[string]any{
			{"check_key": domain.CheckKeyVisualInspection, "passed": true},
			{"check_key": domain.CheckKeySanitizerRinse, "passed": true},
		}}, ptrInt64(cleaningVersion))
	if err != nil {
		return err
	}
	if err := requireStatus(recordResponse, 200, "record cleaning checks"); err != nil {
		return err
	}
	var recorded cleaningView
	if err := recordResponse.decode(&recorded); err != nil {
		return err
	}
	if recorded.Version != cleaningVersion+1 {
		return errInvalidWorkflow("checklist batch did not advance the version")
	}

	// Stale versions and duplicate submissions must fail.
	stale, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/cleaning-checks",
		map[string]any{"results": []map[string]any{
			{"check_key": domain.CheckKeyVisualInspection, "passed": true},
		}}, ptrInt64(cleaningVersion))
	if err != nil {
		return err
	}
	if err := requireCode(stale, 412, string(domain.CodeRevisionConflict),
		"record checks on a stale cleaning version"); err != nil {
		return err
	}
	duplicate, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/cleaning-checks",
		map[string]any{"results": []map[string]any{
			{"check_key": domain.CheckKeyVisualInspection, "passed": true},
		}}, ptrInt64(recorded.Version))
	if err != nil {
		return err
	}
	if err := requireCode(duplicate, 409, string(domain.CodeCleaningCheckConflict),
		"record a check twice"); err != nil {
		return err
	}
	unknown, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/cleaning-checks",
		map[string]any{"results": []map[string]any{
			{"check_key": "mystery-step", "passed": true},
		}}, ptrInt64(recorded.Version))
	if err != nil {
		return err
	}
	if err := requireCode(unknown, 400, string(domain.CodeInvalidInput),
		"record an unknown check"); err != nil {
		return err
	}

	// Finish must still fail while one mandatory item is outstanding.
	stillOpen, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, ptrInt64(recorded.Version))
	if err != nil {
		return err
	}
	if err := requireCode(stillOpen, 409, string(domain.CodeCleaningCheckIncomplete),
		"finish with an outstanding check"); err != nil {
		return err
	}

	// A failed execution still counts as a recorded result but is snapshotted.
	finalResponse, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/cleaning-checks",
		map[string]any{"results": []map[string]any{
			{"check_key": domain.CheckKeyValveGasket, "passed": false,
				"note": "gasket replaced during clean"},
		}}, ptrInt64(recorded.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(finalResponse, 200, "record final check"); err != nil {
		return err
	}
	var finalRecord cleaningView
	if err := finalResponse.decode(&finalRecord); err != nil {
		return err
	}
	if finalRecord.Completion != nil {
		return errInvalidWorkflow("recording checks must not complete the cleaning")
	}
	valveCheck, found := findCheck(finalRecord.Checks, domain.CheckKeyValveGasket)
	if !found || valveCheck.State != domain.CheckFailed ||
		valveCheck.Note != "gasket replaced during clean" {
		return errInvalidWorkflow("failed check result or note was not preserved")
	}

	// Release against the old checklist version must be rejected.
	staleFinish, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, ptrInt64(recorded.Version))
	if err != nil {
		return err
	}
	if err := requireCode(staleFinish, 412, string(domain.CodeRevisionConflict),
		"finish on a stale cleaning version"); err != nil {
		return err
	}

	finishResponse, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, ptrInt64(finalRecord.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(finishResponse, 200, "finish cleaning"); err != nil {
		return err
	}
	var released vesselDetailView
	if err := finishResponse.decode(&released); err != nil {
		return err
	}
	if err := requireVesselState(released.vesselView, domain.VesselAvailable); err != nil {
		return err
	}
	if released.ActiveRunID != nil || released.CurrentCleaningID != nil {
		return errInvalidWorkflow("released vessel must clear run and cleaning links")
	}
	if len(released.CleaningHistory) != 1 {
		return errInvalidWorkflow("finished cleaning must be retained as history")
	}
	history := released.CleaningHistory[0]
	if history.ID != cleaningID || history.State != domain.CleaningComplete ||
		history.Completion == nil {
		return errInvalidWorkflow("history does not preserve the completed record")
	}
	if history.Completion.ChecksTotal != 3 ||
		history.Completion.ChecksRecorded != 3 ||
		history.Completion.ChecksPassed != 2 ||
		history.Completion.ChecksFailed != 1 ||
		history.Completion.RunState != domain.RunCompleted ||
		history.Completion.CompletedAt.IsZero() {
		return errInvalidWorkflow("completion snapshot has unexpected content")
	}

	// Repeating release and checklist writes on a finished cleaning must fail.
	repeatFinish, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(repeatFinish, 409, string(domain.CodeVesselState),
		"finish cleaning twice"); err != nil {
		return err
	}
	lateCheck, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/cleaning-checks",
		map[string]any{"results": []map[string]any{
			{"check_key": domain.CheckKeyValveGasket, "passed": true},
		}}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(lateCheck, 409, string(domain.CodeVesselState),
		"record checks after release"); err != nil {
		return err
	}

	// The run detail must still point at the same immutable completed record.
	runAfter, err := getRunDetail(api, started.Run.ID)
	if err != nil {
		return err
	}
	if runAfter.Cleaning == nil || runAfter.Cleaning.ID != cleaningID ||
		runAfter.Cleaning.Completion == nil {
		return errInvalidWorkflow("run detail lost the completed cleaning snapshot")
	}

	// A second cleaning cycle must create a new record without overwriting
	// the first completed history entry.
	secondRun, err := createSecondCompletedRun(api, formula.ID, vessel.ID)
	if err != nil {
		return err
	}
	if err := finishCurrentCleaning(api, vessel.ID); err != nil {
		return err
	}
	vesselAfterSecond, err := getVesselDetail(api, vessel.ID)
	if err != nil {
		return err
	}
	if len(vesselAfterSecond.CleaningHistory) != 2 {
		return errInvalidWorkflow("completed cleaning history was overwritten")
	}
	ids := map[string]bool{
		vesselAfterSecond.CleaningHistory[0].ID: true,
		vesselAfterSecond.CleaningHistory[1].ID: true,
	}
	if !ids[cleaningID] || !ids[secondRun.CleaningIDValue] {
		return errInvalidWorkflow("second cleaning cycle lost a historical record")
	}
	if vesselAfterSecond.CleaningHistory[0].Number != 1 ||
		vesselAfterSecond.CleaningHistory[1].Number != 2 {
		return errInvalidWorkflow("cleaning history is not ordered by sequence number")
	}
	return nil
}

type secondRunResult struct {
	Run             runView
	CleaningIDValue string
}

func createSecondCompletedRun(api *client, formulaID, vesselID string) (secondRunResult, error) {
	runResponse, err := api.call("POST", "/v1/runs",
		createRunPayload("RUN-CLEANING-B", formulaID), nil)
	if err != nil {
		return secondRunResult{}, err
	}
	if err := requireStatus(runResponse, 201, "create second cleaning run"); err != nil {
		return secondRunResult{}, err
	}
	var run runView
	if err := runResponse.decode(&run); err != nil {
		return secondRunResult{}, err
	}
	reserveResponse, err := api.call("POST", "/v1/runs/"+run.ID+"/reserve",
		map[string]any{"vessel_id": vesselID}, ptrInt64(run.Version))
	if err != nil {
		return secondRunResult{}, err
	}
	if err := requireStatus(reserveResponse, 200, "reserve vessel again"); err != nil {
		return secondRunResult{}, err
	}
	var reserved pairView
	if err := reserveResponse.decode(&reserved); err != nil {
		return secondRunResult{}, err
	}
	startResponse, err := api.call("POST", "/v1/runs/"+run.ID+"/start",
		nil, ptrInt64(reserved.Run.Version))
	if err != nil {
		return secondRunResult{}, err
	}
	if err := requireStatus(startResponse, 200, "start second run"); err != nil {
		return secondRunResult{}, err
	}
	var started pairView
	if err := startResponse.decode(&started); err != nil {
		return secondRunResult{}, err
	}
	if err := appendStandardObservations(api, run.ID, started.Run.Version); err != nil {
		return secondRunResult{}, err
	}
	conditioningResponse, err := api.call("POST", "/v1/runs/"+run.ID+"/conditioning",
		nil, nil)
	if err != nil {
		return secondRunResult{}, err
	}
	if err := requireStatus(conditioningResponse, 200, "condition second run"); err != nil {
		return secondRunResult{}, err
	}
	if err := conditioningResponse.decode(&run); err != nil {
		return secondRunResult{}, err
	}
	completeResponse, err := api.call("POST", "/v1/runs/"+run.ID+"/complete",
		nil, ptrInt64(run.Version))
	if err != nil {
		return secondRunResult{}, err
	}
	if err := requireStatus(completeResponse, 200, "complete second run"); err != nil {
		return secondRunResult{}, err
	}
	var pair pairView
	if err := completeResponse.decode(&pair); err != nil {
		return secondRunResult{}, err
	}
	if pair.Vessel.CurrentCleaningID == nil {
		return secondRunResult{}, errInvalidWorkflow("second cleaning has no record")
	}
	return secondRunResult{Run: pair.Run, CleaningIDValue: *pair.Vessel.CurrentCleaningID}, nil
}

// abortCleaningTerminal proves release still requires the checklist when the
// associated run reached the aborted terminal state rather than completed.
func abortCleaningTerminal(api *client) error {
	formula, err := ensureApprovedFormula(api, "Abort Cleaning Formula")
	if err != nil {
		return err
	}
	vessel, err := createCheckVessel(api, "VSL-304", 250)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formula.ID, vessel.ID, "RUN-ABORT-CLEAN")
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
	if err := requireVesselState(aborted.Vessel, domain.VesselCleaning); err != nil {
		return err
	}
	if aborted.Vessel.CurrentCleaningID == nil {
		return errInvalidWorkflow("aborted fermenting run must start a cleaning")
	}

	// Terminal run alone is not enough: the mandatory checks are still required.
	blocked, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning",
		nil, nil)
	if err != nil {
		return err
	}
	if err := requireCode(blocked, 409, string(domain.CodeCleaningCheckIncomplete),
		"finish aborted-run cleaning without checks"); err != nil {
		return err
	}
	return finishCurrentCleaning(api, vessel.ID)
}

func appendStandardObservations(api *client, runID string, version int64) error {
	base := time.Now().UTC().Add(10 * time.Second)
	points := []struct {
		gravity     float64
		temperature float64
	}{
		{gravity: 1.050, temperature: 20.0},
		{gravity: 1.028, temperature: 20.5},
		{gravity: 1.014, temperature: 19.8},
	}
	for index, point := range points {
		sequence := index + 1
		response, err := api.call("POST", "/v1/runs/"+runID+"/observations",
			map[string]any{
				"sequence":      sequence,
				"gravity":       point.gravity,
				"temperature_c": point.temperature,
				"observed_at":   verifiedAt(base, sequence),
				"note":          fmt.Sprintf("observation %d", sequence),
			}, ptrInt64(version))
		if err != nil {
			return err
		}
		if err := requireStatus(response, 200, "append observation"); err != nil {
			return err
		}
		var run runView
		if err := response.decode(&run); err != nil {
			return err
		}
		version = run.Version
	}
	return nil
}

func findCheck(checks []checkView, key string) (checkView, bool) {
	for _, check := range checks {
		if check.Key == key {
			return check, true
		}
	}
	return checkView{}, false
}

func getVesselDetail(api *client, vesselID string) (vesselDetailView, error) {
	response, err := api.call("GET", "/v1/vessels/"+vesselID, nil, nil)
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

func getRunDetail(api *client, runID string) (runDetailView, error) {
	response, err := api.call("GET", "/v1/runs/"+runID, nil, nil)
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

// finishCurrentCleaning records every mandatory check as passing and releases
// the vessel, returning an error when any step does not succeed.
func finishCurrentCleaning(api *client, vesselID string) error {
	detail, err := getVesselDetail(api, vesselID)
	if err != nil {
		return err
	}
	if detail.CurrentCleaning == nil {
		return errInvalidWorkflow("vessel has no current cleaning to finish")
	}
	results := make([]map[string]any, 0, len(detail.CurrentCleaning.Checks))
	for _, check := range detail.CurrentCleaning.Checks {
		results = append(results, map[string]any{
			"check_key": check.Key,
			"passed":    true,
		})
	}
	recordResponse, err := api.call("POST",
		"/v1/vessels/"+vesselID+"/cleaning-checks",
		map[string]any{"results": results},
		ptrInt64(detail.CurrentCleaning.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(recordResponse, 200, "record helper checks"); err != nil {
		return err
	}
	var record cleaningView
	if err := recordResponse.decode(&record); err != nil {
		return err
	}
	finishResponse, err := api.call("POST", "/v1/vessels/"+vesselID+"/finish-cleaning",
		nil, ptrInt64(record.Version))
	if err != nil {
		return err
	}
	return requireStatus(finishResponse, 200, "release helper vessel")
}
