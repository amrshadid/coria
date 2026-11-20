package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"maps"
	"strconv"
	"sync"
	"time"

	"github.com/amrshadid/coria/internal/access"
	"github.com/amrshadid/coria/internal/blob"
	"github.com/amrshadid/coria/internal/fault"
	"github.com/amrshadid/coria/internal/place"
	"github.com/amrshadid/coria/internal/tenant"
)

// What the reading of archived bytes shares between ReadFiles and the plain
// HTTP download (data.proto, DataService): the bound of open reads, the
// recheck of the caller's key while one is open, the storages a read opens,
// and the copy that checks an instance's bytes against its manifest.

// The bounds of reading archived bytes (data.proto, "Limits").
const (
	// ChunkSize is the most bytes one FileChunk of ReadFiles holds: 1 MiB
	// in this version, under the 3 MiB a client accepts and well under the
	// 4 MB a stock gRPC client takes for a whole message.
	ChunkSize = 1 << 20
	// MaxOpenReads is the most streams of ReadFiles and downloads one
	// caller, a key or the local operator, holds open at once.
	MaxOpenReads = 16
	// MaxServerReads is the most the server holds open at once, for every
	// caller together: each holds a storage's connection or file and two
	// buffers of ChunkSize.
	MaxServerReads = 256
	// StallTimeout ends a download whose client reads nothing for this
	// long, so a client that stalls does not hold a storage's connection
	// for ever.
	StallTimeout = 5 * time.Minute
)

// httpChunk is the most bytes a download reads from the storage and writes
// to the connection at once: a quarter of ChunkSize, so a download holds
// half a MiB of buffers, and each write is large enough that a gigabyte
// costs a few thousand system calls.
const httpChunk = ChunkSize / 4

// openReads counts the reads of archived bytes open now, per owner and in
// all. Its zero value is ready to use. One process serves an installation
// (the lock on its data directory), so a count in memory is the bound.
type openReads struct {
	mu      sync.Mutex
	byOwner map[string]int
	total   int
}

// take counts one more read for owner and returns the function that ends
// it, or an Exhausted error when owner or the server is at its bound. The
// read is refused, never queued: a client keeps a small pool of downloads
// and the one past the bound is told so.
func (o *openReads) take(owner string) (func(), error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.total >= MaxServerReads {
		return nil, fault.New(fault.Exhausted, "the server holds as many streams and downloads open as it serves at once; try again later")
	}
	if o.byOwner[owner] >= MaxOpenReads {
		return nil, fault.New(fault.Exhausted, "the caller holds "+strconv.Itoa(MaxOpenReads)+" streams and downloads open already; end one first")
	}
	if o.byOwner == nil {
		o.byOwner = map[string]int{}
	}
	o.byOwner[owner]++
	o.total++
	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			if o.byOwner[owner]--; o.byOwner[owner] <= 0 {
				delete(o.byOwner, owner)
			}
			o.total--
		})
	}, nil
}

// watchKey returns ctx canceled, with errRefusedWhileOpen as its cause,
// once the caller's key or grant may no longer read, checked again every
// RecheckInterval, and the function that ends the checks. A long read is
// then ended with the answer a new request would get, which Confirm
// records. The function returns once the checking goroutine has, so no
// check of a request runs after its handler returned.
func (c *core) watchKey(ctx context.Context, d access.Decision) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		t := time.NewTicker(RecheckInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := c.access.Confirm(ctx, d); err != nil {
					cancel(errRefusedWhileOpen{err})
					return
				}
			}
		}
	}()
	return ctx, func() {
		cancel(nil)
		<-ended
	}
}

// errRefusedWhileOpen is the cause of a read or a stream ended because its
// caller's key or grant may no longer read, with the refusal.
type errRefusedWhileOpen struct{ err error }

// Error returns the refusal's message.
func (e errRefusedWhileOpen) Error() string { return e.err.Error() }

// refusedWhileOpen returns the refusal that ended ctx, when the caller's
// key or grant was found unable to read while the read was open.
func refusedWhileOpen(ctx context.Context) (error, bool) {
	var refused errRefusedWhileOpen
	if errors.As(context.Cause(ctx), &refused) {
		return refused.err, true
	}
	return nil, false
}

// readStores opens the storages a read reaches, each once, for the tenant
// whose pipeline it holds, and closes them at its end.
type readStores struct {
	c          *core
	p          tenant.Pipeline
	tenantName string
	tenant     place.Tenant
	open       map[string]blob.Store
}

// newReadStores returns the storages of the tenant named name, none open
// yet.
func (c *core) newReadStores(p tenant.Pipeline, name string) *readStores {
	return &readStores{c: c, p: p, tenantName: name, open: map[string]blob.Store{}}
}

// get returns the storage named name, opening it on first use. A storage
// that cannot be opened is the storage not answering or the server's
// fault, as the opener says.
func (s *readStores) get(ctx context.Context, name string) (blob.Store, error) {
	if st, ok := s.open[name]; ok {
		return st, nil
	}
	if s.tenant.ID == "" {
		row, err := s.c.master.TenantByName(ctx, s.tenantName)
		if err != nil {
			return nil, err
		}
		s.tenant = place.TenantOf(row.ID, s.tenantName, s.p)
	}
	srow, err := s.p.Storage(ctx, name)
	if err != nil {
		return nil, err
	}
	st, err := s.c.opener().OpenForReading(ctx, s.tenant, srow)
	if err != nil {
		return nil, err
	}
	s.open[name] = st
	return st, nil
}

