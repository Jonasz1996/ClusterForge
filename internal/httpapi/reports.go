package httpapi

import (
	"errors"
	"net/http"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
	"github.com/Jonasz1996/clusterforge/internal/reports"
)

// recoveryReport is gen.RecoveryReport.
type recoveryReport struct {
	reports.Recovery
	SavedMonths []string `json:"saved_months"`
}

func (s *Server) GetRecoveryReport(w http.ResponseWriter, r *http.Request, params gen.GetRecoveryReportParams) {
	month := ""
	if params.Month != nil {
		month = *params.Month
	}
	rep, err := s.reports.Recovery(r.Context(), month)
	switch {
	case errors.Is(err, reports.ErrMonth), errors.Is(err, reports.ErrFuture):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	months, err := s.reports.Months(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, recoveryReport{Recovery: rep, SavedMonths: months})
}
