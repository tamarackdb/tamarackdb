package ndjson

import (
	"bytes"
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

func TestFlushHeaderFirstThenBody(t *testing.T) {
	w := NewWriter()
	if err := w.WriteValue(testValue{X: true}); err != nil {
		t.Fatalf("WriteValue() error = %v", err)
	}
	if err := w.WriteValue(testValue{X: false}); err != nil {
		t.Fatalf("WriteValue() error = %v", err)
	}

	var buf bytes.Buffer
	if err := w.Flush(&buf, testValue{X: true}); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	want := "{\"x\":true}\n{\"x\":true}\n{\"x\":false}\n"
	if buf.String() != want {
		t.Errorf("Flush() output = %q, want %q", buf.String(), want)
	}
}

func TestFlushEmptyBody(t *testing.T) {
	w := NewWriter()
	var buf bytes.Buffer
	if err := w.Flush(&buf, testValue{X: false}); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if buf.String() != "{\"x\":false}\n" {
		t.Errorf("Flush() output = %q, want header-only line", buf.String())
	}
}

func TestWriteValueMarshalError(t *testing.T) {
	w := NewWriter()
	if err := w.WriteValue(failingMarshal{}); err == nil {
		t.Fatal("WriteValue() error = nil, want error from MarshalJSON")
	}
	if w.Len() != 0 {
		t.Errorf("Len() = %d, want 0 after a failed WriteValue", w.Len())
	}
}

func TestFlushDestinationWriteError(t *testing.T) {
	w := NewWriter()
	if err := w.WriteValue(testValue{X: true}); err != nil {
		t.Fatalf("WriteValue() error = %v", err)
	}
	if err := w.Flush(failingWriter{}, testValue{X: true}); err == nil {
		t.Fatal("Flush() error = nil, want error from destination Write")
	}
}

func TestLenTracksBufferedBody(t *testing.T) {
	w := NewWriter()
	if w.Len() != 0 {
		t.Errorf("Len() = %d, want 0 initially", w.Len())
	}
	if err := w.WriteLine([]byte("abc")); err != nil {
		t.Fatalf("WriteLine() error = %v", err)
	}
	if w.Len() != 4 { // "abc" + '\n'
		t.Errorf("Len() = %d, want 4", w.Len())
	}
}
