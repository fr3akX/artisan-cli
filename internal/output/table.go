package output

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// DetailField is one stable label/value line in human detail output.
type DetailField struct {
	Label string
	Value string
}

// WriteTable renders complete values without width-based truncation.
func WriteTable(w io.Writer, headers []string, rows [][]string) error {
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if err := writeTableRow(table, headers); err != nil {
		return err
	}
	for _, row := range rows {
		if len(row) != len(headers) {
			return fmt.Errorf("table row has %d columns, want %d", len(row), len(headers))
		}
		if err := writeTableRow(table, row); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeTableRow(w io.Writer, values []string) error {
	for index, value := range values {
		if index > 0 {
			if _, err := io.WriteString(w, "\t"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(w, EscapeVisible(value)); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// WriteDetails renders labeled values in caller-defined stable order.
func WriteDetails(w io.Writer, fields []DetailField) error {
	for _, field := range fields {
		if _, err := fmt.Fprintf(w, "%s: %s\n", EscapeVisible(field.Label), EscapeVisible(field.Value)); err != nil {
			return err
		}
	}
	return nil
}

// EscapeVisible renders structural and control characters as visible ASCII escapes.
// Printable Unicode is preserved and values are never truncated.
func EscapeVisible(value string) string {
	var escaped strings.Builder
	for _, character := range value {
		switch character {
		case '\\':
			escaped.WriteString(`\\`)
		case '\t':
			escaped.WriteString(`\t`)
		case '\r':
			escaped.WriteString(`\r`)
		case '\n':
			escaped.WriteString(`\n`)
		default:
			if character <= 0x1f || (character >= 0x7f && character <= 0x9f) {
				_, _ = fmt.Fprintf(&escaped, `\x%02X`, character)
			} else {
				escaped.WriteRune(character)
			}
		}
	}
	return escaped.String()
}
