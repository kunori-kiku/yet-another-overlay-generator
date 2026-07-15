//go:build windows

package agent

import "golang.org/x/sys/windows"

func replaceFileAtomic(from, to string) error {
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

// MoveFileEx with WRITE_THROUGH supplies the Windows durability boundary; opening and
// syncing a directory handle is not portable through os.File.
func syncDirectory(string) error { return nil }
