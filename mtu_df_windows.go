//go:build windows

package main

import (
	"fmt"
	"syscall"
)

// setDFBit is not implemented on Windows.
func setDFBit(_ syscall.RawConn) error {
	return fmt.Errorf("DF bit not supported on Windows")
}
