package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadCreatesDefaultsAndKeepsMissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	defaults := Defaults("/volume1/synochan")

	s, err := Load(path, defaults)
	if err != nil || !reflect.DeepEqual(s.Get(), defaults) {
		t.Fatalf("first load: %+v %v", s.Get(), err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("defaults not written")
	}

	os.WriteFile(path, []byte(`{"save_html":true}`), 0o644)
	s, err = Load(path, defaults)
	if err != nil || !s.Get().SaveHTML || s.Get().MinSecondsBetweenScrapes != 60 {
		t.Fatalf("partial file: %+v %v", s.Get(), err)
	}

	os.WriteFile(path, []byte(`{"min_seconds_between_scrapes":1}`), 0o644)
	s, _ = Load(path, defaults)
	if s.Get().MinSecondsBetweenScrapes != 60 {
		t.Fatal("invalid file should fall back to defaults")
	}
}

func TestSetValidatesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, _ := Load(path, Defaults("/volume1/x"))
	bad := s.Get()
	bad.SavePath = "relative/path"
	if s.Set(bad) == nil {
		t.Fatal("relative path accepted")
	}
	good := s.Get()
	good.MaxConcurrentDownloads = 4
	if err := s.Set(good); err != nil {
		t.Fatal(err)
	}
	again, _ := Load(path, Defaults("/volume1/x"))
	if again.Get().MaxConcurrentDownloads != 4 {
		t.Fatal("not persisted")
	}
}
