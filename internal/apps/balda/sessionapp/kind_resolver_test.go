package sessionapp

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemoryapp"
)

func TestSessionKindResolverAdapterUsesTransportClassifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		scope deliverycmd.LocatorScopeKind
		want  baldasession.Kind
	}{
		{name: "personal", scope: deliverycmd.LocatorScopePersonal, want: baldasession.KindDM},
		{name: "group", scope: deliverycmd.LocatorScopeGroup, want: baldasession.KindChat},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := sessionmemoryapp.NewScopeResolver(map[string]sessionmemoryapp.ScopeClassifier{
				"fixture": func(deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
					return test.scope, nil
				},
			})
			got, err := (SessionKindResolverAdapter{Resolver: resolver}).ResolveKind(baldasession.SessionLocator{
				SessionID: "session-1", ChannelType: "fixture", AddressKey: "address-1", AddressJSON: `{}`,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ResolveKind() = %q, want %q", got, test.want)
			}
		})
	}
}
