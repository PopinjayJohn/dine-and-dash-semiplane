package store

// boolArg encodes a Go bool for a SQLite integer column. Written out rather
// than done inline so every place that stores a flag agrees on the encoding.
func boolArg(b bool) any {
	if b {
		return 1
	}
	return 0
}
