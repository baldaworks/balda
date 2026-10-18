package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/skillcliapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
)

type fakeSkillCLI struct {
	inventory skillcliapp.Inventory
	entry     skillcliapp.Entry
	result    skillcliapp.Result
	err       error
	called    string
	review    skillcliapp.Review
}

func (f *fakeSkillCLI) List(context.Context) (skillcliapp.Inventory, error) {
	f.called = "list"
	return f.inventory, f.err
}

func (f *fakeSkillCLI) Show(context.Context, string) (skillcliapp.Entry, error) {
	f.called = "show"
	return f.entry, f.err
}

func (f *fakeSkillCLI) Install(_ context.Context, _ skillcliapp.InstallRequest, confirm skillcliapp.Confirm) (skillcliapp.Result, error) {
	f.called = "install"
	return f.confirm(confirm)
}

func (f *fakeSkillCLI) Update(_ context.Context, _ string, confirm skillcliapp.Confirm) (skillcliapp.Result, error) {
	f.called = "update"
	return f.confirm(confirm)
}

func (f *fakeSkillCLI) Change(_ context.Context, operation skillcmd.Operation, _ string, confirm skillcliapp.Confirm) (skillcliapp.Result, error) {
	f.called = string(operation)
	return f.confirm(confirm)
}

func (f *fakeSkillCLI) Refresh(_ context.Context, confirm skillcliapp.Confirm) (skillcliapp.Result, error) {
	f.called = "refresh"
	return f.confirm(confirm)
}

func (f *fakeSkillCLI) Recover(context.Context) (skillcliapp.Result, error) {
	f.called = "recover"
	return f.result, f.err
}

func (f *fakeSkillCLI) confirm(confirm skillcliapp.Confirm) (skillcliapp.Result, error) {
	if f.err != nil {
		return skillcliapp.Result{}, f.err
	}
	ok, err := confirm(f.review)
	if err != nil {
		return skillcliapp.Result{}, &skillcliapp.Error{Code: skillcliapp.CodeConfirmationRequired}
	}
	if !ok {
		return skillcliapp.Result{}, &skillcliapp.Error{Code: skillcliapp.CodeCancelled}
	}
	return f.result, nil
}

func executeSkillCommand(t *testing.T, fake skillCLI, args ...string) (string, string, error) {
	t.Helper()
	original := prepareSkillCLI
	prepareSkillCLI = func(context.Context, bool) (skillCLI, func(), error) {
		return fake, func() {}, nil
	}
	t.Cleanup(func() { prepareSkillCLI = original })
	cmd := skillsCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	_, err := cmd.ExecuteC()
	return stdout.String(), stderr.String(), err
}

func TestSkillsCommandShape(t *testing.T) {
	cmd := skillsCommand()
	want := map[string]bool{"list": true, "show": true, "install": true, "update": true, "enable": true, "disable": true, "remove": true, "refresh": true, "recover": true}
	for _, child := range cmd.Commands() {
		delete(want, child.Name())
	}
	if len(want) != 0 {
		t.Fatalf("missing commands: %v", want)
	}
}

func TestSkillsListJSON(t *testing.T) {
	fake := &fakeSkillCLI{inventory: skillcliapp.Inventory{Generation: 4, Entries: []skillcliapp.Entry{{ID: "user-skill/managed-one", Name: "one", Ownership: skillcmd.OwnershipManaged}}}}
	stdout, stderr, err := executeSkillCommand(t, fake, "list", "--json")
	if err != nil || stderr != "" || fake.called != "list" {
		t.Fatalf("stdout=%q stderr=%q err=%v called=%q", stdout, stderr, err, fake.called)
	}
	var envelope skillcliapp.Envelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || !envelope.OK || envelope.Inventory == nil || envelope.Inventory.Generation != 4 {
		t.Fatalf("envelope=%+v err=%v", envelope, err)
	}
}

func TestSkillsJSONMutationRequiresYes(t *testing.T) {
	fake := &fakeSkillCLI{review: skillcliapp.Review{Operation: "install", Impact: "accept"}}
	stdout, stderr, err := executeSkillCommand(t, fake, "install", "https://example.test/skill.git", "--json")
	if err == nil || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	var envelope skillcliapp.Envelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != skillcliapp.CodeConfirmationRequired {
		t.Fatalf("envelope=%+v decode=%v raw=%q", envelope, decodeErr, stdout)
	}
}

