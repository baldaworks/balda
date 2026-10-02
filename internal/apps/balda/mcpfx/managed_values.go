package mcpfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
)

type managedValueResolver interface {
	ResolveValues(revision mcpcmd.Revision) (mcpcmd.LaunchValues, error)
}

// ResolveManagedLaunch resolves credentials only at the trusted launch boundary.
func ResolveManagedLaunch(revision mcpcmd.Revision, resolver managedValueResolver) (mcpruntime.LaunchConfig, error) {
	if resolver == nil {
		return mcpruntime.LaunchConfig{}, mcpcmd.ErrInvalid
	}
	values, err := resolver.ResolveValues(revision)
	if err != nil {
		return mcpruntime.LaunchConfig{}, err
	}
	definition := revision.Definition
	transport := string(definition.Transport)
	if definition.Transport == mcpcmd.TransportHTTP {
		transport = transportStreamableHTTP
	}
	return mcpruntime.LaunchConfig{Transport: transport, Command: definition.Command, Args: append([]string(nil), definition.Args...),
		WorkingDir: definition.Directory, URL: definition.URL, Env: values.Env, Headers: values.Headers,
		EnforceHTTPOrigin: definition.Transport != mcpcmd.TransportStdio}, nil
}
