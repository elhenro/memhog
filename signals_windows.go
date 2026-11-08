//go:build windows

package main

import (
	"errors"
	"os"
)

// Windows has no SIGSTOP/SIGCONT; pause-based features are disabled there.
const pauseSupported = false

var errPauseUnsupported = errors.New("pause/resume unsupported on windows")

func sendSignal(pid int32, act signalAction) error {
	switch act {
	case actStop, actCont:
		return errPauseUnsupported
	}
	p, err := os.FindProcess(int(pid))
	if err != nil {
		return err
	}
	return p.Kill() // TerminateProcess — no graceful SIGTERM equivalent
}
