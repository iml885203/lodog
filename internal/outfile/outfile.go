// Package outfile writes a command's result to the file the operator named,
// or to stdout when they named none.
package outfile

import (
	"fmt"
	"io"
	"os"
)

// Write puts data in the file at path, or on stdout when path is empty.
func Write(stdout io.Writer, path string, data []byte) error {
	if path == "" {
		_, err := stdout.Write(data)
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
