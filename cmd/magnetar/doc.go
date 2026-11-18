// Command magnetar is the Magnetar binary: the server of an installation and
// the command line that operates it through its API.
//
// The commands are built in internal/cli. init makes an installation and
// serve runs its server (internal/cli/servecmd); tenant, key, grant, setting
// and audit with their verbs, whoami and version are clients of the API, and
// cobra provides the help and the shell completion; see "magnetar --help".
// With no arguments, when input and output are a terminal, the interactive
// program opens (internal/tui).
//
// This package wires the parts of Magnetar together and does nothing else. It
// must never hold logic of its own, because what is only here cannot be tested
// without building the binary.
package main
