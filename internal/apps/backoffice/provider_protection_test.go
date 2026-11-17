package backoffice

import "github.com/baldaworks/balda/internal/apps/balda/secrets"

func newProviderProtector(encodedKey string) (*secrets.Protector, error) {
	root, err := secrets.NewRoot(encodedKey)
	if err != nil {
		return nil, err
	}
	return root.Protector(secrets.ScopeProviderRevision)
}
