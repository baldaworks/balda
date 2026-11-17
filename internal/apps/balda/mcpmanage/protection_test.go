package mcpmanage

import (
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/secrets"
)

func newTestService(encodedKey string) (*Service, error) {
	root, err := secrets.NewRoot(encodedKey)
	if err != nil {
		return nil, mcpcmd.ErrCredentials
	}
	revisions, err := root.Protector(secrets.ScopeMCPRevision)
	if err != nil {
		return nil, mcpcmd.ErrCredentials
	}
	grants, err := root.Protector(secrets.ScopeMCPGrant)
	if err != nil {
		return nil, mcpcmd.ErrCredentials
	}
	return New(revisions, grants)
}

func newLegacyTestService(encodedKey string) (*Service, error) {
	// Deprecated root-direct encryption is required only to build retained v1 fixtures.
	//nolint:staticcheck
	protector, err := secrets.New(encodedKey)
	if err != nil {
		return nil, mcpcmd.ErrCredentials
	}
	return New(protector, protector)
}
