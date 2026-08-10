package command

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fr3akX/artisan-cli/internal/output"
)

const maxPricePerKgEURCents int64 = 2_147_483_647

func parsePricePerKgEUR(raw string) (int64, *output.Error) {
	invalid := func() (int64, *output.Error) {
		return 0, &output.Error{ExitCode: 2, Code: "invalid_price_per_kg_eur", Message: "Price per kg must be a nonnegative decimal EUR amount with at most two decimal places"}
	}
	if raw == "" || strings.Count(raw, ".") > 1 {
		return invalid()
	}
	parts := strings.SplitN(raw, ".", 2)
	wholeDigits := parts[0]
	if !asciiDigits(wholeDigits) || (len(wholeDigits) > 1 && wholeDigits[0] == '0') {
		return invalid()
	}
	fractionDigits := "00"
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 || !asciiDigits(parts[1]) {
			return invalid()
		}
		fractionDigits = parts[1]
		if len(fractionDigits) == 1 {
			fractionDigits += "0"
		}
	}
	whole, err := strconv.ParseInt(wholeDigits, 10, 64)
	if err != nil || whole > maxPricePerKgEURCents/100 {
		return invalid()
	}
	fraction, err := strconv.ParseInt(fractionDigits, 10, 64)
	if err != nil {
		return invalid()
	}
	cents := whole*100 + fraction
	if cents > maxPricePerKgEURCents {
		return invalid()
	}
	return cents, nil
}

func asciiDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func formatEURCents(cents int64) string {
	return fmt.Sprintf("€%d.%02d", cents/100, cents%100)
}

func formatSignedEURCents(cents int64) string {
	if cents < 0 {
		return "-" + formatEURCents(-cents)
	}
	return formatEURCents(cents)
}

func optionalEURCents(cents *int64) string {
	if cents == nil {
		return "-"
	}
	return formatEURCents(*cents)
}
