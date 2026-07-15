//go:build !windows

package agent

import "syscall"

// Custody-path tests create state/key directories with t.TempDir. Use the same private
// creation mask the production writers require, independent of the developer/CI shell's umask.
func init() { syscall.Umask(0077) }
