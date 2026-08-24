//go:build windows

package api

import (
	"errors"
	"os"
)

func injectDownloadDurabilityFailure(operations *downloadOperations) {
	operations.flushDirectory = func(directory *os.File) error {
		if _, err := directory.Stat(); err != nil {
			return err
		}
		return errors.New("directory flush")
	}
}
