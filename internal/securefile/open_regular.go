package securefile

import (
	"bytes"
	"errors"
	"io"
	"os"
)

// ErrInvalidRegularSnapshot is returned for every unsafe, changing, empty, or
// oversized snapshot source. Its text deliberately contains no source detail.
var ErrInvalidRegularSnapshot = errors.New("invalid regular snapshot")

// snapshotTestHooks provides deterministic race boundaries to package tests.
// Production callers always use the zero value.
type snapshotTestHooks struct {
	event func(string) error
}

func (hooks snapshotTestHooks) emit(name string) error {
	if hooks.event == nil {
		return nil
	}
	return hooks.event(name)
}

// ReadRegularSnapshot reads one nonempty regular file through component-bound
// handles. It rejects links/reparse points and any identity, size, timestamp,
// or requested-path change observed before the snapshot is returned.
func ReadRegularSnapshot(path string, maxBytes int64) ([]byte, error) {
	return readRegularSnapshot(path, maxBytes, snapshotTestHooks{})
}

func invalidRegularSnapshot() error { return ErrInvalidRegularSnapshot }

func maxInt() int { return int(^uint(0) >> 1) }

func readSnapshotBytes(file *os.File, size int, hooks snapshotTestHooks) ([]byte, error) {
	contents := make([]byte, size)
	first := (size + 1) / 2
	if _, err := io.ReadFull(file, contents[:first]); err != nil {
		return nil, err
	}
	if err := hooks.emit("during-read"); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(file, contents[first:]); err != nil {
		return nil, err
	}
	var trailing [1]byte
	if count, err := file.Read(trailing[:]); count != 0 || !errors.Is(err, io.EOF) {
		return nil, ErrInvalidRegularSnapshot
	}
	return contents, nil
}

func verifySnapshotBytes(file *os.File, contents []byte) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	verified := make([]byte, len(contents))
	if _, err := io.ReadFull(file, verified); err != nil || !bytes.Equal(contents, verified) {
		return ErrInvalidRegularSnapshot
	}
	var trailing [1]byte
	if count, err := file.Read(trailing[:]); count != 0 || !errors.Is(err, io.EOF) {
		return ErrInvalidRegularSnapshot
	}
	return nil
}
