package api

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/amrshadid/magnetar/internal/blob"
	"github.com/amrshadid/magnetar/internal/blob/dir"
	"github.com/amrshadid/magnetar/internal/blob/s3"
	"github.com/amrshadid/magnetar/internal/core/settings"
	"github.com/amrshadid/magnetar/internal/fault"
	"github.com/amrshadid/magnetar/internal/master"
	"github.com/amrshadid/magnetar/internal/tenant"
)

// MaxSecretAccessKeyLength bounds an S3 storage's secret access key, the
// contract's 128 bytes of printable ASCII.
const MaxSecretAccessKeyLength = 128

// directoryMode is the mode of a directory the server makes for a storage:
// only the user the server runs as may list, enter or write it, so no other
// local user reads a tenant's archive.
const directoryMode = 0o700

// installationValue returns the value of the setting named name that
// applies on the installation. A setting the server's list does not hold, or
// a stored value its rule refuses, is the server's fault: every value is
// checked before it is stored.
func (c *core) installationValue(ctx context.Context, name string) (string, error) {
	def, ok := c.settings.Lookup(name)
	if !ok {
		return "", fault.New(fault.Internal, "api: the server's list of settings has no "+name)
	}
	v, _, err := c.onInstallation(ctx, def)
	if err != nil {
		return "", err
	}
	if def.Validate(v) != nil {
		return "", fault.New(fault.Internal, "api: the stored "+name+" does not follow its rule")
	}
	return v, nil
}

// tenantValue returns the value of the setting named name that applies on
// the tenant whose store is st: its own, else the installation's (none for a
// setting with no installation value), else the built-in default.
func (c *core) tenantValue(ctx context.Context, st tenant.Store, name string) (string, error) {
	def, ok := c.settings.Lookup(name)
	if !ok {
		return "", fault.New(fault.Internal, "api: the server's list of settings has no "+name)
	}
	v, err := st.Setting(ctx, name)
	switch {
	case fault.KindOf(err) == fault.NotFound && def.TenantOnly:
		v = def.Default
	case fault.KindOf(err) == fault.NotFound:
		return c.installationValue(ctx, name)
	case err != nil:
		return "", err
	}
	if def.Validate(v) != nil {
		return "", fault.New(fault.Internal, "api: the tenant's stored "+name+" does not follow its rule")
	}
	return v, nil
}

// directoryRoots returns the installation's storage.directory-roots.
func (c *core) directoryRoots(ctx context.Context) (settings.DirectoryRoots, error) {
	v, err := c.installationValue(ctx, settings.StorageDirectoryRoots)
	if err != nil {
		return nil, err
	}
	return settings.ParseDirectoryRoots(v)
}

// place is where a directory storage's directory is on the server.
type place struct {
	// root is the root's directory, every symbolic link in the root's own
	// path resolved: the root's path is the installation's to choose, and
	// may pass through one.
	root string
	// segments are the directory's below the root: the tenant's name, then
	// the storage's path.
	segments []string
	// existing is how many of the segments exist, from the first.
	existing int
}

// dir returns the directory's path.
func (p place) dir() string { return filepath.Join(append([]string{p.root}, p.segments...)...) }

// exists reports whether the directory itself exists.
func (p place) exists() bool { return p.existing == len(p.segments) }

// errNoRoot answers a root the installation does not list.
func errNoRoot(name string) error {
	return precondition("storage.directory.root: the installation lists no directory root named " + name)
}

// resolvePlace returns where the directory of a directory storage of the
// tenant named tenantName is, under the installation's roots, and refuses,
// as a precondition, a root the installation does not list or that is not
// there, and a path below the root that passes through a symbolic link or a
// file. Each existing segment is looked at with Lstat, which follows no
// link, so a link anywhere below the root is found whatever it points at,
// inside the root or outside it. The root's own path is never in a message.
//
// A link made later, after the storage is added, is found the same way:
// openPlace runs this again every time it opens the directory, and the
// directory driver then follows no link below the directory it is given
// (package blob/dir). A link swapped in between this check and the open is
// the window left; the driver's os.Root still keeps every file it opens
// inside the directory it opened.
func resolvePlace(roots settings.DirectoryRoots, tenantName string, ds tenant.DirectoryStorage) (place, error) {
	root, ok := roots.Lookup(ds.Root)
	if !ok {
		return place{}, errNoRoot(ds.Root)
	}
	if !filepath.IsAbs(root.Path) {
		return place{}, precondition("storage.directory.root: the root " + ds.Root + " is not an absolute path on this server")
	}
	real, err := filepath.EvalSymlinks(root.Path)
	if err != nil {
		return place{}, fault.Wrap(fault.Precondition, pathless(err), "storage.directory.root: the directory of the root "+ds.Root+" is not there on this server")
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return place{}, precondition("storage.directory.root: the root " + ds.Root + " is not a directory on this server")
	}
	p := place{root: real, segments: []string{tenantName}}
	if ds.Path != "" {
		p.segments = append(p.segments, strings.Split(ds.Path, "/")...)
	}
	cur := real
	for _, seg := range p.segments {
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return p, nil
		case err != nil:
			return place{}, fault.Wrap(fault.Unavailable, pathless(err), "storage.directory: the server could not examine the storage's directory")
		case info.Mode()&fs.ModeSymlink != 0:
			return place{}, precondition("storage.directory.path: the directory's path passes through a symbolic link below the root, which is refused")
		case !info.IsDir():
			return place{}, precondition("storage.directory.path: a file that is not a directory is where the directory's path goes")
		}
		p.existing++
	}
	return p, nil
}

