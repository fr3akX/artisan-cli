//go:build windows

package main

import "os"

func handledSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