// close closes every storage opened.
func (s *readStores) close() {
	for _, st := range s.open {
		_ = st.Close() // Only read from; nothing to lose.
	}
}

// The ways the bytes of an instance on its storage are found not to be
// what its manifest records. Each is data loss: the catalog holds the
// instance, and its bytes are not passed on as good.
var (
	errShort    = errors.New("the instance's bytes on the storage are fewer than its manifest records")
	errLong     = errors.New("the instance's bytes on the storage are more than its manifest records")
	errChecksum = errors.New("the instance's bytes on the storage do not have the SHA-256 its manifest records")
)

// copyChecked passes the bytes r yields to emit, in pieces of at most the
// length of each buffer, checking that they are exactly length bytes and,
// when sum is not nil, that their SHA-256 is *sum. The last piece is
// passed only after both checks, so the bytes of an instance that differs
// from its manifest never reach the client whole: errShort, errLong or
// errChecksum is returned instead, with the bytes passed before it
// counted. An error of r or of emit is returned as it is. The two buffers
// take turns: one holds the piece not yet passed while the other is read
// into.
//
// The SHA-256 of a body of more than one piece is computed on its own
// goroutine while the piece before is passed: one SHA-256 runs at about
// the speed of a socket's writes (some 2 GB/s on a core with the SHA
// instructions), so hashing and writing one after the other would halve
// the rate. Neither buffer is read into again before the hash of what it
// held is done, and copyChecked returns only once the hash has let go of
// both, so a caller may reuse them at once.
func copyChecked(r io.Reader, length int64, sum *tenant.SHA256, bufs [2][]byte, emit func([]byte) error) (int64, error) {
	var h *pipedHash
	if sum != nil {
		h = newPipedHash(length > int64(len(bufs[0])))
		defer h.stop()
	}
	r = io.LimitReader(r, length+1)
	var (
		read, passed int64
		held         []byte
		turn         int
	)
	for {
		b := bufs[turn]
		if h != nil {
			// b held the piece before held; its hash must be done before
			// it is read into.
			h.waitBehind(1)
		}
		n, err := io.ReadFull(r, b)
		if n > 0 {
			read += int64(n)
			if read > length {
				// Past the length: the piece held, which ends at the
				// length, is not passed, so the body never looks whole.
				return passed, errLong
			}
			if h != nil {
				h.write(b[:n])
			}
			if held != nil {
				if err := emit(held); err != nil {
					return passed, err
				}
				passed += int64(len(held))
			}
			held, turn = b[:n], 1-turn
		}
		switch {
		case err == nil:
			continue
		case !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF):
			return passed, err
		}
		// The end of what r holds.
		switch {
		case read < length:
			return passed, errShort
		case h != nil && h.sum() != *sum:
			return passed, errChecksum
		}
		if held != nil {
			if err := emit(held); err != nil {
				return passed, err
			}
			passed += int64(len(held))
		}
		return passed, nil
	}
}

// pipedHash is the SHA-256 of copyChecked, computed on the caller's
// goroutine for a body of one piece and on its own for a longer one.
type pipedHash struct {
	h hash.Hash
	// in carries the pieces to the goroutine, done one token per piece
	// hashed, and finished is closed when the goroutine has returned; all
	// three are nil when the hash is computed in line.
	in       chan []byte
	done     chan struct{}
	finished chan struct{}
	// sent and hashed count the pieces given and the pieces done.
	sent, hashed int
}

// newPipedHash returns a hash, with its own goroutine when piped.
func newPipedHash(piped bool) *pipedHash {
	p := &pipedHash{h: sha256.New()}
	if !piped {
		return p
	}
	// At most two pieces are outstanding (waitBehind), so neither channel
	// ever blocks its sender for long, and done never blocks at all.
	p.in, p.done, p.finished = make(chan []byte, 2), make(chan struct{}, 2), make(chan struct{})
	go func() {
		defer close(p.finished)
		for b := range p.in {
			_, _ = p.h.Write(b) // A hash never fails.
			p.done <- struct{}{}
		}
	}()
	return p
}

// write adds b to the hash. When piped, b must not change until the hash
// has gone past it (waitBehind).
func (p *pipedHash) write(b []byte) {
	if p.in == nil {
		_, _ = p.h.Write(b) // A hash never fails.
		return
	}
	p.in <- b
	p.sent++
}

// waitBehind waits until at most n pieces given are not yet hashed.
func (p *pipedHash) waitBehind(n int) {
	for p.in != nil && p.sent-p.hashed > n {
		<-p.done
		p.hashed++
	}
}

// sum returns the SHA-256 of every piece given.
func (p *pipedHash) sum() tenant.SHA256 {
	p.waitBehind(0)
	return tenant.SHA256(p.h.Sum(nil))
}

// stop ends the goroutine and waits for it, so no piece is read after
// copyChecked returns.
func (p *pipedHash) stop() {
	if p.in == nil {
		return
	}
	close(p.in)
	<-p.finished
	p.in = nil
}

// mismatched reports whether err is one of copyChecked's findings that the
// bytes differ from the manifest.
func mismatched(err error) bool {
	return errors.Is(err, errShort) || errors.Is(err, errLong) || errors.Is(err, errChecksum)
}

// withDetails returns base with the pairs of more added, base unchanged.
// An audit entry's facts replace the check's details as a whole, so a
// later entry of a request carries its request's details again.
func withDetails(base map[string]string, more map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(more))
	maps.Copy(out, base)
	maps.Copy(out, more)
	return out
}
