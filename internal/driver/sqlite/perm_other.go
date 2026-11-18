//go:build !unix

package sqlite

// checkPrivate does nothing where there are no Unix modes. On Windows a
// file's access is its access list, inherited from its directory, so the
// directory the operator chooses decides who can open the database; reading
// and checking that list is not done in M0 (the key file does it in package
// secret, and the same check may be shared here later).
func checkPrivate(string) error { return nil }
