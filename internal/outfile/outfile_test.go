package outfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithoutAPathTheDataGoesToStdout(t *testing.T) {
	var out bytes.Buffer
	if err := Write(&out, "", []byte("data")); err != nil || out.String() != "data" {
		t.Fatalf("out = %q, err = %v", out.String(), err)
	}
}

func TestWithAPathTheDataGoesToTheFileAndNotToStdout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o.yaml")
	var out bytes.Buffer
	if err := Write(&out, path, []byte("data")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path) //nolint:gosec // G304: a test temp path
	if err != nil || string(got) != "data" || out.Len() != 0 {
		t.Fatalf("file = %q, stdout = %q, err = %v", got, out.String(), err)
	}
}

func TestAnUnwritablePathNamesThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "o.yaml")
	if err := Write(&bytes.Buffer{}, path, nil); err == nil || !strings.Contains(err.Error(), "write "+path) {
		t.Fatalf("err = %v", err)
	}
}
