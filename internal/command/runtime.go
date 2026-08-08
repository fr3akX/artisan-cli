package command

import "io"

// Runtime contains process resources used by commands.
type Runtime struct {
	In           io.Reader
	Out          io.Writer
	Err          io.Writer
	Getenv       func(string) string
	ConfigDir    string
	IsTerminal   func(fd int) bool
	ReadPassword func(fd int) ([]byte, error)
}
