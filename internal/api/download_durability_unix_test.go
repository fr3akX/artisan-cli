//go:build !windows

package api

import "errors"

func injectDownloadDurabilityFailure(operations *downloadOperations) {
	operations.syncParent = func(string) error { return errors.New("parent sync") }
}
