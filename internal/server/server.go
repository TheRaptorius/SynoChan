// Package server exposes the manager over a small JSON API.
package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"synochan/internal/core"
	"synochan/internal/logx"
)

// UserHeader carries the DSM user name from the CGI front end to the daemon.
const UserHeader = "X-Synochan-User"

// Info is static information about the running instance.
type Info struct {
	Version string `json:"version"`
	RepoURL string `json:"repo_url"`
	Author  string `json:"author"`
	LogPath string `json:"-"`
	DataDir string `json:"data_dir"`
}

type handler struct {
	m       *core.Manager
	info    Info
	started time.Time
}

// New returns the API handler. Routes live under /api/.
func New(m *core.Manager, info Info) http.Handler {
	h := &handler{m: m, info: info, started: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", h.state)
	mux.HandleFunc("GET /api/info", h.getInfo)
	mux.HandleFunc("POST /api/add", h.add)
	mux.HandleFunc("POST /api/threads/remove", h.removeThreads)
	mux.HandleFunc("POST /api/threads/rename", h.renameThread)
	mux.HandleFunc("POST /api/threads/clear", h.clearThreads)
	mux.HandleFunc("POST /api/threads/prioritize", h.prioritize)
	mux.HandleFunc("POST /api/threads/unprioritize", h.unprioritize)
	mux.HandleFunc("POST /api/boards/remove", h.removeBoards)
	mux.HandleFunc("POST /api/boards/clear", h.clearBoards)
	mux.HandleFunc("POST /api/pause", h.pause)
	mux.HandleFunc("GET /api/settings", h.getSettings)
	mux.HandleFunc("POST /api/settings", h.setSettings)
	mux.HandleFunc("GET /api/logs", h.logs)
	mux.HandleFunc("GET /api/urls", h.urls)
	return noStore(mux)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return errors.New("invalid request body")
	}
	return nil
}

func (h *handler) state(w http.ResponseWriter, r *http.Request) {
	if since := r.URL.Query().Get("rev"); since != "" {
		if n, err := strconv.ParseUint(since, 10, 64); err == nil && n == h.m.Revision() {
			writeJSON(w, http.StatusOK, map[string]any{"rev": n, "unchanged": true, "queue": h.m.QueueStatus()})
			return
		}
	}
	writeJSON(w, http.StatusOK, h.m.Snapshot())
}

func (h *handler) getInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    h.info.Version,
		"repo_url":   h.info.RepoURL,
		"author":     h.info.Author,
		"data_dir":   h.info.DataDir,
		"user":       r.Header.Get(UserHeader),
		"go_version": runtime.Version(),
		"arch":       runtime.GOARCH,
		"started":    h.started,
	})
}

func (h *handler) add(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": h.m.AddURLs(req.Text)})
}

type keysRequest struct {
	Keys []string `json:"keys"`
}

type itemError struct {
	Key   string `json:"key"`
	Error string `json:"error"`
}

func (h *handler) removeThreads(w http.ResponseWriter, r *http.Request) {
	var req keysRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var errs []itemError
	for _, key := range req.Keys {
		if err := h.m.RemoveThread(key); err != nil && !errors.Is(err, core.ErrNotFound) {
			errs = append(errs, itemError{Key: key, Error: err.Error()})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"errors": errs})
}

func (h *handler) renameThread(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key     string `json:"key"`
		Subject string `json:"subject"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.m.RenameThread(req.Key, req.Subject); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, core.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handler) clearThreads(w http.ResponseWriter, r *http.Request) {
	h.m.ClearThreads()
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handler) prioritize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys  []string `json:"keys"`
		First bool     `json:"first"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.m.Prioritize(req.Keys, req.First); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handler) unprioritize(w http.ResponseWriter, r *http.Request) {
	var req keysRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	h.m.Unprioritize(req.Keys)
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handler) removeBoards(w http.ResponseWriter, r *http.Request) {
	var req keysRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	for _, key := range req.Keys {
		h.m.RemoveBoard(key)
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handler) clearBoards(w http.ResponseWriter, r *http.Request) {
	h.m.ClearBoards()
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *handler) pause(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paused bool `json:"paused"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	h.m.SetPaused(req.Paused)
	writeJSON(w, http.StatusOK, h.m.QueueStatus())
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.m.Settings())
}

func (h *handler) setSettings(w http.ResponseWriter, r *http.Request) {
	s := h.m.Settings() // Fields missing from the request keep their current value.
	if err := readJSON(r, &s); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.m.UpdateSettings(s); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, h.m.Settings())
}

func (h *handler) logs(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.URL.Query().Get("lines"))
	if err != nil || n <= 0 || n > 5000 {
		n = 300
	}
	lines := []string{}
	if h.info.LogPath != "" {
		if tail := logx.Tail(h.info.LogPath, n); tail != nil {
			lines = tail
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

func (h *handler) urls(w http.ResponseWriter, r *http.Request) {
	urls := h.m.URLs(r.URL.Query().Get("kind"))
	if urls == nil {
		urls = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"urls": urls})
}
