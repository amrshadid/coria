package tenant

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amrshadid/magnetar/internal/core/naming"
	"github.com/amrshadid/magnetar/internal/fault"
)

// StorageType is a storage's type. Its text is what a store keeps.
type StorageType string

// The storage types.
const (
	// StorageDirectory is a directory on the server, under a root the
	// installation names.
	StorageDirectory StorageType = "directory"
	// StorageS3 is a bucket of an object store that speaks the S3 API.
	StorageS3 StorageType = "s3"
)

// The bounds of a storage's configuration, the API contract's.
const (
	// MaxCapacityBytes is the largest capacity a storage may state: 2^62.
	MaxCapacityBytes = 1 << 62
	// MaxDirectoryPathLength bounds a directory storage's path.
	MaxDirectoryPathLength = 1024
	// MaxDirectorySegments bounds the segments of that path.
	MaxDirectorySegments = 16
	// MaxSegmentLength bounds one segment.
	MaxSegmentLength = 128
	// MaxEndpointLength bounds an S3 endpoint.
	MaxEndpointLength = 2048
	// MaxRegionLength bounds an S3 region.
	MaxRegionLength = 64
	// MaxPrefixLength bounds an S3 prefix.
	MaxPrefixLength = 512
	// MaxAccessKeyIDLength bounds an S3 access key id.
	MaxAccessKeyIDLength = 128
	// MaxSealedSecretLength bounds a sealed secret access key: a secret of
	// at most 128 bytes sealed (package secret) is far shorter.
	MaxSealedSecretLength = 1 << 10
)

// Storage is a place a tenant's archived files are written to. Exactly the
// configuration of its Type is meaningful; the other is zero.
type Storage struct {
	// Name is the storage's name, under the rule of a tenant's name.
	Name string
	// Type is its type.
	Type StorageType
	// Directory is a directory storage's configuration.
	Directory DirectoryStorage
	// S3 is an S3 storage's configuration.
	S3 S3Storage
	// CapacityBytes is the most bytes it may hold, 0 for no limit of its
	// own.
	CapacityBytes int64
	// Created is when it was added; Updated when it last changed. Set by
	// the store from the time a change is given.
	Created, Updated time.Time
}

// DirectoryStorage is where a directory storage is: a root the installation
// names, and a path below the tenant's directory in it.
type DirectoryStorage struct {
	// Root is the name of the root, under the rule of a tenant's name.
	Root string
	// Path is segments joined by "/", empty for the tenant's directory.
	Path string
}

// S3Storage is where an S3 storage is and the credentials it uses.
type S3Storage struct {
	// Endpoint is the store's base URL, as the caller normalized it.
	Endpoint string
	// Region is the bucket's region.
	Region string
	// Bucket is the bucket's name.
	Bucket string
	// Prefix is where every key of the storage starts, empty or ending
	// with "/".
	Prefix string
	// PathStyle says the bucket is addressed in the URL's path.
	PathStyle bool
	// AccessKeyID names the credentials; it is not a secret.
	AccessKeyID string
	// SealedSecret is the secret access key sealed by the caller (package
	// secret), given to a change only; a store never returns it.
	SealedSecret []byte
	// SecretSet says whether a secret is held. Answered by a store; ignored
	// in a change.
	SecretSet bool
}

// StorageField names one field of a storage a partial change sets, as the
// API contract's update mask names it.
type StorageField string

// The fields of a storage a change can name.
const (
	FieldCapacityBytes   StorageField = "capacity_bytes"
	FieldDirectoryRoot   StorageField = "directory.root"
	FieldDirectoryPath   StorageField = "directory.path"
	FieldS3Endpoint      StorageField = "s3.endpoint"
	FieldS3Region        StorageField = "s3.region"
	FieldS3Bucket        StorageField = "s3.bucket"
	FieldS3Prefix        StorageField = "s3.prefix"
	FieldS3PathStyle     StorageField = "s3.path_style"
	FieldS3AccessKeyID   StorageField = "s3.access_key_id"
	FieldS3SecretKey     StorageField = "s3.secret_access_key"
	maxStorageFieldCount              = 10
)

