package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

const maxBodyBytes = 1 << 20

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, gen.Error{Code: code, Message: msg})
}

// decode leest een JSON-body met een maximale grootte en zonder onbekende velden.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "ongeldige JSON: "+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "body bevat meer dan één JSON-object")
		return false
	}
	return true
}
