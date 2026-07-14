//go:build !darwin && !linux && !windows

package main

import (
	"fmt"
	"syscall"
)

// setDFBit is not supported on this platform.
func setDFBit(_ syscall.RawConn) error {
	return fmt.Errorf("DF bit not supported on this platform")
}
