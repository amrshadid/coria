package sidecar

import (
	"errors"
	"io"
)

// Added yields the instances a merge adds, in increasing order of SOP
// Instance UID, each once: false when there are no more. It is called once
// per instance, as the merge reaches it, so an instance's data set is made
// only when it is written and memory follows one line, not the scan.
type Added func() (Instance, bool, error)

// List returns an Added that yields the instances given, for a caller that
// has them at hand.
func List(ins []Instance) Added {
	i := 0
	return func() (Instance, bool, error) {
		if i == len(ins) {
			return Instance{}, false, nil
		}
		i++
		return ins[i-1], true, nil
	}
}

// Merge writes to w the instances of the sidecar old (nil for none) and of
// added, in order: an instance of old whose UID is in drop or among added is
// left out, and added's take their place. It returns how many instances it
// wrote. Memory follows the longest line.
//
// Every instance added must have its frame table (ErrIncomplete
// otherwise). The sidecar written is of this version when old is nil or of
// this version, and of version 1 when old is, since the lines it keeps of
// old may have none; Complete is the rewrite that gives them theirs.
func Merge(w io.Writer, old io.Reader, added Added, drop func(sopUID string) bool) (int, error) {
	return merge(w, old, added, drop, false)
}

// Complete is Merge for the rewrite that gives a sidecar of version 1 the
// frame tables its lines lack: added holds a line, with its table, for
// every line of old that has pixel data and no table, and the sidecar
// written is of this version. A line it would keep without a table is
// ErrIncomplete, and what was written to w is then not a sidecar.
func Complete(w io.Writer, old io.Reader, added Added) (int, error) {
	return merge(w, old, added, nil, true)
}

// merge is Merge, writing this version whatever old's when complete.
func merge(w io.Writer, old io.Reader, added Added, drop func(sopUID string) bool, complete bool) (int, error) {
	out := NewWriter(w)
	var rd *Reader
	if old != nil {
		rd = NewReader(old)
	}
	next := func() (Instance, []byte, bool, error) {
		if rd == nil {
			return Instance{}, nil, false, nil
		}
		in, line, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return Instance{}, nil, false, nil
		}
		return in, line, err == nil, err
	}
	cur, line, ok, err := next()
	if err != nil {
		return 0, err
	}
	if rd != nil && rd.version == 1 && !complete {
		out.version = 1
	}
	add, more, err := added()
	if err != nil {
		return 0, err
	}
	for ok || more {
		switch {
		case ok && (!more || cur.SOPInstanceUID < add.SOPInstanceUID):
			if drop == nil || !drop(cur.SOPInstanceUID) {
				if out.count > 0 && cur.SOPInstanceUID <= out.last {
					return 0, ErrOrder
				}
				if out.version == Version && cur.NeedsFrames() {
					return 0, ErrIncomplete
				}
				if err := out.addLine(cur.SOPInstanceUID, line); err != nil {
					return 0, err
				}
			}
			if cur, line, ok, err = next(); err != nil {
				return 0, err
			}
		default:
			if ok && cur.SOPInstanceUID == add.SOPInstanceUID {
				if cur, line, ok, err = next(); err != nil {
					return 0, err
				}
			}
			if err := out.Add(add); err != nil {
				return 0, err
			}
			if add, more, err = added(); err != nil {
				return 0, err
			}
		}
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	return out.count, nil
}
