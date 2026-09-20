package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var b errorBody
	b.Error.Code = code
	b.Error.Message = msg
	writeJSON(w, status, b)
}

// decodeJSON strictly decodes a JSON body, rejecting unknown fields and empty
// bodies. Returns false (and writes the error) on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body is required")
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// parseIDR converts a JSON number to an integer rupiah amount, rejecting any
// fractional value. Money is only ever whole rupiah; a fractional input is a
// client bug we refuse rather than silently round.
func parseIDR(n json.Number) (int64, error) {
	if n == "" {
		return 0, fmt.Errorf("amount is required")
	}
	v, err := n.Int64()
	if err != nil {
		return 0, fmt.Errorf("amount must be a whole number of IDR (no decimals)")
	}
	return v, nil
}
