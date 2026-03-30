//go:build linux

package collectors

import "syscall"

// getDiskUsage gibt used/total Bytes für einen Mountpoint zurück
func getDiskUsage(path string) (used, total uint64, err error) {
	var stat syscall.Statfs_t
	if err = syscall.Statfs(path, &stat); err != nil {
		return 0, 0, err
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	if total >= free {
		used = total - free
	}
	return used, total, nil
}
