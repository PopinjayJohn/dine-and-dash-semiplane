package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
)

// Hash is the SHA-256 of a file's bytes, which is how the sync engine answers
// "did this change?" without parsing anything.
//
// It is a function over bytes rather than a method on a Document because the
// comparison the indexer makes is between a file it has just read and a row it
// already has, and one of those is not a document yet.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// tempPrefix marks a file this package is in the middle of writing.
//
// It starts with a dot so a crash that leaves one behind is invisible in
// Obsidian's file list, and it says what it is so that a DM who finds one in a
// text editor can delete it without wondering what it was.
const tempPrefix = ".wiki-tmp-"

// crashPoint names a step in an atomic write. It exists so a test can fail at
// that step, because "the write is atomic" is otherwise a claim about a process
// dying at a moment no test can reach.
type crashPoint string

const (
	// crashBeforeTemp is a death before anything has been written.
	crashBeforeTemp crashPoint = "before the temporary file"

	// crashAfterTemp is a death with a temporary file written and the original
	// untouched.
	crashAfterTemp crashPoint = "after the temporary file is written"

	// crashAfterTempSync is a death with the temporary file on disk. The
	// original is still untouched, and the new content has not moved.
	crashAfterTempSync crashPoint = "after the temporary file is synced"

	// crashAfterRename is a death with the rename done and the directory not
	// synced. The new content is in place and complete; whether it survives a
	// power cut is a different question, and one the fsync is there to answer.
	crashAfterRename crashPoint = "after the rename"
)

// errCrash marks a simulated death, so a test can tell "the write died here"
// from "the write failed", and so the cleanup can skip what a real death skips.
var errCrash = errors.New("vault: simulated crash")

// errCrashed is what a simulated death returns. A caller will never see it in
// production, and the message says where it happened, because a test failure
// that reads "simulated crash after the rename" saves an hour of looking.
func errCrashed(at crashPoint) error {
	return fmt.Errorf("%w %s", errCrash, at)
}

// writeAtomic replaces the vault-relative name with data, so a reader sees
// either the bytes that were there or the bytes that are now, and never a
// mixture.
//
// The sequence is the one ADR 0001 fixes: a temporary file in the same
// directory, fsync, rename, fsync the directory. Each step earns its place:
//
//   - The same directory, because a rename across a filesystem is a copy, and a
//     copy is not atomic. A temporary file in /tmp is a copy.
//   - fsync before the rename, so the content is on disk before anything points
//     at it. Renaming first and syncing later can leave a file that exists and
//     is empty.
//   - fsync the directory after, because a rename is a directory entry. Without
//     it the rename itself can be lost by a power cut, leaving the old file.
//
// Every name is resolved through the directory handle, so a temporary file
// cannot be planted outside the vault by a symlink in the directory being
// written to.
//
// crash is the test seam. It is empty in production, and a non-empty value is
// this package pretending the process died at that step -- which also means
// skipping the cleanup that a real death would skip, so the test sees what a
// crash leaves behind rather than what an error path tidies up.
func (v *Vault) writeAtomic(name string, data []byte) (err error) {
	// A crash before the temporary file leaves nothing at all, which is the
	// only crash that needs no assertion beyond "the old file is still there".
	if v.crash == crashBeforeTemp {
		return errCrashed(v.crash)
	}

	dir := pathDir(name)
	if mkdirErr := v.root.MkdirAll(dir, 0o700); mkdirErr != nil {
		return fmt.Errorf("creating %s: %w", dir, mkdirErr)
	}

	tempName := v.createTemp(dir)
	temp, err := v.root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating the temporary file %s: %w", tempName, err)
	}
	moved := false

	// From here on, an ordinary failure takes the temporary file with it. A
	// simulated crash does not, and skipping that is the entire reason the seam
	// exists: the test has to see what a crash leaves behind, not what an error
	// path tidies up.
	defer func() {
		_ = temp.Close()
		if !moved && !errors.Is(err, errCrash) {
			_ = v.root.Remove(tempName)
		}
	}()

	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("writing %s: %w", tempName, err)
	}

	if v.crash == crashAfterTemp {
		return errCrashed(v.crash)
	}

	if err := temp.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", tempName, err)
	}

	if v.crash == crashAfterTempSync {
		return errCrashed(v.crash)
	}

	if err := temp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tempName, err)
	}

	// A DM's campaign is not a world-readable document, and the handle's
	// CreateTemp already makes files private to this user; saying it here means
	// the mode is a decision rather than a side effect.
	if err := v.root.Chmod(tempName, 0o600); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("setting the mode of %s: %w", tempName, err)
	}

	if err := v.root.Rename(tempName, name); err != nil {
		return fmt.Errorf("replacing %s: %w", name, err)
	}
	moved = true

	if v.crash == crashAfterRename {
		return errCrashed(v.crash)
	}

	return v.syncDir(dir)
}

// tempCounter makes a temporary name unique within this process. Together with
// the process id it is unique across the processes that can share a data
// directory, and O_EXCL turns a collision into an error rather than two writers
// sharing one file -- which would be a page that is neither of them.
var tempCounter atomic.Uint64

// createTemp returns the name of a temporary file in a vault directory, inside
// the vault. The caller creates it.
//
// os.Root has no CreateTemp, so the name is built here. It is a process id and
// a counter rather than random bytes, which keeps this package free of a
// randomness that would have to be injected and tested for no benefit: a
// temporary file name appears in no output and in no index.
func (v *Vault) createTemp(dir string) string {
	name := fmt.Sprintf("%s%d-%d", tempPrefix, os.Getpid(), tempCounter.Add(1))
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

// pathDir is the directory part of a vault-relative name, as the handle wants
// it: a slash, and "." for a name at the top of the vault.
func pathDir(name string) string {
	index := strings.LastIndex(name, "/")
	if index < 0 {
		return "."
	}
	return name[:index]
}

// syncDir makes a rename durable by syncing the directory entry that records
// it.
//
// Windows has no way to sync a directory, and a rename there is durable enough
// for a file the DM is looking at in Obsidian, so the failure is not turned into
// a write that appears to have failed. Everywhere else a failure to sync is
// reported, because that is a machine that is losing writes.
func (v *Vault) syncDir(dir string) error {
	handle, err := v.root.Open(dir)
	if err != nil {
		return fmt.Errorf("opening %s to sync it: %w", dir, err)
	}
	defer func() { _ = handle.Close() }()

	if err := handle.Sync(); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("syncing %s: %w", dir, err)
	}
	return nil
}

// isNotExist reports whether an error is a missing file, which is an ordinary
// answer rather than a failure in most of this package.
func isNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}