// checkEmpty refuses, as a precondition, a directory that exists and holds
// anything: a storage is added on a directory that is absent or empty, so it
// never takes over files it did not write.
func checkEmpty(p place) error {
	if !p.exists() {
		return nil
	}
	f, err := os.Open(p.dir())
	if err != nil {
		return fault.Wrap(fault.Unavailable, pathless(err), "storage.directory: the server could not read the storage's directory")
	}
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(1)
	switch {
	case errors.Is(err, io.EOF) || (err == nil && len(names) == 0):
		return nil
	case err != nil:
		return fault.Wrap(fault.Unavailable, pathless(err), "storage.directory: the server could not read the storage's directory")
	}
	return precondition("storage.directory: the directory is not empty; a storage is added on a directory that is absent or empty")
}

// makePlace makes the segments of p that do not exist, each with
// directoryMode, through an os.Root on the root, which refuses a path that
// would leave it. Each segment is looked at again after it is made, with
// Lstat, so a link that took a segment's place meanwhile is refused.
func makePlace(p place) error {
	r, err := os.OpenRoot(p.root)
	if err != nil {
		return fault.Wrap(fault.Unavailable, pathless(err), "storage.directory: the server could not open the root's directory")
	}
	defer func() { _ = r.Close() }()
	for i := p.existing; i < len(p.segments); i++ {
		rel := filepath.Join(p.segments[:i+1]...)
		if err := r.Mkdir(rel, directoryMode); err != nil && !errors.Is(err, fs.ErrExist) {
			if errors.Is(err, fs.ErrPermission) {
				return fault.Wrap(fault.Precondition, pathless(err), "storage.directory: the server may not make the storage's directory under the root")
			}
			return fault.Wrap(fault.Unavailable, pathless(err), "storage.directory: the server could not make the storage's directory")
		}
		info, err := r.Lstat(rel)
		if err != nil {
			return fault.Wrap(fault.Unavailable, pathless(err), "storage.directory: the server could not examine the directory it made")
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return precondition("storage.directory.path: the directory's path passes through a symbolic link below the root, which is refused")
		}
	}
	return nil
}

// overlapsPlace reports whether two directories are one or lie one inside
// the other, compared after resolution, segment by segment with case
// folded: the port compares the paths it stores under one root; this
// compares where they are, so two roots whose paths reach one directory are
// seen too.
func overlapsPlace(a, b string) bool {
	a, b = strings.ToLower(filepath.Clean(a)), strings.ToLower(filepath.Clean(b))
	sep := string(filepath.Separator)
	return a == b || strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
}

// pathless returns err with no path in its text, for a cause that goes to
// the log: a root's path is the installation's and a log line is read more
// widely than the settings. errors.Is still sees its cause.
func pathless(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return fault.Wrap(fault.Internal, pe.Err, pe.Op)
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return fault.Wrap(fault.Internal, le.Err, le.Op)
	}
	return err
}

// s3Endpoint is an S3 storage's endpoint as the installation allows it.
type s3Endpoint struct {
	// url is the endpoint in the one spelling the port keeps.
	url string
	// entry is the installation's entry for it.
	entry settings.S3Endpoint
}

// allow returns the address check of connections to the endpoint: every
// address for an entry that allows private addresses, else the public ones
// only (s3.AllowPublic).
func (e s3Endpoint) allow() func(netip.Addr) bool {
	if e.entry.PrivateAddress {
		return func(netip.Addr) bool { return true }
	}
	return s3.AllowPublic
}

