package skillapp

import (
	"context"
	"errors"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
)

func TestNewRequiresPorts(t *testing.T) {
	if _, err := New("", nil, nil); err != skillcmd.ErrUnavailable {
		t.Fatalf("New(nil,nil) = %v", err)
	}
}

func TestNewReadOnlyRejectsMutationWithoutStaging(t *testing.T) {
	service, err := NewReadOnly(&unusedStore{}, &unusedCatalog{})
	if err != nil {
		t.Fatal(err)
	}
	if service.stagingDir != "" || !service.readOnly {
		t.Fatalf("read-only service=%+v", service)
	}
	if _, err := service.ReviewInstall(t.Context(), InstallRequest{Source: "/private/source"}); err != skillcmd.ErrForbidden {
		t.Fatalf("ReviewInstall() = %v", err)
	}
	if _, err := service.RetryPublication(t.Context(), skillcmd.NewCLIAuthority(time.Now())); err != skillcmd.ErrForbidden {
		t.Fatalf("RetryPublication() = %v", err)
	}
}

func TestReviewRejectsUnsafePathBeforeFetching(t *testing.T) {
	service := &Service{}
	_, err := service.ReviewInstall(context.Background(), InstallRequest{Kind: "local", Source: "/secret", Path: "../escape"})
	if err != skillcmd.ErrInvalid {
		t.Fatalf("ReviewInstall unsafe path = %v", err)
	}
}

func TestCandidateExpiryAndSafeOriginProjection(t *testing.T) {
	now := time.Now()
	service := &Service{now: func() time.Time { return now }, candidates: map[string]reviewed{"expired": {candidate: skillcmd.Candidate{ExpiresAt: now}}}}
	if _, err := service.Accept(t.Context(), "expired", skillcmd.Authority{}); err != skillcmd.ErrConflict {
		t.Fatalf("expired review=%v", err)
	}
	label := originLabel(skillcmd.Origin{Kind: "git", Source: "https://user:password-canary@example.test/repo?token=query-canary#fragment-canary"})
	if label != "Git: https://example.test/repo" {
		t.Fatalf("safe origin=%q", label)
	}
	if label := originLabel(skillcmd.Origin{Kind: "local", Source: "/private/path-canary"}); strings.Contains(label, "path-canary") {
		t.Fatalf("local origin leaked: %s", label)
	}
}

func TestOriginInputPolicy(t *testing.T) {
	for _, request := range []InstallRequest{
		{Source: "/tmp", Path: "../escape"}, {Source: "/tmp", Path: "/absolute"}, {Source: "/tmp", Path: "a/../b"}, {Source: "/tmp", Path: "a\\b"}, {Source: "/tmp", Ref: "main"}, {Source: "https://example.test/repo", Ref: "-option"}, {Source: "ext::command", Kind: "git"}, {Source: "/tmp\x00secret"},
	} {
		if _, err := normalizeOrigin(request); err != skillcmd.ErrInvalid {
			t.Errorf("unsafe input %#v=%v", request, err)
		}
	}
}

func TestStartupCleansOnlyOwnedTransientSources(t *testing.T) {
	stateDir := t.TempDir()
	stage := filepath.Join(stateDir, "skill-staging")
	owned := filepath.Join(stage, "source-"+uuid.NewString())
	preserved := filepath.Join(stage, "unrelated")
	for _, directory := range []string{owned, preserved} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(stateDir, &unusedStore{}, &unusedCatalog{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transient remains: %v", err)
	}
	if _, err := os.Stat(preserved); err != nil {
		t.Fatalf("unrelated staging removed: %v", err)
	}
	info, err := os.Stat(stage)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("staging permissions=%v,%v", info, err)
	}
	symlinkState := t.TempDir()
	if err := os.Symlink(stage, filepath.Join(symlinkState, "skill-staging")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(symlinkState, &unusedStore{}, &unusedCatalog{}); err != skillcmd.ErrUnavailable {
		t.Fatalf("symlink staging=%v", err)
	}
}

type unusedStore struct{ ManagementStore }
type unusedCatalog struct{ Catalog }

func TestReviewCacheBoundsAndExpiresWithoutChangingSelection(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	service := &Service{catalog: &metadataCatalog{}, candidates: make(map[string]reviewed), now: func() time.Time { return now }}
	installation := skillcmd.Installation{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "managed-cache"}}, Origin: skillcmd.Origin{Kind: sourceLocal, Source: root, Path: "."}}
	var first skillcmd.Candidate
	for index := range 128 {
		candidate, err := service.review(t.Context(), skillcmd.Observation{}, installation, skillcmd.Authority{}, skillcmd.OperationInstall)
		if err != nil {
			t.Fatalf("review%d=%v", index, err)
		}
		if index == 0 {
			first = candidate
		}
	}
	if _, err := service.review(t.Context(), skillcmd.Observation{}, installation, skillcmd.Authority{}, skillcmd.OperationInstall); err != skillcmd.ErrUnavailable {
		t.Fatalf("unbounded review cache=%v", err)
	}
	now = now.Add(11 * time.Minute)
	if _, err := service.Accept(t.Context(), first.Handle, skillcmd.Authority{}); err != skillcmd.ErrConflict {
		t.Fatalf("expired live review=%v", err)
	}
	if _, err := service.review(t.Context(), skillcmd.Observation{}, installation, skillcmd.Authority{}, skillcmd.OperationInstall); err != nil {
		t.Fatalf("expired slots not reclaimed: %v", err)
	}
}

type metadataCatalog struct{ Catalog }

func (c *metadataCatalog) CaptureCandidate(_ context.Context, _ *os.Root, _ string, id runtimecatalogcmd.SourceID) (runtimecatalogcmd.Source, error) {
	return runtimecatalogcmd.Source{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: id, Revision: "immutable-revision"}, Skills: []runtimecatalogcmd.SkillMetadata{{Name: "selected", Description: "metadata only"}}}, nil
}
