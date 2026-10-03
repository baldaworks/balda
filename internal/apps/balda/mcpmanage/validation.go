package mcpmanage

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

var publicIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func validPublicID(id string) bool {
	return publicIDPattern.MatchString(id) && id != "balda"
}

func (s *Definitions) validateRevision(r mcpcmd.Revision, providers []string) error {
	d := r.Definition
	if d.Targets.All {
		if len(d.Targets.Providers) != 0 {
			return mcpcmd.ErrInvalid
		}
	} else {
		if len(d.Targets.Providers) == 0 {
			return mcpcmd.ErrInvalid
		}
		known := make(map[string]bool, len(providers))
		for _, id := range providers {
			known[id] = true
		}
		seen := make(map[string]bool, len(d.Targets.Providers))
		for _, id := range d.Targets.Providers {
			if !known[id] || seen[id] {
				return mcpcmd.ErrInvalid
			}
			seen[id] = true
		}
	}
	return s.validateTransportRevision(r)
}

func (s *Definitions) validateTransportRevision(r mcpcmd.Revision) error {
	d := r.Definition
	if len(d.Env) > 128 || len(d.Headers) > 128 || len(d.Args) > 256 || len(d.Scopes) > 64 {
		return mcpcmd.ErrInvalid
	}
	for _, arg := range d.Args {
		if len(arg) > 16<<10 || strings.ContainsRune(arg, 0) {
			return mcpcmd.ErrInvalid
		}
	}
	if !safeText(d.Command, 4096) || !safeText(d.Directory, 4096) {
		return mcpcmd.ErrInvalid
	}
	switch d.Transport {
	case mcpcmd.TransportStdio:
		if strings.TrimSpace(d.Command) == "" || d.URL != "" || len(d.Headers) != 0 || d.OAuth || len(d.Scopes) != 0 {
			return mcpcmd.ErrInvalid
		}
	case mcpcmd.TransportHTTP, mcpcmd.TransportSSE:
		if d.Command != "" || len(d.Args) != 0 || d.Directory != "" || len(d.Env) != 0 || !validRemoteURL(d.URL) {
			return mcpcmd.ErrInvalid
		}
	default:
		return mcpcmd.ErrInvalid
	}
	if !d.OAuth && len(d.Scopes) != 0 {
		return mcpcmd.ErrInvalid
	}
	for _, scope := range d.Scopes {
		if scope == "" || len(scope) > 256 {
			return mcpcmd.ErrInvalid
		}
		for _, c := range scope {
			if c < 0x21 || c > 0x7e || c == '"' || c == '\\' {
				return mcpcmd.ErrInvalid
			}
		}
	}
	values, err := s.credentials.openRevision(r)
	if err != nil {
		return err
	}
	for name, binding := range d.Env {
		if !environmentName.MatchString(name) || !validBinding(binding) {
			return mcpcmd.ErrInvalid
		}
		value := binding.Value
		if binding.Kind == mcpcmd.ValueProtected {
			value = values.Env[name]
		}
		if strings.ContainsRune(value, 0) {
			return mcpcmd.ErrInvalid
		}
	}
	seenHeaders := make(map[string]bool, len(d.Headers))
	for name, binding := range d.Headers {
		canonical := http.CanonicalHeaderKey(name)
		if !validHeaderName(name) || seenHeaders[canonical] || !validBinding(binding) {
			return mcpcmd.ErrInvalid
		}
		seenHeaders[canonical] = true
		switch canonical {
		case "Host", "Origin", "Cookie", "Connection", "Proxy-Authorization", "Proxy-Connection", "Transfer-Encoding", "Content-Length", "Upgrade", "Trailer", "Te", "Mcp-Session-Id", "Mcp-Protocol-Version":
			return mcpcmd.ErrInvalid
		case "Authorization":
			if d.OAuth {
				return mcpcmd.ErrConflict
			}
		}
		value := binding.Value
		if binding.Kind == mcpcmd.ValueProtected {
			value = values.Headers[name]
		}
		if !safeHeaderValue(value) {
			return mcpcmd.ErrInvalid
		}
	}
	return nil
}

func validBinding(b mcpcmd.ValueBinding) bool {
	if len(b.Value) > 16<<10 {
		return false
	}
	switch b.Kind {
	case mcpcmd.ValueLiteral:
		return true
	case mcpcmd.ValueProtected:
		return b.Value == ""
	case mcpcmd.ValueEnvironment:
		return len(b.Value) <= 256 && environmentName.MatchString(b.Value)
	default:
		return false
	}
}

func validRemoteURL(raw string) bool {
	if len(raw) > 8192 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func safeText(value string, limit int) bool {
	return len(value) <= limit && !strings.ContainsAny(value, "\x00\r\n")
}

func validHeaderName(name string) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", c) {
			continue
		}
		return false
	}
	return true
}

func safeHeaderValue(value string) bool {
	for _, c := range value {
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}
