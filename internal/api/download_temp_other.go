//go:build !windows

package api

import "os"

func createDownloadTemp(directory, pattern string) (*os.File, error) {
	return os.CreateTemp(directory, pattern)
}
