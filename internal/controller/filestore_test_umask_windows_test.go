//go:build windows

package controller

func setSecureTestUmask() int { return 0 }
func restoreTestUmask(int)    {}
