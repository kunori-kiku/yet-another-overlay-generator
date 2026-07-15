//go:build windows

package agent

import "os"

// Windows ACLs are not represented by os.FileMode permission bits. The common
// validator still rejects symlinks and non-directories; the service installer owns
// the ACL policy for its state/key directories.
func validateSecureDirPlatform(string, os.FileInfo) error { return nil }
func validateFileOwnerPlatform(os.FileInfo) error         { return nil }
