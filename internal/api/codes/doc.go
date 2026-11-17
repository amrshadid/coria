// Package codes is the one table that pairs an API error code with a fault
// kind, so the server and the command line cannot disagree about what a code
// means: the server turns a kind into a code, the command line turns the code
// back into a kind and, through fault.Kind.ExitCode, into an exit status.
//
// The table follows the contract's list of error kinds in
// api/proto/magnetar/v1/access.proto. Codes the contract says any method may
// answer without naming a kind (a deadline, a message over the size limit)
// are mapped on the receiving side only, because a server never answers them
// from a fault kind.
//
// This package must never log or print, and must never put into a message
// anything but the message that fault carries for the caller: an error that
// crosses the API is shown to a person on the other side.
package codes
