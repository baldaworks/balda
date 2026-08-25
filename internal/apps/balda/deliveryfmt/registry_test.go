package deliveryfmt

import (
	"errors"
	"strings"
	"testing"
)

type stubFormatter struct {
	name Name
}

func (f stubFormatter) Name() Name {
	return f.name
}

func (f stubFormatter) Format(text string) (Message, error) {
	return Message{Name: f.name, Text: text, PlainFallback: text}, nil
}

func TestRegistryResolve(t *testing.T) {
	t.Parallel()

	const (
		name       Name           = "telegram_rich_markdown"
		capability DeliveryFormat = "rich_markdown"
	)
	registry, err := NewRegistry(
		[]Format{{Name: name, Instructions: "Use rich Markdown.", Example: "**Hello**"}},
		[]FormatterRegistration{{Name: name, Formatter: stubFormatter{name: name}}},
		[]Route{{Transport: TransportTelegram, DeliveryFormat: capability, RegisteredName: name}},
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	gotName, gotFormat, gotFormatter, err := registry.Resolve(TransportTelegram, capability)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if gotName != name {
		t.Errorf("Resolve() name = %q, want %q", gotName, name)
	}
	if gotFormat.Name != name {
		t.Errorf("Resolve() format name = %q, want %q", gotFormat.Name, name)
	}
	if gotFormatter.Name() != name {
		t.Errorf("Resolve() formatter name = %q, want %q", gotFormatter.Name(), name)
	}
}

func TestRegistryRejectsInvalidRegistration(t *testing.T) {
	t.Parallel()

	validFormat := Format{
		Name:         NameTelegramRichMarkdown,
		Instructions: "Use rich Markdown.",
		Example:      "**Hello**",
	}
	validFormatter := FormatterRegistration{
		Name:      validFormat.Name,
		Formatter: stubFormatter{name: validFormat.Name},
	}
	validRoute := Route{
		Transport:      TransportTelegram,
		DeliveryFormat: DeliveryFormatRichMarkdown,
		RegisteredName: validFormat.Name,
	}

	tests := []struct {
		name       string
		formats    []Format
		formatters []FormatterRegistration
		routes     []Route
		wantError  string
	}{
		{
			name:       "duplicate format",
			formats:    []Format{validFormat, validFormat},
			formatters: []FormatterRegistration{validFormatter},
			routes:     []Route{validRoute},
			wantError:  "duplicate name",
		},
		{
			name:       "duplicate formatter",
			formats:    []Format{validFormat},
			formatters: []FormatterRegistration{validFormatter, validFormatter},
			routes:     []Route{validRoute},
			wantError:  "duplicate name",
		},
		{
			name:       "duplicate route",
			formats:    []Format{validFormat},
			formatters: []FormatterRegistration{validFormatter},
			routes:     []Route{validRoute, validRoute},
			wantError:  "duplicate route",
		},
		{
			name:       "missing format",
			formatters: []FormatterRegistration{validFormatter},
			routes:     []Route{validRoute},
			wantError:  "format \"telegram_rich_markdown\" is not registered",
		},
		{
			name:      "missing formatter",
			formats:   []Format{validFormat},
			routes:    []Route{validRoute},
			wantError: "formatter \"telegram_rich_markdown\" is not registered",
		},
		{
			name:       "missing formatter implementation",
			formats:    []Format{validFormat},
			formatters: []FormatterRegistration{{Name: validFormat.Name}},
			wantError:  "formatter is required",
		},
		{
			name:       "malformed registered name",
			formats:    []Format{{Name: "Telegram Rich", Instructions: "rules", Example: "example"}},
			formatters: []FormatterRegistration{validFormatter},
			wantError:  "must be normalized",
		},
		{
			name:       "unnormalized transport",
			formats:    []Format{validFormat},
			formatters: []FormatterRegistration{validFormatter},
			routes: []Route{{
				Transport:      "Telegram",
				DeliveryFormat: validRoute.DeliveryFormat,
				RegisteredName: validRoute.RegisteredName,
			}},
			wantError: "must be normalized",
		},
		{
			name:       "unnormalized delivery format",
			formats:    []Format{validFormat},
			formatters: []FormatterRegistration{validFormatter},
			routes: []Route{{
				Transport:      validRoute.Transport,
				DeliveryFormat: " rich_markdown ",
				RegisteredName: validRoute.RegisteredName,
			}},
			wantError: "must be normalized",
		},
		{
			name:       "missing instructions",
			formats:    []Format{{Name: validFormat.Name, Example: validFormat.Example}},
			formatters: []FormatterRegistration{validFormatter},
			routes:     []Route{validRoute},
			wantError:  "instructions are required",
		},
		{
			name:       "missing example",
			formats:    []Format{{Name: validFormat.Name, Instructions: validFormat.Instructions}},
			formatters: []FormatterRegistration{validFormatter},
			routes:     []Route{validRoute},
			wantError:  "example is required",
		},
		{
			name:    "formatter implementation name mismatch",
			formats: []Format{validFormat},
			formatters: []FormatterRegistration{{
				Name:      validFormat.Name,
				Formatter: stubFormatter{name: "other"},
			}},
			routes:    []Route{validRoute},
			wantError: "does not match registration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewRegistry(tt.formats, tt.formatters, tt.routes)
			if err == nil {
				t.Fatal("NewRegistry() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("NewRegistry() error = %q, want substring %q", err, tt.wantError)
			}
		})
	}
}

func TestRegistryResolveUnknownRoute(t *testing.T) {
	t.Parallel()

	registry, err := NewRegistry(nil, nil, nil)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	gotName, gotFormat, gotFormatter, err := registry.Resolve(TransportTelegram, DeliveryFormatRichMarkdown)
	if !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrRouteNotFound", err)
	}
	if gotName != "" || gotFormat != (Format{}) || gotFormatter != nil {
		t.Errorf("Resolve() results = (%q, %+v, %T), want zero values", gotName, gotFormat, gotFormatter)
	}
}

