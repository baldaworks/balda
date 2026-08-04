package deliveryfmt

import (
	"errors"
	"fmt"
	"strings"
)

// ErrRouteNotFound reports that a transport delivery format is not registered.
var ErrRouteNotFound = errors.New("delivery format route not found")

// Name identifies a registered application message format.
type Name string

// DeliveryFormat identifies an opaque, transport-owned delivery capability.
type DeliveryFormat string

// Format contains application-owned instructions for producing a message.
type Format struct {
	Name         Name
	Instructions string
	Example      string
}

// Message is formatted content tagged with its registered application name.
// PlainFallback is safe to send without provider formatting when an explicit
// formatting rejection permits one fallback attempt.
type Message struct {
	Name          Name
	Text          string
	PlainFallback string
}

// Formatter converts assistant text into a registered message representation.
type Formatter interface {
	Name() Name
	Format(text string) (Message, error)
}

// FormatterRegistration binds a formatter implementation to a registry name.
type FormatterRegistration struct {
	Name      Name
	Formatter Formatter
}

// Route maps a transport-owned delivery format to an application registry name.
type Route struct {
	Transport      string
	DeliveryFormat DeliveryFormat
	RegisteredName Name
}

type routeKey struct {
	transport      string
	deliveryFormat DeliveryFormat
}

// Registry is an immutable collection of formats, formatters, and transport routes.
type Registry struct {
	formats    map[Name]Format
	formatters map[Name]Formatter
	routes     map[routeKey]Name
}

// NewRegistry validates and constructs an immutable registry.
func NewRegistry(
	formats []Format,
	formatters []FormatterRegistration,
	routes []Route,
) (*Registry, error) {
	registry := &Registry{
		formats:    make(map[Name]Format, len(formats)),
		formatters: make(map[Name]Formatter, len(formatters)),
		routes:     make(map[routeKey]Name, len(routes)),
	}

	for _, format := range formats {
		if err := validateName(format.Name); err != nil {
			return nil, fmt.Errorf("register format: %w", err)
		}
		if strings.TrimSpace(format.Instructions) == "" {
			return nil, fmt.Errorf("register format %q: instructions are required", format.Name)
		}
		if strings.TrimSpace(format.Example) == "" {
			return nil, fmt.Errorf("register format %q: example is required", format.Name)
		}
		if _, ok := registry.formats[format.Name]; ok {
			return nil, fmt.Errorf("register format %q: duplicate name", format.Name)
		}
		registry.formats[format.Name] = format
	}

	for _, registration := range formatters {
		if registration.Formatter == nil {
			return nil, fmt.Errorf("register formatter: formatter is required")
		}
		if err := validateName(registration.Name); err != nil {
			return nil, fmt.Errorf("register formatter: %w", err)
		}
		if registration.Formatter.Name() != registration.Name {
			return nil, fmt.Errorf(
				"register formatter %q: implementation name %q does not match registration",
				registration.Name,
				registration.Formatter.Name(),
			)
		}
		if _, ok := registry.formatters[registration.Name]; ok {
			return nil, fmt.Errorf("register formatter %q: duplicate name", registration.Name)
		}
		registry.formatters[registration.Name] = registration.Formatter
	}

	for _, route := range routes {
		if err := validateIdentifier("transport", route.Transport); err != nil {
			return nil, fmt.Errorf("register route: %w", err)
		}
		if err := validateIdentifier("delivery format", string(route.DeliveryFormat)); err != nil {
			return nil, fmt.Errorf("register route for transport %q: %w", route.Transport, err)
		}
		if err := validateName(route.RegisteredName); err != nil {
			return nil, fmt.Errorf(
				"register route %s/%s: %w",
				route.Transport,
				route.DeliveryFormat,
				err,
			)
		}
		if _, ok := registry.formats[route.RegisteredName]; !ok {
			return nil, fmt.Errorf(
				"register route %s/%s: format %q is not registered",
				route.Transport,
				route.DeliveryFormat,
				route.RegisteredName,
			)
		}
		if _, ok := registry.formatters[route.RegisteredName]; !ok {
			return nil, fmt.Errorf(
				"register route %s/%s: formatter %q is not registered",
				route.Transport,
				route.DeliveryFormat,
				route.RegisteredName,
			)
		}
		key := routeKey{transport: route.Transport, deliveryFormat: route.DeliveryFormat}
		if _, ok := registry.routes[key]; ok {
			return nil, fmt.Errorf(
				"register route %s/%s: duplicate route",
				route.Transport,
				route.DeliveryFormat,
			)
		}
		registry.routes[key] = route.RegisteredName
	}

	return registry, nil
}

// Resolve returns the registered name, prompt format, and delivery formatter for a route.
func (r *Registry) Resolve(
	transport string,
	deliveryFormat DeliveryFormat,
) (Name, Format, Formatter, error) {
	if r == nil {
		return "", Format{}, nil, fmt.Errorf("resolve delivery format: registry is required")
	}

	name, ok := r.routes[routeKey{transport: transport, deliveryFormat: deliveryFormat}]
	if !ok {
		return "", Format{}, nil, fmt.Errorf(
			"%w: transport %q delivery format %q",
			ErrRouteNotFound,
			transport,
			deliveryFormat,
		)
	}

	return name, r.formats[name], r.formatters[name], nil
}

func validateName(name Name) error {
	return validateIdentifier("registered name", string(name))
}

func validateIdentifier(kind, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", kind)
	}
	if strings.TrimSpace(value) != value || strings.ToLower(value) != value {
		return fmt.Errorf("%s %q must be normalized", kind, value)
	}
	for i, r := range value {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if i > 0 && (r >= '0' && r <= '9' || r == '_' || r == '-') {
			continue
		}
		return fmt.Errorf("%s %q is malformed", kind, value)
	}
	return nil
}
