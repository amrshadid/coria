package registry

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/amrshadid/magnetar/internal/master"
)

// hidden replaces each part of an address found in a text.
const hidden = "<hidden>"

// secretsOf returns the parts of a database address that must never appear
// in a reason, a log line or an error: the whole address, and, for a URL
// (postgres://user:password@host:port/db), its user information, user,
// password and host; for a keyword/value string (host=... password=...),
// the values of its host, hostaddr, user and password. Hosts and users are
// not secrets on their own, but together with a leaked password they are
// the rest of what is needed to log in, and the API contract keeps them out
// of every answer but the removal's location.
func secretsOf(address string) []string {
	if address == "" {
		return nil
	}
	out := []string{address}
	if u, err := url.Parse(address); err == nil && u.Scheme != "" {
		if u.User != nil {
			out = append(out, u.User.String(), u.User.Username())
			if p, ok := u.User.Password(); ok {
				out = append(out, p, url.QueryEscape(p), url.PathEscape(p))
			}
		}
		out = append(out, u.Host, u.Hostname())
		// A password may also be given as a query parameter.
		for _, k := range []string{"password", "user", "host"} {
			out = append(out, u.Query()[k]...)
		}
	}
	for _, field := range strings.Fields(address) {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(k) {
		case "password", "host", "hostaddr", "user":
			out = append(out, strings.Trim(v, `'"`))
		}
	}
	// Longest first, so a password inside the address is not replaced
	// before the address it is part of, leaving the rest of it in view.
	slices.SortFunc(out, func(a, b string) int { return len(b) - len(a) })
	return slices.Compact(out)
}

// anyURL and anyPassword catch what the address-specific strings miss: a
// driver may quote an address it rewrote (another host from a list, a
// resolved address), or a password given another way.
var (
	anyURL      = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"']*`)
	anyPassword = regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[=:]\s*("[^"]*"|'[^']*'|\S+)`)
)

// scrub returns text with every secret replaced by hidden, then any URL and
// any "password=..." it still holds, cut to the length a state reason may
// have, on a UTF-8 boundary, so the master never refuses it.
func scrub(text string, secrets []string) string {
	for _, s := range secrets {
		if s != "" {
			text = strings.ReplaceAll(text, s, hidden)
		}
	}
	text = anyURL.ReplaceAllString(text, hidden)
	text = anyPassword.ReplaceAllString(text, "$1="+hidden)
	text = strings.ToValidUTF8(strings.ReplaceAll(text, "\x00", ""), "?")
	if len(text) > master.MaxStateReasonLength {
		cut := master.MaxStateReasonLength
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return text
}
