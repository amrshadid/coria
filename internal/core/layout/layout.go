package layout

import (
	"errors"
	"fmt"
	"strings"

	"github.com/amrshadid/coria/internal/core/label"
	"github.com/amrshadid/coria/internal/core/uid"
)

// The roots of the two trees, with their trailing slash, as a storage lists
// them by prefix.
const (
	DataRoot = "data/"
	MetaRoot = "meta/"
)

// The names of the two files of a scan's meta directory, and the extension
// of an instance's file.
const (
	ManifestName = "manifest.json"
	MetadataName = "metadata.json"
	InstanceExt  = ".dcm"
)

// MaxKeyLength is the longest key this package builds: a data key with
// three labels of 128 bytes and a UID of 64 is 460 bytes. Parse refuses a
// longer string before looking into it.
const MaxKeyLength = len(DataRoot) + 3*(label.MaxLength+1) + uid.MaxLength + len(InstanceExt)

// Kind says which of the three files of a scan a key names.
type Kind int

// The kinds.
const (
	// Instance is an instance's DICOM file under data/.
	Instance Kind = iota + 1
	// Manifest is a scan's manifest under meta/.
	Manifest
	// Metadata is a scan's sidecar of headers under meta/.
	Metadata
)

// String returns the kind's name.
func (k Kind) String() string {
	switch k {
	case Instance:
		return "instance"
	case Manifest:
		return "manifest"
	case Metadata:
		return "metadata"
	}
	return "unknown"
}

// Key is a key of the layout in parts.
type Key struct {
	// Kind is which file it names.
	Kind Kind
	// Path is the scan the file belongs to.
	Path label.Path
	// SOPInstanceUID is the instance's UID for Instance, and empty
	// otherwise.
	SOPInstanceUID string
}

// ErrKey is the error Parse returns, wrapped, for any string that is not a
// key of this layout.
var ErrKey = errors.New("layout: not a key of the layout")

// String builds the key, or returns an error if a part is not valid.
func (k Key) String() (string, error) {
	if err := k.Path.Check(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrKey, err)
	}
	dir := k.Path.Subject + "/" + k.Path.Session + "/" + k.Path.Scan + "/"
	switch k.Kind {
	case Instance:
		if err := uid.Check(k.SOPInstanceUID); err != nil {
			return "", fmt.Errorf("%w: %w", ErrKey, err)
		}
		return DataRoot + dir + k.SOPInstanceUID + InstanceExt, nil
	case Manifest, Metadata:
		if k.SOPInstanceUID != "" {
			return "", fmt.Errorf("%w: a %s key has no SOP Instance UID", ErrKey, k.Kind)
		}
		name := ManifestName
		if k.Kind == Metadata {
			name = MetadataName
		}
		return MetaRoot + dir + name, nil
	}
	return "", fmt.Errorf("%w: unknown kind", ErrKey)
}

// InstanceKey returns the key of an instance's file in the scan at p.
func InstanceKey(p label.Path, sopInstanceUID string) (string, error) {
	return Key{Kind: Instance, Path: p, SOPInstanceUID: sopInstanceUID}.String()
}

// ManifestKey returns the key of the manifest of the scan at p.
func ManifestKey(p label.Path) (string, error) { return Key{Kind: Manifest, Path: p}.String() }

// MetadataKey returns the key of the sidecar of headers of the scan at p.
func MetadataKey(p label.Path) (string, error) { return Key{Kind: Metadata, Path: p}.String() }

// InstancePrefix returns the prefix, ending in "/", under which a storage
// lists the instance files of the scan at p.
func InstancePrefix(p label.Path) (string, error) {
	if err := p.Check(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrKey, err)
	}
	return DataRoot + p.Subject + "/" + p.Session + "/" + p.Scan + "/", nil
}

// Parse returns the parts of key, or an error wrapping ErrKey if key is not
// exactly what Key.String builds for some valid parts.
func Parse(key string) (Key, error) {
	if len(key) > MaxKeyLength {
		return Key{}, fmt.Errorf("%w: longer than any key", ErrKey)
	}
	f := strings.Split(key, "/")
	if len(f) != 5 {
		return Key{}, fmt.Errorf("%w: not five segments", ErrKey)
	}
	k := Key{Path: label.Path{Subject: f[1], Session: f[2], Scan: f[3]}}
	switch root, name := f[0]+"/", f[4]; {
	case root == DataRoot && strings.HasSuffix(name, InstanceExt):
		k.Kind, k.SOPInstanceUID = Instance, strings.TrimSuffix(name, InstanceExt)
	case root == MetaRoot && name == ManifestName:
		k.Kind = Manifest
	case root == MetaRoot && name == MetadataName:
		k.Kind = Metadata
	default:
		return Key{}, fmt.Errorf("%w: neither an instance under data/ nor a scan file under meta/", ErrKey)
	}
	// The round trip is the check: whatever Parse took apart must build
	// back to the same bytes, so Parse can never accept a form String
	// would not produce.
	s, err := k.String()
	if err != nil {
		return Key{}, err
	}
	if s != key {
		return Key{}, fmt.Errorf("%w: does not build back to itself", ErrKey)
	}
	return k, nil
}
