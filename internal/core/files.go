package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"synochan/internal/config"
	"synochan/internal/fourchan"
)

// threadDir is a thread's folder: <root>/4chan/<board>/<name>, the name by the folder name setting.
// Without a subject the folder is named by the number alone.
func threadDir(root, board string, id int64, subject string, format config.FolderNameFormat) string {
	number := strconv.FormatInt(id, 10)
	subject = fourchan.SanitiseSubject(subject)
	name := number
	if subject != "" {
		switch format {
		case config.FolderNumberSubject:
			name = number + " - " + subject
		case config.FolderSubjectNumber:
			name = subject + " - " + number
		}
	}
	return filepath.Join(root, fourchan.SiteName, board, fourchan.ClampName(name))
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

// renameDir moves a folder to want. If want is taken, " (1)", " (2)", … is appended. Returns the new path.
func renameDir(from, want string) (string, error) {
	candidate := want
	for n := 1; ; n++ {
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			break
		}
		if n > 1000 {
			return "", fmt.Errorf("no free folder name for %s", want)
		}
		candidate = fmt.Sprintf("%s (%d)", want, n)
	}
	if err := os.Rename(from, candidate); err != nil {
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
