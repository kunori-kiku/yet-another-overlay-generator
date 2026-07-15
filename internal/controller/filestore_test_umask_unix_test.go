//go:build !windows

package controller

import "syscall"

// Go's temporary-directory mode is filtered through the process umask. Keep
// package test fixtures suitable for the same custody checks production uses,
// even on developer shells configured with a collaboration-friendly 0002.
func setSecureTestUmask() int  { return syscall.Umask(0077) }
func restoreTestUmask(old int) { syscall.Umask(old) }
