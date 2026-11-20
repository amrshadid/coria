package sqlkit

import (
	"github.com/amrshadid/coria/internal/core/naming"
	"github.com/amrshadid/coria/internal/tenant"
)

// A receiver's configuration is kept in two JSON columns, config and
// naming, written and read here so every SQL driver keeps the same text: a
// database moved from one driver to another, or read by an outside tool,
// reads the same. The field names are the columns' contract and never
// change; a field added later is read as its zero value from an older row.

// dimseConfig is a receiver's configuration of its type as its config
// column holds it: a DIMSE receiver's fields, or a stow receiver's, which
// are left out of a DIMSE receiver's text so a row written before the stow
// type existed reads and writes back the same.
type dimseConfig struct {
	AETitle          string             `json:"ae_title"`
	AnyCaller        bool               `json:"any_caller"`
	Callers          []callerRuleConfig `json:"callers"`
	TLS              *tlsConfig         `json:"tls"`
	SOPClasses       []string           `json:"sop_classes"`
	TransferSyntaxes []string           `json:"transfer_syntaxes"`
	STOWKeys         []string           `json:"stow_key_ids,omitempty"`
	MaxRequestBytes  int64              `json:"stow_max_request_bytes,omitempty"`
}

// callerRuleConfig is one caller a DIMSE receiver accepts.
type callerRuleConfig struct {
	CallingAETitle string `json:"calling_ae_title"`
	AddressRange   string `json:"address_range"`
}

// tlsConfig is where a DIMSE receiver's certificate and key are.
type tlsConfig struct {
	CertificateFile string `json:"certificate_file"`
	PrivateKeyFile  string `json:"private_key_file"`
}

// namingConfig is a receiver's naming rule as its naming column holds it.
type namingConfig struct {
	Subject          string `json:"subject"`
	Session          string `json:"session"`
	Scan             string `json:"scan"`
	PatternAttribute string `json:"pattern_attribute"`
	Pattern          string `json:"pattern"`
}

// ReceiverConfigText returns the text of a receiver's config column and of
// its naming column, empty when it has no naming rule of its own.
func ReceiverConfigText(r tenant.Receiver) (config, namingText string, err error) {
	c := dimseConfig{AETitle: r.DIMSE.AETitle, AnyCaller: r.DIMSE.AnyCaller,
		SOPClasses: r.DIMSE.SOPClasses, TransferSyntaxes: r.DIMSE.TransferSyntaxes,
		STOWKeys: r.STOW.Keys, MaxRequestBytes: r.STOW.MaxRequestBytes}
	for _, cr := range r.DIMSE.Callers {
		c.Callers = append(c.Callers, callerRuleConfig(cr))
	}
	if r.DIMSE.TLS != nil {
		tc := tlsConfig(*r.DIMSE.TLS)
		c.TLS = &tc
	}
	if config, err = JSONText(c); err != nil {
		return "", "", err
	}
	if r.Naming != nil {
		if namingText, err = JSONText(namingConfig(*r.Naming)); err != nil {
			return "", "", err
		}
	}
	return config, namingText, nil
}

// ReadReceiverConfig sets r's DIMSE or stow configuration and naming rule from the
// text of its config and naming columns, as ReceiverConfigText wrote them.
func ReadReceiverConfig(r *tenant.Receiver, config, namingText string) error {
	var c dimseConfig
	if err := FromJSON(config, &c); err != nil {
		return err
	}
	r.DIMSE.AETitle, r.DIMSE.AnyCaller = c.AETitle, c.AnyCaller
	r.DIMSE.SOPClasses, r.DIMSE.TransferSyntaxes = c.SOPClasses, c.TransferSyntaxes
	r.STOW = tenant.STOWReceiver{Keys: c.STOWKeys, MaxRequestBytes: c.MaxRequestBytes}
	r.DIMSE.Callers = nil
	for _, cr := range c.Callers {
		r.DIMSE.Callers = append(r.DIMSE.Callers, tenant.CallerRule(cr))
	}
	r.DIMSE.TLS = nil
	if c.TLS != nil {
		t := tenant.ReceiverTLS(*c.TLS)
		r.DIMSE.TLS = &t
	}
	r.Naming = nil
	if namingText != "" {
		var n namingConfig
		if err := FromJSON(namingText, &n); err != nil {
			return err
		}
		spec := naming.Spec(n)
		r.Naming = &spec
	}
	return nil
}
