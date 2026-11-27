package transport

import (
	"context"
	"encoding/json"

	"github.com/tokensafe-dev/tokensafe-backend/go/pkg/protocol"
)

// Dispatcher coordinates incoming JSON-RPC 2.0 requests to the active OumvRuntimeAdapter.
type Dispatcher struct {
	adapter protocol.OumvRuntimeAdapter
}

// NewDispatcher creates a new JSON-RPC dispatcher bound to an adapter.
func NewDispatcher(adapter protocol.OumvRuntimeAdapter) *Dispatcher {
	return &Dispatcher{
		adapter: adapter,
	}
}

// Handle executes a raw incoming JSON-RPC message and produces a response envelope.
func (d *Dispatcher) Handle(ctx context.Context, reqBytes []byte) *protocol.OumvResponse {
	var req protocol.OumvRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		return protocol.NewErrorResponse(nil, &protocol.OumvError{
			Code:    protocol.ErrParseError,
			Message: "Parse error: " + err.Error(),
		})
	}

	if err := req.Validate(); err != nil {
		return protocol.NewErrorResponse(req.ID, &protocol.OumvError{
			Code:    protocol.ErrInvalidRequest,
			Message: "Invalid Request: " + err.Error(),
		})
	}

	return d.Dispatch(ctx, &req)
}

// sessionAware is implemented by runtimes that issue handshake sessions.
type sessionAware interface {
	ValidateSession(id string) bool
	Disconnect(id string) bool
}

func sessionIDFromParams(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var probe struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return probe.SessionID
}

// EndSession closes the session a streaming connection was bound to (spec §2.4:
// closing the socket ends the session). Returns false for an unknown or empty id.
func (d *Dispatcher) EndSession(id string) bool {
	if sessions, ok := d.adapter.(sessionAware); ok && id != "" {
		return sessions.Disconnect(id)
	}
	return false
}

