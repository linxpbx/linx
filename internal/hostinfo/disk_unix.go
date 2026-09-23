//go:build linux || darwin

package hostinfo

import (
	"os"
	"syscall"
)

var rootFS = os.DirFS("/")

func diskUsage(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize) // int64 on linux, uint32 on darwin
	return st.Bavail * bsize, st.Blocks * bsize, nil
}
