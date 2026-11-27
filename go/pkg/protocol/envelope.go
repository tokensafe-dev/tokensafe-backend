package protocol

import (
	"encoding/json"
	"fmt"
)

// CurrentProtocolVersion represents the supported OUMV specification version.
const CurrentProtocolVersion = "1.5.0"

// OumvRequest represents a standard JSON-RPC 2.0 request envelope.
type OumvRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// OumvResponse represents a standard JSON-RPC 2.0 response envelope.
type OumvResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *OumvError  `json:"error,omitempty"`
}

// NewSuccessResponse creates a successful JSON-RPC 2.0 response.
func NewSuccessResponse(id interface{}, result interface{}) *OumvResponse {
	return &OumvResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

// NewErrorResponse creates an error JSON-RPC 2.0 response.
func NewErrorResponse(id interface{}, err *OumvError) *OumvResponse {
	return &OumvResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   err,
	}
}

// NewRequest creates a JSON-RPC 2.0 request envelope.
func NewRequest(id interface{}, method string, params interface{}) *OumvRequest {
	req := &OumvRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
	}
	if params != nil {
		raw, _ := json.Marshal(params)
		req.Params = raw
	}
	return req
}

// Validate ensures the request envelope conforms to JSON-RPC 2.0.
func (r *OumvRequest) Validate() error {
	if r.JSONRPC != "2.0" {
		return fmt.Errorf("invalid jsonrpc version %q, expected \"2.0\"", r.JSONRPC)
	}
	if r.Method == "" {
		return fmt.Errorf("method cannot be empty")
	}
	return nil
}
