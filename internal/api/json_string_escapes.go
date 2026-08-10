package api

import "errors"

// validateJSONStringSurrogateEscapes rejects UTF-16 surrogate escape sequences
// that encoding/json would otherwise silently replace with U+FFFD.
func validateJSONStringSurrogateEscapes(data []byte) error {
	inString := false
	for index := 0; index < len(data); index++ {
		switch data[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || index+1 >= len(data) {
				continue
			}
			if data[index+1] != 'u' {
				// The escaped byte cannot terminate the current JSON string or
				// begin another escape sequence.
				index++
				continue
			}
			value, ok := decodeJSONHexQuad(data, index+2)
			if !ok {
				// The JSON decoder reports malformed non-hex or truncated escapes.
				continue
			}
			switch {
			case value >= 0xd800 && value <= 0xdbff:
				lowStart := index + 6
				if lowStart+5 >= len(data) || data[lowStart] != '\\' || data[lowStart+1] != 'u' {
					return errors.New("unpaired high surrogate escape in JSON string")
				}
				low, lowOK := decodeJSONHexQuad(data, lowStart+2)
				if !lowOK || low < 0xdc00 || low > 0xdfff {
					return errors.New("unpaired high surrogate escape in JSON string")
				}
				index = lowStart + 5
			case value >= 0xdc00 && value <= 0xdfff:
				return errors.New("unpaired low surrogate escape in JSON string")
			default:
				index += 5
			}
		}
	}
	return nil
}

func decodeJSONHexQuad(data []byte, start int) (uint16, bool) {
	if start < 0 || start+4 > len(data) {
		return 0, false
	}
	var value uint16
	for _, digit := range data[start : start+4] {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value |= uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