// storageFields says, for each field, the type it belongs to (empty for
// both) and whether it says where the storage's files are.
var storageFields = map[StorageField]struct {
	typ      StorageType
	location bool
}{
	FieldCapacityBytes: {"", false},
	FieldDirectoryRoot: {StorageDirectory, true},
	FieldDirectoryPath: {StorageDirectory, true},
	FieldS3Endpoint:    {StorageS3, true},
	FieldS3Region:      {StorageS3, false},
	FieldS3Bucket:      {StorageS3, true},
	FieldS3Prefix:      {StorageS3, true},
	FieldS3PathStyle:   {StorageS3, false},
	FieldS3AccessKeyID: {StorageS3, false},
	FieldS3SecretKey:   {StorageS3, false},
}

// Validate returns an Invalid error unless s can be stored as a new
// storage: every field by the contract's rule, and an S3 storage with a
// secret.
func (s Storage) Validate() error {
	if err := s.validate(); err != nil {
		return err
	}
	if s.Type == StorageS3 && len(s.S3.SealedSecret) == 0 {
		return invalid("an S3 storage is added with its secret access key")
	}
	return nil
}

// validate checks every field but the presence of a secret, which a stored
// storage holds and a change need not repeat.
func (s Storage) validate() error {
	if err := ValidateName("storage", s.Name); err != nil {
		return err
	}
	if s.CapacityBytes < 0 || s.CapacityBytes > MaxCapacityBytes {
		return invalid("a storage's capacity is 0 to 2^62 bytes")
	}
	switch s.Type {
	case StorageDirectory:
		if !s.S3.isZero() {
			return invalid("a directory storage has no S3 configuration")
		}
		if err := ValidateName("root", s.Directory.Root); err != nil {
			return err
		}
		return validateDirectoryPath(s.Directory.Path)
	case StorageS3:
		if s.Directory != (DirectoryStorage{}) {
			return invalid("an S3 storage has no directory configuration")
		}
		return s.S3.validate()
	}
	return invalid("a storage's type is directory or s3")
}

// isZero reports whether c holds nothing; S3Storage holds a slice, so it is
// not comparable with ==.
func (c S3Storage) isZero() bool {
	return c.Endpoint == "" && c.Region == "" && c.Bucket == "" && c.Prefix == "" && !c.PathStyle &&
		c.AccessKeyID == "" && len(c.SealedSecret) == 0 && !c.SecretSet
}