// findEndpoint returns the installation's entry for an S3 endpoint a
// request gives, both put in the one spelling the port keeps
// (tenant.NormalizeEndpoint), so two spellings of one endpoint are one
// endpoint and a spelling cannot pass where its other spelling would not.
// An endpoint that is not a valid one is Invalid, naming field; one the
// installation does not list, or an http one its entry does not allow, is a
// precondition.
func (c *core) findEndpoint(ctx context.Context, field, endpoint string) (s3Endpoint, error) {
	norm, err := tenant.NormalizeEndpoint(endpoint)
	if err != nil {
		return s3Endpoint{}, fieldOf(field, err)
	}
	v, err := c.installationValue(ctx, settings.StorageS3Endpoints)
	if err != nil {
		return s3Endpoint{}, err
	}
	entries, err := settings.ParseS3Endpoints(v)
	if err != nil {
		return s3Endpoint{}, fault.New(fault.Internal, "api: the stored "+settings.StorageS3Endpoints+" does not follow its rule")
	}
	for _, e := range entries {
		if n, err := tenant.NormalizeEndpoint(e.URL); err == nil && n == norm {
			if strings.HasPrefix(norm, "http://") && !e.PlainHTTP {
				return s3Endpoint{}, precondition(field + ": the installation's entry for this endpoint does not allow plain http")
			}
			return s3Endpoint{url: norm, entry: e}, nil
		}
	}
	return s3Endpoint{}, precondition(field + ": the installation does not list this endpoint")
}

// secretAccessKey returns an Invalid error naming field unless secret is a
// secret access key the contract accepts: 1 to 128 bytes of printable
// ASCII. The message never holds the secret.
func secretAccessKey(field, secret string) error {
	if secret == "" {
		return invalid(field, "is required and not empty")
	}
	if len(secret) > MaxSecretAccessKeyLength {
		return invalid(field, "is at most 128 bytes")
	}
	for i := 0; i < len(secret); i++ {
		if secret[i] <= 0x20 || secret[i] >= 0x7f {
			return invalid(field, "is printable ASCII")
		}
	}
	return nil
}

// secretContext is what an S3 storage's sealed secret is bound to: the
// tenant's id and the storage's name, so a sealed secret copied to another
// storage's row, or another tenant's, does not open there (package secret:
// the context is part of what the seal authenticates).
func secretContext(tenantID, storage string) string {
	return "tenant/" + tenantID + "/storage/" + storage + "/s3-secret"
}

// sealSecret seals an S3 storage's secret access key for its row.
func (c *core) sealSecret(tenantID, storage, secret string) ([]byte, error) {
	sealed, err := c.keys.Seal(secretContext(tenantID, storage), []byte(secret))
	if err != nil {
		return nil, fault.Wrap(fault.Internal, err, "api: the secret access key could not be sealed")
	}
	if len(sealed) > tenant.MaxSealedSecretLength {
		return nil, fault.New(fault.Internal, "api: a sealed secret access key is longer than the store keeps")
	}
	return sealed, nil
}

// storageSecrets is what a tenant's store needs, beside tenant.Pipeline, for
// a storage to be opened: the sealed secret access key of an S3 storage,
// which Pipeline.Storage never returns. No store has it yet.
type storageSecrets interface {
	// StorageSecret returns the sealed secret access key of the S3 storage
	// named name, or NotFound.
	StorageSecret(ctx context.Context, name string) ([]byte, error)
}

// errNoSecrets is the failure of a test of an S3 storage on a store that
// cannot hand its sealed secret back.
var errNoSecrets = errors.New("the tenant's store cannot hand back an S3 storage's sealed secret access key")

// serverError is a failure to open a storage that is the server's, not the
// storage's: a database that does not answer, a store that cannot hand back
// a secret, a seal that does not open. TestStorage answers it as the call's
// error, never as a step of the test.
type serverError struct{ err error }

// Error returns the cause's text, for the log.
func (e *serverError) Error() string { return e.err.Error() }

// Unwrap returns the cause, so its kind and message are the error's.
func (e *serverError) Unwrap() error { return e.err }

