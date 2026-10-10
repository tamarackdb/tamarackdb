package ndjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

type testValue struct {
	X bool `json:"x"`
}

type failingMarshal struct{}

func (failingMarshal) MarshalJSON() ([]byte, error) { return nil, errors.New("boom") }

type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteValueWritesImmediately(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.WriteValue(testValue{X: true}); err != nil {
		t.Fatalf("WriteValue() error = %v", err)
	}
	if buf.String() != "{\"x\":true}\n" {
		t.Errorf("after first WriteValue(), dst = %q, want %q", buf.String(), "{\"x\":true}\n")
	}

	if err := w.WriteValue(testValue{X: false}); err != nil {
		t.Fatalf("WriteValue() error = %v", err)
	}
	want := "{\"x\":true}\n{\"x\":false}\n"
	if buf.String() != want {
		t.Errorf("after second WriteValue(), dst = %q, want %q", buf.String(), want)
	}
}

func TestWriteLineWritesImmediately(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WriteLine([]byte("abc")); err != nil {
		t.Fatalf("WriteLine() error = %v", err)
	}
	if buf.String() != "abc\n" {
		t.Errorf("dst = %q, want %q", buf.String(), "abc\n")
	}
}

func TestWriteValueMarshalError(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WriteValue(failingMarshal{}); err == nil {
		t.Fatal("WriteValue() error = nil, want error from MarshalJSON")
	}
	if buf.Len() != 0 {
		t.Errorf("dst.Len() = %d, want 0: a failed marshal must write nothing", buf.Len())
	}
}

func TestWriteValueDestinationWriteError(t *testing.T) {
	w := NewWriter(failingWriter{})
	if err := w.WriteValue(testValue{X: true}); err == nil {
		t.Fatal("WriteValue() error = nil, want error from destination Write")
	}
}

// countingWriter counts the calls to Write.
type countingWriter struct {
	bytes.Buffer
	writes int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes++
	return c.Buffer.Write(p)
}

func TestWriteAppendWritesOneLineInOneWrite(t *testing.T) {
	var dst countingWriter
	w := NewWriter(&dst)
	for _, s := range []string{"a long first line", "b"} {
		if err := w.WriteAppend(func(b []byte) []byte { return AppendString(b, s) }); err != nil {
			t.Fatalf("WriteAppend() error = %v", err)
		}
	}
	if want := "\"a long first line\"\n\"b\"\n"; dst.String() != want {
		t.Errorf("dst = %q, want %q: the reused buffer must not leak the first line into the second", dst.String(), want)
	}
	if dst.writes != 2 {
		t.Errorf("Write calls = %d, want 2, one per line", dst.writes)
	}
}

// appendStringCases holds a string of each kind AppendString escapes
// differently.
var appendStringCases = []string{
	"",
	"plain ascii",
	`quote " and backslash \`,
	"control \b \f \n \r \t \x00 \x1f \x7f",
	"html < > &",
	"accents é à ç, and 漢字, and 🌲",
	"separators \u2028 and \u2029",
	"invalid \xff utf-8 \xe2\x82 cut",
	`{"json":"payload","n":[1,2]}`,
}

func TestAppendStringMatchesEncodingJSON(t *testing.T) {
	for _, s := range appendStringCases {
		want, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("json.Marshal(%q) error = %v", s, err)
		}
		if got := AppendString(nil, s); !bytes.Equal(got, want) {
			t.Errorf("AppendString(%q) = %s, want %s", s, got, want)
		}
	}
}

func FuzzAppendStringMatchesEncodingJSON(f *testing.F) {
	for _, s := range appendStringCases {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("json.Marshal(%q) error = %v", s, err)
		}
		if got := AppendString(nil, s); !bytes.Equal(got, want) {
			t.Errorf("AppendString(%q) = %s, want %s", s, got, want)
		}
	})
}