func (c S3Storage) validate() error {
	if c.Endpoint == "" || len(c.Endpoint) > MaxEndpointLength ||
		!allBytes(c.Endpoint, func(b byte) bool { return b > 0x20 && b < 0x7f }) {
		return invalid("an S3 endpoint is a URL of 1 to 2048 bytes of printable ASCII")
	}
	if c.Region == "" || len(c.Region) > MaxRegionLength ||
		!allBytes(c.Region, func(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' }) {
		return invalid("an S3 region is 1 to 64 of a-z, 0-9 and -")
	}
	if err := validateBucket(c.Bucket); err != nil {
		return err
	}
	if err := validatePrefix(c.Prefix); err != nil {
		return err
	}
	if c.AccessKeyID == "" || len(c.AccessKeyID) > MaxAccessKeyIDLength ||
		!allBytes(c.AccessKeyID, func(b byte) bool { return b > 0x20 && b < 0x7f }) {
		return invalid("an S3 access key id is 1 to 128 bytes of printable ASCII")
	}
	if len(c.SealedSecret) > MaxSealedSecretLength {
		return invalid("a sealed secret access key is at most " + strconv.Itoa(MaxSealedSecretLength) + " bytes")
	}
	return nil
}

// validateDirectoryPath applies the contract's rule of a directory storage's
// path: at most 16 segments and 1024 bytes, each 1 to 128 of letters,
// digits, ".", "_" and "-", not starting with a dot; empty for the tenant's
// directory.
func validateDirectoryPath(p string) error {
	if p == "" {
		return nil
	}
	const rule = "a directory storage's path is at most 16 segments and 1024 bytes, each 1 to 128 of A-Z, a-z, 0-9, ., _ and -, not starting with a dot"
	segs := strings.Split(p, "/")
	if len(p) > MaxDirectoryPathLength || len(segs) > MaxDirectorySegments {
		return invalid(rule)
	}
	for _, seg := range segs {
		if seg == "" || len(seg) > MaxSegmentLength || seg[0] == '.' || !allBytes(seg, pathByte) {
			return invalid(rule)
		}
	}
	return nil
}

// validateBucket applies the S3 API's rule of a bucket's name, as the
// contract states it.
func validateBucket(b string) error {
	ok := len(b) >= 3 && len(b) <= 63 && allBytes(b, func(c byte) bool {
		return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '-'
	})
	if ok {
		first, last := b[0], b[len(b)-1]
		ok = first != '.' && first != '-' && last != '.' && last != '-'
	}
	if !ok {
		return invalid("an S3 bucket is 3 to 63 of a-z, 0-9, . and -, starting and ending with a letter or a digit")
	}
	return nil
}

// validatePrefix applies the contract's rule of an S3 prefix.
func validatePrefix(p string) error {
	if p == "" {
		return nil
	}
	const rule = "an S3 prefix is at most 512 of A-Z, a-z, 0-9, ., _, - and /, ending with / and with no empty, . or .. part"
	if len(p) > MaxPrefixLength || p[len(p)-1] != '/' || !allBytes(p, func(b byte) bool { return pathByte(b) || b == '/' }) {
		return invalid(rule)
	}
	for _, part := range strings.Split(p[:len(p)-1], "/") {
		if part == "" || part == "." || part == ".." {
			return invalid(rule)
		}
	}
	return nil
}

// ApplyStorage returns stored with the fields named set to their values in
// change, after checking the fields against the storage's type and the
// contract's rules of a partial change, and whether a field that says where
// the files are was named. A driver calls it inside its transaction with
// the row it read, so every driver applies a change the same way.
func ApplyStorage(stored, change Storage, fields []StorageField) (Storage, bool, error) {
	if len(fields) == 0 {
		return stored, false, invalid("a change of a storage names at least one field")
	}
	if len(fields) > maxStorageFieldCount {
		return stored, false, invalid("a change of a storage names each field once")
	}
	out := stored
	out.S3.SealedSecret = nil
	location := false
	for _, f := range fields {
		info, ok := storageFields[f]
		if !ok {
			return stored, false, invalid("a change of a storage names a field it does not have: " + strconv.Quote(string(f)))
		}
		if info.typ != "" && info.typ != stored.Type {
			return stored, false, invalid("a storage's type cannot change: " + string(f) + " is not a field of a " + string(stored.Type) + " storage")
		}
		location = location || info.location
		switch f {
		case FieldCapacityBytes:
			out.CapacityBytes = change.CapacityBytes
		case FieldDirectoryRoot:
			out.Directory.Root = change.Directory.Root
		case FieldDirectoryPath:
			out.Directory.Path = change.Directory.Path
		case FieldS3Endpoint:
			out.S3.Endpoint = change.S3.Endpoint
		case FieldS3Region:
			out.S3.Region = change.S3.Region
		case FieldS3Bucket:
			out.S3.Bucket = change.S3.Bucket
		case FieldS3Prefix:
			out.S3.Prefix = change.S3.Prefix
		case FieldS3PathStyle:
			out.S3.PathStyle = change.S3.PathStyle
		case FieldS3AccessKeyID:
			out.S3.AccessKeyID = change.S3.AccessKeyID
		case FieldS3SecretKey:
			out.S3.SealedSecret = change.S3.SealedSecret
		}
	}
	if slices.Contains(fields, FieldS3AccessKeyID) != slices.Contains(fields, FieldS3SecretKey) {
		return stored, false, invalid("an S3 storage's access key id and secret access key change together")
	}
	if slices.Contains(fields, FieldS3SecretKey) && len(change.S3.SealedSecret) == 0 {
		return stored, false, invalid("a secret access key that is changed is not empty")
	}
	if err := out.validate(); err != nil {
		return stored, false, err
	}
	return out, location, nil
}

// StoragesOverlap reports whether two storages of one tenant would write
// into each other's files: two directory storages under one root whose
// paths are equal or one lies inside the other, segment by segment with
// ASCII case folded (a file system that ignores case makes two spellings
// one directory), or two S3 storages on one endpoint and bucket of which
// one prefix starts with the other.
func StoragesOverlap(a, b Storage) bool {
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case StorageDirectory:
		if a.Directory.Root != b.Directory.Root {
			return false
		}
		pa, pb := strings.ToLower(a.Directory.Path), strings.ToLower(b.Directory.Path)
		return segmentPrefix(pa, pb) || segmentPrefix(pb, pa)
	case StorageS3:
		if a.S3.Endpoint != b.S3.Endpoint || a.S3.Bucket != b.S3.Bucket {
			return false
		}
		return strings.HasPrefix(a.S3.Prefix, b.S3.Prefix) || strings.HasPrefix(b.S3.Prefix, a.S3.Prefix)
	}
	return false
}

