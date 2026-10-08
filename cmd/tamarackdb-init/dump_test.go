package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

const validLine = `{"sequence":7,"time":"2024-03-01T09:12:44.000001Z","type":"order-placed",` +
	`"identifiers":{"orderId":"o-1","sku":["a","b"]},"metadata":{"tenantId":"acme"},"payload":"..."}`

// line builds a line from validLine with old replaced by new.
func line(old, new string) string {
	if !strings.Contains(validLine, old) {
		panic("line: " + old + " not in validLine")
	}
	return strings.Replace(validLine, old, new, 1)
}

func TestParseLineAcceptsAnEvent(t *testing.T) {
	ev, err := parseLine([]byte(validLine))
	if err != nil {
		t.Fatalf("parseLine() error = %v", err)
	}
	if ev.Sequence != 7 || ev.Type != "order-placed" || ev.Payload != "..." {
		t.Errorf("event = %+v", ev)
	}
	if got := ev.Time.Format("2006-01-02T15:04:05.000000Z07:00"); got != "2024-03-01T09:12:44.000001Z" {
		t.Errorf("Time = %s", got)
	}
	if len(ev.Identifiers) != 3 || len(ev.Metadata) != 1 {
		t.Errorf("Identifiers = %v, Metadata = %v", ev.Identifiers, ev.Metadata)
	}
}

func TestParseLineAccepts(t *testing.T) {
	cases := map[string]string{
		"whitespace":      strings.ReplaceAll(validLine, `":`, `" : `),
		"carriage return": validLine + "\r",
		"empty tags":      line(`"identifiers":{"orderId":"o-1","sku":["a","b"]},"metadata":{"tenantId":"acme"}`, `"identifiers":{},"metadata":{}`),
		"empty payload":   line(`"payload":"..."`, `"payload":""`),
		"fields in another order": `{"payload":"","metadata":{},"identifiers":{},"type":"t",` +
			`"time":"2024-03-01T09:12:44.000000Z","sequence":1}`,
	}
	for name, l := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLine([]byte(l)); err != nil {
				t.Errorf("parseLine(%s) error = %v", l, err)
			}
		})
	}
}

func TestParseLineRefuses(t *testing.T) {
	var many []string
	for i := range 21 {
		many = append(many, fmt.Sprintf(`"id%d":"v"`, i))
	}
	cases := map[string]string{
		"missing sequence":     line(`"sequence":7,`, ``),
		"missing time":         line(`"time":"2024-03-01T09:12:44.000001Z",`, ``),
		"missing type":         line(`"type":"order-placed",`, ``),
		"missing identifiers":  line(`"identifiers":{"orderId":"o-1","sku":["a","b"]},`, ``),
		"missing metadata":     line(`"metadata":{"tenantId":"acme"},`, ``),
		"missing payload":      line(`,"payload":"..."`, ``),
		"null sequence":        line(`"sequence":7`, `"sequence":null`),
		"null time":            line(`"time":"2024-03-01T09:12:44.000001Z"`, `"time":null`),
		"null type":            line(`"type":"order-placed"`, `"type":null`),
		"null identifiers":     line(`"identifiers":{"orderId":"o-1","sku":["a","b"]}`, `"identifiers":null`),
		"null metadata":        line(`"metadata":{"tenantId":"acme"}`, `"metadata":null`),
		"null payload":         line(`"payload":"..."`, `"payload":null`),
		"extra field":          line(`"payload":"..."`, `"payload":"...","position":7`),
		"repeated field":       line(`"payload":"..."`, `"payload":"...","type":"other"`),
		"repeated identifier":  line(`"orderId":"o-1"`, `"orderId":"o-1","orderId":"o-2"`),
		"sequence 0":           line(`"sequence":7`, `"sequence":0`),
		"negative sequence":    line(`"sequence":7`, `"sequence":-1`),
		"decimal sequence":     line(`"sequence":7`, `"sequence":7.0`),
		"string sequence":      line(`"sequence":7`, `"sequence":"7"`),
		"time with offset":     line(`.000001Z`, `.000001+00:00`),
		"time with 3 digits":   line(`.000001Z`, `.001Z`),
		"time without zone":    line(`.000001Z`, `.000001`),
		"time in another zone": line(`09:12:44.000001Z`, `05:12:44.000001-04:00`),
		"empty type":           line(`"type":"order-placed"`, `"type":""`),
		"identifiers array":    line(`{"orderId":"o-1","sku":["a","b"]}`, `[]`),
		"21 identifiers":       line(`{"orderId":"o-1","sku":["a","b"]}`, "{"+strings.Join(many, ",")+"}"),
		"duplicate identifier": line(`["a","b"]`, `["a","a"]`),
		"payload object":       line(`"payload":"..."`, `"payload":{}`),
		"data after the event": validLine + ` {}`,
		"not an object":        `[` + validLine + `]`,
		"null":                 `null`,
		"invalid JSON":         validLine[:len(validLine)-1],
	}
	for name, l := range cases {
		t.Run(name, func(t *testing.T) {
			if ev, err := parseLine([]byte(l)); err == nil {
				t.Errorf("parseLine(%s) = %+v, want an error", l, ev)
			}
		})
	}
}

func readAll(dump string) (int, error) {
	r := newDumpReader(strings.NewReader(dump))
	n := 0
	for {
		_, err := r.next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		n++
	}
}

func TestDumpReaderLines(t *testing.T) {
	l := validLine
	cases := []struct {
		name string
		dump string
		want int
		err  string // "" for no error
	}{
		{"final newline", l + "\n" + l + "\n", 2, ""},
		{"no final newline", l + "\n" + l, 2, ""},
		{"empty dump", "", 0, ""},
		{"empty line in the middle", l + "\n\n" + l, 1, "line 2: empty line"},
		{"two final newlines", l + "\n\n", 1, "line 2: empty line"},
		{"blank line", l + "\n  \n" + l, 1, "line 2: empty line"},
		{"only a newline", "\n", 0, "line 1: empty line"},
		{"bad line names its number", l + "\n" + l + "\n{}\n", 2, "line 3: "},
		{"long line", strings.Replace(l, `"..."`, `"`+strings.Repeat("x", 200_000)+`"`, 1), 1, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := readAll(c.dump)
			if n != c.want {
				t.Errorf("read %d event(s), want %d", n, c.want)
			}
			switch {
			case c.err == "" && err != nil:
				t.Errorf("error = %v, want none", err)
			case c.err != "" && (err == nil || !strings.HasPrefix(err.Error(), c.err)):
				t.Errorf("error = %v, want one starting with %q", err, c.err)
			}
		})
	}
}
