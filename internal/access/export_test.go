package access

// CountVerifications replaces a's comparison of secrets with one that counts
// its calls and then compares as before, and returns the count so far. It is
// the seam a test uses to show that a key with an unknown id costs the same
// comparison as a wrong secret.
func CountVerifications(a *Access) func() int {
	n := 0
	compare := a.verify
	a.verify = func(secret string, stored []byte) bool {
		n++
		return compare(secret, stored)
	}
	return func() int { return n }
}
