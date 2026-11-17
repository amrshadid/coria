//go:build !windows

package secret

import (
	"fmt"
	"os"

	"github.com/amrshadid/magnetar/internal/fault"
)

// privateMode is the only access a key file may give: read and write for its
// owner, nothing for the group or anyone else.
const privateMode = 0o600

// createPrivate creates a new file at path that only its owner may open. It
// fails if anything exists at path (O_EXCL), so it never opens a file or a
// symbolic link someone else placed there. The umask can only remove bits
// from 0600, never add them.
func createPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateMode) //nolint:gosec // G304: the path is built by CreateKeyFile from the operator's own configuration.
}

// checkPrivate refuses a key file that is not a regular file, or whose mode
// gives the group or other users any access. Write access counts as much as
// read access: a user who can replace the key can make the archive seal new
// secrets under a key they know.
func checkPrivate(f *os.File, path string) error {
	info, err := f.Stat()
	if err != nil {
		return fault.Wrap(fault.Internal, err, "secret: the key file "+path+" cannot be checked")
	}
	if !info.Mode().IsRegular() {
		return fault.New(fault.Invalid, "secret: the key file "+path+" is not a regular file")
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fault.New(fault.Invalid, fmt.Sprintf(
			"secret: the key file %s may be opened by other users (mode %04o); "+
				"make it private to its owner with: chmod 600 %s", path, perm, path))
	}
	return nil
}

// syncDir flushes the directory that holds a new key file, so the file's name
// survives a power loss as well as its contents: on Linux and macOS a new
// directory entry is durable only once the directory itself is synced.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the directory of the operator's own key file.
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
