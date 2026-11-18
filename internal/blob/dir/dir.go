package dir

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/amrshadid/magnetar/internal/blob"
	"github.com/amrshadid/magnetar/internal/fault"
)

// tempDir is the directory at the root where every Put writes its bytes
// before renaming them into place. Its name begins with blob.ReservedPrefix,
// which no key segment may, so no key can name, list or remove it or
// anything in it.
const tempDir = blob.ReservedPrefix + "tmp"

// The modes of what the driver makes. Objects are patient data after
// de-identification and still not for other users of the machine: only the
// user the server runs as may read them.
const (
	fileMode = 0o600
	dirMode  = 0o700
)

// copyBufferSize is the size of the buffer a Put copies through. It is
// large enough that a 256 MB object takes a few hundred writes, and small
// enough that many Puts at once hold little memory.
const copyBufferSize = 1 << 20

var copyBuffers = sync.Pool{New: func() any {
	b := make([]byte, copyBufferSize)
	return &b
}}

// Store is a blob.Store on a directory. Make one with Open.
type Store struct {
	root      *os.Root
	closed    atomic.Bool
	closeOnce sync.Once
	closeErr  error
	steps
}

// steps are the calls whose order makes a Put whole and durable, and the
// reading of directories, as fields so that a test in this package can
// watch them, count them or make one fail. Open uses realSteps.
type steps struct {
	syncFile func(*os.File) error
	syncDir  func(root *os.Root, dir string) error
	rename   func(root *os.Root, from, to string) error
	openDir  func(root *os.Root, dir string) (*os.File, error)
	// sameFS reports whether a and b, examined with Lstat, are on one file
	// system, and whether the system can tell at all.
	sameFS func(a, b fs.FileInfo) (same, known bool)
}

func realSteps() steps {
	return steps{
		syncFile: (*os.File).Sync,
		syncDir:  syncDirectory,
		rename:   renameRetrying,
		openDir:  (*os.Root).Open,
		sameFS:   sameFileSystem,
	}
}

var _ blob.Store = (*Store)(nil)

// Open opens the storage at the directory path, makes its temporary
// directory if it is missing, and removes every temporary file a crash left
// there. It reads that one directory and nothing else, so its cost does not
// grow with the number of objects.
//
// It refuses, as Invalid, a path that does not exist, that is not a
// directory, or in which this process cannot create a file, and a temporary
// directory that is not a plain directory or, where the system can tell, is
// on another file system than the root. A missing
// directory is not made: a storage on a volume that is not mounted would
// otherwise be written to the empty mount point, on another disk, and look
// healthy until the volume came back. The path is the operator's and may be
// a symbolic link; nothing below it is followed.
func Open(dirPath string) (*Store, error) {
	return open(dirPath, realSteps())
}

// open is Open with the steps given, for a test that watches them.
func open(dirPath string, st steps) (*Store, error) {
	info, err := os.Stat(dirPath) // #nosec G703 -- the operator's own choice of storage directory, only examined.
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, fault.Wrap(fault.Invalid, err, "the storage's directory does not exist; make it, or mount its volume")
	case err != nil:
		return nil, fault.Wrap(fault.Unavailable, err, "the storage's directory cannot be examined")
	case !info.IsDir():
		return nil, fault.New(fault.Invalid, "the storage's path is not a directory")
	}
	root, err := os.OpenRoot(dirPath)
	if err != nil {
		return nil, fault.Wrap(fault.Unavailable, err, "the storage's directory cannot be opened")
	}
	s := &Store{root: root, steps: st}
	for _, step := range []func() error{s.prepareTemp, s.probe, s.cleanTemp} {
		if err := step(); err != nil {
			_ = root.Close() // The step's error is the one that matters.
			return nil, err
		}
	}
	return s, nil
}

