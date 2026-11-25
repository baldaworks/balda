package main

import (
	"os"
	"strings"

	"github.com/baldaworks/runtime/v2/appconfig"
)

// loadBaldaDocument preserves explicit empty overrides for the string settings
// where empty selects a distinct behavior. The runtime loader handles nonempty
// environment values, including overrides for null YAML leaves.
func loadBaldaDocument(runtime appconfig.RuntimeLoadOptions, opts appconfig.AppLoadOptions, doc *baldaConfigDocument) (string, error) {
	profile, err := appconfig.LoadConfigDocument(runtime, opts, doc)
	if err != nil {
		return "", err
	}
	prefix := opts.EnvPrefix
	if prefix == "" {
		prefix = opts.AppName
	}
	emptyOverride := func(path string) bool {
		name := strings.ToUpper(prefix + "_" + strings.ReplaceAll(path, ".", "_"))
		value, set := os.LookupEnv(name)
		return set && value == ""
	}
	if emptyOverride("security.key") {
		doc.Balda.Security.Key = ""
	}
	if emptyOverride("http.base_path") {
		root := ""
		doc.Balda.HTTP.BasePath = &root
	}
	return profile, nil
}