// Dispatch routes a validated OumvRequest to the corresponding adapter method.
func (d *Dispatcher) Dispatch(ctx context.Context, req *protocol.OumvRequest) *protocol.OumvResponse {
	if sessions, ok := d.adapter.(sessionAware); ok {
		sessionID := sessionIDFromParams(req.Params)

		if req.Method == protocol.MethodDisconnect {
			closed := sessions.Disconnect(sessionID)
			return protocol.NewSuccessResponse(req.ID, map[string]interface{}{"closed": closed})
		}

		if req.Method != protocol.MethodHandshake && !sessions.ValidateSession(sessionID) {
			return protocol.NewErrorResponse(req.ID, protocol.NewValidationFailedError(
				"VALIDATION_FAILED: unknown or expired sessionId — re-run oumv/handshake",
				map[string]string{"reason": "session_expired", "sessionId": sessionID},
			))
		}
	}

	switch req.Method {
	case protocol.MethodHandshake:
		var params protocol.OumvHandshakeParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return protocol.NewErrorResponse(req.ID, protocol.NewInvalidParamsError(err.Error()))
			}
		}
		res, oumvErr := d.adapter.Handshake(ctx, &params)
		if oumvErr != nil {
			return protocol.NewErrorResponse(req.ID, oumvErr)
		}
		return protocol.NewSuccessResponse(req.ID, res)

	case protocol.MethodObserve:
		var params protocol.ObserveParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return protocol.NewErrorResponse(req.ID, protocol.NewInvalidParamsError(err.Error()))
			}
		}
		res, oumvErr := d.adapter.Observe(ctx, &params)
		if oumvErr != nil {
			return protocol.NewErrorResponse(req.ID, oumvErr)
		}
		return protocol.NewSuccessResponse(req.ID, res)

	case protocol.MethodUnderstand:
		var params protocol.UnderstandParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return protocol.NewErrorResponse(req.ID, protocol.NewInvalidParamsError(err.Error()))
			}
		}
		res, oumvErr := d.adapter.Understand(ctx, &params)
		if oumvErr != nil {
			return protocol.NewErrorResponse(req.ID, oumvErr)
		}
		return protocol.NewSuccessResponse(req.ID, res)

	case protocol.MethodGetSource:
		var params protocol.GetSourceParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return protocol.NewErrorResponse(req.ID, protocol.NewInvalidParamsError(err.Error()))
			}
		}
		res, oumvErr := d.adapter.GetSource(ctx, &params)
		if oumvErr != nil {
			return protocol.NewErrorResponse(req.ID, oumvErr)
		}
		return protocol.NewSuccessResponse(req.ID, res)

	case protocol.MethodApplyEdit:
		var params protocol.ApplyEditParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return protocol.NewErrorResponse(req.ID, protocol.NewInvalidParamsError(err.Error()))
			}
		}
		res, oumvErr := d.adapter.ApplyEdit(ctx, &params)
		if oumvErr != nil {
			return protocol.NewErrorResponse(req.ID, oumvErr)
		}
		return protocol.NewSuccessResponse(req.ID, res)

	case protocol.MethodVerify:
		var params protocol.VerifyParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return protocol.NewErrorResponse(req.ID, protocol.NewInvalidParamsError(err.Error()))
			}
		}
		res, oumvErr := d.adapter.Verify(ctx, &params)
		if oumvErr != nil {
			return protocol.NewErrorResponse(req.ID, oumvErr)
		}
		return protocol.NewSuccessResponse(req.ID, res)

	// Wire 1.6.0 optional surface (spec §3/§4.4/§14): a runtime that does not
	// implement the method answers -32601 like any unknown method.
	case protocol.MethodPreviewEdit:
		impl, ok := d.adapter.(protocol.PreviewEditor)
		if !ok {
			return protocol.NewErrorResponse(req.ID, protocol.NewMethodNotFoundError(req.Method))
		}
		var params protocol.PreviewEditParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return protocol.NewErrorResponse(req.ID, protocol.NewInvalidParamsError(err.Error()))
			}
		}
		res, oumvErr := impl.PreviewEdit(ctx, &params)
		if oumvErr != nil {
			return protocol.NewErrorResponse(req.ID, oumvErr)
		}
		return protocol.NewSuccessResponse(req.ID, res)

	case protocol.MethodCancel:
		impl, ok := d.adapter.(protocol.Canceller)
		if !ok {
			return protocol.NewErrorResponse(req.ID, protocol.NewMethodNotFoundError(req.Method))
		}
		var params protocol.CancelParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return protocol.NewErrorResponse(req.ID, protocol.NewInvalidParamsError(err.Error()))
			}
		}
		res, oumvErr := impl.Cancel(ctx, &params)
		if oumvErr != nil {
			return protocol.NewErrorResponse(req.ID, oumvErr)
		}
		return protocol.NewSuccessResponse(req.ID, res)

	case protocol.MethodIndex:
		impl, ok := d.adapter.(protocol.Indexer)
		if !ok {
			return protocol.NewErrorResponse(req.ID, protocol.NewMethodNotFoundError(req.Method))
		}
		var params protocol.IndexParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return protocol.NewErrorResponse(req.ID, protocol.NewInvalidParamsError(err.Error()))
			}
		}
		res, oumvErr := impl.Index(ctx, &params)
		if oumvErr != nil {
			return protocol.NewErrorResponse(req.ID, oumvErr)
		}
		return protocol.NewSuccessResponse(req.ID, res)

	case protocol.MethodQuery:
		impl, ok := d.adapter.(protocol.Querier)
		if !ok {
			return protocol.NewErrorResponse(req.ID, protocol.NewMethodNotFoundError(req.Method))
		}
		var params protocol.QueryParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return protocol.NewErrorResponse(req.ID, protocol.NewInvalidParamsError(err.Error()))
			}
		}
		res, oumvErr := impl.Query(ctx, &params)
		if oumvErr != nil {
			return protocol.NewErrorResponse(req.ID, oumvErr)
		}
		return protocol.NewSuccessResponse(req.ID, res)

	case protocol.MethodBuild:
		impl, ok := d.adapter.(protocol.Builder)
		if !ok {
			return protocol.NewErrorResponse(req.ID, protocol.NewMethodNotFoundError(req.Method))
		}
		var params protocol.BuildParams
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return protocol.NewErrorResponse(req.ID, protocol.NewInvalidParamsError(err.Error()))
			}
		}
		res, oumvErr := impl.Build(ctx, &params)
		if oumvErr != nil {
			return protocol.NewErrorResponse(req.ID, oumvErr)
		}
		return protocol.NewSuccessResponse(req.ID, res)

	default:
		return protocol.NewErrorResponse(req.ID, protocol.NewMethodNotFoundError(req.Method))
	}
}
