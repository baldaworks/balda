package app

import (
	"context"

	"github.com/normahq/balda/sessionmemory"
)

// TraceService adapts the read-only canonical derived reader to the portable
// TraceReader capability.  It never accepts a scope from a transport adapter;
// the caller must have resolved the exact scope before constructing the
// request.
type TraceService struct {
	reader sessionmemory.CanonicalDerivedReader
}

// NewTraceService constructs a bounded trace adapter.
func NewTraceService(reader sessionmemory.CanonicalDerivedReader) (*TraceService, error) {
	if reader == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "trace reader is required", nil)
	}
	return &TraceService{reader: reader}, nil
}

// Trace implements TraceReader.
func (s *TraceService) Trace(ctx context.Context, request sessionmemory.TraceRequest) (sessionmemory.TraceResponse, error) {
	if s == nil || s.reader == nil {
		return sessionmemory.TraceResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "trace service is unavailable", nil)
	}
	normalized, err := sessionmemory.NormalizeTraceRequest(request, sessionmemory.MaxTraceNodes)
	if err != nil {
		return sessionmemory.TraceResponse{}, err
	}
	return s.reader.Trace(ctx, normalized)
}

var _ TraceReader = (*TraceService)(nil)
