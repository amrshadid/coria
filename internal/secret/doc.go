// Package secret holds the installation's key file, seals and opens the
// secrets stored in the master database with it, and makes and checks API
// keys.
//
// # The key file
//
// A text file with one line per key version:
//
//	magnetar-key-v1 <version> <key>
//
// The version is a decimal integer from 1 to 4294967295 written without
// leading zeros, and the key is 32 random bytes in standard base64 with
// padding (44 characters). Fields are separated by one space; a line ends
// with a line feed, or with a carriage return and a line feed. No two lines
// share a version. The highest version seals; every version in the file
// opens, so a key can be rotated later by adding a line, with no change of
// format.
//
// CreateKeyFile writes a new file with one key, version 1, and never replaces
// a file that exists. LoadKeyFile refuses a file that other users may open,
// and says how to fix that, because anyone who can read the file can open
// every secret it sealed. What "other users" means differs between systems
// (the mode bits on Linux and macOS, the access list on Windows); that
// difference is kept in perm_unix.go and perm_windows.go and nowhere else.
//
// # A sealed value
//
// Seal encrypts with AES-256-GCM (NIST SP 800-38D) under a fresh random
// 96-bit nonce. The sealed value is, byte by byte:
//
//	1 byte    format, 0x01
//	4 bytes   key version, big-endian
//	12 bytes  nonce
//	n+16      ciphertext of the n-byte plaintext, then the 16-byte tag
//
// The additional authenticated data is the first five bytes (format and key
// version) followed by the context the caller names, such as
// "tenant/<id>/database". The context binds a secret to the row it belongs
// to, so a sealed value copied into another row does not open; the header in
// the additional data means a changed format byte or key version fails
// authentication as well as the lookup. scripts/check-seal.py implements this
// format with Python's cryptography package, and a test runs it against this
// package in both directions.
//
// Open fails, with kind Internal and one fixed message, whatever the reason:
// a wrong key or context, a changed or missing byte, an unknown format or key
// version. A reason would tell a caller which part of a forgery to fix.
//
// # API keys
//
// A key is mgt_<id>_<secret>: a 12-character public id that finds the key's
// row, and a 44-character secret holding 256 random bits, both drawn from the
// Base58 alphabet (digits and letters without 0, O, I and l, which are easy
// to confuse). The prefix and the underscores let a secret scanner recognize
// a leaked key, and a double-click selects the whole of it. Only the SHA-256
// of the secret is stored: the secret is random and 256 bits long, so a slow
// password hash would add nothing (NIST SP 800-63B-4, 3.1.2.2).
//
// This package must never write a key, a secret's plaintext or an API key's
// secret into an error, a log line or any value it returns other than the one
// that is the point of the call, and it must never use cryptography outside
// the Go standard library.
package secret
