//go:build !windows

package main

import "os"

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}
