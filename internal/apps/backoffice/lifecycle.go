package backoffice

import (
	"net/url"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
)

const (
	lifecycleActive  = "active"
	lifecycleDeleted = "deleted"
	lifecycleAll     = "all"
	deletedLabel     = "Deleted"
)

func lifecycleSelection(query url.Values) (string, bool) {
	selected := query.Get("lifecycle")
	if selected == "" {
		return lifecycleActive, true
	}
	switch selected {
	case lifecycleActive, lifecycleDeleted, lifecycleAll:
		return selected, true
	default:
		return "", false
	}
}

func lifecycleView(basePath, selected string) webui.LifecycleView {
	createPath := queryPath(basePath, url.Values{"new": {"1"}})
	if basePath == "/mcp" {
		createPath = "/mcp/new"
	}
	return webui.LifecycleView{
		Selected:    selected,
		ActivePath:  basePath,
		DeletedPath: queryPath(basePath, url.Values{"lifecycle": {lifecycleDeleted}}),
		AllPath:     queryPath(basePath, url.Values{"lifecycle": {lifecycleAll}}),
		ListPath:    lifecyclePath(basePath, selected),
		CreatePath:  lifecyclePath(createPath, selected),
	}
}

func lifecyclePath(path, selected string) string {
	if selected == lifecycleActive {
		return path
	}
	return queryPath(path, url.Values{"lifecycle": {selected}})
}

func queryPath(path string, values url.Values) string {
	parsed, err := url.Parse(path)
	if err != nil {
		return path
	}
	query := parsed.Query()
	for key, entries := range values {
		query.Del(key)
		for _, entry := range entries {
			query.Add(key, entry)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
