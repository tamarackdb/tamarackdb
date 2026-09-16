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
