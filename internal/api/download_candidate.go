package api

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
)

// copyHeldDownloadCandidate registers the exact newly-created candidate before
// any copy, sync, or digest work can fail. Callers therefore retain enough
// identity to clean partial candidates conservatively on every pre-native exit.
func copyHeldDownloadCandidate(source, candidate *os.File, register func(os.FileInfo) error, operations downloadOperations) (int64, [sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if source == nil || candidate == nil || register == nil {
		return 0, digest, errors.New("download candidate copy requires held files and registration")
	}
	info, err := candidate.Stat()
	if err != nil {
		return 0, digest, errors.Join(err, register(nil), candidate.Close())
	}
	if !info.Mode().IsRegular() {
		return 0, digest, errors.Join(errors.New("download candidate is not regular"), register(info), candidate.Close())
	}
	if err := register(info); err != nil {
		return 0, digest, errors.Join(err, candidate.Close())
	}

	copyCount, copyErr := operations.copyCandidate(candidate, io.NewSectionReader(source, 0, 1<<63-1))
	syncErr := operations.syncCandidate(candidate)
	count, digest, digestErr := operations.digestCandidate(candidate)
	closeErr := candidate.Close()
	if err := errors.Join(copyErr, syncErr, digestErr, closeErr); err != nil {
		return count, digest, err
	}
	if copyCount != count {
		return count, digest, errDownloadDigestMismatch
	}
	return count, digest, nil
}