// segmentPrefix reports whether path p is q or lies inside it.
func segmentPrefix(p, q string) bool {
	return q == "" || p == q || strings.HasPrefix(p, q+"/")
}

// ReceiverType is a receiver's type. Its text is what a store keeps.
type ReceiverType string

// ReceiverDIMSE is a DICOM listener on one port (PS3.8, PS3.7).
const ReceiverDIMSE ReceiverType = "dimse"

// The bounds of a receiver's configuration, the API contract's.
const (
	MinQuietPeriod   = 5 * time.Second
	MaxQuietPeriod   = time.Hour
	MinMaxOpenTime   = time.Minute
	MaxMaxOpenTime   = 24 * time.Hour
	MinMaxFileBytes  = 1024
	MaxMaxFileBytes  = 1 << 38
	MaxCallerRules   = 256
	MaxSOPClasses    = 512
	MaxTransferSyns  = 64
	MaxTLSFileLength = 255
	MaxAddressRange  = 64
)

// The defaults the contract gives a receiver that leaves them out; the
// caller fills them before a receiver reaches a store, which stores what it
// is given.
const (
	DefaultQuietPeriod  = 60 * time.Second
	DefaultMaxOpenTime  = time.Hour
	DefaultMaxFileBytes = 1 << 32
)

// Receiver is a way for data to arrive at a tenant, and how what arrives is
// handled.
type Receiver struct {
	// Name is the receiver's name, under the rule of a tenant's name.
	Name string
	// Type is its type; DIMSE holds that type's configuration.
	Type ReceiverType
	// DIMSE is a DIMSE receiver's configuration.
	DIMSE DIMSEReceiver
	// Enabled says it accepts data.
	Enabled bool
	// Storage names the storage what it receives is archived to.
	Storage string
	// Naming is its naming rule, nil for the default (naming.DefaultSpec).
	Naming *naming.Spec
	// QuietPeriod is how long without a file before a reception is
	// complete: whole seconds, 5 seconds to 1 hour.
	QuietPeriod time.Duration
	// MaxOpenTime is the longest a reception stays receiving: whole
	// seconds, 1 minute to 24 hours, not less than QuietPeriod.
	MaxOpenTime time.Duration
	// MaxFileBytes is the largest file it accepts, 1024 to 2^38.
	MaxFileBytes int64
	// Created is when it was added; Updated when it last changed.
	Created, Updated time.Time
}

// DIMSEReceiver is a DICOM listener's configuration.
type DIMSEReceiver struct {
	// Port is the TCP port, 1 to 65535.
	Port int
	// BindAddress is the server's address it listens on, empty for the
	// installation's default.
	BindAddress string
	// AETitle is the AE title a caller must call.
	AETitle string
	// AnyCaller admits every caller; when false, Callers lists those
	// admitted, and an empty list admits nobody.
	AnyCaller bool
	// Callers are the rules a caller must match one of.
	Callers []CallerRule
	// TLS is where its certificate and key are, nil for no TLS.
	TLS *ReceiverTLS
	// SOPClasses are the SOP Class UIDs it accepts, empty for the server's
	// default set.
	SOPClasses []string
	// TransferSyntaxes are the Transfer Syntax UIDs it accepts, empty for
	// the server's default set.
	TransferSyntaxes []string
}

// CallerRule admits callers by calling AE title, address range, or both.
type CallerRule struct {
	// CallingAETitle is the AE title a caller must give, empty for any.
	CallingAETitle string
	// AddressRange is a range in CIDR form with its host bits zero, empty
	// for any address.
	AddressRange string
}

// ReceiverTLS names the files of a receiver's certificate and key in the
// tenant's TLS directory.
type ReceiverTLS struct {
	// CertificateFile is the certificate chain's file name.
	CertificateFile string
	// PrivateKeyFile is the private key's file name.
	PrivateKeyFile string
}

// ReceiverField names one field of a receiver a partial change sets, as the
// API contract's update mask names it.
type ReceiverField string

