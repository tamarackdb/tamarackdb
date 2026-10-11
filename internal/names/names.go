package names

const (
	MaxNameLength  = 128 // max bytes in a name
	MaxValueLength = 255 // max bytes in a value
)

// NameRule and ValueRule describe the rules, for error messages.
const (
	NameRule  = "1 to 128 bytes, only ASCII letters, digits, and - _ . : @ /"
	ValueRule = "1 to 255 bytes, with no control character"
)

// ValidName reports whether s is a valid name.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > MaxNameLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '-', c == '_', c == '.', c == ':', c == '@', c == '/':
		default:
			return false
		}
	}
	return true
}

// ValidValue reports whether s is a valid value. It checks bytes, not
// runes: a control character is always a single byte in UTF-8.
func ValidValue(s string) bool {
	if len(s) == 0 || len(s) > MaxValueLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}