// prepareTemp makes the temporary directory when it is missing, syncing the
// root so that it survives a crash, and checks that it is a plain directory
// on the root's file system.
//
// A rename cannot cross file systems (rename(2): EXDEV), and copying instead
// would not be whole or nothing, so the temporary directory and the objects
// must share one. Where the system gives a device number (Unix), a temporary
// directory on another device than the root, such as a mount, is refused
// here. A directory below the root that is a mount cannot be found without
// walking the tree; a Put into it fails at its rename, saying why.
func (s *Store) prepareTemp() error {
	info, err := s.root.Lstat(tempDir)
	if notFound(err) {
		if err := s.root.Mkdir(tempDir, dirMode); err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return fault.Wrap(fault.Invalid, err, "the storage's directory cannot be written by the user magnetar runs as")
			}
			return fault.Wrap(fault.Unavailable, err, "the storage could not make its temporary directory")
		}
		if err := s.syncDir(s.root, "."); err != nil {
			return fault.Wrap(fault.Unavailable, err, "the storage could not sync its directory")
		}
		info, err = s.root.Lstat(tempDir)
	}
	if err != nil {
		return fault.Wrap(fault.Unavailable, err, "the storage could not examine its temporary directory")
	}
	if info.Mode().Type() != fs.ModeDir {
		return fault.New(fault.Invalid, "the storage's temporary directory "+tempDir+" is not a plain directory (a link, a mount point or a file); remove it")
	}
	rootInfo, err := s.root.Lstat(".")
	if err != nil {
		return fault.Wrap(fault.Unavailable, err, "the storage could not examine its directory")
	}
	if same, known := s.sameFS(rootInfo, info); known && !same {
		return fault.New(fault.Invalid, "the storage's temporary directory "+tempDir+" is on another file system than the storage's directory; a storage's directory must be one file system")
	}
	return nil
}

// probe creates, syncs and removes a file in the temporary directory, so
// that a root this process cannot write is refused at Open and not at the
// first reception.
func (s *Store) probe() error {
	name, err := tempName()
	if err != nil {
		return err
	}
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fault.Wrap(fault.Invalid, err, "the storage's directory cannot be written by the user magnetar runs as")
		}
		return fault.Wrap(fault.Unavailable, err, "the storage's directory cannot be written")
	}
	_, werr := f.Write([]byte{0})
	serr := f.Sync()
	cerr := f.Close()
	rerr := s.root.Remove(name)
	if err := errors.Join(werr, serr, cerr, rerr); err != nil {
		return fault.Wrap(fault.Unavailable, err, "the storage's directory cannot be written")
	}
	return nil
}

// Close closes the root. A stream from Read that is still open stays
// readable. Closing again returns what the first Close did.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		if err := s.root.Close(); err != nil {
			s.closeErr = fault.Wrap(fault.Unavailable, err, "the storage could not be closed")
		}
	})
	return s.closeErr
}

// begin refuses a call on a closed store or with an ended context.
func (s *Store) begin(ctx context.Context) error {
	if s.closed.Load() {
		return fault.New(fault.Unavailable, "the storage is closed")
	}
	if err := ctx.Err(); err != nil {
		return fault.Wrap(fault.Unavailable, err, "the storage call was canceled")
	}
	return nil
}

// Put writes r to key, whole or not at all; see blob.Store. The steps, and
// why each sync is there, are in the package's documentation.
func (s *Store) Put(ctx context.Context, key string, r io.Reader) (blob.Written, error) {
	if err := s.begin(ctx); err != nil {
		return blob.Written{}, err
	}
	if err := blob.CheckKey(key); err != nil {
		return blob.Written{}, err
	}
	dir := parent(key)
	if err := s.prepareDirs(key, dir); err != nil {
		return blob.Written{}, err
	}
	f, tmp, err := s.createTemp()
	if err != nil {
		return blob.Written{}, err
	}
	done := false
	defer func() {
		if !done {
			_ = f.Close()          // Already closed on some paths; the Put's own error is returned.
			_ = s.root.Remove(tmp) // A temporary file that cannot be removed now is removed at the next Open.
			s.prune(dir)
		}
	}()
	w, err := copyHashed(ctx, f, r)
	if err != nil {
		return blob.Written{}, err
	}
	if err := s.syncFile(f); err != nil {
		return blob.Written{}, fault.Wrap(fault.Unavailable, err, "the storage could not sync the object's bytes")
	}
	if err := f.Close(); err != nil {
		return blob.Written{}, fault.Wrap(fault.Unavailable, err, "the storage could not close the object's file")
	}
	if err := s.moveIntoPlace(tmp, key, dir); err != nil {
		return blob.Written{}, err
	}
	done = true
	// From here the new object is in place. A failure to sync a directory
	// is still reported, so the caller writes it again, since the name
	// might not survive a crash of the machine.
	if err := s.syncDir(s.root, native(dir)); err != nil && !notFound(err) {
		return blob.Written{}, fault.Wrap(fault.Unavailable, err, "the storage wrote the object but could not sync its directory")
	}
	if err := s.syncDir(s.root, tempDir); err != nil {
		return blob.Written{}, fault.Wrap(fault.Unavailable, err, "the storage wrote the object but could not sync its temporary directory")
	}
	return w, nil
}

