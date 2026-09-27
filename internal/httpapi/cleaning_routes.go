package httpapi

import (
	"net/http"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

type recordCleaningCheckRequest struct {
	Sequence int                `json:"sequence"`
	Result   domain.CheckResult `json:"result"`
	Note     string             `json:"note"`
}

func registerCleaningRoutes(mux *http.ServeMux, app *service.Service) {
	mux.HandleFunc("GET /v1/cleaning-records/{id}", func(response http.ResponseWriter, request *http.Request) {
		record, err := app.GetCleaningRecord(request.PathValue("id"))
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, record)
	})

	mux.HandleFunc("POST /v1/cleaning-records/{id}/checks", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[recordCleaningCheckRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		expected, err := expectedVersion(request)
		if !handleDecode(response, err) {
			return
		}
		record, vessel, err := app.RecordCleaningCheck(request.PathValue("id"),
			service.RecordCleaningCheckInput{
				ExpectedVersion: expected,
				Sequence:        body.Sequence,
				Result:          body.Result,
				Note:            body.Note,
			})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{
			"cleaning_record": record,
			"vessel":          vessel,
		})
	})
}
