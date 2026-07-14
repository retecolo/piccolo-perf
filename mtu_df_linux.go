//go:build linux

package main

import (
	"fmt"
	"syscall"
)

// setDFBit sets the Don't Fragment bit on the raw socket connection using
// IP_MTU_DISCOVER / IP_PMTUDISC_DO on Linux.
func setDFBit(rc syscall.RawConn) error {
	const (
		ipMTUDiscover  = 0xa // IP_MTU_DISCOVER
		ipPMTUDiscDo   = 0x2 // IP_PMTUDISC_DO
	)
	var setsockoptErr error
	ctrlErr := rc.Control(func(fd uintptr) {
		setsockoptErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, ipMTUDiscover, ipPMTUDiscDo)
	})
	if ctrlErr != nil {
		return fmt.Errorf("control: %w", ctrlErr)
	}
	if setsockoptErr != nil {
		return fmt.Errorf("setsockopt IP_MTU_DISCOVER: %w", setsockoptErr)
	}
	return nil
}
