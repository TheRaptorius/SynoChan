package logx

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestRotationAndTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	f, err := Open(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		fmt.Fprintf(f, "line %02d\n", i)
	}
	f.Close()
	lines := Tail(path, 5)
	if len(lines) != 5 || lines[4] != "line 29" || lines[0] != "line 25" {
		t.Fatalf("tail: %q", lines)
	}
	// Spans the rotated file when the current one is short.
	all := Tail(path, 1000)
	if all[len(all)-1] != "line 29" || len(all) < 20 {
		t.Fatalf("tail all: %d lines", len(all))
	}
	if Tail(filepath.Join(t.TempDir(), "missing"), 5) != nil {
		t.Fatal("missing file should give nil")
	}
}
