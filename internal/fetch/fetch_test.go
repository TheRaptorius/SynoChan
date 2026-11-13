package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newClient() *Client { return New(func() string { return "test-agent" }) }

func TestGetIfModifiedUsesLastModifiedAndSendsHeader(t *testing.T) {
	lm := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var gotIMS, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIMS = r.Header.Get("If-Modified-Since")
		gotUA = r.Header.Get("User-Agent")
		if gotIMS != "" {
			if t, err := http.ParseTime(gotIMS); err == nil && !lm.After(t) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.Header().Set("Last-Modified", lm.Format(http.TimeFormat))
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := newClient()

	res, err := c.GetIfModified(context.Background(), srv.URL, time.Time{})
	if err != nil || res.NotModified || string(res.Body) != `{"ok":true}` || !res.LastModified.Equal(lm) {
		t.Fatalf("first: %+v %v", res, err)
	}
	if gotIMS != "" || gotUA != "test-agent" {
		t.Fatalf("headers: ims=%q ua=%q", gotIMS, gotUA)
	}

	res, err = c.GetIfModified(context.Background(), srv.URL, res.LastModified)
	if err != nil || !res.NotModified || res.Body != nil || !res.LastModified.Equal(lm) {
		t.Fatalf("second: %+v %v", res, err)
	}
	if gotIMS != lm.Format(http.TimeFormat) {
		t.Fatalf("If-Modified-Since = %q", gotIMS)
	}
}

func TestGetIfModifiedFallsBackToDateHeader(t *testing.T) {
	date := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", date.Format(http.TimeFormat))
		w.Write([]byte("x"))
	}))
	defer srv.Close()
	res, err := newClient().GetIfModified(context.Background(), srv.URL, time.Time{})
	if err != nil || !res.LastModified.Equal(date) {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestStatusErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gone":
			w.WriteHeader(http.StatusNotFound)
		case "/slow-down":
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/broken":
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	c := newClient()

	_, err := c.GetIfModified(context.Background(), srv.URL+"/gone", time.Time{})
	if !IsGone(err) {
		t.Errorf("404 should be gone: %v", err)
	}
	_, err = c.GetIfModified(context.Background(), srv.URL+"/slow-down", time.Time{})
	var tm *TooManyRequestsError
	if !errors.As(err, &tm) || tm.RetryAfter != 42*time.Second {
		t.Errorf("429: %v", err)
	}
	_, err = c.GetIfModified(context.Background(), srv.URL+"/broken", time.Time{})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 502 || IsGone(err) {
		t.Errorf("502: %v", err)
	}
}

func TestParseRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if d := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); d != 90*time.Second {
		t.Fatalf("got %v", d)
	}
	if d := parseRetryAfter("nonsense", now); d != 0 {
		t.Fatalf("got %v", d)
	}
}

func TestDownloadToFileIsAtomic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("image-bytes"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := newClient()

	dest := filepath.Join(dir, "a.jpg")
	if err := c.DownloadToFile(context.Background(), srv.URL+"/a.jpg", dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "image-bytes" {
		t.Fatalf("content %q", b)
	}
	if err := c.DownloadToFile(context.Background(), srv.URL+"/missing", filepath.Join(dir, "b.jpg")); !IsGone(err) {
		t.Fatalf("expected gone, got %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}
