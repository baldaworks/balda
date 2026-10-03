// Package mcpmanage owns worker MCP management policy.
package mcpmanage

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"maps"
	"os"
	"regexp"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

type Service struct {
	credentials cipher.AEAD
}

// New accepts one deployment-owned base64 AES-256 key. Empty is supported for
// installations without protected data; protected writes require a key.
func New(credentialKey string) (*Service, error) {
	s := &Service{}
	if credentialKey == "" {
		return s, nil
	}
	key, err := base64.StdEncoding.Strict().DecodeString(credentialKey)
	defer clear(key)
	if err != nil || len(key) != 32 {
		return nil, mcpcmd.ErrCredentials
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, mcpcmd.ErrCredentials
	}
	s.credentials, err = cipher.NewGCM(block)
	if err != nil {
		return nil, mcpcmd.ErrCredentials
	}
	return s, nil
}

// CredentialReader includes retained revision and worker-grant credentials.
type CredentialReader interface {
	ListMCPRevisions(ctx context.Context) ([]mcpcmd.Revision, error)
	ListMCPGrants(ctx context.Context) ([]mcpcmd.Grant, error)
}

type protectedValues struct {
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const maxProtectedPayload = 128 << 10

// PrepareRevision preserves omitted values and applies explicit edits. It
// re-encrypts retained secrets for the new immutable identity.
func (s *Service) PrepareRevision(previous *mcpcmd.Revision, next mcpcmd.Revision, edits mcpcmd.ValueEdits) (mcpcmd.Revision, error) {
	if next.ConnectionID == "" || next.ID == "" {
		return mcpcmd.Revision{}, mcpcmd.ErrInvalid
	}
	values := protectedValues{}
	if previous != nil {
		if previous.ConnectionID != next.ConnectionID || previous.ID == next.ID {
			return mcpcmd.Revision{}, mcpcmd.ErrInvalid
		}
		var err error
		values, err = s.openRevision(*previous)
		if err != nil {
			return mcpcmd.Revision{}, err
		}
		next.Definition.Env = previous.Definition.Env
		next.Definition.Headers = previous.Definition.Headers
	}
	next.Definition.Env, next.Definition.Headers = maps.Clone(next.Definition.Env), maps.Clone(next.Definition.Headers)
	next.Definition.Args = append([]string(nil), next.Definition.Args...)
	next.Definition.Targets.Providers = append([]string(nil), next.Definition.Targets.Providers...)
	next.Definition.Scopes = append([]string(nil), next.Definition.Scopes...)
	if next.Definition.AuthBinding != nil {
		binding := *next.Definition.AuthBinding
		next.Definition.AuthBinding = &binding
	}
	if next.Definition.Env == nil {
		next.Definition.Env = make(map[string]mcpcmd.ValueBinding)
	}
	if next.Definition.Headers == nil {
		next.Definition.Headers = make(map[string]mcpcmd.ValueBinding)
	}
	if values.Env == nil {
		values.Env = make(map[string]string)
	}
	if values.Headers == nil {
		values.Headers = make(map[string]string)
	}
	if err := applyValueEdits(next.Definition.Env, values.Env, edits.Env); err != nil {
		return mcpcmd.Revision{}, err
	}
	if err := applyValueEdits(next.Definition.Headers, values.Headers, edits.Headers); err != nil {
		return mcpcmd.Revision{}, err
	}
	if err := checkProtectedBindings(next.Definition.Env, values.Env); err != nil {
		return mcpcmd.Revision{}, err
	}
	if err := checkProtectedBindings(next.Definition.Headers, values.Headers); err != nil {
		return mcpcmd.Revision{}, err
	}
	payload, err := s.sealRevision(next, values)
	if err != nil {
		return mcpcmd.Revision{}, err
	}
	next.ProtectedValues = payload
	return next, nil
}

func applyValueEdits(bindings map[string]mcpcmd.ValueBinding, protected map[string]string, edits map[string]mcpcmd.ValueEdit) error {
	for key, edit := range edits {
		if key == "" || len(key) > 256 || len(edit.Value) > 16<<10 {
			return mcpcmd.ErrInvalid
		}
		switch edit.Operation {
		case mcpcmd.ValueKeep:
			current, found := bindings[key]
			if !found || edit.Value != "" || (edit.Kind != "" && edit.Kind != current.Kind) {
				return mcpcmd.ErrInvalid
			}
		case mcpcmd.ValueRemove:
			if edit.Value != "" {
				return mcpcmd.ErrInvalid
			}
			delete(bindings, key)
			delete(protected, key)
		case mcpcmd.ValueSet:
			delete(protected, key)
			binding := mcpcmd.ValueBinding{Kind: edit.Kind, Value: edit.Value}
			switch edit.Kind {
			case mcpcmd.ValueProtected:
				protected[key] = edit.Value
				binding.Value = ""
			case mcpcmd.ValueEnvironment:
				if len(edit.Value) > 256 || !environmentName.MatchString(edit.Value) {
					return mcpcmd.ErrInvalid
				}
			case mcpcmd.ValueLiteral:
			default:
				return mcpcmd.ErrInvalid
			}
			bindings[key] = binding
		default:
			return mcpcmd.ErrInvalid
		}
	}
	return nil
}

func revisionAAD(r mcpcmd.Revision) []byte {
	// JSON array framing prevents ambiguous concatenated identities.
	aad, _ := json.Marshal([]string{"balda-mcp-revision-v1", r.ConnectionID, r.ID})
	return aad
}

func (s *Service) sealRevision(r mcpcmd.Revision, values protectedValues) ([]byte, error) {
	if len(values.Env) == 0 && len(values.Headers) == 0 {
		return nil, nil
	}
	if s.credentials == nil {
		return nil, mcpcmd.ErrCredentials
	}
	plain, err := json.Marshal(values)
	defer clear(plain)
	if err != nil || len(plain) > maxProtectedPayload {
		return nil, mcpcmd.ErrInvalid
	}
	prefix := make([]byte, 1+s.credentials.NonceSize())
	prefix[0] = 1
	if _, err := rand.Read(prefix[1:]); err != nil {
		return nil, mcpcmd.ErrCredentials
	}
	return s.credentials.Seal(prefix, prefix[1:], plain, revisionAAD(r)), nil
}

func (s *Service) openRevision(r mcpcmd.Revision) (protectedValues, error) {
	var values protectedValues
	if len(r.ProtectedValues) > 0 {
		if s.credentials == nil || len(r.ProtectedValues) < 1+s.credentials.NonceSize()+s.credentials.Overhead() ||
			len(r.ProtectedValues) > maxProtectedPayload+1+s.credentials.NonceSize()+s.credentials.Overhead() || r.ProtectedValues[0] != 1 {
			return values, mcpcmd.ErrCredentials
		}
		prefixEnd := 1 + s.credentials.NonceSize()
		plain, err := s.credentials.Open(nil, r.ProtectedValues[1:prefixEnd], r.ProtectedValues[prefixEnd:], revisionAAD(r))
		if err != nil {
			return values, mcpcmd.ErrCredentials
		}
		defer clear(plain)
		if err := json.Unmarshal(plain, &values); err != nil {
			return protectedValues{}, mcpcmd.ErrCredentials
		}
	}
	if err := checkProtectedBindings(r.Definition.Env, values.Env); err != nil {
		return protectedValues{}, err
	}
	if err := checkProtectedBindings(r.Definition.Headers, values.Headers); err != nil {
		return protectedValues{}, err
	}
	return values, nil
}

func checkProtectedBindings(bindings map[string]mcpcmd.ValueBinding, values map[string]string) error {
	count := 0
	for key, binding := range bindings {
		switch binding.Kind {
		case mcpcmd.ValueProtected:
			if _, exists := values[key]; !exists || binding.Value != "" {
				return mcpcmd.ErrCredentials
			}
			count++
		case mcpcmd.ValueLiteral:
		case mcpcmd.ValueEnvironment:
			if len(binding.Value) > 256 || !environmentName.MatchString(binding.Value) {
				return mcpcmd.ErrInvalid
			}
		default:
			return mcpcmd.ErrInvalid
		}
	}
	if count != len(values) {
		return mcpcmd.ErrCredentials
	}
	return nil
}

func resolveBindings(bindings map[string]mcpcmd.ValueBinding, protected map[string]string) (map[string]string, error) {
	values := make(map[string]string, len(bindings))
	for key, binding := range bindings {
		switch binding.Kind {
		case mcpcmd.ValueProtected:
			values[key] = protected[key]
		case mcpcmd.ValueLiteral:
			values[key] = binding.Value
		case mcpcmd.ValueEnvironment:
			value, present := os.LookupEnv(binding.Value)
			if !present {
				return nil, mcpcmd.ErrCredentials
			}
			values[key] = value
		default:
			return nil, mcpcmd.ErrInvalid
		}
	}
	return values, nil
}

// ResolveValues resolves explicit deployment references at transport launch.
// Public literals are never implicitly expanded or treated as references.
func (s *Service) ResolveValues(r mcpcmd.Revision) (mcpcmd.LaunchValues, error) {
	protected, err := s.openRevision(r)
	if err != nil {
		return mcpcmd.LaunchValues{}, err
	}
	env, err := resolveBindings(r.Definition.Env, protected.Env)
	if err != nil {
		return mcpcmd.LaunchValues{}, err
	}
	headers, err := resolveBindings(r.Definition.Headers, protected.Headers)
	if err != nil {
		return mcpcmd.LaunchValues{}, err
	}
	return mcpcmd.LaunchValues{Env: env, Headers: headers}, nil
}

// ValidateCredentials checks every retained revision before provider/ingress
// startup. Historical pins need the same deployment key as current definitions.
func (s *Service) ValidateCredentials(ctx context.Context, reader CredentialReader) error {
	if reader == nil {
		return mcpcmd.ErrInvalid
	}
	revisions, err := reader.ListMCPRevisions(ctx)
	if err != nil {
		return err
	}
	for _, revision := range revisions {
		if _, err := s.openRevision(revision); err != nil {
			return err
		}
	}
	grants, err := reader.ListMCPGrants(ctx)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if _, err := s.OpenGrant(grant); err != nil {
			return err
		}
	}
	return nil
}
