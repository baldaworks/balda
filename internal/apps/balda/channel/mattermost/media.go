package mattermost

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxMediaFileBytes caps a single Mattermost upload. Mattermost's own default
// FileSettings.MaxFileSize is 100 MB; this transport stays under it so a
// rejected upload is caused by the server, not by us.
const maxMediaFileBytes = 100 << 20

// openMediaFile opens a local path for upload, rejecting directories and
// oversized files before any bytes are streamed to the server.
func openMediaFile(path string) (*os.File, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("media path is required")
	}
	info, err := os.Stat(trimmed)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("media path %q is a directory", trimmed)
	}
	if info.Size() <= 0 {
		return nil, fmt.Errorf("media file %q is empty", trimmed)
	}
	if info.Size() > maxMediaFileBytes {
		return nil, fmt.Errorf(
			"media file %q is %d bytes, exceeding the %d byte Mattermost upload limit",
			trimmed, info.Size(), maxMediaFileBytes,
		)
	}
	return os.Open(trimmed)
}

// mediaBaseName returns the upload filename for a local path.
func mediaBaseName(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	if base == "." || base == string(filepath.Separator) || strings.TrimSpace(base) == "" {
		return "attachment"
	}
	return base
}
