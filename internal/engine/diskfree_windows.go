//go:build windows

package engine

import "golang.org/x/sys/windows"

func diskFree(dir string) (free, total int64) {
	var avail, tot, freeB uint64
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, 0
	}
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &tot, &freeB); err != nil {
		return 0, 0
	}
	return int64(avail), int64(tot)
}
