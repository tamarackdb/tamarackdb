package names

import (
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"simple", "seat-reserved", true},
		{"every allowed character", "azAZ09-_.:@/", true},
		{"schema suffix", "user-profile@7", true},
		{"namespace", "billing/invoice-paid", true},
		{"max length", strings.Repeat("a", MaxNameLength), true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", MaxNameLength+1), false},
		{"space", "seat reserved", false},
		{"newline", "seat\nreserved", false},
		{"tab", "seat\treserved", false},
		{"accent", "réservé", false},
		{"quote", `seat"reserved`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidName(c.s); got != c.want {
				t.Errorf("ValidName(%q) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}

func TestValidValue(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"simple", "123", true},
		{"email", "alice+test@example.com", true},
		{"space and slash", "a b/c", true},
		{"accent", "Hélène", true},
		{"max length", strings.Repeat("a", MaxValueLength), true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", MaxValueLength+1), false},
		{"newline", "a\nb", false},
		{"tab", "a\tb", false},
		{"nul", "a\x00b", false},
		{"del", "a\x7fb", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidValue(c.s); got != c.want {
				t.Errorf("ValidValue(%q) = %v, want %v", c.s, got, c.want)
			}
		})
	}
}
