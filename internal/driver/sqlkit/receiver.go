package sqlkit

import (
	"context"
	"slices"

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
	ForwardTo        []string           `json:"forward_to,omitempty"`
	// DeidProfile and DeidVersion name the profile version the receiver
	// de-identifies with; absent, as in every row written before
	// receivers de-identified, the receiver archives as received.
	DeidProfile string `json:"deid_profile,omitempty"`
	DeidVersion int32  `json:"deid_version,omitempty"`
	// Routes and RoutesQuarantineUnmatched are the receiver's routing;
	// absent, as in every row written before receivers routed, it has
	// none.
	Routes                    []routeConfig `json:"routes,omitempty"`
	RoutesQuarantineUnmatched bool          `json:"routes_quarantine_unmatched,omitempty"`
}

// routeConfig is one route of a receiver's routing.
type routeConfig struct {
	Name              string            `json:"name"`
	Conditions        []conditionConfig `json:"conditions,omitempty"`
	Storage           string            `json:"storage"`
	Profile           string            `json:"profile,omitempty"`
	Version           int32             `json:"version,omitempty"`
	ReplacesForwardTo bool              `json:"replaces_forward_to,omitempty"`
	ForwardTo         []string          `json:"forward_to,omitempty"`
}

// conditionConfig is one condition of a route.
type conditionConfig struct {
	Subject string `json:"subject"`
	Test    string `json:"test"`
	Value   string `json:"value,omitempty"`
	Present bool   `json:"present,omitempty"`
	Negate  bool   `json:"negate,omitempty"`
}

// receptionRouteConfig is what a reception's routing chose, as its route
// column holds it; the empty text for nothing of its own.
type receptionRouteConfig struct {
	ReleasedStorage   string   `json:"released_storage,omitempty"`
	Route             string   `json:"route,omitempty"`
	Profile           string   `json:"profile,omitempty"`
	Version           int32    `json:"version,omitempty"`
	ReplacesForwardTo bool     `json:"replaces_forward_to,omitempty"`
	ForwardTo         []string `json:"forward_to,omitempty"`
}

// ReceptionRouteText returns the text of a reception's route column: empty
// for a route that chose nothing of its own.
func ReceptionRouteText(r tenant.ReceptionRoute) (string, error) {
	if r.IsZero() {
		return "", nil
	}
	return JSONText(receptionRouteConfig{ReleasedStorage: r.ReleasedStorage, Route: r.Route, Profile: r.Profile.Name,
		Version: r.Profile.Version, ReplacesForwardTo: r.ReplacesForwardTo, ForwardTo: r.ForwardTo})
}

// ReadReceptionRoute returns the route a reception's route column holds,
// as ReceptionRouteText wrote it.
func ReadReceptionRoute(text string) (tenant.ReceptionRoute, error) {
	if text == "" {
		return tenant.ReceptionRoute{}, nil
	}
	var c receptionRouteConfig
	if err := FromJSON(text, &c); err != nil {
		return tenant.ReceptionRoute{}, err
	}
	return tenant.ReceptionRoute{ReleasedStorage: c.ReleasedStorage, Route: c.Route,
		Profile: tenant.ProfileRef{Name: c.Profile, Version: c.Version}, ReplacesForwardTo: c.ReplacesForwardTo,
		ForwardTo: c.ForwardTo}, nil
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
		STOWKeys: r.STOW.Keys, MaxRequestBytes: r.STOW.MaxRequestBytes, ForwardTo: r.ForwardTo,
		DeidProfile: r.Profile.Name, DeidVersion: r.Profile.Version,
		RoutesQuarantineUnmatched: r.Routing.QuarantineUnmatched}
	for _, rt := range r.Routing.Routes {
		rc := routeConfig{Name: rt.Name, Storage: rt.Storage, Profile: rt.Profile.Name, Version: rt.Profile.Version,
			ReplacesForwardTo: rt.ReplacesForwardTo, ForwardTo: rt.ForwardTo}
		for _, cd := range rt.Conditions {
			rc.Conditions = append(rc.Conditions, conditionConfig{Subject: string(cd.Subject), Test: string(cd.Test),
				Value: cd.Value, Present: cd.Present, Negate: cd.Negate})
		}
		c.Routes = append(c.Routes, rc)
	}
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
	r.ForwardTo = c.ForwardTo
	r.Profile = tenant.ProfileRef{Name: c.DeidProfile, Version: c.DeidVersion}
	r.Routing = routingOf(c)
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

// ReceiverDeidentifies reports whether the receiver whose config column
// holds config names a profile version, and so de-identifies what it
// receives (tenant.Receiver.Deidentifies).
func ReceiverDeidentifies(config string) (bool, error) {
	var c dimseConfig
	if err := FromJSON(config, &c); err != nil {
		return false, err
	}
	return c.DeidProfile != "", nil
}

// routingOf returns the routing a receiver's config column holds.
func routingOf(c dimseConfig) tenant.Routing {
	out := tenant.Routing{QuarantineUnmatched: c.RoutesQuarantineUnmatched}
	for _, rc := range c.Routes {
		rt := tenant.Route{Name: rc.Name, Storage: rc.Storage, Profile: tenant.ProfileRef{Name: rc.Profile, Version: rc.Version},
			ReplacesForwardTo: rc.ReplacesForwardTo, ForwardTo: rc.ForwardTo}
		for _, cd := range rc.Conditions {
			rt.Conditions = append(rt.Conditions, tenant.RouteCondition{Subject: tenant.RouteSubject(cd.Subject),
				Test: tenant.RouteTest(cd.Test), Value: cd.Value, Present: cd.Present, Negate: cd.Negate})
		}
		out.Routes = append(out.Routes, rt)
	}
	return out
}

// ReceiverRoutingTo returns the name of a receiver one of whose routes
// archives to the storage, or "": the receivers are read whole, since a
// receiver's routes are in its configuration's text and a tenant's
// receivers are few. A driver calls it in the transaction that removes a
// storage, beside its check of the receivers' own storage.
func ReceiverRoutingTo(ctx context.Context, tx SendingTx, storage string) (string, error) {
	return receiverWhose(ctx, tx, func(c dimseConfig) bool {
		return slices.Contains(routingOf(c).Storages(), storage)
	})
}

// receiverWhose returns the name of the first receiver, by name, whose
// configuration ok holds for, or "".
func receiverWhose(ctx context.Context, tx SendingTx, ok func(dimseConfig) bool) (string, error) {
	found := ""
	err := tx.Query(ctx, `SELECT name, config FROM receivers ORDER BY name`, nil, func(r Row) error {
		var name, config string
		if err := r.Scan(&name, &config); err != nil {
			return err
		}
		var c dimseConfig
		if err := FromJSON(config, &c); err != nil {
			return err
		}
		if found == "" && ok(c) {
			found = name
		}
		return nil
	})
	return found, err
}
