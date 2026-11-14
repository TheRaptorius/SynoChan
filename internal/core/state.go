package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
	"unicode/utf8"

	"synochan/internal/config"
)

const stateVersion = 1

type stateFile struct {
	Version int           `json:"version"`
	SavedAt time.Time     `json:"saved_at"`
	Threads []threadState `json:"threads"`
	Boards  []boardState  `json:"boards"`
	// FilterMemory keeps the last own filters of every board by key, also after the board was removed,
	// so they can be offered again when the board is added back.
	FilterMemory map[string]config.BoardFilters `json:"filter_memory,omitempty"`
}

type threadState struct {
	Site         string    `json:"site"`
	Board        string    `json:"board"`
	ID           int64     `json:"id"`
	Subject      *string   `json:"subject,omitempty"`
	FileCount    *int      `json:"file_count,omitempty"`
	LastModified time.Time `json:"last_modified,omitzero"`
	Root         string    `json:"root"`
	Dir          string    `json:"dir,omitempty"`
	SaveTo       string    `json:"save_to,omitempty"` // Folder of earlier versions; read only.
	Priority     int       `json:"priority,omitempty"`
	FromBoard    string    `json:"from_board,omitempty"`
	MatchSubject string    `json:"match_subject,omitempty"`
	MatchText    string    `json:"match_text,omitempty"`
	AddedAt      time.Time `json:"added_at"`
	SavedAssets  []string  `json:"saved_assets"`
	FailedAssets []string  `json:"failed_assets,omitempty"`
}

type boardState struct {
	Site             string               `json:"site"`
	Code             string               `json:"code"`
	GreatestThreadID int64                `json:"greatest_thread_id"`
	LastModified     time.Time            `json:"last_modified,omitzero"`
	SaveTo           string               `json:"save_to"`
	AddedAt          time.Time            `json:"added_at"`
	Filters          *config.BoardFilters `json:"filters,omitempty"`
}

func (t *Thread) state() threadState {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := threadState{
		Site:         t.Site,
		Board:        t.Board,
		ID:           t.ID,
		LastModified: t.lastModified,
		Root:         t.Root,
		Dir:          t.dir,
		Priority:     t.prio,
		FromBoard:    t.FromBoard,
		MatchSubject: t.matchSubject,
		MatchText:    truncate(t.matchText, 4000),
		AddedAt:      t.AddedAt,
		SavedAssets:  make([]string, 0, len(t.saved)),
	}
	if t.subjectSet {
		subject := t.subject
		s.Subject = &subject
	}
	if t.fileCountKnown {
		fc := t.fileCount
		s.FileCount = &fc
	}
	for id := range t.saved {
		s.SavedAssets = append(s.SavedAssets, id.String())
	}
	for id := range t.failed {
		s.FailedAssets = append(s.FailedAssets, id.String())
	}
	slices.Sort(s.SavedAssets)
	slices.Sort(s.FailedAssets)
	return s
}

func (m *Manager) threadFromState(s threadState) *Thread {
	root, dir := s.Root, s.Dir
	if root == "" && s.SaveTo != "" {
		// Earlier versions stored <root>/4chan/<board>/<number>.
		root = filepath.Dir(filepath.Dir(filepath.Dir(s.SaveTo)))
		if dir == "" {
			if info, err := os.Stat(s.SaveTo); err == nil && info.IsDir() {
				dir = s.SaveTo
			}
		}
	}
	if dir != "" {
		if _, err := os.Stat(dir); err != nil {
			dir = "" // Moved or deleted meanwhile: created again with the next file.
		}
	}
	t := m.newThread(s.Board, s.ID, root)
	t.dir = dir
	t.prio = max(0, s.Priority)
	t.FromBoard = s.FromBoard
	t.matchSubject, t.matchText = s.MatchSubject, s.MatchText
	t.AddedAt = s.AddedAt
	if s.Subject != nil {
		t.subject, t.subjectSet = *s.Subject, true
	}
	if s.FileCount != nil {
		t.fileCount, t.fileCountKnown = *s.FileCount, true
	}
	uploadsSaved := 0
	for _, raw := range s.SavedAssets {
		id, err := ParseAssetID(raw)
		if err != nil {
			continue
		}
		t.saved[id] = struct{}{}
		t.seen[id] = struct{}{}
		if id.Type == AssetUpload {
			uploadsSaved++
		}
	}
	// Files given up on are tried again after a restart.
	// Only send If-Modified-Since when every file was saved. Otherwise the next scrape downloads the thread
	// regardless, so files that were found but not saved before a restart are queued again.
	if s.FileCount != nil && *s.FileCount == uploadsSaved {
		t.lastModified = s.LastModified
	}
	return t
}

func (b *Board) state() boardState {
	b.mu.Lock()
	defer b.mu.Unlock()
	var filters *config.BoardFilters
	if len(b.filters.Skip) > 0 || len(b.filters.Only) > 0 {
		f := b.filters
		filters = &f
	}
	return boardState{
		Filters:          filters,
		Site:             b.Site,
		Code:             b.Code,
		GreatestThreadID: b.greatestThreadID,
		LastModified:     b.lastModified,
		SaveTo:           b.SaveTo,
		AddedAt:          b.AddedAt,
	}
}

func (m *Manager) boardFromState(s boardState) *Board {
	b := m.newBoard(s.Code, s.SaveTo)
	b.AddedAt = s.AddedAt
	b.greatestThreadID = s.GreatestThreadID
	b.lastModified = s.LastModified
	if s.Filters != nil {
		b.filters = *s.Filters
	}
	return b
}

func readState(path string) (stateFile, error) {
	var st stateFile
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("parse %s: %w", path, err)
	}
	if st.Version > stateVersion {
		return st, fmt.Errorf("%s was written by a newer version (format %d)", path, st.Version)
	}
	return st, nil
}

func writeState(path string, st stateFile) error {
	st.Version = stateVersion
	st.SavedAt = time.Now()
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, data, 0o640)
}

// truncate shortens s to at most n bytes without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