// openPlace opens the storage s of the tenant named tenantName, of id
// tenantID, through the storage port: a directory storage at its directory,
// checked again as resolvePlace checks it; an S3 storage at its endpoint,
// with the address check its installation entry gives and its secret opened
// from its row. A failure of the server is a *serverError; anything else is
// the storage's: its directory not there or behind a link, its endpoint no
// longer listed, or the driver's own failure.
func (c *core) openPlace(ctx context.Context, p tenant.Pipeline, tenantID, tenantName string, s tenant.Storage) (blob.Store, error) {
	switch s.Type {
	case tenant.StorageDirectory:
		roots, err := c.directoryRoots(ctx)
		if err != nil {
			return nil, &serverError{err}
		}
		pl, err := resolvePlace(roots, tenantName, s.Directory)
		if err != nil {
			return nil, err
		}
		if !pl.exists() {
			return nil, fault.Wrap(fault.NotFound, fs.ErrNotExist, "the storage's directory is not there")
		}
		return dir.Open(pl.dir())
	case tenant.StorageS3:
		ep, err := c.findEndpoint(ctx, "storage.s3.endpoint", s.S3.Endpoint)
		if err != nil {
			if fault.KindOf(err) == fault.Precondition {
				return nil, err
			}
			return nil, &serverError{err}
		}
		sp, ok := p.(storageSecrets)
		if !ok {
			return nil, &serverError{fault.Wrap(fault.Internal, errNoSecrets, "api: an S3 storage cannot be opened")}
		}
		sealed, err := sp.StorageSecret(ctx, s.Name)
		if err != nil {
			return nil, &serverError{err}
		}
		plain, err := c.keys.Open(secretContext(tenantID, s.Name), sealed)
		if err != nil {
			return nil, &serverError{fault.Wrap(fault.Internal, err, "api: an S3 storage's secret access key does not open with the installation key")}
		}
		return s3.Open(ctx, s3.Config{
			Endpoint: ep.url, Region: s.S3.Region, Bucket: s.S3.Bucket, Prefix: s.S3.Prefix, PathStyle: s.S3.PathStyle,
			AccessKeyID: s3.NewSecret(s.S3.AccessKeyID), SecretAccessKey: s3.NewSecret(string(plain)),
			AllowHTTP: ep.entry.PlainHTTP, AllowAddress: ep.allow(),
			// A test touches nothing but its own object; the cleanup of old
			// uploads is the pipeline's, when it opens the storage to use it.
			StaleUploadAge: -1,
		})
	}
	return nil, &serverError{fault.New(fault.Internal, "api: a storage of a type this server does not know")}
}

// tenantStorages returns every storage of a tenant, page after page.
func tenantStorages(ctx context.Context, p tenant.Pipeline) ([]tenant.Storage, error) {
	var out []tenant.Storage
	page := master.Page{Limit: master.MaxPageLimit}
	for i := 0; ; i++ {
		if i == maxGrantPages {
			return nil, fault.New(fault.Internal, "api: a tenant's storages did not end")
		}
		batch, next, err := p.ListStorages(ctx, page)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if next == "" {
			return out, nil
		}
		page.Cursor = next
	}
}

// tenantReceivers returns every receiver of a tenant, page after page.
func tenantReceivers(ctx context.Context, p tenant.Pipeline) ([]tenant.Receiver, error) {
	var out []tenant.Receiver
	page := master.Page{Limit: master.MaxPageLimit}
	for i := 0; ; i++ {
		if i == maxGrantPages {
			return nil, fault.New(fault.Internal, "api: a tenant's receivers did not end")
		}
		batch, next, err := p.ListReceivers(ctx, page)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if next == "" {
			return out, nil
		}
		page.Cursor = next
	}
}

// checkDirectory checks the directory a directory storage s of the tenant
// named tenantName is to be at, as AddStorage, or SetStorage changing where
// its files are, does: the root listed and there, no link below it, the
// directory absent or empty, and no other directory storage of the tenant
// (others, without s itself) at, inside or around it once resolved. It
// returns the place, to be made once every check passed.
func (c *core) checkDirectory(ctx context.Context, tenantName string, s tenant.Storage, others []tenant.Storage) (place, error) {
	roots, err := c.directoryRoots(ctx)
	if err != nil {
		return place{}, err
	}
	pl, err := resolvePlace(roots, tenantName, s.Directory)
	if err != nil {
		return place{}, err
	}
	if err := checkEmpty(pl); err != nil {
		return place{}, err
	}
	for _, o := range others {
		if o.Type != tenant.StorageDirectory || o.Name == s.Name {
			continue
		}
		root, ok := roots.Lookup(o.Directory.Root)
		if !ok {
			continue
		}
		real, err := filepath.EvalSymlinks(root.Path)
		if err != nil {
			continue
		}
		other := filepath.Join(append([]string{real, tenantName}, splitPath(o.Directory.Path)...)...)
		if overlapsPlace(pl.dir(), other) {
			return place{}, precondition("storage.directory: the directory is, contains or lies inside the directory of the storage " + o.Name)
		}
	}
	return pl, nil
}

// splitPath returns the segments of a directory storage's path.
func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}