// moveIntoPlace renames the temporary file tmp to key. A concurrent Delete
// may have removed the key's directory since prepareDirs made it, once it
// was empty again; it is made once more and the rename tried again, and the
// few tries allowed are spent only in that race.
func (s *Store) moveIntoPlace(tmp, key, dir string) error {
	for range 8 {
		err := s.rename(s.root, tmp, native(key))
		switch {
		case err == nil:
			return nil
		case isCrossDevice(err):
			return fault.Wrap(fault.Unavailable, err, "the storage's directory spans more than one file system (a volume is mounted below it); a storage's directory must be one file system")
		case notFound(err):
			if err := s.mkdirs(dir); err != nil {
				return err
			}
			continue
		}
		if info, lerr := s.root.Lstat(native(key)); lerr == nil && info.IsDir() {
			return conflictUnder()
		}
		return fault.Wrap(fault.Unavailable, err, "the storage could not put the object in place")
	}
	return fault.New(fault.Unavailable, "the storage's directories kept being removed while a put used them")
}

// prepareDirs makes the directories of key, and refuses, as Conflict, a
// key whose place is a directory or lies under a file. It runs before any
// byte is read, so a Put that cannot succeed consumes nothing.
func (s *Store) prepareDirs(key, dir string) error {
	if err := s.mkdirs(dir); err != nil {
		return err
	}
	info, err := s.root.Lstat(native(key))
	switch {
	case err == nil && info.IsDir():
		return conflictUnder()
	case err != nil && !notFound(err):
		return fault.Wrap(fault.Unavailable, err, "the storage could not examine the object's place")
	}
	return nil
}

// createTemp creates an empty temporary file in the temporary directory and
// returns it open for writing, with its name relative to the root.
func (s *Store) createTemp() (*os.File, string, error) {
	name, err := tempName()
	if err != nil {
		return nil, "", err
	}
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return nil, "", fault.Wrap(fault.Unavailable, err, "the storage could not create a file in its temporary directory")
	}
	return f, name, nil
}

// mkdirs makes each directory of dir that is missing, and syncs the
// directory it was made in, so the new directory survives a crash with the
// object put in it. A directory that is a symbolic link fails it; one that
// is a file is a Conflict.
func (s *Store) mkdirs(dir string) error {
	if dir == "." {
		return nil
	}
	segs := strings.Split(dir, "/")
	for i := range segs {
		p := strings.Join(segs[:i+1], "/")
		for attempt := 0; ; attempt++ {
			info, err := s.root.Lstat(native(p))
			if err == nil {
				if err := checkDirInfo(info); err != nil {
					return err
				}
				break
			}
			if !notFound(err) {
				return fault.Wrap(fault.Unavailable, err, "the storage could not examine a directory")
			}
			err = s.root.Mkdir(native(p), dirMode)
			if err == nil {
				if err := s.syncDir(s.root, native(parent(p))); err != nil && !notFound(err) {
					return fault.Wrap(fault.Unavailable, err, "the storage could not sync a directory it made")
				}
				break
			}
			// Made meanwhile by another Put: examine it again. Anything
			// else, or a name that keeps flipping, is a failure.
			if !errors.Is(err, fs.ErrExist) || attempt > 8 {
				return fault.Wrap(fault.Unavailable, err, "the storage could not make a directory")
			}
		}
	}
	return nil
}

