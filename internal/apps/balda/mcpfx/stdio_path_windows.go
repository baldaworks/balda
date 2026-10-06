package mcpfx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// Resolve the same path categories as os/exec and syscall.StartProcess. Windows
// root-relative and drive-relative names are not ordinary children of Dir.
func stdioExecutablePath(directory, executable string) (string, error) {
	volume := filepath.VolumeName(executable)
	rootRelative := len(executable) > 0 && os.IsPathSeparator(executable[0])
	if !windowsExecutableExtension(executable) {
		// os/exec resolves extensions for these categories in the host context
		// before syscall applies Dir. An already recognized extension skips that
		// lookup, allowing the executable to exist only in the child directory.
		lookup := executable
		if volume == "" && !rootRelative {
			lookup = filepath.Join(directory, executable)
		}
		resolved, err := exec.LookPath(lookup)
		if err != nil {
			return "", err
		}
		executable += strings.TrimPrefix(resolved, lookup)
	}
	if !filepath.IsAbs(executable) && strings.HasPrefix(directory, `\\`) {
		return "", syscall.EINVAL // native relative execution does not accept a UNC Dir
	}
	if !filepath.IsAbs(executable) && volume != "" {
		if len(executable) == len(volume) {
			return "", syscall.EINVAL
		}
		if strings.EqualFold(volume, filepath.VolumeName(directory)) {
			executable = directory + `\` + executable[len(volume):]
		}
	} else if rootRelative && volume == "" {
		executable = filepath.VolumeName(directory) + executable
	} else if !filepath.IsAbs(executable) {
		executable = filepath.Join(directory, executable)
	}
	path, err := windows.FullPath(executable)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", syscall.EACCES
	}
	return path, nil
}

func windowsExecutableExtension(executable string) bool {
	extensions := strings.Split(strings.ToLower(os.Getenv("PATHEXT")), ";")
	if os.Getenv("PATHEXT") == "" {
		extensions = []string{".com", ".exe", ".bat", ".cmd"}
	}
	for _, extension := range extensions {
		if extension != "" && !strings.HasPrefix(extension, ".") {
			extension = "." + extension
		}
		if extension != "" && strings.EqualFold(filepath.Ext(executable), extension) {
			return true
		}
	}
	return false
}
