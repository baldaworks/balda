package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

type provenance struct {
	Dependencies []dependency `json:"dependencies"`
}

type dependency struct {
	Name    string      `json:"name"`
	Version string      `json:"version"`
	License string      `json:"license"`
	Source  string      `json:"source"`
	Files   []assetFile `json:"files"`
}

type assetFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// VerifyAssets checks every declared embedded vendor file against pinned provenance.
func VerifyAssets() error {
	data, err := embedded.ReadFile("static/vendor/provenance.json")
	if err != nil {
		return fmt.Errorf("read frontend provenance: %w", err)
	}
	var manifest provenance
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse frontend provenance: %w", err)
	}
	declared := make(map[string]struct{})
	for _, dependency := range manifest.Dependencies {
		if strings.TrimSpace(dependency.Name) == "" || strings.TrimSpace(dependency.Version) == "" ||
			strings.TrimSpace(dependency.License) == "" || strings.TrimSpace(dependency.Source) == "" || len(dependency.Files) == 0 {
			return fmt.Errorf("frontend dependency provenance is incomplete")
		}
		for _, file := range dependency.Files {
			path := "static/vendor/" + file.Path
			if _, exists := declared[path]; exists {
				return fmt.Errorf("frontend asset is declared more than once: %s", file.Path)
			}
			contents, err := embedded.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read declared frontend asset %s: %w", file.Path, err)
			}
			sum := sha256.Sum256(contents)
			if hex.EncodeToString(sum[:]) != file.SHA256 {
				return fmt.Errorf("frontend asset digest mismatch: %s", file.Path)
			}
			declared[path] = struct{}{}
		}
	}
	err = fs.WalkDir(embedded, "static/vendor", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path == "static/vendor/provenance.json" {
			return nil
		}
		if _, ok := declared[path]; !ok {
			return fmt.Errorf("embedded vendor asset is not declared: %s", path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("verify embedded vendor inventory: %w", err)
	}
	return nil
}
