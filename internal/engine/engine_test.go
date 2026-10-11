package engine_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// testCapabilities is the shipped capability declarations, read from the tree.
// The engine's tests use the real research: a decision that is right about a
// capability nobody declared is not worth much.
func testCapabilities(t *testing.T) fs.FS {
	t.Helper()
	return os.DirFS(filepath.Join("..", "..", "assets", "capabilities"))
}
