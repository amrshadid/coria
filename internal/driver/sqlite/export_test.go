package sqlite

// setBetweenReads makes ReadEvents and StatusSnapshot of t call f inside
// their read transaction, between their reads (nil for none). It is the only
// way a test sets tenantSeams.betweenReads.
func (t *Tenant) setBetweenReads(f func()) { t.seams.betweenReads = f }
