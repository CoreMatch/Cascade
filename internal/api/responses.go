package api

import (
	"crypto/rand"
	"encoding/hex"

	"github.com/gin-gonic/gin"
)

// Stable lower_snake error codes for the Cascade management API. Every code
// is registered in HA-Contract docs/api/error-codes.md. The backend-facing
// UPPER_SNAKE codes (FORBIDDEN, SESSION_NOT_FOUND, ROOM_CONFLICT, ...) come
// from the Cascade backend contract and are forwarded verbatim instead of
// being re-registered here.
const (
	codeInvalidJSONBody              = "invalid_json_body"
	codeInvalidRequest               = "invalid_request"
	codeInvalidCredentials           = "invalid_credentials"
	codeInternalError                = "internal_error"
	codeRouteNotFound                = "route_not_found"
	codeCascadeRoomConflict          = "cascade_room_conflict"
	codeCascadeBackendUnavailable    = "cascade_backend_unavailable"
	codeCascadeInvalidInternalKey    = "cascade_invalid_internal_key"
	codeCascadeDatabaseNotConfigured = "cascade_database_not_configured"
	codeCascadeBindingNotFound       = "cascade_binding_not_found"
)

// newRequestID returns a random 32-character hex identifier carried in every
// error envelope so operators can correlate a response with its log lines.
func newRequestID() string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

type errorMeta struct {
	RequestID string `json:"request_id"`
}

type errorEnvelope struct {
	Success bool      `json:"success"`
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Meta    errorMeta `json:"meta"`
}

// writeError emits the unified management-API error envelope:
// {"success":false,"code":...,"message":...,"meta":{"request_id":...}}.
func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorEnvelope{
		Success: false,
		Code:    code,
		Message: message,
		Meta:    errorMeta{RequestID: newRequestID()},
	})
}
