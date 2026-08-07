//go:build !linux && !darwin && !windows

package api

import "os"

func atomicInstallDownloadNoReplace(from, to string) error {
	if err := os.Link(from, to); err != nil {
		return err
	}
	_ = os.Remove(from)
	return nil
}

func atomicReplaceDownload(from, to string) error {
	return os.Rename(from, to)
}
