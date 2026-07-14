//go:build darwin

package main

import (
	"fmt"
	"syscall"
)

const ipDontFrag = 0x1c // IP_DONTFRAG on macOS

// setDFBit sets the Don't Fragment bit on the raw socket connection.
func setDFBit(rc syscall.RawConn) error {
	var setsockoptErr error
	ctrlErr := rc.Control(func(fd uintptr) {
		setsockoptErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, ipDontFrag, 1)
	})
	if ctrlErr != nil {
		return fmt.Errorf("control: %w", ctrlErr)
	}
	if setsockoptErr != nil {
		return fmt.Errorf("setsockopt IP_DONTFRAG: %w", setsockoptErr)
	}
	return nil
}
