package ekman

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
)

// Error is an error with an HTTP status and a stable machine-readable code. Handlers return it
// to choose what the client sees; any other error becomes a logged 500.
type Error struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string // per-field messages, e.g. from Validation
}

// Error implements error.
func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// NotFound returns a 404 with code "not_found".
func NotFound(msg string) *Error {
	return &Error{Status: http.StatusNotFound, Code: "not_found", Message: msg}
}

// BadRequest returns a 400 with the given code.
func BadRequest(code, msg string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: code, Message: msg}
}

// Unauthorized returns a 401 with code "unauthorized": the caller must sign in.
func Unauthorized(msg string) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: msg}
}

// Forbidden returns a 403 with code "forbidden": signed in, but not allowed.
func Forbidden(msg string) *Error {
	return &Error{Status: http.StatusForbidden, Code: "forbidden", Message: msg}
}

// Conflict returns a 409 with the given code.
func Conflict(code, msg string) *Error {
	return &Error{Status: http.StatusConflict, Code: code, Message: msg}
}

// Unprocessable returns a 422 with the given code.
func Unprocessable(code, msg string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: code, Message: msg}
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	RequestID string            `json:"requestId,omitempty"`
}

// toError maps any error to the wire error. ok is false for errors that are server faults.
func toError(err error) (e *Error, ok bool) {
	var ee *Error
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &ee):
		if ee.Status == 0 {
			cp := *ee
			cp.Status = http.StatusInternalServerError
			return &cp, cp.Code != ""
		}
		return ee, true
	case errors.Is(err, sql.ErrNoRows):
		return NotFound("not found"), true
	case errors.As(err, &tooBig):
		return &Error{Status: http.StatusRequestEntityTooLarge, Code: "too_large", Message: "request body too large"}, true
	}
	return &Error{Status: http.StatusInternalServerError, Code: "internal", Message: "internal server error"}, false
}

// WriteError writes err in ekman's error shape. Errors that are not *Error (and not mapped,
// like sql.ErrNoRows) are logged and sent as a generic 500. Use it in Raw handlers.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	e, ok := toError(err)
	st := stateFrom(r.Context())
	if !ok && st != nil {
		level := slogError
		if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
			level = slogInfo // the client went away; not our fault
		}
		st.log(r.Context(), level, "handler error", "err", err.Error())
	}
	body := errorBody{Error: errorDetail{Code: e.Code, Message: e.Message, Fields: e.Fields}}
	if st != nil {
		body.Error.RequestID = st.id
	}
	writeJSON(w, e.Status, body)
}

// WriteJSON writes v as JSON with the given status. Use it in Raw handlers.
func WriteJSON(w http.ResponseWriter, status int, v any) { writeJSON(w, status, v) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"response encoding failed"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}
