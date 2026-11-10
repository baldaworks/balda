package mcpmanage

import (
	"slices"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

const offlineAccessScope = "offline_access"

// authorizationScopes combines resource intent with explicitly supported renewal.
func authorizationScopes(metadata OAuthMetadata, requested []string) ([]string, error) {
	if len(requested) > 64 {
		return nil, mcpcmd.ErrInvalid
	}
	selected, err := selectedResourceScopes(requested)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		selected, err = selectedResourceScopes(metadata.ResourceScopes)
		if err != nil {
			return nil, err
		}
	}
	if !includesScopes(metadata.ResourceScopes, selected) || !includesScopes(metadata.Scopes, selected) {
		return nil, mcpcmd.ErrInvalid
	}
	if slices.Contains(metadata.Scopes, offlineAccessScope) && slices.Contains(metadata.GrantTypes, "refresh_token") {
		selected = append(selected, offlineAccessScope)
	}
	if len(selected) > 64 {
		return nil, mcpcmd.ErrInvalid
	}
	return selected, nil
}

func selectedResourceScopes(scopes []string) ([]string, error) {
	var selected []string
	for _, scope := range scopes {
		if !validOAuthScope(scope) {
			return nil, mcpcmd.ErrInvalid
		}
		if scope != offlineAccessScope && !slices.Contains(selected, scope) {
			selected = append(selected, scope)
			if len(selected) > 64 {
				return nil, mcpcmd.ErrInvalid
			}
		}
	}
	return selected, nil
}

// containsResourceScopes excludes protocol renewal from MCP access requirements.
func containsResourceScopes(granted, required []string) bool {
	for _, scope := range required {
		if scope != offlineAccessScope && !slices.Contains(granted, scope) {
			return false
		}
	}
	return true
}

// grantedTokenScopes preserves omitted scope and records only requested consent.
func grantedTokenScopes(requested, response []string) ([]string, bool) {
	if response == nil {
		return slices.Clone(requested), true
	}
	for _, scope := range response {
		if !validOAuthScope(scope) {
			return nil, false
		}
	}
	if !containsResourceScopes(response, requested) {
		return nil, false
	}
	granted := make([]string, 0, len(requested))
	for _, scope := range requested {
		if slices.Contains(response, scope) && !slices.Contains(granted, scope) {
			granted = append(granted, scope)
		}
	}
	return granted, true
}

func validOAuthScope(scope string) bool {
	if scope == "" || len(scope) > 256 {
		return false
	}
	for _, c := range scope {
		if c < 0x21 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}
