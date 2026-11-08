//go:build !windows

package main

import "syscall"

const pauseSupported = true

func sendSignal(pid int32, act signalAction) error {
	var sig syscall.Signal
	switch act {
	case actTerm:
		sig = syscall.SIGTERM
	case actKill:
		sig = syscall.SIGKILL
	case actStop:
		sig = syscall.SIGSTOP
	case actCont:
		sig = syscall.SIGCONT
	}
	return syscall.Kill(int(pid), sig)
}
