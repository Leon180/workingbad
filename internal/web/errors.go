package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/Leon180/workingbad/internal/repository"
)

// statusFor maps a repository-layer error to the right HTTP status code.
// Pre-fix every mutation handler returned 400 for every error, which made
// DB outages look like client mistakes and hid genuine validation errors
// in the same bucket as "row not found" (go-reviewer P2, silent-failure-
// hunter P1).
//
// Mapping:
//
//	ErrVersionConflict       → 409 Conflict (concurrent supersede lost)
//	ErrNotFound              → 404 Not Found
//	ErrInvalidInput          → 400 Bad Request (validator rejected input)
//	context.Canceled / DeadlineExceeded → 499 (client gone; conventional)
//	nil sentinel + non-nil   → 500 Internal Server Error (DB/transport)
//
// A nil error returns 200 (only sensible default if someone misuses the
// helper). Callers should branch on err != nil first.
func statusFor(err error) int {
	if err == nil {
		return http.StatusOK
	}
	switch {
	case errors.Is(err, repository.ErrVersionConflict):
		return http.StatusConflict
	case errors.Is(err, repository.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, repository.ErrInvalidInput):
		return http.StatusBadRequest
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// 499 is not in the RFC but is conventional for "client went away".
		// http.StatusText(499) returns "" — fine, we serve our own body.
		return 499
	default:
		return http.StatusInternalServerError
	}
}
