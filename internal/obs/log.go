package obs

import (
	"io"
	"log/slog"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/amrshadid/magnetar/internal/server/redact"
)

// Format is how a log line is written.
type Format string

// The formats NewLogger writes.
const (
	// JSON writes one JSON object per line, for a log collector. It is the
	// default (D39).
	JSON Format = "json"
	// Text writes key=value pairs, for a person reading a terminal.
	Text Format = "text"
)

// HiddenValue is what an attribute named "authorization" reads in the log.
const HiddenValue = "[hidden]"

// redactMessages is whether the handler redacts protobuf messages. It is
// always true in the binary; a test turns it off to show that the test of
// the redaction fails without it.
var redactMessages = true

// LogOptions configures NewLogger.
type LogOptions struct {
	// Format is JSON or Text; empty is JSON.
	Format Format
	// Level is the lowest level written; the zero value is Info.
	Level slog.Leveler
}

// NewLogger returns a logger writing to w with the guards described in the
// package documentation. An unknown Format is written as JSON.
func NewLogger(w io.Writer, opts LogOptions) *slog.Logger {
	hopts := &slog.HandlerOptions{Level: opts.Level, ReplaceAttr: guard}
	if opts.Format == Text {
		return slog.New(slog.NewTextHandler(w, hopts))
	}
	return slog.New(slog.NewJSONHandler(w, hopts))
}

// ParseLevel reads a level's name: debug, info, warn or error, in any case.
// It reports false for anything else.
func ParseLevel(s string) (slog.Level, bool) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo, false
	}
	return l, true
}

// guard is the handler's ReplaceAttr: it hides an authorization attribute
// and redacts a protobuf message, wherever either appears.
func guard(_ []string, a slog.Attr) slog.Attr {
	if strings.EqualFold(a.Key, "authorization") {
		return slog.String(a.Key, HiddenValue)
	}
	if a.Value.Kind() == slog.KindAny {
		if m, ok := a.Value.Any().(proto.Message); ok && redactMessages {
			return slog.Attr{Key: a.Key, Value: redact.LogValue(m)}
		}
	}
	return a
}
