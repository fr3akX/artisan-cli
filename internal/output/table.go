package output

import (
	"fmt"
	"io"
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
		if _, err := io.WriteString(w, value); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

// WriteDetails renders labeled values in caller-defined stable order.
func WriteDetails(w io.Writer, fields []DetailField) error {
	for _, field := range fields {
		if _, err := fmt.Fprintf(w, "%s: %s\n", field.Label, field.Value); err != nil {
			return err
		}
	}
	return nil
}
