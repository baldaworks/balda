package mcp

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/balda/sessionmemory"
)

type recallSearcherFunc func(context.Context, sessionmemory.RecallRequest) (sessionmemory.RecallResponse, error)

func (f recallSearcherFunc) Search(ctx context.Context, request sessionmemory.RecallRequest) (sessionmemory.RecallResponse, error) {
	return f(ctx, request)
}

func TestSearchUsesOnlyResolvedScopeAndReturnsBoundedReference(t *testing.T) {
	t.Parallel()
	scope := sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal}
	var received sessionmemory.RecallRequest
	service := New(Config{
		Enabled: true,
		ScopeResolver: ScopeResolverFunc(func(context.Context, *mcp.CallToolRequest) (sessionmemory.Scope, error) {
			return scope, nil
		}),
		Searcher: recallSearcherFunc(func(_ context.Context, request sessionmemory.RecallRequest) (sessionmemory.RecallResponse, error) {
			received = request
			return sessionmemory.RecallResponse{
				SchemaVersion: sessionmemory.RecallSchemaVersionV1,
				Trust:         sessionmemory.ReferenceTrustUntrusted,
				Scope:         scope,
				Results: []sessionmemory.RecallReference{{
					SchemaVersion: sessionmemory.RecallSchemaVersionV1,
					Trust:         sessionmemory.ReferenceTrustUntrusted,
					Scope:         scope,
					ItemID:        "item-1",
					RevisionID:    "revision-1",
					Revision:      1,
					Kind:          sessionmemory.MemoryKindState,
					State:         sessionmemory.RevisionStateActive,
					Text:          "untrusted reference",
					CreatedAt:     time.Unix(1, 0).UTC(),
				}},
			}, nil
		}),
	})
	_, output, err := service.search(context.Background(), nil, SearchInput{Query: "reference", Limit: 1})
	if err != nil || !output.OK {
		t.Fatalf("search() = output %#v, error %v", output, err)
	}
	if received.Scope != scope || received.Query != "reference" || received.Limit != 1 {
		t.Fatalf("search request = %#v", received)
	}
	if len(output.Results) != 1 || output.Results[0].Text != "untrusted reference" {
		t.Fatalf("search output = %#v", output.Results)
	}
}

func TestSearchRejectsForeignResolvedResult(t *testing.T) {
	t.Parallel()
	scope := sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal}
	foreign := sessionmemory.Scope{Key: "telegram:-100:1", Kind: sessionmemory.ScopeKindGroup}
	service := New(Config{
		Enabled: true,
		ScopeResolver: ScopeResolverFunc(func(context.Context, *mcp.CallToolRequest) (sessionmemory.Scope, error) {
			return scope, nil
		}),
		Searcher: recallSearcherFunc(func(context.Context, sessionmemory.RecallRequest) (sessionmemory.RecallResponse, error) {
			return sessionmemory.RecallResponse{
				SchemaVersion: sessionmemory.RecallSchemaVersionV1,
				Trust:         sessionmemory.ReferenceTrustUntrusted,
				Scope:         foreign,
			}, nil
		}),
	})
	result, output, err := service.search(context.Background(), nil, SearchInput{Query: "secret"})
	if err != nil || result == nil || !result.IsError || output.OK {
		t.Fatalf("foreign search = result %#v output %#v error %v", result, output, err)
	}
	if output.Error == nil || output.Error.Code != string(sessionmemory.CodeScopeViolation) {
		t.Fatalf("foreign search error = %#v", output.Error)
	}
}

func TestMCPContractUsesNeutralNamesAndNoCallerScopeFields(t *testing.T) {
	t.Parallel()
	if ToolName != "session_memory.search" || TraceToolName != "session_memory.trace" {
		t.Fatalf("tool names = %q/%q", ToolName, TraceToolName)
	}
	typeOfInput := reflect.TypeOf(SearchInput{})
	for _, forbidden := range []string{"Scope", "Locator", "ChannelType", "AddressKey"} {
		if _, ok := typeOfInput.FieldByName(forbidden); ok {
			t.Fatalf("SearchInput exposes caller scope field %q", forbidden)
		}
	}
}
