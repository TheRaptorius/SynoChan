// Package fetch wraps HTTP requests with the status handling the scraper relies on.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// StatusError is returned for unsuccessful responses other than 304 and 429.
type StatusError struct {
	Code int
	URL  string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("GET %s: HTTP %d", e.URL, e.Code)
}

// IsGone reports whether err means the content no longer exists (404 or 410).
func IsGone(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Code == http.StatusNotFound || se.Code == http.StatusGone)
}

// TooManyRequestsError is returned for 429 responses.
type TooManyRequestsError struct {
	URL        string
	RetryAfter time.Duration // Zero when the server gave none.
}

func (e *TooManyRequestsError) Error() string {
	return fmt.Sprintf("GET %s: HTTP 429 (retry after %s)", e.URL, e.RetryAfter)
}

// Client performs requests with the configured user agent.
type Client struct {
	http      *http.Client
	userAgent func() string
	// JSONTimeout limits API and page requests. Downloads use their own, longer limit.
	JSONTimeout     time.Duration
	DownloadTimeout time.Duration
}

// New creates a client. userAgent is read on every request so settings changes apply immediately.
func New(userAgent func() string) *Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}
	return &Client{
		http:            &http.Client{Transport: transport},
		userAgent:       userAgent,
		JSONTimeout:     100 * time.Second,
		DownloadTimeout: 30 * time.Minute,
	}
}

func (c *Client) newRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent())
	return req, nil
}

func checkStatus(resp *http.Response, url string) error {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &TooManyRequestsError{URL: url, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	case resp.StatusCode == http.StatusNotModified:
		return nil
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return &StatusError{Code: resp.StatusCode, URL: url}
	}
	return nil
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// ConditionalResult is the outcome of GetIfModified.
type ConditionalResult struct {
	Body        []byte // Nil when not modified.
	NotModified bool
	// LastModified is the server's time for this version, to send as If-Modified-Since next time:
	// the Last-Modified header, else the Date header, else the local time the request was sent.
	LastModified time.Time
}

// GetIfModified fetches url, sending since (if not zero) as If-Modified-Since.
func (c *Client) GetIfModified(ctx context.Context, url string, since time.Time) (ConditionalResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.JSONTimeout)
	defer cancel()

	req, err := c.newRequest(ctx, url)
	if err != nil {
		return ConditionalResult{}, err
	}
	if !since.IsZero() {
		req.Header.Set("If-Modified-Since", since.UTC().Format(http.TimeFormat))
	}
	// Taken before sending, so when the server gives no time nothing posted while the request is in flight is skipped next time.
	sentAt := time.Now()

	resp, err := c.http.Do(req)
	if err != nil {
		return ConditionalResult{}, err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, url); err != nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return ConditionalResult{}, err
	}

	headerTime := func(name string) time.Time {
		if t, err := http.ParseTime(resp.Header.Get(name)); err == nil {
			return t
		}
		return time.Time{}
	}
	lastModified := headerTime("Last-Modified")

	if resp.StatusCode == http.StatusNotModified {
		// A 304 may omit Last-Modified, then keep what we asked with rather than the response date.
		if lastModified.IsZero() {
			lastModified = since
		}
		if lastModified.IsZero() {
			lastModified = sentAt
		}
		return ConditionalResult{NotModified: true, LastModified: lastModified}, nil
	}

	if lastModified.IsZero() {
		lastModified = headerTime("Date")
	}
	if lastModified.IsZero() {
		lastModified = sentAt
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return ConditionalResult{}, err
	}
	return ConditionalResult{Body: body, LastModified: lastModified}, nil
}

// GetString fetches url as text.
func (c *Client) GetString(ctx context.Context, url string) (string, error) {
	res, err := c.GetIfModified(ctx, url, time.Time{})
	if err != nil {
		return "", err
	}
	return string(res.Body), nil
}

// DownloadToFile streams url into dest. The file is written under a temporary name and renamed when
// complete, so an interrupted download never leaves a truncated file behind.
func (c *Client) DownloadToFile(ctx context.Context, url, dest string) error {
	ctx, cancel := context.WithTimeout(ctx, c.DownloadTimeout)
	defer cancel()

	req, err := c.newRequest(ctx, url)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp, url); err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotModified {
		return &StatusError{Code: resp.StatusCode, URL: url}
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".partial-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // No-op after a successful rename.

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}
