// Package config holds the user-editable settings and persists them as JSON.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ImageFilenameFormat decides how downloaded files are named.
type ImageFilenameFormat int

const (
	FilenameID            ImageFilenameFormat = 0 // 1570301.jpg
	FilenameOriginal      ImageFilenameFormat = 1 // LittleSaintJames.jpg
	FilenameIDAndOriginal ImageFilenameFormat = 2 // 1570301 - LittleSaintJames.jpg
	FilenameOriginalAndID ImageFilenameFormat = 3 // LittleSaintJames - 1570301.jpg
)

// FolderNameFormat decides how a thread folder is named.
type FolderNameFormat int

const (
	FolderNumber        FolderNameFormat = 0 // 123456
	FolderNumberSubject FolderNameFormat = 1 // 123456 - Subject
	FolderSubjectNumber FolderNameFormat = 2 // Subject - 123456
)

// Theme is the colour scheme of the UI.
type Theme string

const (
	ThemeLight Theme = "light"
	ThemeDark  Theme = "dark"
)

// MinSecondsBetweenScrapesFloor is the lowest allowed recheck interval; the image board's API rules ask
// for at least 10 seconds between updates of a thread.
const MinSecondsBetweenScrapesFloor = 10

// MaxConcurrentDownloadsCeiling is the highest allowed number of parallel requests.
const MaxConcurrentDownloadsCeiling = 16

// Settings are the user-editable options.
type Settings struct {
	SavePath                 string `json:"save_path"`
	MinSecondsBetweenScrapes int    `json:"min_seconds_between_scrapes"`
	// SpaceDownloads starts file downloads at most one per second. API requests are always spaced one
	// second apart, as the image board's API rules require.
	SpaceDownloads         bool                `json:"space_downloads"`
	MaxConcurrentDownloads int                 `json:"max_concurrent_downloads"`
	ImageFilenameFormat    ImageFilenameFormat `json:"image_filename_format"`
	FolderName             FolderNameFormat    `json:"folder_name"`
	Theme                  Theme               `json:"theme"`
	SaveHTML               bool                `json:"save_html"`
	SaveThumbnails         bool                `json:"save_thumbnails"`
	KeepLists              bool                `json:"keep_lists"`
	UserAgent              string              `json:"user_agent"`
	StartPaused            bool                `json:"start_paused"`
}

// DefaultUserAgent mimics a regular browser, as the image board rejects unknown clients.
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// Defaults returns the settings used on first start.
func Defaults(savePath string) Settings {
	return Settings{
		SavePath:                 savePath,
		MinSecondsBetweenScrapes: 60,
		MaxConcurrentDownloads:   3,
		ImageFilenameFormat:      FilenameID,
		FolderName:               FolderNumberSubject,
		Theme:                    ThemeLight,
		KeepLists:                true,
		UserAgent:                DefaultUserAgent,
	}
}

// Validate checks values that do not depend on the file system.
func (s *Settings) Validate() error {
	var problems []string
	if strings.TrimSpace(s.SavePath) == "" {
		problems = append(problems, "save folder must not be empty")
	} else if !filepath.IsAbs(s.SavePath) {
		problems = append(problems, "save folder must be an absolute path")
	}
	if s.MinSecondsBetweenScrapes < MinSecondsBetweenScrapesFloor {
		problems = append(problems, fmt.Sprintf("recheck interval must be at least %d seconds", MinSecondsBetweenScrapesFloor))
	}
	if s.MaxConcurrentDownloads < 1 || s.MaxConcurrentDownloads > MaxConcurrentDownloadsCeiling {
		problems = append(problems, fmt.Sprintf("parallel downloads must be between 1 and %d", MaxConcurrentDownloadsCeiling))
	}
	if s.ImageFilenameFormat < FilenameID || s.ImageFilenameFormat > FilenameOriginalAndID {
		problems = append(problems, "unknown file name format")
	}
	if s.FolderName < FolderNumber || s.FolderName > FolderSubjectNumber {
		problems = append(problems, "unknown folder name format")
	}
	if s.Theme != ThemeLight && s.Theme != ThemeDark {
		problems = append(problems, "unknown theme")
	}
	if strings.TrimSpace(s.UserAgent) == "" {
		problems = append(problems, "user agent must not be empty")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// Store keeps the current settings in memory and on disk.
type Store struct {
	path     string
	mu       sync.RWMutex
	settings Settings
}

// Load reads settings from path, falling back to defaults for a missing file or invalid content.
func Load(path string, defaults Settings) (*Store, error) {
	s := &Store{path: path, settings: defaults}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, s.save(defaults)
	}
	if err != nil {
		return nil, err
	}
	loaded := defaults // Fields missing from the file keep their defaults.
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := loaded.Validate(); err != nil {
		// Keep running with defaults rather than refusing to start over a broken file.
		return s, nil
	}
	s.settings = loaded
	return s, nil
}

// NewMemory returns a store that never touches the disk.
func NewMemory(settings Settings) *Store {
	return &Store{settings: settings}
}

// Get returns a copy of the current settings.
func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// Set validates, persists and applies new settings.
func (s *Store) Set(settings Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.save(settings); err != nil {
		return err
	}
	s.settings = settings
	return nil
}

func (s *Store) save(settings Settings) error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.path, data, 0o640)
}

// WriteFileAtomic writes data to a temporary file next to path, syncs it and renames it over path.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // No-op after a successful rename.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
