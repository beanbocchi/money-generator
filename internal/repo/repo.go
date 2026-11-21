// Package repo locates the repository root for host tools.
package repo

import (
	"fmt"
	"os"
	"path/filepath"
)

// Root is MG_ROOT if set, else the parent of the directory holding the binary (make
// build writes bin/mg), as the bash tools used $(dirname "$0")/.. . The marker, a path
// relative to the root, must exist, so a stray binary fails loudly instead of acting on
// the wrong directory.
func Root(marker string) (string, error) {
	root := os.Getenv("MG_ROOT")
	if root == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("locate binary: %w", err)
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return "", fmt.Errorf("locate binary: %w", err)
		}
		root = filepath.Dir(filepath.Dir(exe))
	}
	if _, err := os.Stat(filepath.Join(root, marker)); err != nil {
		return "", fmt.Errorf("%s has no %s; run bin/mg from the repo or set MG_ROOT", root, marker)
	}
	return root, nil
}