// The fields of a receiver a change can name.
const (
	FieldEnabled          ReceiverField = "enabled"
	FieldStorage          ReceiverField = "storage"
	FieldNaming           ReceiverField = "naming"
	FieldQuietPeriod      ReceiverField = "quiet_period"
	FieldMaxOpenTime      ReceiverField = "max_open_time"
	FieldMaxFileBytes     ReceiverField = "max_file_bytes"
	FieldDIMSEPort        ReceiverField = "dimse.port"
	FieldDIMSEBindAddress ReceiverField = "dimse.bind_address"
	FieldDIMSEAETitle     ReceiverField = "dimse.ae_title"
	FieldDIMSECallers     ReceiverField = "dimse.allowed_callers"
	FieldDIMSEAnyCaller   ReceiverField = "dimse.any_caller"
	FieldDIMSETLS         ReceiverField = "dimse.tls"
	FieldDIMSETLSCert     ReceiverField = "dimse.tls.certificate_file"
	FieldDIMSETLSKey      ReceiverField = "dimse.tls.private_key_file"
	FieldDIMSESOPClasses  ReceiverField = "dimse.sop_classes"
	FieldDIMSESyntaxes    ReceiverField = "dimse.transfer_syntaxes"
	maxReceiverFieldCount               = 16
)

// Validate returns an Invalid error unless r can be stored, by the
// contract's rule of each field. Whether its storage exists and whether its
// port is free are the store's to check, in its transaction.
func (r Receiver) Validate() error {
	if err := ValidateName("receiver", r.Name); err != nil {
		return err
	}
	if err := ValidateName("storage", r.Storage); err != nil {
		return err
	}
	if r.QuietPeriod < MinQuietPeriod || r.QuietPeriod > MaxQuietPeriod || r.QuietPeriod%time.Second != 0 {
		return invalid("a quiet period is a whole number of seconds from 5 seconds to 1 hour")
	}
	if r.MaxOpenTime < MinMaxOpenTime || r.MaxOpenTime > MaxMaxOpenTime || r.MaxOpenTime%time.Second != 0 {
		return invalid("a longest open time is a whole number of seconds from 1 minute to 24 hours")
	}
	if r.MaxOpenTime < r.QuietPeriod {
		return invalid("a longest open time is not less than the quiet period")
	}
	if r.MaxFileBytes < MinMaxFileBytes || r.MaxFileBytes > MaxMaxFileBytes {
		return invalid("a largest file is 1024 to 2^38 bytes")
	}
	if r.Naming != nil {
		if _, err := naming.Parse(*r.Naming); err != nil {
			return fault.Wrap(fault.Invalid, err, "the naming rule is not valid: "+err.Error())
		}
	}
	if r.Type != ReceiverDIMSE {
		return invalid("a receiver's type is dimse")
	}
	return r.DIMSE.validate()
}

func (d DIMSEReceiver) validate() error {
	if d.Port < 1 || d.Port > 65535 {
		return invalid("a port is 1 to 65535")
	}
	if d.BindAddress != "" {
		a, err := netip.ParseAddr(d.BindAddress)
		if err != nil || a.Zone() != "" || a.String() != d.BindAddress {
			return invalid("a bind address is an IPv4 address in dotted form or an IPv6 address in the form of RFC 5952, without a zone")
		}
	}
	if err := ValidateAETitle(d.AETitle); err != nil {
		return err
	}
	if d.AnyCaller && len(d.Callers) > 0 {
		return invalid("a receiver admits any caller or the callers it lists, not both")
	}
	if len(d.Callers) > MaxCallerRules {
		return invalid("a receiver lists at most 256 caller rules")
	}
	for _, c := range d.Callers {
		if c.CallingAETitle == "" && c.AddressRange == "" {
			return invalid("a caller rule names an AE title, an address range, or both")
		}
		if c.CallingAETitle != "" {
			if err := ValidateAETitle(c.CallingAETitle); err != nil {
				return err
			}
		}
		if c.AddressRange != "" {
			p, err := netip.ParsePrefix(c.AddressRange)
			if err != nil || len(c.AddressRange) > MaxAddressRange || p.Masked() != p {
				return invalid("an address range is in CIDR form with its host bits zero")
			}
		}
	}
	if d.TLS != nil {
		for _, f := range []string{d.TLS.CertificateFile, d.TLS.PrivateKeyFile} {
			if f == "" || len(f) > MaxTLSFileLength || f[0] == '.' || !allBytes(f, pathByte) {
				return invalid("a TLS file's name is 1 to 255 of A-Z, a-z, 0-9, ., _ and -, not starting with a dot")
			}
		}
	}
	if len(d.SOPClasses) > MaxSOPClasses {
		return invalid("a receiver accepts at most 512 SOP classes")
	}
	for _, u := range d.SOPClasses {
		if err := validateUID("SOP Class UID", u); err != nil {
			return err
		}
	}
	if len(d.TransferSyntaxes) > MaxTransferSyns {
		return invalid("a receiver accepts at most 64 transfer syntaxes")
	}
	for _, u := range d.TransferSyntaxes {
		if err := validateUID("Transfer Syntax UID", u); err != nil {
			return err
		}
	}
	return nil
}

