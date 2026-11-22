package transport

import (
	"context"
	"encoding/json"

	"github.com/tokensafe-dev/tokensafe-backend/pkg/protocol"
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

// Dispatch routes a validated OumvRequest to the corresponding adapter method.
func (d *Dispatcher) Dispatch(ctx context.Context, req *protocol.OumvRequest) *protocol.OumvResponse {
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

	default:
		return protocol.NewErrorResponse(req.ID, protocol.NewMethodNotFoundError(req.Method))
	}
}
