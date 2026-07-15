//go:build windows

package controller

import "golang.org/x/sys/windows"

func replaceStoreFileAtomic(from, to string) error {
	fromPtr, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(fromPtr, toPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// MOVEFILE_WRITE_THROUGH is the Windows durability boundary. Directory handles
// cannot be portably opened and flushed through os.File.
func syncStoreDirectory(string) error { return nil }
