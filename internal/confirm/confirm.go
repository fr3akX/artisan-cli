// Package confirm implements the CLI's explicit mutation confirmation policy.
package confirm

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Ask applies the noninteractive --yes gate or, on a terminal, asks the caller
// to type the complete word "yes". It returns false for EOF and every other
// response so ambiguous input can never authorize a mutation.
func Ask(in io.Reader, out io.Writer, terminal, yes bool, prompt string) (bool, error) {
	if yes {
		return true, nil
	}
	if !terminal {
		return false, nil
	}
	if _, err := fmt.Fprintf(out, "%s Type yes to continue: ", prompt); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(io.LimitReader(in, 4097)).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}
