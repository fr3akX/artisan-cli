package command_test

import (
	"io"

	"github.com/fr3akX/artisan-cli/internal/command"
)

// Compile-time source compatibility: Runtime originally exposed seven exported
// fields and therefore supported external positional composite literals.
var _ = command.Runtime{
	io.Reader(nil),
	io.Writer(nil),
	io.Writer(nil),
	func(string) string { return "" },
	"",
	func(int) bool { return false },
	func(int) ([]byte, error) { return nil, nil },
}
