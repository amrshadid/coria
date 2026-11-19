package tenant

import (
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/amrshadid/coria/internal/core/naming"
	"github.com/amrshadid/coria/internal/fault"
)

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

// Admits reports whether the receiver admits a caller that gives the
// Calling AE Title aeTitle from addr (receiver.proto, allowed_callers):
// every caller when it says any caller; otherwise a caller that matches at
// least one rule, where a rule matches when each part it names matches (the
// title compared exactly, case and spaces included, since PS3.8 9.3.2 makes
// it a fixed field the caller fills; the address inside the range). An
// empty list admits nobody, so a receiver is closed until a person lists a
// caller, and an address that is not valid matches no range. An IPv4
// address carried as IPv6 (::ffff:a.b.c.d, as a listener on both families
// sees an IPv4 caller) is compared as the IPv4 address it is.
func (d DIMSEReceiver) Admits(aeTitle string, addr netip.Addr) bool {
	if d.AnyCaller {
		return true
	}
	addr = addr.Unmap().WithZone("")
	for _, rule := range d.Callers {
		if rule.CallingAETitle != "" && rule.CallingAETitle != aeTitle {
			continue
		}
		if rule.AddressRange != "" {
			p, err := netip.ParsePrefix(rule.AddressRange)
			if err != nil || !addr.IsValid() || !p.Contains(addr) {
				continue
			}
		}
		return true
	}
	return false
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
		if a.Is4In6() {
			// One address, two spellings: kept in its IPv4 form only, so
			// the clash rule sees one address.
			return invalid("an IPv4-mapped IPv6 address is written in its IPv4 form, such as 192.0.2.1")
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
// contend for one port at the operating system: on the same port, two bind
// addresses clash when they are equal, when either is empty (every
// address), when either is "::" (every IPv6 address, and on a dual-stack
// system every IPv4 address too, so the rule is the strict one), and when
// either is "0.0.0.0" and the other is an IPv4 address (every IPv4
// address). A specific IPv4 and a specific IPv6 address do not clash.
// IPv4-mapped IPv6 addresses are refused by Validate, so each address has
// one spelling here.
func ReceiversClash(a, b Receiver) bool {
	if a.Type != ReceiverDIMSE || b.Type != ReceiverDIMSE || a.DIMSE.Port != b.DIMSE.Port {
		return false
	}
	x, y := a.DIMSE.BindAddress, b.DIMSE.BindAddress
	if x == "" || y == "" || x == y || x == "::" || y == "::" {
		return true
	}
	ipv4 := func(s string) bool { ad, err := netip.ParseAddr(s); return err == nil && ad.Is4() }
	return (x == "0.0.0.0" && ipv4(y)) || (y == "0.0.0.0" && ipv4(x))
}
