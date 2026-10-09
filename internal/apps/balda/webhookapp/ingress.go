package webhookapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	"github.com/baldaworks/balda/internal/apps/balda/destinationcmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

// ConfiguredRoute supplies a config route and its auth value.
// Disabled routes can be selected for administrator tests but not external POSTs.
type ConfiguredRoute struct {
	Name, Path, PromptTemplate string
	Disabled                   bool
	ReportToKind, ReportToKey  string
	AckOnDelivery              bool
	AuthType, AuthHeader       string
	AuthValue                  string
	DedupeSource, DedupeHeader string
}

// RouteStore reads one current managed definition for each new request.
type RouteStore interface {
	LookupByPath(ctx context.Context, path string) (webhookroutecmd.Record, bool, error)
	Get(ctx context.Context, name string) (webhookroutecmd.Record, bool, error)
}

// Acceptor durably admits one rendered request.
type Acceptor interface {
	Accept(ctx context.Context, request webhookcmd.Request) (webhookcmd.Result, error)
}

type configuredRoute struct {
	prepared  webhookcmd.PreparedRoute
	authType  string
	authValue string
}

// Ingress owns route selection, auth, prompt rendering, and dedupe policy.
type Ingress struct {
	configuredByPath map[string]configuredRoute
	configuredByName map[string]configuredRoute
	store            RouteStore
	acceptor         Acceptor
}

// NewIngress compiles immutable config routes and attaches the live managed store.
func NewIngress(configured []ConfiguredRoute, store RouteStore, acceptor Acceptor) (*Ingress, error) {
	i := &Ingress{configuredByPath: make(map[string]configuredRoute, len(configured)),
		configuredByName: make(map[string]configuredRoute, len(configured)), store: store, acceptor: acceptor}
	if acceptor == nil {
		return nil, fmt.Errorf("webhook admission service is required")
	}
	for _, c := range configured {
		if c.Name == "" || c.Path == "" || c.PromptTemplate == "" {
			if c.Disabled {
				continue
			}
			return nil, fmt.Errorf("invalid configured webhook route %q", c.Name)
		}
		if _, exists := i.configuredByPath[c.Path]; exists && !c.Disabled {
			return nil, fmt.Errorf("duplicate configured webhook path %q", c.Path)
		}
		prepared, err := prepareRoute(c.Name, c.Path, c.PromptTemplate, c.ReportToKind,
			c.ReportToKey, c.AckOnDelivery, c.DedupeSource, c.DedupeHeader, c.AuthHeader)
		if err != nil {
			if c.Disabled {
				continue
			}
			return nil, err
		}
		route := configuredRoute{prepared: prepared, authType: c.AuthType, authValue: c.AuthValue}
		i.configuredByName[c.Name] = route
		if !c.Disabled {
			i.configuredByPath[c.Path] = route
		}
	}
	return i, nil
}

// PrepareExternal authenticates the current route before its body is read.
func (i *Ingress) PrepareExternal(ctx context.Context, path string, headers map[string]string) (webhookcmd.PreparedRoute, error) {
	if route, ok := i.configuredByPath[path]; ok {
		if route.authType == webhookroutecmd.AuthTypeHeader {
			if value := headerValue(headers, route.prepared.AuthHeader); value == "" ||
				subtle.ConstantTimeCompare([]byte(value), []byte(route.authValue)) != 1 {
				return webhookcmd.PreparedRoute{}, webhookcmd.ErrUnauthorized
			}
		}
		return route.prepared, nil
	}
	if i.store == nil {
		return webhookcmd.PreparedRoute{}, webhookcmd.ErrRouteNotFound
	}
	r, found, err := i.store.LookupByPath(ctx, path)
	if err != nil {
		return webhookcmd.PreparedRoute{}, &webhookcmd.DispatchFailedError{Cause: err}
	}
	if !found || r.Source != webhookroutecmd.SourceManaged || !r.Enabled || r.Deleted {
		return webhookcmd.PreparedRoute{}, webhookcmd.ErrRouteNotFound
	}
	if !verifyManagedSecret(headerValue(headers, webhookroutecmd.ManagedSecretHeader), r.SecretVerifier) {
		return webhookcmd.PreparedRoute{}, webhookcmd.ErrUnauthorized
	}
	return preparedRecord(r)
}

// PrepareTest selects a route by name for administrator-authorized test input.
// A disabled route may be tested only after the caller's explicit confirmation.
func (i *Ingress) PrepareTest(ctx context.Context, name string) (webhookcmd.PreparedRoute, error) {
	if route, ok := i.configuredByName[name]; ok {
		return route.prepared, nil
	}
	if i.store == nil {
		return webhookcmd.PreparedRoute{}, webhookcmd.ErrRouteNotFound
	}
	r, found, err := i.store.Get(ctx, name)
	if err != nil {
		return webhookcmd.PreparedRoute{}, &webhookcmd.DispatchFailedError{Cause: err}
	}
	if !found || r.Deleted || r.Source != webhookroutecmd.SourceManaged {
		return webhookcmd.PreparedRoute{}, webhookcmd.ErrRouteNotFound
	}
	return preparedRecord(r)
}

