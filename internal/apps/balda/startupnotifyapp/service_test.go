package startupnotifyapp

import (
	"context"
	"errors"
	"reflect"
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
		{ChannelType: "telegram", Resolver: telegram},
		{ChannelType: "slackagent", Resolver: slack},
	}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Notify(t.Context()); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	wantTargets := []string{"slackagent:U2", "telegram:2", "telegram:9"}
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
	service, err := New(reader, sender, []ResolverBinding{{ChannelType: "telegram", Resolver: resolver}}, zerolog.Nop())
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

func TestNotifyWithNoEligibleBindingsIsNoOp(t *testing.T) {
	resolver := &fakeResolver{channelType: "telegram", errors: map[string]error{}}
	sender := &fakeSender{errors: map[string]error{}}
	service, err := New(primaryReader(nil), sender, []ResolverBinding{{ChannelType: "telegram", Resolver: resolver}}, zerolog.Nop())
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

func TestNotifyUserReadFailureDoesNotFailStartup(t *testing.T) {
	reader := &fakeUserReader{err: errors.New("database unavailable")}
	service, err := New(reader, &fakeSender{}, []ResolverBinding{{
		ChannelType: "telegram", Resolver: &fakeResolver{channelType: "telegram"},
	}}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Notify(t.Context()); err != nil {
		t.Fatalf("Notify() error = %v, want best-effort success", err)
	}
}

func primaryReader(bindings []usercmd.Binding) *fakeUserReader {
	return &fakeUserReader{pages: map[string]usercmd.UserPage{"": {Users: []usercmd.User{{
		ID: "admin", Primary: true, Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Bindings: bindings,
	}}}}}
}
