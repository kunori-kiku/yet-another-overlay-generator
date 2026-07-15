//go:build windows

package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Windows ACLs and ownership are not represented by os.FileMode. We still reject
// reparse points and special files on the opened handles before reading.
// Installation owns the ACL policy; see the package guide for this platform
// limitation. Windows has no supported openat equivalent in this package, so the
// parent and child handles are necessarily opened separately.
func readCustodyFilePlatform(path string, _ custodyReadPolicy) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	dirPath := filepath.Dir(cleanPath)

	dir, dirInfo, err := openWindowsCustodyHandle(dirPath, windows.FILE_READ_ATTRIBUTES, windows.FILE_FLAG_BACKUP_SEMANTICS)
	if err != nil {
		return nil, fmt.Errorf("agent: open custody directory %s: %w", dirPath, err)
	}
	_ = dir.Close()
	if err := validateSecureOwnedDir(dirPath, dirInfo); err != nil {
		return nil, fmt.Errorf("agent: custody directory rejected: %w", err)
	}

	f, after, err := openWindowsCustodyHandle(cleanPath, windows.GENERIC_READ, 0)
	if err != nil {
		return nil, fmt.Errorf("agent: open custody file %s: %w", cleanPath, err)
	}
	defer func() { _ = f.Close() }()
	if !after.Mode().IsRegular() {
		return nil, fmt.Errorf("agent: custody file %s is not regular", cleanPath)
	}
	return readCustodyBytes(f, cleanPath)
}

func openWindowsCustodyHandle(path string, access, extraFlags uint32) (*os.File, os.FileInfo, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil, err
	}
	handle, err := windows.CreateFile(
		pathPtr,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		extraFlags|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(handle), path)
	if f == nil {
		_ = windows.CloseHandle(handle)
		return nil, nil, fmt.Errorf("invalid descriptor")
	}
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &handleInfo); err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("inspect opened handle: %w", err)
	}
	if handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = f.Close()
		return nil, nil, fmt.Errorf("path is a reparse point")
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("stat opened handle: %w", err)
	}
	return f, info, nil
}