// ApplyReceiver returns stored with the fields named set to their values in
// change, checked by the contract's rules of a partial change and then as a
// whole. A driver calls it inside its transaction with the row it read.
func ApplyReceiver(stored, change Receiver, fields []ReceiverField) (Receiver, error) {
	if len(fields) == 0 {
		return stored, invalid("a change of a receiver names at least one field")
	}
	if len(fields) > maxReceiverFieldCount {
		return stored, invalid("a change of a receiver names each field once")
	}
	out := stored
	out.DIMSE.Callers = slices.Clone(stored.DIMSE.Callers)
	out.DIMSE.SOPClasses = slices.Clone(stored.DIMSE.SOPClasses)
	out.DIMSE.TransferSyntaxes = slices.Clone(stored.DIMSE.TransferSyntaxes)
	if stored.DIMSE.TLS != nil {
		t := *stored.DIMSE.TLS
		out.DIMSE.TLS = &t
	}
	if stored.Naming != nil {
		n := *stored.Naming
		out.Naming = &n
	}
	tlsPart := func() *ReceiverTLS {
		if out.DIMSE.TLS == nil {
			out.DIMSE.TLS = &ReceiverTLS{}
		}
		return out.DIMSE.TLS
	}
	for _, f := range fields {
		switch f {
		case FieldEnabled:
			out.Enabled = change.Enabled
		case FieldStorage:
			out.Storage = change.Storage
		case FieldNaming:
			out.Naming = nil
			if change.Naming != nil {
				n := *change.Naming
				out.Naming = &n
			}
		case FieldQuietPeriod:
			out.QuietPeriod = change.QuietPeriod
		case FieldMaxOpenTime:
			out.MaxOpenTime = change.MaxOpenTime
		case FieldMaxFileBytes:
			out.MaxFileBytes = change.MaxFileBytes
		case FieldDIMSEPort:
			out.DIMSE.Port = change.DIMSE.Port
		case FieldDIMSEBindAddress:
			out.DIMSE.BindAddress = change.DIMSE.BindAddress
		case FieldDIMSEAETitle:
			out.DIMSE.AETitle = change.DIMSE.AETitle
		case FieldDIMSECallers:
			out.DIMSE.AnyCaller = false
			out.DIMSE.Callers = slices.Clone(change.DIMSE.Callers)
		case FieldDIMSEAnyCaller:
			if !change.DIMSE.AnyCaller {
				return stored, invalid("any_caller, when named, is true")
			}
			out.DIMSE.AnyCaller, out.DIMSE.Callers = true, nil
		case FieldDIMSETLS:
			out.DIMSE.TLS = nil
			if change.DIMSE.TLS != nil {
				t := *change.DIMSE.TLS
				out.DIMSE.TLS = &t
			}
		case FieldDIMSETLSCert:
			cert := ""
			if change.DIMSE.TLS != nil {
				cert = change.DIMSE.TLS.CertificateFile
			}
			tlsPart().CertificateFile = cert
		case FieldDIMSETLSKey:
			key := ""
			if change.DIMSE.TLS != nil {
				key = change.DIMSE.TLS.PrivateKeyFile
			}
			tlsPart().PrivateKeyFile = key
		case FieldDIMSESOPClasses:
			out.DIMSE.SOPClasses = slices.Clone(change.DIMSE.SOPClasses)
		case FieldDIMSESyntaxes:
			out.DIMSE.TransferSyntaxes = slices.Clone(change.DIMSE.TransferSyntaxes)
		default:
			return stored, invalid("a change of a receiver names a field it does not have: " + strconv.Quote(string(f)))
		}
	}
	if err := out.Validate(); err != nil {
		return stored, err
	}
	return out, nil
}

// ReceiversClash reports whether two DIMSE receivers of one tenant would
// listen on one port and address: the same port, and the same bind address
// or either one empty, since an empty bind address overlaps every address.
func ReceiversClash(a, b Receiver) bool {
	return a.Type == ReceiverDIMSE && b.Type == ReceiverDIMSE && a.DIMSE.Port == b.DIMSE.Port &&
		(a.DIMSE.BindAddress == "" || b.DIMSE.BindAddress == "" || a.DIMSE.BindAddress == b.DIMSE.BindAddress)
}
