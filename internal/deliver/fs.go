package deliver

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// csuiteRoot is the in-container root of the shared persona tree.
// Exposed as a package-level var so tests can redirect writes to
// t.TempDir without introducing a second plumbing seam. Production
// code never mutates this.
var csuiteRoot = "/csuite"

// resolveCsuitePath rewrites a protocol path (/csuite/...) to the
// effective on-disk path. This is the single choke point for the
// tempdir-redirection seam used by tests. Paths that don't start
// with /csuite/ are returned unchanged so absolute system paths
// outside the tree still resolve.
func resolveCsuitePath(path string) string {
	const prefix = "/csuite"
	if path == prefix || strings.HasPrefix(path, prefix+"/") {
		return filepath.Join(csuiteRoot, strings.TrimPrefix(path, prefix))
	}
	return path
}

// atomicCopyFile writes the contents of src to dst atomically: it
// opens a temp file in the same directory as dst with O_EXCL, copies
// body bytes in, fsyncs, and renames over dst. On any failure the
// temp file is removed and the original dst (if any) is untouched.
//
// The same-directory temp guarantees that rename is atomic on
// POSIX filesystems — cross-filesystem rename would fall back to
// copy+delete which defeats the safety story.
//
// Both src and dst are protocol paths (e.g. /csuite/alex/outbox/x.md);
// they are resolved through resolveCsuitePath before any filesystem
// op so tests and prod share the same code path.
func atomicCopyFile(src, dst string) error {
	realDst := resolveCsuitePath(dst)
	realSrc := resolveCsuitePath(src)
	if err := os.MkdirAll(filepath.Dir(realDst), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(realDst), err)
	}
	in, err := os.Open(realSrc) //nolint:gosec // src validated upstream
	if err != nil {
		return fmt.Errorf("open src %s: %w", realSrc, err)
	}
	defer in.Close()

	tmp := realDst + ".tmp"
	// O_EXCL prevents two concurrent routing attempts from trampling
	// each other at the temp path. The per-destination mutex wired
	// in commit 4 makes collisions rare but still possible during
	// rescan / crash-recovery replays.
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create tmp %s: %w", tmp, err)
	}
	cleanup := func() { _ = os.Remove(tmp) }
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		cleanup()
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		cleanup()
		return fmt.Errorf("fsync: %w", err)
	}
	if err := out.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, realDst); err != nil {
		cleanup()
		return fmt.Errorf("rename %s -> %s: %w", tmp, realDst, err)
	}
	return nil
}

// quarantinePath returns the protocol destination path for a file
// that could not be classified. Layout is
// /csuite/quarantine/<source>/<basename> per plan §5. The returned
// path is protocol-style (always rooted at /csuite) so ledger rows
// remain portable across different csuiteRoot redirections.
func quarantinePath(sourcePersona, outboxPath string) string {
	return "/csuite/quarantine/" + sourcePersona + "/" + filepath.Base(outboxPath)
}