// checkDirInfo says what is wrong with info, Lstat's answer for a name that
// must be a directory of a key.
func checkDirInfo(info fs.FileInfo) error {
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return linkError()
	case !info.IsDir():
		return fault.New(fault.Conflict, "the storage holds an object where the key needs a directory; a key cannot hold an object while other keys lie under it")
	}
	return nil
}

func conflictUnder() error {
	return fault.New(fault.Conflict, "the storage holds other keys under this key; a key cannot hold an object while other keys lie under it")
}

func linkError() error {
	return fault.New(fault.Internal, "the storage holds a symbolic link where a directory of the key belongs; the driver follows none")
}

// checkDirs examines each directory of dir with Lstat, so that a symbolic
// link planted where a directory belongs is refused instead of followed. It
// returns an error that notFound recognizes when one is missing or is a
// file.
func (s *Store) checkDirs(dir string) error {
	if dir == "." {
		return nil
	}
	segs := strings.Split(dir, "/")
	for i := range segs {
		info, err := s.root.Lstat(native(strings.Join(segs[:i+1], "/")))
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return linkError()
		}
		if !info.IsDir() {
			return syscall.ENOTDIR
		}
	}
	return nil
}

// copyHashed copies r to f, counting and hashing the bytes as they pass,
// and stops when ctx ends. The copy goes through a buffer of its own rather
// than io.Copy, so neither side's ReadFrom or WriteTo can move bytes past
// the hash or past the check of ctx.
func copyHashed(ctx context.Context, f *os.File, r io.Reader) (blob.Written, error) {
	bp := copyBuffers.Get().(*[]byte)
	defer copyBuffers.Put(bp)
	buf := *bp
	h := sha256.New()
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return blob.Written{}, fault.Wrap(fault.Unavailable, err, "the put was canceled")
		}
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				if isFull(err) {
					return blob.Written{}, fault.Wrap(fault.Unavailable, err, "the storage is full")
				}
				return blob.Written{}, fault.Wrap(fault.Unavailable, err, "the storage could not write the object")
			}
			h.Write(buf[:n]) // A hash never returns an error.
			size += int64(n)
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return blob.Written{}, fmt.Errorf("storage put: reading the object: %w", rerr)
		}
	}
	w := blob.Written{Size: size}
	h.Sum(w.SHA256[:0])
	return w, nil
}

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
	f, info, err := s.openObject(key)
	if err != nil {
		return nil, err
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
	return &section{SectionReader: io.NewSectionReader(f, offset, n), f: f}, nil
}

// section is a range of an open file that closes the file.
type section struct {
	*io.SectionReader
	f *os.File
}

// Close closes the file under the stream.
func (s *section) Close() error {
	if err := s.f.Close(); err != nil {
		return fault.Wrap(fault.Unavailable, err, "the storage could not close the object")
	}
	return nil
}

// openObject opens the object at key and returns it with what it is.
//
// The object is examined with Lstat, so a symbolic link where an object
// belongs is not opened, and then opened and compared with what was
// examined: a Put that replaced the object in between is seen and the open
// tried again, so what is returned is always one object, whole. Each try is
// spent only when a Put won that race, so the tries are not a timing
// guess; a key replaced faster than it can be opened, many times over, is
// reported as Unavailable.
func (s *Store) openObject(key string) (*os.File, fs.FileInfo, error) {
	for range 64 {
		info, err := s.lstatObject(key)
		if err != nil {
			return nil, nil, err
		}
		f, err := s.root.Open(native(key))
		if notFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, fault.Wrap(fault.Unavailable, err, "the storage could not open the object")
		}
		opened, err := f.Stat()
		if err != nil {
			_ = f.Close() // Only read; the Stat error is the answer.
			return nil, nil, fault.Wrap(fault.Unavailable, err, "the storage could not examine the object")
		}
		if os.SameFile(info, opened) {
			return f, opened, nil
		}
		_ = f.Close() // Replaced since it was examined; only read.
	}
	return nil, nil, fault.New(fault.Unavailable, "the object kept being replaced while it was opened")
}

