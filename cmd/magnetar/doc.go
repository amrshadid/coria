// Command magnetar is the Magnetar binary: the command line that operates an
// installation through its API.
//
// The commands are built in internal/cli. They are tenant, key, grant,
// setting and audit with their verbs, whoami, version, and the help and shell
// completion that cobra provides; see "magnetar --help". The server's own
// commands (init and serve) are not here yet.
//
// This package wires the parts of Magnetar together and does nothing else. It
// must never hold logic of its own, because what is only here cannot be tested
// without building the binary.
package main
