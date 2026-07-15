//go:build !windows

package controller

import "os"

func replaceStoreFileAtomic(from, to string) error { return os.Rename(from, to) }

func syncStoreDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}