// Admit renders the authorized immutable snapshot and uses durable admission.
func (i *Ingress) Admit(ctx context.Context, route webhookcmd.PreparedRoute, inbound webhookcmd.Inbound) (webhookcmd.Result, error) {
	if len(inbound.RawBody) > webhookcmd.MaxBodyBytes || route.PromptTemplate == nil ||
		inbound.Path != route.Path || inbound.Method != http.MethodPost {
		return webhookcmd.Result{}, &webhookcmd.InvalidRequestError{Field: "webhook", Message: "invalid body or route"}
	}
	headers := make(map[string]string, len(inbound.Headers))
	for name, value := range inbound.Headers {
		if credentialHeader(name, route.AuthHeader) {
			continue
		}
		headers[name] = value
	}
	var prompt boundedPromptBuffer
	err := route.PromptTemplate.Execute(&prompt, templateData{RequestID: inbound.RequestID,
		Path: inbound.Path, Method: inbound.Method, RawBody: inbound.RawBody, Headers: headers})
	if err != nil || strings.TrimSpace(prompt.String()) == "" {
		return webhookcmd.Result{}, &webhookcmd.InvalidRequestError{Field: "prompt", Message: "template did not render a valid prompt"}
	}
	dedupeBase := strings.TrimSpace(inbound.RequestID)
	switch route.DedupeSource {
	case webhookroutecmd.DedupeSourceHeader:
		if value := strings.TrimSpace(headerValue(inbound.Headers, route.DedupeHeader)); value != "" {
			if credentialHeader(route.DedupeHeader, route.AuthHeader) {
				sum := sha256.Sum256([]byte(value))
				dedupeBase = hex.EncodeToString(sum[:])
			} else {
				dedupeBase = value
			}
		}
	case webhookroutecmd.DedupeSourceBodySHA:
		sum := sha256.Sum256([]byte(inbound.RawBody))
		dedupeBase = hex.EncodeToString(sum[:])
	}
	keyPrefix := "webhook"
	if inbound.Test {
		keyPrefix = "webhook-test"
	}
	return i.acceptor.Accept(ctx, webhookcmd.Request{RequestID: inbound.RequestID,
		RouteName: route.Name, Prompt: strings.TrimSpace(prompt.String()), RawBody: inbound.RawBody,
		Test: inbound.Test, ReportTo: route.ReportTo,
		DedupeKey: strings.Join([]string{keyPrefix, route.Name, dedupeBase}, ":")})
}

func preparedRecord(r webhookroutecmd.Record) (webhookcmd.PreparedRoute, error) {
	prepared, err := prepareRoute(r.Name, r.Path, r.PromptTemplate, r.ReportToKind,
		r.ReportToKey, r.AckOnDelivery, r.DedupeSource, r.DedupeHeader, r.AuthHeader)
	if err != nil {
		return webhookcmd.PreparedRoute{}, &webhookcmd.DispatchFailedError{Cause: err}
	}
	return prepared, nil
}

func prepareRoute(name, path, promptText, reportKind, reportKey string, ack bool,
	dedupeSource, dedupeHeader, authHeader string) (webhookcmd.PreparedRoute, error) {
	tmpl, err := template.New("inbound_webhook." + name).Option("missingkey=error").Parse(promptText)
	if err != nil {
		return webhookcmd.PreparedRoute{}, fmt.Errorf("compile webhook route %q: %w", name, err)
	}
	var reportTo *destinationcmd.Target
	if reportKey != "" {
		reportTo = &destinationcmd.Target{Target: reportKind, Key: reportKey}
	}
	return webhookcmd.PreparedRoute{Name: name, Path: path, PromptTemplate: tmpl,
		ReportTo: reportTo, AckOnDelivery: ack, DedupeSource: dedupeSource,
		DedupeHeader: dedupeHeader, AuthHeader: authHeader}, nil
}

func verifyManagedSecret(provided, verifier string) bool {
	if provided == "" {
		return false
	}
	expected, err := hex.DecodeString(verifier)
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	actual := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(actual[:], expected) == 1
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func credentialHeader(name, routeAuth string) bool {
	return strings.EqualFold(name, routeAuth) && routeAuth != "" ||
		strings.EqualFold(name, webhookroutecmd.ManagedSecretHeader) ||
		strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization") ||
		strings.EqualFold(name, "Cookie")
}

type templateData struct {
	RequestID string
	Path      string
	Method    string
	RawBody   string
	Headers   map[string]string
}

type boundedPromptBuffer struct{ bytes.Buffer }

func (b *boundedPromptBuffer) Write(p []byte) (int, error) {
	if len(p) > webhookcmd.MaxPromptBytes-b.Len() {
		return 0, fmt.Errorf("rendered prompt exceeds %d bytes", webhookcmd.MaxPromptBytes)
	}
	return b.Buffer.Write(p)
}
