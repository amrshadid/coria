package dir

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/amrshadid/coria/internal/blob"
	"github.com/amrshadid/coria/internal/fault"
)

// Read returns a range of key as a stream; see blob.Store.
func (s *Store) Read(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := s.begin(ctx); err != nil {
		return nil, err
	}
	if err := blob.CheckKey(key); err != nil {
		return nil, err
	}
	if err := blob.CheckRange(offset, length); err != nil {
		return nil, err
	}
	if err := s.checkKeyDirs(key); err != nil {
		return nil, err
	}
	f, info, err := s.openChecked(native(key), false)
	if err != nil {
		return nil, s.objectError(err, "the storage could not open the object")
	}
	size := info.Size()
	if offset > size {
		_ = f.Close() // Only read; the range error is the answer.
		return nil, blob.ErrPastEnd()
	}
	n := size - offset
	if length >= 0 && length < n {
		n = length
	}
	return &section{r: io.NewSectionReader(f, offset, n), f: f, s: s}, nil
}

// section is a range of an open file that closes the file. Its errors are
// rewritten like every other error of the store, so none names the root.
type section struct {
	r *io.SectionReader
	f *os.File
	s *Store
}

// Read reads the range.
func (r *section) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = r.s.wrap(fault.Unavailable, err, "the storage could not read the object")
	}
	return n, err
}

// Close closes the file under the stream.
func (r *section) Close() error {
	if err := r.f.Close(); err != nil {
		return r.s.wrap(fault.Unavailable, err, "the storage could not close the object")
	}
	return nil
}

// Errors of openChecked that are about what a name holds, not about the
// file system.
var (
	errWrongType     = errors.New("the name holds something of another type than was opened for")
	errLinked        = errors.New("the name is a symbolic link")
	errKeptReplacing = errors.New("the name kept being replaced while it was opened")
)

// openChecked opens name for reading and returns it with what the open
// descriptor is, only when that is a regular file (or, with wantDir, a
// directory) and is the very file the name holds, examined without
// following a link.
//
// The open comes first, and does not block (O_NONBLOCK on Unix), so a FIFO
// or a device swapped in for the name cannot hang it; the type is then
// taken from the descriptor, never from an earlier look at the name. The
// name is examined after the open and compared with the descriptor: a
// symbolic link there is refused, and an object replaced by a Put in
// between is opened again. Each try is spent only when a Put won that race,
// so the tries are not a timing guess.
func (s *Store) openChecked(name string, wantDir bool) (*os.File, fs.FileInfo, error) {
	for range 64 {
		f, err := s.open(s.root, name)
		if err != nil {
			return nil, nil, err
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close() // Only read; the Stat error is the answer.
			return nil, nil, err
		}
		if (wantDir && !info.IsDir()) || (!wantDir && !info.Mode().IsRegular()) {
			_ = f.Close() // Only read; not what was asked for.
			return nil, nil, errWrongType
		}
		named, err := s.root.Lstat(name)
		if notFound(err) {
			_ = f.Close() // Only read; removed or replaced since the open.
			continue
		}
		if err != nil {
			_ = f.Close() // Only read; the Lstat error is the answer.
			return nil, nil, err
		}
		if os.SameFile(named, info) {
			return f, info, nil
		}
		_ = f.Close() // Only read; the name now holds another file.
		if named.Mode()&fs.ModeSymlink != 0 {
			return nil, nil, errLinked
		}
	}
	return nil, nil, errKeptReplacing
}

// checkKeyDirs examines the directories of key without following a link,
// and says NotFound when one is missing or is a file.
func (s *Store) checkKeyDirs(key string) error {
	if err := s.checkDirs(parent(key)); err != nil {
		if notFound(err) {
			return notFoundError()
		}
		return s.fail(err, "the storage could not examine the object's directories")
	}
	return nil
}

// objectError turns an error from opening or examining an object into one
// with a kind: nothing there, or something there that is not an object (a
// directory, a link, a FIFO), is NotFound.
func (s *Store) objectError(err error, message string) error {
	switch {
	case notFound(err), errors.Is(err, errWrongType), errors.Is(err, errLinked):
		return notFoundError()
	case errors.Is(err, errKeptReplacing):
		return fault.New(fault.Unavailable, "the object kept being replaced while it was opened")
	}
	return s.fail(err, message)
}

// lstatObject returns what is at key, examined without following a link, or
// NotFound when that is nothing, a directory, or not a regular file.
func (s *Store) lstatObject(key string) (fs.FileInfo, error) {
	if err := s.checkKeyDirs(key); err != nil {
		return nil, err
	}
	info, err := s.root.Lstat(native(key))
	if err != nil {
		return nil, s.objectError(err, "the storage could not examine the object")
	}
	if !info.Mode().IsRegular() {
		return nil, notFoundError()
	}
	return info, nil
}

// Stat returns the size of key; see blob.Store.
func (s *Store) Stat(ctx context.Context, key string) (blob.Info, error) {
	if err := s.begin(ctx); err != nil {
		return blob.Info{}, err
	}
	if err := blob.CheckKey(key); err != nil {
		return blob.Info{}, err
	}
	info, err := s.lstatObject(key)
	if err != nil {
		return blob.Info{}, err
	}
	return blob.Info{Size: info.Size()}, nil
}
