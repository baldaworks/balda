package mcpmanage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

func TestProtectedLiteralsSurviveRevisionChangeWithoutEnteringPublicJSON(t *testing.T) {
	s, err := New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	const secret = "protected-worker-header-fixture"
	r, err := s.PrepareRevision(nil, credentialTestRevision(), mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{
		"Authorization": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: secret},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ProtectedValues) == 0 || bytes.Contains(r.ProtectedValues, []byte(secret)) {
		t.Fatal("literal was not protected at rest")
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("literal entered public JSON")
	}
	values, err := s.ResolveValues(r)
	if err != nil || values.Headers["Authorization"] != secret {
		t.Fatalf("protected value unavailable: %v", err)
	}
	next := r
	next.ID = "revision-2"
	next, err = s.PrepareRevision(&r, next, mcpcmd.ValueEdits{})
	if err != nil {
		t.Fatal(err)
	}
	values, err = s.ResolveValues(next)
	if err != nil || values.Headers["Authorization"] != secret {
		t.Fatalf("omitted edit removed protected value: %v", err)
	}
	if bytes.Equal(r.ProtectedValues, next.ProtectedValues) {
		t.Fatal("new revision reused its old protected payload")
	}
	wrong := next
	wrong.ProtectedValues = r.ProtectedValues
	if _, err := s.ResolveValues(wrong); !errors.Is(err, mcpcmd.ErrCredentials) {
		t.Fatalf("another revision's payload accepted: %v", err)
	}
}

