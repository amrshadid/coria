//go:build unix

package sqlite

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/amrshadid/magnetar/internal/fault"
)

// effectiveUID is os.Geteuid, replaceable by a test that needs a file to
// belong to someone else without being root.
var effectiveUID = os.Geteuid

// checkPrivate refuses a database the master must not use, by the same rule
// as the installation's key file: the database file, its -wal and -shm files
// when they exist, and its directory must belong to the user magnetar runs
// as; nobody else may read or write the files, and nobody else may write the
// directory, where they could replace the file or plant a -wal file of their
// own. The master holds key hashes and sealed database addresses, and a user
// who can write the file can grant themselves any role. A directory others
// can list is allowed, as for the key file: it shows the names, and the
// files themselves stay closed. A file that does not exist yet is made
// private by createPrivate.
//
// The message says how to fix it, without the path: a database address never
// appears in an error, and the operator knows the path they gave.
func checkPrivate(path string) error {
	dir := filepath.Dir(path)
	if err := checkOne(dir, true, "the master database's directory", "chmod go-w"); err != nil {
		return err
	}
	for _, f := range []struct{ path, what string }{
		{path, "the master database file"},
		{path + "-wal", "the master database's -wal file"},
		{path + "-shm", "the master database's -shm file"},
	} {
		if err := checkOne(f.path, false, f.what, "chmod 600"); err != nil {
			return err
		}
	}
	return nil
}

// checkOne refuses path when it exists and belongs to another user, or when
// it gives the group or others read or write access (a file) or write access
// (a directory). A path that does not exist is fine, except a directory,
// which must.
func checkOne(path string, isDir bool, what, fix string) error {
	info, err := os.Stat(path) // #nosec G703 -- the operator's own choice of database path, only examined.
	if errors.Is(err, fs.ErrNotExist) && !isDir {
		return nil
	}
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return fault.New(fault.Unavailable, what+" cannot be checked: "+pathErr.Err.Error())
		}
		return fault.New(fault.Unavailable, what+" cannot be checked")
	}
	if isDir && !info.IsDir() {
		return fault.New(fault.Invalid, what+" is not a directory")
	}
	if !isDir && !info.Mode().IsRegular() {
		return fault.New(fault.Invalid, what+" is not a regular file")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int64(st.Uid) != int64(effectiveUID()) {
		return fault.New(fault.Invalid, what+" belongs to another user; run magnetar as its owner, or chown it to the user magnetar runs as")
	}
	if isDir && info.Mode().Perm()&0o022 != 0 {
		return fault.New(fault.Invalid, what+" can be written by users other than its owner ("+info.Mode().Perm().String()+"); run "+fix+" on it")
	}
	if !isDir && info.Mode().Perm()&0o066 != 0 {
		return fault.New(fault.Invalid, what+" can be read or written by users other than its owner ("+info.Mode().Perm().String()+"); run "+fix+" on it")
	}
	return nil
}
