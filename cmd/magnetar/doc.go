// Command magnetar is the Magnetar binary: the archive's server and the command
// line that operates it.
//
// So far it has one command, which says which build is running:
//
//	magnetar version
//	magnetar --version
//
// Anything else is a usage error: one line on standard error and exit code 2.
//
// This package wires the parts of Magnetar together and does nothing else. It
// must never hold logic of its own, because what is only here cannot be tested
// without building the binary.
package main
