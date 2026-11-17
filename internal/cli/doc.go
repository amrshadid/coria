// Package cli is the command line of Magnetar: the commands that operate an
// installation through its API, built with cobra.
//
// Every command is a client of the API and nothing else. It parses its
// arguments, makes one call (or pages through one list), and writes the
// answer as a table for a person or as one JSON document for a program. It
// holds no rule the server also holds: what a name may be, which role a grant
// may give and who may do what are the server's to say, so a command that
// disagreed with the server could never be right. Only what a client must do
// to keep a person from harm lives here: refusing to send a key over plain
// http, refusing a secret as an argument, and requiring --yes before a
// removal.
//
// The grammar is "magnetar <noun> <verb> [name] [flags]". Exit codes come from
// the kind of the error through fault.Kind.ExitCode: 0 done, 1 failed, 2 wrong
// usage, 3 refused, 4 not found.
//
// This package must never take a secret as an argument or a flag value, never
// seal, hash or store one, never print one except the new key that key create
// exists to show once, never write to the terminal text it got from the
// network without cleaning it, and never reach into a database or a storage:
// the API is the only way in.
package cli
