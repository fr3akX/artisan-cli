//go:build !windows

package main

import (
	"os"
	"syscall"
)

func handledSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
