package dir

import (
	"container/heap"
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/amrshadid/magnetar/internal/blob"
	"github.com/amrshadid/magnetar/internal/fault"
)

// readDirChunk is how many directory entries are read at a time. A
// directory is never read whole, so a directory of a million files costs
// the same memory to list as one of ten.
const readDirChunk = 256

// List returns one page of keys; see blob.Store.
//
// A file system returns a directory's names in no useful order, and sorting
// a whole directory would hold all of its names at once. So each step of a
// listing reads a directory through, in chunks, keeping only the smallest
// names after the point already reached, as many as the page still needs,
// in a bounded heap. Memory is bounded by the page size times the depth of
// the tree. A directory with more entries than one page is read once per
// page, which is the price of not holding it.
//
// The token is the last key or prefix of the page before. It needs no state
// kept between calls, and it continues correctly whatever was put or deleted
// meanwhile, since the order is that of the keys themselves.
func (s *Store) List(ctx context.Context, opts blob.ListOptions) (blob.Page, error) {
	if err := s.begin(ctx); err != nil {
		return blob.Page{}, err
	}
	if err := blob.CheckListOptions(opts); err != nil {
		return blob.Page{}, err
	}
	if err := checkToken(opts); err != nil {
		return blob.Page{}, err
	}
	// The prefix is a directory, given by its whole segments, and a filter
	// on the names in it, given by the partial segment after the last "/".
	base, filter := ".", opts.Prefix
	if i := strings.LastIndexByte(opts.Prefix, '/'); i >= 0 {
		base, filter = opts.Prefix[:i], opts.Prefix[i+1:]
	}
	if err := s.checkDirs(base); err != nil {
		if notFound(err) {
			return blob.Page{}, nil
		}
		return blob.Page{}, s.fail(err, "the storage could not examine the listing's directory")
	}
	l := &lister{
		s:     s,
		ctx:   ctx,
		after: opts.Token,
		want:  blob.ListLimit(opts) + 1,
		delim: opts.Delimiter != "",
	}
	keyPrefix := ""
	if base != "." {
		keyPrefix = base + "/"
	}
	if err := l.dir(base, keyPrefix, filter); err != nil {
		return blob.Page{}, err
	}
	var page blob.Page
	limit := l.want - 1
	if len(l.out) > limit {
		l.out = l.out[:limit]
		page.Next = l.out[limit-1].name
	}
	for _, e := range l.out {
		if e.isDir {
			page.Prefixes = append(page.Prefixes, e.name)
		} else {
			page.Keys = append(page.Keys, e.name)
		}
	}
	return page, nil
}

// checkToken refuses a token this listing could not have returned: one
// that is not a key or, with a delimiter, a key followed by "/", or that
// does not begin with the prefix.
func checkToken(opts blob.ListOptions) error {
	t := opts.Token
	if t == "" {
		return nil
	}
	bad := !utf8.ValidString(t) || !strings.HasPrefix(t, opts.Prefix)
	if !bad {
		k := t
		if opts.Delimiter != "" {
			k = strings.TrimSuffix(t, "/")
		}
		bad = blob.CheckKey(k) != nil
	}
	if bad {
		return fault.New(fault.Invalid, "storage listing: the page token is not one this listing returned")
	}
	return nil
}

// entry is a key, or with a delimiter a prefix, by its whole text: a
// directory's entry ends with "/", which is also where it sorts among the
// keys, since every key under it begins with that text.
type entry struct {
	name  string
	isDir bool
}

// lister fills one page.
type lister struct {
	s     *Store
	ctx   context.Context
	after string // the page token: every entry returned sorts after it
	want  int    // the page size plus one, to know whether another page follows
	delim bool
	out   []entry
}

// dir adds to the page, in order, the entries under the directory dirPath,
// whose keys begin with keyPrefix, keeping only names that begin with
// filter.
func (l *lister) dir(dirPath, keyPrefix, filter string) error {
	cursor := ""
	for len(l.out) < l.want {
		batch, more, err := l.smallest(dirPath, keyPrefix, filter, cursor, l.want-len(l.out))
		if err != nil {
			return err
		}
		for _, e := range batch {
			cursor = e.name
			switch {
			case !e.isDir:
				l.out = append(l.out, e)
			case l.delim:
				has, err := l.s.hasObject(l.ctx, path.Join(dirPath, strings.TrimPrefix(strings.TrimSuffix(e.name, "/"), keyPrefix)))
				if err != nil {
					return err
				}
				if has {
					l.out = append(l.out, e)
				}
			default:
				name := strings.TrimSuffix(strings.TrimPrefix(e.name, keyPrefix), "/")
				if err := l.dir(path.Join(dirPath, name), e.name, ""); err != nil {
					return err
				}
			}
			if len(l.out) >= l.want {
				return nil
			}
		}
		if !more {
			return nil
		}
	}
	return nil
}

