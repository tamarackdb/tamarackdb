// Package names holds the rules for the strings that name and key what
// TamarackDB stores. Packages dcb and projection both apply them, when an
// event or a projection is written.
//
// A name is an event type, a tag name, or a projection type. It MUST hold
// 1 to MaxNameLength bytes, each an ASCII letter, a digit, or one of
// - _ . : @ /. A name is chosen by a developer in code, so a short list of
// characters costs nothing.
//
// A value is a tag value or a projection id. It MUST hold 1 to
// MaxValueLength bytes, with no ASCII control character (U+0000 to U+001F,
// and U+007F). A value is data, such as an email or a UUID, so any other
// text is allowed. The JSON decoder already turns invalid UTF-8 into
// U+FFFD, so a value is always valid UTF-8.
//
// These rules MUST NOT get stricter once a version is released: data
// written under the looser rule would become invalid. Loosening them
// stays safe.
package names
