package aliases

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
)

func TestManagedAliasValidationAndResolution(t *testing.T) {
	store := &memoryStore{records: map[string]aliascmd.Record{}}
	service := New(store)
	a := aliascmd.Authority{UserID: "admin", UserVersion: 1, CredentialVersion: 1,
		SessionID: "browser", SessionVersion: 1, At: time.Now().UTC()}
	for _, name := range []string{"owner", "collaborator", "owner@telegram", "Upper", "-bad"} {
		_, err := service.Create(t.Context(), aliascmd.Create{Name: name, LocatorRef: "telegram:1:0", Authority: a})
		if !errors.Is(err, aliascmd.ErrInvalid) {
			t.Errorf("create %q = %v, want invalid", name, err)
		}
	}
	_, err := service.Create(t.Context(), aliascmd.Create{Name: "main_chat", LocatorRef: "telegram:bad", Authority: a})
	if !errors.Is(err, aliascmd.ErrInvalid) {
		t.Fatalf("invalid locator = %v", err)
	}
	if len(store.records) != 0 {
		t.Fatalf("invalid saves changed records: %+v", store.records)
	}
	created, err := service.Create(t.Context(), aliascmd.Create{Name: "main_chat", LocatorRef: " TELEGRAM:-1003953132277:0 ", Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if created.LocatorRef != "telegram:-1003953132277:0" {
		t.Fatalf("canonical locator = %q", created.LocatorRef)
	}
	locator, err := service.Resolve(t.Context(), "main_chat")
	if err != nil || locator.AddressKey != "-1003953132277:0" {
		t.Fatalf("resolved locator = %+v, %v", locator, err)
	}
	if err := service.Delete(t.Context(), aliascmd.Delete{Name: "main_chat", ExpectedVersion: 1, Authority: a}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Resolve(t.Context(), "main_chat")
	if !errors.Is(err, aliascmd.ErrNotFound) {
		t.Fatalf("deleted alias resolution = %v", err)
	}
}

type memoryStore struct{ records map[string]aliascmd.Record }

func (s *memoryStore) CheckAuthority(context.Context, aliascmd.Authority) error { return nil }
func (s *memoryStore) Get(_ context.Context, name string) (aliascmd.Record, bool, error) {
	r, ok := s.records[name]
	return r, ok, nil
}
func (s *memoryStore) List(context.Context) ([]aliascmd.Record, error) { return nil, nil }
func (s *memoryStore) Save(_ context.Context, m aliascmd.Mutation) error {
	switch m.Kind {
	case aliascmd.MutationCreate, aliascmd.MutationRetarget:
		s.records[m.Record.Name] = m.Record
	case aliascmd.MutationDelete:
		delete(s.records, m.Record.Name)
	}
	return nil
}
