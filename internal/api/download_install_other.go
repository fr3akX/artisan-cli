//go:build !linux && !darwin && !windows

package api

import "os"

func atomicInstallDownloadNoReplace(_ *downloadFileIdentity, from, to string) (bool, error) {
	if err := os.Link(from, to); err != nil {
		return false, err
	}
	if err := os.Remove(from); err != nil {
		return true, err
	}
	return true, nil
}

func atomicReplaceDownload(_ *downloadFileIdentity, from, to string) (bool, error) {
	if err := os.Rename(from, to); err != nil {
		return false, err
	}
	return true, nil
}
