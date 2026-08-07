// Package confirm implements the CLI's explicit mutation confirmation policy.
package confirm

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxConfirmationResponseBytes = 4096

var errConfirmationTooLong = errors.New("confirmation response is too long")

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
	// Read at most one byte beyond the largest possible valid line: 4,096
	// content bytes plus CRLF. This detects overflow without unbounded buffering.
	line, err := bufio.NewReader(io.LimitReader(in, maxConfirmationResponseBytes+3)).ReadString('\n')
	complete := strings.HasSuffix(line, "\n")
	response := line
	if complete {
		response = strings.TrimSuffix(response, "\n")
		if strings.HasSuffix(response, "\r") {
			response = strings.TrimSuffix(response, "\r")
		}
	}
	if len(response) > maxConfirmationResponseBytes {
		return false, errConfirmationTooLong
	}
	if err != nil && err != io.EOF {
		return false, err
	}
	if !complete {
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(response), "yes"), nil
}