// lstatObject returns what is at key, examined without following a link, or
// NotFound when that is nothing, a directory, or not a regular file.
func (s *Store) lstatObject(key string) (fs.FileInfo, error) {
	if err := s.checkDirs(parent(key)); err != nil {
		if notFound(err) {
			return nil, notFoundError()
		}
		return nil, s.fail(err, "the storage could not examine the object's directories")
	}
	info, err := s.root.Lstat(native(key))
	if notFound(err) {
		return nil, notFoundError()
	}
	if err != nil {
		return nil, s.fail(err, "the storage could not examine the object")
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

// Delete removes key and then each directory above it left empty; see
// blob.Store. A symbolic link where the object would be is not an object,
// and is left alone.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := s.begin(ctx); err != nil {
		return err
	}
	if err := blob.CheckKey(key); err != nil {
		return err
	}
	if _, err := s.lstatObject(key); err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return nil
		}
		return err
	}
	dir := parent(key)
	if err := removeRetrying(s.root, native(key)); err != nil && !notFound(err) {
		return fault.Wrap(fault.Unavailable, err, "the storage could not remove the object")
	}
	// Without it a crash could bring the object back.
	if err := s.syncDir(s.root, native(dir)); err != nil && !notFound(err) {
		return fault.Wrap(fault.Unavailable, err, "the storage removed the object but could not sync its directory")
	}
	s.prune(dir)
	return nil
}

// prune removes dir and each directory above it while they are empty, so
// that a prefix disappears with its last key, as it does on a bucket. It
// stops at the first directory that is not empty or cannot be removed: an
// empty directory left behind holds no object, is never listed as a prefix,
// and is removed at the next Open, so no failure here is an error.
func (s *Store) prune(dir string) {
	for dir != "." {
		if err := s.root.Remove(native(dir)); err != nil {
			return
		}
		_ = s.syncDir(s.root, native(parent(dir))) // Durability of a removed empty directory changes no object.
		dir = parent(dir)
	}
}

// Usage reports the space of the volume the root is on; see blob.Store.
func (s *Store) Usage(ctx context.Context) (blob.Usage, error) {
	if err := s.begin(ctx); err != nil {
		return blob.Usage{}, err
	}
	u, err := volumeUsage(s.root.Name())
	if err != nil {
		return blob.Usage{}, fault.Wrap(fault.Unavailable, err, "the storage could not read its volume's free space")
	}
	return u, nil
}

// fail turns an error from the file system into one with a kind.
func (s *Store) fail(err error, message string) error {
	var fe *fault.Error
	switch {
	case errors.As(err, &fe):
		return err
	case errors.Is(err, os.ErrClosed) || s.closed.Load():
		return fault.Wrap(fault.Unavailable, err, "the storage is closed")
	case notFound(err):
		return notFoundError()
	}
	return fault.Wrap(fault.Unavailable, err, message)
}

func notFoundError() error {
	return fault.New(fault.NotFound, "the storage holds no object at this key")
}

// notFound reports whether err means that nothing is at a path: it does not
// exist, or a directory on the way to it is a file.
func notFound(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// parent returns the key of the directory key is in, "." at the top.
func parent(key string) string {
	return path.Dir(key)
}

// native returns key as a path of this system, relative to the root.
func native(key string) string {
	return filepath.FromSlash(key)
}

// tempName returns a new temporary file's path relative to the root, in the
// temporary directory. Its 64 random bits make a clash between two Puts too
// unlikely to plan for, and O_EXCL refuses one anyway.
func tempName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fault.Wrap(fault.Internal, err, "no random bytes for a temporary name")
	}
	return filepath.Join(tempDir, hex.EncodeToString(b[:])), nil
}

// renameRetrying renames from to to, trying again for a short while when
// the system says a file is busy (see isBusy), which on Windows a virus
// scanner or an indexer holding the old file causes for moments at a time.
// On other systems isBusy is always false and this is one rename.
func renameRetrying(root *os.Root, from, to string) error {
	return retryBusy(func() error { return root.Rename(from, to) })
}

// removeRetrying removes name, trying again as renameRetrying does.
func removeRetrying(root *os.Root, name string) error {
	return retryBusy(func() error { return root.Remove(name) })
}

func retryBusy(op func() error) error {
	wait := time.Millisecond
	for {
		err := op()
		if err == nil || !isBusy(err) || wait > time.Second {
			return err
		}
		time.Sleep(wait)
		wait *= 2
	}
}
