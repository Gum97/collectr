//go:build unix

package storage

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func usableSpace(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("checking free space: %w", err)
	}
	return st.Bavail * uint64(st.Bsize), nil
}
