package protocol

import "fmt"

// Standard JSON-RPC 2.0 Error Codes
const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternalError  = -32603
)

// OUMV Domain-Specific Error Codes (-32001 to -32006)
const (
	// ErrConflictDetected indicates an Optimistic Concurrency Control (OCC) version mismatch.
	ErrConflictDetected = -32001

	// ErrSyntaxError indicates target code edit failed compiler, parser or type checks.
	ErrSyntaxError = -32002

	// ErrSecurityViolation indicates path traversal out of workspace or credential leak.
	ErrSecurityViolation = -32003

	// ErrTargetNotFound indicates target handler, endpoint, struct or component is not registered.
	ErrTargetNotFound = -32004

	// ErrActionNotFound indicates requested action, method or effect is not defined.
	ErrActionNotFound = -32005

	// ErrUnsupportedCapability indicates requested feature is not supported by runtime adapter.
	ErrUnsupportedCapability = -32006

	// ErrPermissionDenied indicates caller lacks capability or mutation permission.
	ErrPermissionDenied = -32007

	// ErrRuntimeUnavailable indicates target dev-server or database is unreachable.
	ErrRuntimeUnavailable = -32008

	// ErrResourceNotFound indicates target resource was not found.
	ErrResourceNotFound = -32009

	// ErrStaleResourceVersion indicates stale version token during OCC edit.
	ErrStaleResourceVersion = -32010

	// ErrValidationFailed indicates payload failed schema or business constraint validation.
	ErrValidationFailed = -32011
)

// OumvError represents a structured JSON-RPC 2.0 error object.
type OumvError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (e *OumvError) Error() string {
	return fmt.Sprintf("OUMV error %d: %s", e.Code, e.Message)
}

// Helper constructors for standard OUMV errors.

func NewConflictError(msg string, data interface{}) *OumvError {
	if msg == "" {
		msg = "CONFLICT_DETECTED: file version mismatch during OCC mutation"
	}
	return &OumvError{Code: ErrConflictDetected, Message: msg, Data: data}
}

func NewSyntaxError(msg string, data interface{}) *OumvError {
	if msg == "" {
		msg = "SYNTAX_ERROR: in-memory compilation or type check failed"
	}
	return &OumvError{Code: ErrSyntaxError, Message: msg, Data: data}
}

func NewSecurityViolationError(msg string, data interface{}) *OumvError {
	if msg == "" {
		msg = "SECURITY_VIOLATION: prohibited operation or path traversal"
	}
	return &OumvError{Code: ErrSecurityViolation, Message: msg, Data: data}
}

func NewTargetNotFoundError(target string) *OumvError {
	return &OumvError{
		Code:    ErrTargetNotFound,
		Message: fmt.Sprintf("TARGET_NOT_FOUND: %q is not registered or active in runtime", target),
	}
}

func NewActionNotFoundError(action string) *OumvError {
	return &OumvError{
		Code:    ErrActionNotFound,
		Message: fmt.Sprintf("ACTION_NOT_FOUND: %q is not defined on target", action),
	}
}

func NewUnsupportedCapabilityError(capName string) *OumvError {
	return &OumvError{
		Code:    ErrUnsupportedCapability,
		Message: fmt.Sprintf("UNSUPPORTED_CAPABILITY: %q is not supported by this runtime adapter", capName),
	}
}

func NewMethodNotFoundError(method string) *OumvError {
	return &OumvError{
		Code:    ErrMethodNotFound,
		Message: fmt.Sprintf("Method %q not found", method),
	}
}

func NewPermissionDeniedError(msg string) *OumvError {
	if msg == "" {
		msg = "PERMISSION_DENIED: operation or capability is not permitted by policy"
	}
	return &OumvError{
		Code:    ErrPermissionDenied,
		Message: msg,
	}
}

func NewInvalidParamsError(msg string) *OumvError {
	return &OumvError{
		Code:    ErrInvalidParams,
		Message: msg,
	}
}
