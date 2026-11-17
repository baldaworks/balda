package mcpfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/secrets"
)

func newMCPService(encodedKey string) (*mcpmanage.Service, error) {
	root, err := secrets.NewRoot(encodedKey)
	if err != nil {
		return nil, err
	}
	revisions, err := root.Protector(secrets.ScopeMCPRevision)
	if err != nil {
		return nil, err
	}
	grants, err := root.Protector(secrets.ScopeMCPGrant)
	if err != nil {
		return nil, err
	}
	return mcpmanage.New(revisions, grants)
}
