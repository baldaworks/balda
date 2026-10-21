package startupnotifyapp

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/rs/zerolog"
)

type fakeUserReader struct {
	pages map[string]usercmd.UserPage
	err   error
	calls int
}

func (r *fakeUserReader) ListUsers(_ context.Context, page usercmd.PageRequest) (usercmd.UserPage, error) {
	r.calls++
	if r.err != nil {
		return usercmd.UserPage{}, r.err
	}
	return r.pages[page.AfterID], nil
}

type fakeResolver struct {
	channelType string
	errors      map[string]error
	principals  []string
}

func (r *fakeResolver) ResolveDM(_ context.Context, binding usercmd.Binding) (deliverycmd.Locator, error) {
	r.principals = append(r.principals, binding.Principal)
	if err := r.errors[binding.Principal]; err != nil {
		return deliverycmd.Locator{}, err
	}
	return deliverycmd.Locator{
		ChannelType: r.channelType,
		AddressKey:  binding.Principal,
		AddressJSON: `{}`,
		SessionID:   r.channelType + "-" + binding.Principal,
	}, nil
}

type fakeSender struct {
	errors  map[string]error
	targets []string
	texts   []string
}

func (s *fakeSender) SendPlain(_ context.Context, locator deliverycmd.Locator, text string) error {
	target := locator.ChannelType + ":" + locator.AddressKey
	s.targets = append(s.targets, target)
	s.texts = append(s.texts, text)
	return s.errors[target]
}

