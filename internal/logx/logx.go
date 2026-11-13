// Package logx writes the log to a size-capped file and reads its tail for the UI.
package logx

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
)

// RotatingFile is an io.Writer that moves the file to "<path>.1" when it grows past MaxBytes.
type RotatingFile struct {
	Path     string
	MaxBytes int64

	mu   sync.Mutex
	f    *os.File
	size int64
}

// Open opens (or creates) the log file for appending.
func Open(path string, maxBytes int64) (*RotatingFile, error) {
	r := &RotatingFile{Path: path, MaxBytes: maxBytes}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, info.Size()
	return nil
}

func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.MaxBytes > 0 && r.size+int64(len(p)) > r.MaxBytes {
		r.f.Close()
		os.Rename(r.Path, r.Path+".1")
		if err := r.open(); err != nil {
			r.f = nil
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Close closes the file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// Tail returns up to n last lines of the log, including the rotated file when needed.
func Tail(path string, n int) []string {
	lines := tailFile(path, n)
	if len(lines) < n {
		older := tailFile(path+".1", n-len(lines))
		lines = append(older, lines...)
	}
	return lines
}

func tailFile(path string, n int) []string {
	if n <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	const chunk = 64 << 10
	size := info.Size()
	var buf []byte
	for offset := size; offset > 0 && bytes.Count(buf, []byte{'\n'}) <= n; {
		read := int64(chunk)
		if offset < read {
			read = offset
		}
		offset -= read
		part := make([]byte, read)
		if _, err := f.ReadAt(part, offset); err != nil && err != io.EOF {
			return nil
		}
		buf = append(part, buf...)
		if size-offset > 4<<20 {
			break
		}
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
