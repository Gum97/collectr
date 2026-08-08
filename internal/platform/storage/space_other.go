//go:build !unix

package storage

// usableSpace is unavailable on this platform; callers treat zero as unknown.
func usableSpace(string) (uint64, error) { return 0, nil }
