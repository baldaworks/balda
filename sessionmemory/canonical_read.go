package sessionmemory

import "context"

// CanonicalDerivedReader is the storage-neutral compatibility port used by
// application providers that still expose the established derived search and
// trace contracts. Implementations must hydrate from canonical state and
// enforce the request's exact scope and bounds.
type CanonicalDerivedReader interface {
	SearchDerived(ctx context.Context, request DerivedSearchRequest) (DerivedSearchResponse, error)
	Trace(ctx context.Context, request TraceRequest) (TraceResponse, error)
}
