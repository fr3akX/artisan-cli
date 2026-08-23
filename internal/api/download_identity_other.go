//go:build !linux && !darwin && !windows

package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/fr3akX/artisan-cli/internal/securefile"
)

type heldOtherDownloadPublication struct {
	parent        *os.File
	parentInfo    os.FileInfo
	parentPath    string
	writer        *os.File
	sourceInfo    os.FileInfo
	sourceName    string
	candidate     string
	candidateInfo os.FileInfo
	backup        string
	backupInfo    os.FileInfo
	closed        bool
}

func protectDownloadFile(file *os.File) error { return securefile.ProtectPrivateFile(file) }

func newHeldDownloadPublication(target *downloadTarget) (heldDownloadPublication, error) {
	parent, err := os.Open(target.directory)
	if err != nil {
		return nil, err
	}
	parentInfo, err := parent.Stat()
	if err != nil || !parentInfo.IsDir() {
		_ = parent.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("download parent is not a directory")
	}
	if target.operations.afterParentHeld != nil {
		if err := target.operations.afterParentHeld(target); err != nil {
			_ = parent.Close()
			return nil, err
		}
	}
	current, currentErr := os.Lstat(target.directory)
	if currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(parentInfo, current) {
		_ = parent.Close()
		return nil, errors.Join(errDownloadIdentityAmbiguous, currentErr)
	}
	var writer *os.File
	if target.operations.createTemp != nil {
		writer, err = target.operations.createTemp(target.directory, "."+filepath.Base(target.destination)+".tmp-*")
	} else {
		writer, err = os.CreateTemp(target.directory, "."+filepath.Base(target.destination)+".tmp-*")
	}
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	info, err := writer.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = writer.Close()
		_ = parent.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("download source is not regular")
	}
	return &heldOtherDownloadPublication{parent: parent, parentInfo: parentInfo, parentPath: target.directory, writer: writer, sourceInfo: info, sourceName: filepath.Base(writer.Name())}, nil
}

func (p *heldOtherDownloadPublication) writerFile() *os.File     { return p.writer }
func (p *heldOtherDownloadPublication) heldSourceFile() *os.File { return p.writer }
func (p *heldOtherDownloadPublication) temporaryPath() string {
	return filepath.Join(p.parentPath, p.sourceName)
}
func (p *heldOtherDownloadPublication) closeWriterBeforePublish() bool { return false }
func (p *heldOtherDownloadPublication) close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	return p.parent.Close()
}
func (p *heldOtherDownloadPublication) pathMatches() bool {
	info, err := os.Lstat(p.parentPath)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && os.SameFile(info, p.parentInfo)
}
func (p *heldOtherDownloadPublication) matches(name string, want os.FileInfo) bool {
	info, err := os.Lstat(filepath.Join(p.parentPath, name))
	return err == nil && info.Mode().IsRegular() && os.SameFile(info, want)
}
func (p *heldOtherDownloadPublication) removeOwned(target *downloadTarget, name string, want os.FileInfo) error {
	if !p.pathMatches() || !p.matches(name, want) {
		return errDownloadIdentityAmbiguous
	}
	if target.operations.afterCleanupCheck != nil {
		if err := target.operations.afterCleanupCheck(target, name); err != nil {
			return err
		}
	}
	if !p.pathMatches() || !p.matches(name, want) {
		return errDownloadIdentityAmbiguous
	}
	return os.Remove(filepath.Join(p.parentPath, name))
}
func (p *heldOtherDownloadPublication) abort(target *downloadTarget) error {
	return p.removeOwned(target, p.sourceName, p.sourceInfo)
}

func otherRandomName(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
func (p *heldOtherDownloadPublication) copyCandidate(target *downloadTarget) error {
	name, err := otherRandomName("." + filepath.Base(target.destination) + ".candidate-")
	if err != nil {
		return err
	}
	path := filepath.Join(p.parentPath, name)
	candidate, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(candidate, io.NewSectionReader(p.writer, 0, 1<<63-1))
	syncErr := candidate.Sync()
	info, infoErr := candidate.Stat()
	count, digest, hashErr := digestDownloadDescriptor(candidate)
	closeErr := candidate.Close()
	if err := errors.Join(copyErr, syncErr, infoErr, hashErr, closeErr); err != nil {
		return err
	}
	if count != target.sealedCount || digest != target.sealedDigest {
		return errDownloadDigestMismatch
	}
	p.candidate, p.candidateInfo = name, info
	return nil
}
func (p *heldOtherDownloadPublication) namedExact(name string, want os.FileInfo, count int64, digest [32]byte) bool {
	file, err := os.Open(filepath.Join(p.parentPath, name))
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, want) {
		return false
	}
	gotCount, gotDigest, err := digestDownloadDescriptor(file)
	return err == nil && gotCount == count && gotDigest == digest && p.matches(name, want)
}
func (p *heldOtherDownloadPublication) destinationExact(target *downloadTarget) bool {
	file, err := os.Open(target.destination)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, p.candidateInfo) {
		return false
	}
	count, digest, err := digestDownloadDescriptor(file)
	return err == nil && count == target.sealedCount && digest == target.sealedDigest
}
func (p *heldOtherDownloadPublication) sync(target *downloadTarget) error {
	if target.operations.syncParent != nil {
		return target.operations.syncParent(p.parentPath)
	}
	return p.parent.Sync()
}