func TestNotifySelectsEligibleBindingsDeterministically(t *testing.T) {
	telegram := &fakeResolver{channelType: "telegram", errors: map[string]error{}}
	slack := &fakeResolver{channelType: "slackagent", errors: map[string]error{}}
	sender := &fakeSender{errors: map[string]error{}}
	reader := &fakeUserReader{pages: map[string]usercmd.UserPage{
		"": {
			Users:       []usercmd.User{{ID: "operator", Role: usercmd.RoleOperator, Status: usercmd.StatusActive}},
			NextAfterID: "operator",
		},
		"operator": {Users: []usercmd.User{{
			ID: "admin", Primary: true, Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive,
			Bindings: []usercmd.Binding{
				{ID: "b4", ChannelType: "telegram", Principal: "9"},
				{ID: "b1", ChannelType: " SlackAgent ", Principal: " U2 "},
				{ID: "b3", ChannelType: "telegram", Principal: "2"},
				{ID: "duplicate", ChannelType: "TELEGRAM", Principal: "2"},
				{ID: "unsupported", ChannelType: "zulip", Principal: "5"},
				{ID: "empty", ChannelType: "telegram"},
			},
		}}},
	}}
	service, err := New(reader, sender, []ResolverBinding{
		{ChannelType: "telegram", Enabled: true, Resolver: telegram},
		{ChannelType: "slackagent", Enabled: true, Resolver: slack},
	}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Notify(t.Context()); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	wantTargets := []string{"slackagent:U2", "telegram:2", "telegram:2", "telegram:9"}
	if !reflect.DeepEqual(sender.targets, wantTargets) {
		t.Fatalf("delivery targets = %v, want %v", sender.targets, wantTargets)
	}
	if reader.calls != 2 {
		t.Fatalf("ListUsers() calls = %d, want 2", reader.calls)
	}
	for _, text := range sender.texts {
		if text != ReadyMessage {
			t.Errorf("message = %q, want %q", text, ReadyMessage)
		}
	}
}

func TestNotifyIsolatesResolverAndDeliveryFailures(t *testing.T) {
	resolver := &fakeResolver{
		channelType: "telegram",
		errors:      map[string]error{"1": errors.New("resolve failed")},
	}
	sender := &fakeSender{errors: map[string]error{"telegram:2": errors.New("send failed")}}
	reader := primaryReader([]usercmd.Binding{
		{ID: "one", ChannelType: "telegram", Principal: "1"},
		{ID: "two", ChannelType: "telegram", Principal: "2"},
		{ID: "three", ChannelType: "telegram", Principal: "3"},
	})
	service, err := New(reader, sender, []ResolverBinding{{ChannelType: "telegram", Enabled: true, Resolver: resolver}}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Notify(t.Context()); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if want := []string{"telegram:2", "telegram:3"}; !reflect.DeepEqual(sender.targets, want) {
		t.Fatalf("delivery targets = %v, want %v", sender.targets, want)
	}
}

func TestNotifyLogsBoundedOutcomes(t *testing.T) {
	var logs bytes.Buffer
	resolver := &fakeResolver{
		channelType: "telegram",
		errors:      map[string]error{"1": errors.New("secret resolver response")},
	}
	sender := &fakeSender{errors: map[string]error{"telegram:2": errors.New("secret send response")}}
	reader := primaryReader([]usercmd.Binding{
		{ID: "resolve", ChannelType: "telegram", Principal: "1"},
		{ID: "send", ChannelType: "telegram", Principal: "2"},
		{ID: "sent", ChannelType: "telegram", Principal: "3"},
		{ID: "disabled", ChannelType: "zulip", Principal: "4"},
	})
	service, err := New(reader, sender, []ResolverBinding{{
		ChannelType: "telegram", Enabled: true, Resolver: resolver,
	}}, zerolog.New(&logs))
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Notify(t.Context()); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	got := logs.String()
	for _, field := range []string{
		`"outcome":"failed","reason":"resolve_failed"`,
		`"outcome":"failed","reason":"send_failed"`,
		`"outcome":"sent"`,
		`"outcome":"skipped","reason":"integration_disabled"`,
	} {
		if !strings.Contains(got, field) {
			t.Errorf("logs missing %s: %s", field, got)
		}
	}
	if strings.Contains(got, "secret") || strings.Contains(got, ReadyMessage) {
		t.Fatalf("logs contain upstream error or message body: %s", got)
	}
}

func TestNotifyWithNoEligibleBindingsIsNoOp(t *testing.T) {
	resolver := &fakeResolver{channelType: "telegram", errors: map[string]error{}}
	sender := &fakeSender{errors: map[string]error{}}
	service, err := New(primaryReader(nil), sender, []ResolverBinding{{ChannelType: "telegram", Enabled: true, Resolver: resolver}}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Notify(t.Context()); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if len(resolver.principals) != 0 || len(sender.targets) != 0 {
		t.Fatalf("no-op resolved %v and sent %v", resolver.principals, sender.targets)
	}
}

func TestNotifyUserReadFailureFailsStartup(t *testing.T) {
	reader := &fakeUserReader{err: errors.New("database unavailable")}
	service, err := New(reader, &fakeSender{}, []ResolverBinding{{
		ChannelType: "telegram", Enabled: true, Resolver: &fakeResolver{channelType: "telegram"},
	}}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Notify(t.Context()); !errors.Is(err, reader.err) {
		t.Fatalf("Notify() error = %v, want %v", err, reader.err)
	}
}

func TestNewRejectsMissingMandatoryWiring(t *testing.T) {
	if _, err := New(nil, &fakeSender{}, nil, zerolog.Nop()); err == nil {
		t.Fatal("New() error = nil with no user reader")
	}
	if _, err := New(primaryReader(nil), nil, nil, zerolog.Nop()); err == nil {
		t.Fatal("New() error = nil with no sender")
	}
}

func TestDisabledResolverIsSkipped(t *testing.T) {
	resolver := &fakeResolver{channelType: "telegram", errors: map[string]error{}}
	sender := &fakeSender{errors: map[string]error{}}
	service, err := New(primaryReader([]usercmd.Binding{{
		ID: "telegram", ChannelType: "telegram", Principal: "1",
	}}), sender, []ResolverBinding{{ChannelType: "telegram", Resolver: resolver}}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Notify(t.Context()); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if len(resolver.principals) != 0 || len(sender.targets) != 0 {
		t.Fatalf("disabled resolver resolved %v and sent %v", resolver.principals, sender.targets)
	}
}

func primaryReader(bindings []usercmd.Binding) *fakeUserReader {
	return &fakeUserReader{pages: map[string]usercmd.UserPage{"": {Users: []usercmd.User{{
		ID: "admin", Primary: true, Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Bindings: bindings,
	}}}}}
}