func TestBuiltinRoutes(t *testing.T) {
	t.Parallel()

	routes := BuiltinRoutes()
	want := map[routeKey]Name{
		{transport: TransportTelegram, deliveryFormat: DeliveryFormatNone}:     NamePlainText,
		{transport: TransportSlackAgent, deliveryFormat: DeliveryFormatMrkdwn}: NameSlackMrkdwn,
		{transport: TransportSlackAgent, deliveryFormat: DeliveryFormatNone}:   NamePlainText,
		{transport: TransportZulip, deliveryFormat: DeliveryFormatNone}:        NamePlainText,
	}

	if len(routes) != len(want) {
		t.Fatalf("len(BuiltinRoutes()) = %d, want %d", len(routes), len(want))
	}
	for _, route := range routes {
		key := routeKey{transport: route.Transport, deliveryFormat: route.DeliveryFormat}
		if got, ok := want[key]; !ok || got != route.RegisteredName {
			t.Errorf("BuiltinRoutes() contains unexpected route %+v", route)
		}
	}

	routes[0].Transport = "changed"
	if got := BuiltinRoutes()[0].Transport; got != TransportTelegram {
		t.Errorf("BuiltinRoutes() shared mutable state: first transport = %q", got)
	}
}

func TestBuiltinRoutesResolve(t *testing.T) {
	t.Parallel()

	routes := BuiltinRoutes()
	names := make(map[Name]struct{})
	for _, route := range routes {
		names[route.RegisteredName] = struct{}{}
	}

	formats := make([]Format, 0, len(names))
	formatters := make([]FormatterRegistration, 0, len(names))
	for name := range names {
		formats = append(formats, Format{
			Name:         name,
			Instructions: "Current format instructions.",
			Example:      "Current format example.",
		})
		formatters = append(formatters, FormatterRegistration{
			Name:      name,
			Formatter: stubFormatter{name: name},
		})
	}

	registry, err := NewRegistry(formats, formatters, routes)
	if err != nil {
		t.Fatalf("NewRegistry(current routes) error = %v", err)
	}
	for _, route := range routes {
		gotName, gotFormat, gotFormatter, err := registry.Resolve(route.Transport, route.DeliveryFormat)
		if err != nil {
			t.Errorf("Resolve(%q, %q) error = %v", route.Transport, route.DeliveryFormat, err)
			continue
		}
		if gotName != route.RegisteredName || gotFormat.Name != route.RegisteredName || gotFormatter.Name() != route.RegisteredName {
			t.Errorf(
				"Resolve(%q, %q) = (%q, %q, %q), want registered name %q",
				route.Transport,
				route.DeliveryFormat,
				gotName,
				gotFormat.Name,
				gotFormatter.Name(),
				route.RegisteredName,
			)
		}
	}
}
