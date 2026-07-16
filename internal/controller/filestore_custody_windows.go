//go:build windows

package controller

import "os"

// os.FileMode does not expose Windows ACL ownership or access-control entries.
// The common checks still reject reparse-point symlinks and non-directories,
// while deployment/service configuration remains responsible for restricting
// the FileStore root ACL. Failing every Windows FileStore closed here would make
// the supported backend unusable without actually proving an ACL property.
func validateSecureStoreDirPlatform(os.FileInfo) error        { return nil }
func validateStoreFilePlatform(os.FileInfo) error             { return nil }
func tightenOwnedStoreRoot(string, os.FileInfo) (bool, error) { return false, nil }