func TestSkillsMutationJSONIncludesReviewAndOutcome(t *testing.T) {
	fake := &fakeSkillCLI{
		review: skillcliapp.Review{Operation: "disable", Entry: skillcliapp.Entry{ID: "user-skill/managed-one", Name: "one"}, Impact: "disable managed skill"},
		result: skillcliapp.Result{Operation: "disable", Accepted: true, Published: true, Generation: 5, RestartRequired: true, ResetRequired: true},
	}
	stdout, stderr, err := executeSkillCommand(t, fake, "disable", "one", "--yes", "--json")
	if err != nil || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	var envelope skillcliapp.Envelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil || !envelope.OK || envelope.Review == nil || envelope.Result == nil || !envelope.Result.Published {
		t.Fatalf("envelope=%+v decode=%v", envelope, decodeErr)
	}
}

func TestSkillsPendingMutationReturnsResultAndNonZero(t *testing.T) {
	fake := &fakeSkillCLI{
		review: skillcliapp.Review{Operation: "update", Entry: skillcliapp.Entry{ID: "user-skill/managed-one", Name: "one"}, Impact: "accept reviewed skill revision"},
		result: skillcliapp.Result{Operation: "update", Accepted: true, Pending: true, Generation: 6, RestartRequired: true, ResetRequired: true},
	}
	stdout, stderr, err := executeSkillCommand(t, fake, "update", "one", "--yes", "--json")
	if err == nil || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	var envelope skillcliapp.Envelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil || envelope.OK || envelope.Error == nil || envelope.Error.Code != skillcliapp.CodePublicationPending || envelope.Result == nil || !envelope.Result.Accepted || !envelope.Result.Pending {
		t.Fatalf("envelope=%+v decode=%v", envelope, decodeErr)
	}
	var exitCoder interface{ ExitCode() int }
	if !errors.As(err, &exitCoder) || exitCoder.ExitCode() == 0 {
		t.Fatalf("pending exit=%v", err)
	}
}

func TestSkillsPlainNonInteractiveMutationRequiresYes(t *testing.T) {
	fake := &fakeSkillCLI{review: skillcliapp.Review{Operation: "remove", Impact: "remove"}}
	stdout, _, err := executeSkillCommand(t, fake, "remove", "one")
	var public *skillcliapp.Error
	if !errors.As(err, &public) || public.Code != skillcliapp.CodeConfirmationRequired {
		t.Fatalf("stdout=%q err=%v", stdout, err)
	}
	if !strings.Contains(stdout, "Operation: remove") {
		t.Fatalf("review not rendered: %q", stdout)
	}
}

func TestSkillsPrepareFailureUsesSafeJSON(t *testing.T) {
	original := prepareSkillCLI
	prepareSkillCLI = func(context.Context, bool) (skillCLI, func(), error) {
		return nil, nil, errors.New("password=hunter2 /protected/path")
	}
	t.Cleanup(func() { prepareSkillCLI = original })
	cmd := skillsCommand()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"list", "--json"})
	_, err := cmd.ExecuteC()
	if err == nil || stderr.String() != "" || strings.Contains(stdout.String(), "hunter2") || strings.Contains(stdout.String(), "/protected") {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout.String(), stderr.String(), err)
	}
	var envelope skillcliapp.Envelope
	if decodeErr := json.Unmarshal(stdout.Bytes(), &envelope); decodeErr != nil || envelope.Error == nil || envelope.Error.Code != skillcliapp.CodeUnavailable {
		t.Fatalf("envelope=%+v decode=%v", envelope, decodeErr)
	}
}

func TestSkillsArgumentFailureUsesJSONEnvelope(t *testing.T) {
	stdout, stderr, err := executeSkillCommand(t, &fakeSkillCLI{}, "show", "--json")
	if err == nil || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	var envelope skillcliapp.Envelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil || envelope.Error == nil || envelope.Error.Code != skillcliapp.CodeInvalid {
		t.Fatalf("envelope=%+v decode=%v raw=%q", envelope, decodeErr, stdout)
	}
}