// smallest reads dirPath through and returns, in order, up to n of its
// entries that sort after cursor and that the page still needs, and whether
// there may be more.
//
// An entry is needed when something it stands for sorts after the token: a
// file whose key does, a prefix that does, or, without a delimiter, a
// directory whose text the token begins with, since keys inside it may
// still follow the token.
func (l *lister) smallest(dirPath, keyPrefix, filter, cursor string, n int) ([]entry, bool, error) {
	if err := l.ctx.Err(); err != nil {
		return nil, false, fault.Wrap(fault.Unavailable, err, "the listing was canceled")
	}
	h := &entryHeap{}
	more := false
	err := l.s.eachEntry(dirPath, func(d fs.DirEntry) {
		name := d.Name()
		if !strings.HasPrefix(name, filter) {
			return
		}
		e := entry{name: keyPrefix + name}
		switch {
		case d.Type().IsRegular():
		case d.IsDir():
			e.isDir = true
			e.name += "/"
		default:
			return // A symbolic link or a device is not an object.
		}
		if e.name <= cursor {
			return
		}
		needed := e.name > l.after || (e.isDir && !l.delim && strings.HasPrefix(l.after, e.name))
		if !needed {
			return
		}
		if h.Len() < n {
			heap.Push(h, e)
			return
		}
		more = true
		if e.name < (*h)[0].name {
			(*h)[0] = e
			heap.Fix(h, 0)
		}
	})
	if err != nil {
		if notFound(err) {
			return nil, false, nil // Removed by a Delete since it was seen.
		}
		return nil, false, l.s.fail(err, "the storage could not read a directory")
	}
	out := []entry(*h)
	slices.SortFunc(out, func(a, b entry) int { return strings.Compare(a.name, b.name) })
	return out, more, nil
}

// eachEntry calls fn for each entry of the directory dirPath that could be
// part of a key, reading the directory a chunk at a time. It skips a name
// that is not valid as a key's segment, which takes in the driver's own
// temporary files and anything another program left that no key could
// name.
func (s *Store) eachEntry(dirPath string, fn func(fs.DirEntry)) error {
	f, err := s.openDir(s.root, native(dirPath))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() // Only read.
	for {
		entries, err := f.ReadDir(readDirChunk)
		for _, d := range entries {
			if blob.CheckKey(d.Name()) == nil {
				fn(d)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// hasObject reports whether any object lies under the directory dirPath, so
// that a directory left empty, or holding only a Put's temporary file, is
// not listed as a prefix: on a bucket, a prefix exists only while a key
// under it does. It stops at the first object it finds.
func (s *Store) hasObject(ctx context.Context, dirPath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fault.Wrap(fault.Unavailable, err, "the listing was canceled")
	}
	f, err := s.openDir(s.root, native(dirPath))
	if notFound(err) {
		return false, nil
	}
	if err != nil {
		return false, s.fail(err, "the storage could not read a directory")
	}
	defer func() { _ = f.Close() }() // Only read.
	for {
		entries, err := f.ReadDir(readDirChunk)
		for _, d := range entries {
			if blob.CheckKey(d.Name()) != nil {
				continue
			}
			if d.Type().IsRegular() {
				return true, nil
			}
			if d.IsDir() {
				has, err := s.hasObject(ctx, path.Join(dirPath, d.Name()))
				if err != nil || has {
					return has, err
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, s.fail(err, "the storage could not read a directory")
		}
	}
}

// entryHeap is a max-heap of entries by name, so its top is the largest of
// the smallest n seen so far, the one to drop when a smaller comes.
type entryHeap []entry

func (h entryHeap) Len() int           { return len(h) }
func (h entryHeap) Less(i, j int) bool { return h[i].name > h[j].name }
func (h entryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *entryHeap) Push(x any)        { *h = append(*h, x.(entry)) }
func (h *entryHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}

// cleanTemp removes every file in the temporary directory: none of them is
// an object, and every one is a Put that a crash, a kill or a failed
// removal left behind. It reads that one directory a chunk at a time, and
// reads it again while the reading before removed something, since a
// system may skip names in a directory read while names are removed from
// it. It never walks the tree, so Open costs the same for ten objects as
// for a hundred million.
//
// A directory a crashed Put made for its key and never filled is not
// removed here; it holds no object, so it is never listed as a prefix, and
// the next Delete or failed Put in it removes it.
func (s *Store) cleanTemp() error {
	for {
		removed, err := s.cleanTempOnce()
		if err != nil {
			return fault.Wrap(fault.Unavailable, err, "the storage could not remove what a crash left in its temporary directory")
		}
		if !removed {
			return nil
		}
		if err := s.syncDir(s.root, tempDir); err != nil {
			return fault.Wrap(fault.Unavailable, err, "the storage could not sync its temporary directory")
		}
	}
}

func (s *Store) cleanTempOnce() (bool, error) {
	f, err := s.openDir(s.root, tempDir)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }() // Only read.
	removed := false
	for {
		entries, rerr := f.ReadDir(readDirChunk)
		for _, d := range entries {
			if !d.Type().IsRegular() {
				continue // Nothing a Put makes; left for a person to look at.
			}
			if err := s.root.Remove(filepath.Join(tempDir, d.Name())); err != nil && !notFound(err) {
				return removed, err
			}
			removed = true
		}
		if errors.Is(rerr, io.EOF) {
			return removed, nil
		}
		if rerr != nil {
			return removed, rerr
		}
	}
}