func TestProtectedValuesRejectTamperingWrongKeyAndMissingMaterial(t *testing.T) {
	s, err := New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.PrepareRevision(nil, credentialTestRevision(), mcpcmd.ValueEdits{Env: map[string]mcpcmd.ValueEdit{
		"TOKEN": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "fixture-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	missingKey, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range []*Service{wrongKey, missingKey} {
		if _, err := service.ResolveValues(r); !errors.Is(err, mcpcmd.ErrCredentials) {
			t.Fatalf("unavailable key accepted: %v", err)
		}
	}
	for _, change := range []func(*mcpcmd.Revision){
		func(r *mcpcmd.Revision) { r.ProtectedValues[len(r.ProtectedValues)-1] ^= 1 },
		func(r *mcpcmd.Revision) { r.ConnectionID = "other-connection" },
		func(r *mcpcmd.Revision) { r.ProtectedValues = nil },
		func(r *mcpcmd.Revision) { r.ProtectedValues = r.ProtectedValues[:2] },
	} {
		bad := r
		bad.ProtectedValues = bytes.Clone(r.ProtectedValues)
		change(&bad)
		if _, err := s.ResolveValues(bad); !errors.Is(err, mcpcmd.ErrCredentials) {
			t.Fatalf("invalid protected revision accepted: %v", err)
		}
	}
}

func TestValueEditsKeepReplaceRemoveWithoutChangingPreviousRevision(t *testing.T) {
	s, err := New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.PrepareRevision(nil, credentialTestRevision(), mcpcmd.ValueEdits{Env: map[string]mcpcmd.ValueEdit{
		"TOKEN": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "first"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	next := first
	next.ID = "revision-2"
	kept, err := s.PrepareRevision(&first, next, mcpcmd.ValueEdits{Env: map[string]mcpcmd.ValueEdit{"TOKEN": {Operation: mcpcmd.ValueKeep}}})
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.ResolveValues(kept)
	if err != nil || v.Env["TOKEN"] != "first" {
		t.Fatalf("keep lost value: %v", err)
	}
	next.ID = "revision-3"
	replaced, err := s.PrepareRevision(&kept, next, mcpcmd.ValueEdits{Env: map[string]mcpcmd.ValueEdit{
		"TOKEN": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "second"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.ResolveValues(replaced)
	if err != nil || v.Env["TOKEN"] != "second" {
		t.Fatalf("replace lost value: %v", err)
	}
	v, err = s.ResolveValues(first)
	if err != nil || v.Env["TOKEN"] != "first" {
		t.Fatalf("edit mutated retained revision: %v", err)
	}
	next.ID = "revision-4"
	removed, err := s.PrepareRevision(&replaced, next, mcpcmd.ValueEdits{Env: map[string]mcpcmd.ValueEdit{"TOKEN": {Operation: mcpcmd.ValueRemove}}})
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.ResolveValues(removed)
	if _, present := v.Env["TOKEN"]; err != nil || present || len(removed.ProtectedValues) != 0 {
		t.Fatalf("remove retained material: %v", err)
	}
	_, err = s.PrepareRevision(&first, next, mcpcmd.ValueEdits{Env: map[string]mcpcmd.ValueEdit{"MISSING": {Operation: mcpcmd.ValueKeep}}})
	if !errors.Is(err, mcpcmd.ErrInvalid) {
		t.Fatalf("keep of missing value accepted: %v", err)
	}
}

func TestEnvironmentReferencesResolveAtLaunchAndDistinguishEmptyFromMissing(t *testing.T) {
	const envName = "BALDA_MCP_CREDENTIAL_REFERENCE_FIXTURE"
	t.Setenv(envName, "first")
	s, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.PrepareRevision(nil, credentialTestRevision(), mcpcmd.ValueEdits{Env: map[string]mcpcmd.ValueEdit{
		"REMOTE_TOKEN": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueEnvironment, Value: envName},
		"LITERAL":      {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueLiteral, Value: "${" + envName + "}"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first", "changed", ""} {
		t.Setenv(envName, want)
		v, err := s.ResolveValues(r)
		got, present := v.Env["REMOTE_TOKEN"]
		if err != nil || !present || got != want || v.Env["LITERAL"] != "${"+envName+"}" {
			t.Fatalf("explicit reference/literal changed: %v", err)
		}
	}
	if err := os.Unsetenv(envName); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveValues(r); !errors.Is(err, mcpcmd.ErrCredentials) {
		t.Fatalf("missing reference silently resolved: %v", err)
	}
}

type retainedRevisionReader []mcpcmd.Revision

func (r retainedRevisionReader) ListMCPRevisions(context.Context) ([]mcpcmd.Revision, error) {
	return r, nil
}

func TestCredentialReadinessChecksHistoricalProtectedRevisions(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	s, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.PrepareRevision(nil, credentialTestRevision(), mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{
		"Authorization": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "worker-secret"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	public := credentialTestRevision()
	public.ID = "current-public-revision"
	noKey, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if err := noKey.ValidateCredentials(t.Context(), retainedRevisionReader{public}); err != nil {
		t.Fatalf("public-only installation requires a key: %v", err)
	}
	if err := noKey.ValidateCredentials(t.Context(), retainedRevisionReader{public, r}); !errors.Is(err, mcpcmd.ErrCredentials) {
		t.Fatalf("historical protected data bypassed key readiness: %v", err)
	}
	restarted, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ValidateCredentials(t.Context(), retainedRevisionReader{public, r}); err != nil {
		t.Fatalf("same-key restart lost credentials: %v", err)
	}
	if _, err := noKey.PrepareRevision(nil, public, mcpcmd.ValueEdits{Env: map[string]mcpcmd.ValueEdit{"SECRET": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "worker-secret"}}}); !errors.Is(err, mcpcmd.ErrCredentials) {
		t.Fatalf("protected write without key succeeded: %v", err)
	}
	if _, err := New("malformed-key-fixture"); !errors.Is(err, mcpcmd.ErrCredentials) {
		t.Fatalf("invalid key accepted: %v", err)
	}
}

func credentialTestRevision() mcpcmd.Revision {
	return mcpcmd.Revision{ConnectionID: "connection", ID: "revision-1", CreatedAt: time.Now().UTC(),
		Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://example.com/mcp", Targets: mcpcmd.Targets{All: true}}}
}
