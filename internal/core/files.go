package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"synochan/internal/config"
	"synochan/internal/fourchan"
)

func threadSaveDir(root, board string, id int64) string {
	return filepath.Join(root, fourchan.SiteName, board, strconv.FormatInt(id, 10))
}

func boardSaveDir(root, board string) string {
	return filepath.Join(root, fourchan.SiteName, board)
}

var volumePrefix = regexp.MustCompile(`^/volume(?:USB)?\d+(/.+)$`)

// SharePath turns an absolute DSM path ("/volume1/downloads/x") into the path File Station shows
// ("/downloads/x"). Returns "" for paths outside a volume.
func SharePath(p string) string {
	if m := volumePrefix.FindStringSubmatch(p); m != nil {
		return m[1]
	}
	return ""
}

// renamedThreadDir returns the folder name a removed thread is moved to, by the configured format.
func renamedThreadDir(saveTo, subject string, id int64, format config.FolderNameFormat) string {
	subject = fourchan.SanitiseSubject(subject)
	parent := filepath.Dir(saveTo)
	var name string
	if format == config.FolderSubjectID {
		name = fmt.Sprintf("%s - %d", subject, id)
	} else {
		name = fmt.Sprintf("%s - %s", filepath.Base(saveTo), subject)
	}
	return filepath.Join(parent, fourchan.ClampName(strings.TrimSpace(name)))
}

// moveThreadDir renames a thread folder to include its subject. An existing destination gets " (1)", " (2)", … appended.
// Returns the new path, or "" when there was nothing to move.
func moveThreadDir(saveTo, subject string, id int64, format config.FolderNameFormat) (string, error) {
	if _, err := os.Stat(saveTo); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	dest := renamedThreadDir(saveTo, subject, id, format)
	candidate := dest
	for n := 1; ; n++ {
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			break
		}
		if n > 1000 {
			return "", fmt.Errorf("no free folder name for %s", dest)
		}
		candidate = fmt.Sprintf("%s (%d)", dest, n)
	}
	if err := os.Rename(saveTo, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

// CheckWritable creates dir if needed and verifies files can be created in it.
func CheckWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return friendlyPermissionError(dir, err)
	}
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return friendlyPermissionError(dir, err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

func friendlyPermissionError(dir string, err error) error {
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("no permission to write to %s. In Control Panel > Shared Folder, give the system user %q read/write access to that shared folder", dir, PackageUser)
	}
	return fmt.Errorf("cannot write to %s: %w", dir, err)
}

// PackageUser is the DSM system user the package runs as.
const PackageUser = "synochan"
