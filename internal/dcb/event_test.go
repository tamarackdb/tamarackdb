package dcb

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestIdentifierSetMarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		set  IdentifierSet
		want string
	}{
		{"empty", nil, `{}`},
		{"single value", IdentifierSet{{Name: "otherId", Value: "baz"}}, `{"otherId":"baz"}`},
		{"multiple values same name", IdentifierSet{
			{Name: "courseId", Value: "foo"},
			{Name: "courseId", Value: "bar"},
		}, `{"courseId":["foo","bar"]}`},
		{"multiple names sorted", IdentifierSet{
			{Name: "otherId", Value: "baz"},
			{Name: "courseId", Value: "foo"},
		}, `{"courseId":"foo","otherId":"baz"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.set)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("Marshal() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestIdentifierSetUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string][]string
		wantErr bool
	}{
		{"string value", `{"otherId":"baz"}`, map[string][]string{"otherId": {"baz"}}, false},
		{"array value", `{"courseId":["foo","bar"]}`, map[string][]string{"courseId": {"foo", "bar"}}, false},
		{"mixed", `{"courseId":["foo","bar"],"otherId":"baz"}`, map[string][]string{"courseId": {"foo", "bar"}, "otherId": {"baz"}}, false},
		{"non-string array element", `{"courseId":[1,2]}`, nil, true},
		{"empty array value", `{"courseId":[]}`, nil, true},
		{"non-object top level", `["a","b"]`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s IdentifierSet
			err := json.Unmarshal([]byte(tt.input), &s)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Unmarshal() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			got := map[string][]string{}
			for _, id := range s {
				got[id.Name] = append(got[id.Name], id.Value)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Unmarshal() = %+v, want %+v", got, tt.want)
			}
			for name, wantVals := range tt.want {
				gotVals := got[name]
				if len(gotVals) != len(wantVals) {
					t.Errorf("name %q: got %v, want %v", name, gotVals, wantVals)
					continue
				}
				for i, v := range wantVals {
					if gotVals[i] != v {
						t.Errorf("name %q: got %v, want %v", name, gotVals, wantVals)
						break
					}
				}
			}
		})
	}
}

func TestIdentifierSetRoundTrip(t *testing.T) {
	original := IdentifierSet{
		{Name: "courseId", Value: "foo"},
		{Name: "courseId", Value: "bar"},
		{Name: "otherId", Value: "baz"},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var roundTripped IdentifierSet
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !sameSet(original, roundTripped) {
		t.Errorf("round trip mismatch: got %+v, want %+v (order-independent)", roundTripped, original)
	}
}

func sameSet[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[T]int{}
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
	}
	for _, c := range counts {
		if c != 0 {
			return false
		}
	}
	return true
}

func TestEventDataValidate(t *testing.T) {
	const ct = "2026-09-01T14:23:05.123456Z"
	makeIdentifiers := func(n int) IdentifierSet {
		out := make(IdentifierSet, n)
		for i := range out {
			out[i] = Identifier{Name: "id", Value: string(rune('a' + i))}
		}
		return out
	}
	makeMetadata := func(n int) MetadataSet {
		out := make(MetadataSet, n)
		for i := range out {
			out[i] = Metadata{Name: "md", Value: string(rune('a' + i))}
		}
		return out
	}

	tests := []struct {
		name    string
		event   EventData
		wantErr error
	}{
		{"valid minimal event", EventData{Type: "user-created", ClientTime: ct}, nil},
		{"missing type", EventData{ClientTime: ct}, ErrMissingType},
		{"missing clientTime", EventData{Type: "t"}, ErrMissingClientTime},
		{"exactly 20 identifiers", EventData{Type: "t", ClientTime: ct, Identifiers: makeIdentifiers(20)}, nil},
		{"21 identifiers", EventData{Type: "t", ClientTime: ct, Identifiers: makeIdentifiers(21)}, ErrTooManyIdentifiers},
		{"exactly 20 metadata", EventData{Type: "t", ClientTime: ct, Metadata: makeMetadata(20)}, nil},
		{"21 metadata", EventData{Type: "t", ClientTime: ct, Metadata: makeMetadata(21)}, ErrTooManyMetadata},
		{"duplicate identifier", EventData{Type: "t", ClientTime: ct, Identifiers: IdentifierSet{
			{Name: "userId", Value: "123"}, {Name: "userId", Value: "123"},
		}}, ErrDuplicateIdentifier},
		{"duplicate metadata", EventData{Type: "t", ClientTime: ct, Metadata: MetadataSet{
			{Name: "tenantId", Value: "acme"}, {Name: "tenantId", Value: "acme"},
		}}, ErrDuplicateMetadata},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.event.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("Validate() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() error = %v, want %v", err, tt.wantErr)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("Validate() error is not a *ValidationError: %v", err)
			}
		})
	}
}

func TestValidateClientTime(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"valid", "2026-09-01T14:23:05.123456Z", nil},
		{"valid, all-zero fraction", "2026-09-01T14:23:05.000000Z", nil},
		{"missing", "", ErrMissingClientTime},
		{"3 fractional digits (JavaScript toISOString)", "2026-09-01T14:23:05.123Z", ErrInvalidClientTime},
		{"9 fractional digits", "2026-09-01T14:23:05.123456789Z", ErrInvalidClientTime},
		{"no fractional digits", "2026-09-01T14:23:05Z", ErrInvalidClientTime},
		{"offset +00:00", "2026-09-01T14:23:05.123456+00:00", ErrInvalidClientTime},
		{"offset -04:00", "2026-09-01T10:23:05.123456-04:00", ErrInvalidClientTime},
		{"lowercase z", "2026-09-01T14:23:05.123456z", ErrInvalidClientTime},
		{"not a timestamp", "yesterday", ErrInvalidClientTime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateClientTime(tt.in)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateClientTime(%q) error = %v, want nil", tt.in, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateClientTime(%q) error = %v, want %v", tt.in, err, tt.wantErr)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("ValidateClientTime(%q) error is not a *ValidationError: %v", tt.in, err)
			}
			if tt.wantErr == ErrInvalidClientTime && ve.Message != clientTimeFormatMessage {
				t.Errorf("message = %q, want %q", ve.Message, clientTimeFormatMessage)
			}
		})
	}
	if clientTimeFormatMessage != "clientTime must be UTC with exactly 6 fractional digits, e.g. 2026-09-01T14:23:05.123456Z" {
		t.Errorf("clientTimeFormatMessage = %q", clientTimeFormatMessage)
	}
}

func TestFormatTimeIsAValidClientTime(t *testing.T) {
	loc := time.FixedZone("EDT", -4*3600)
	for _, tm := range []time.Time{
		time.Date(2026, 9, 1, 10, 23, 5, 123000000, loc),
		time.Date(2026, 9, 1, 14, 23, 5, 0, time.UTC),
		time.Date(2026, 9, 1, 14, 23, 5, 123456789, time.UTC),
	} {
		s := FormatTime(tm)
		if err := ValidateClientTime(s); err != nil {
			t.Errorf("ValidateClientTime(FormatTime(%v)) = %v, want nil (got %q)", tm, err, s)
		}
	}
	if got, want := FormatTime(time.Date(2026, 9, 1, 10, 23, 5, 123000000, loc)), "2026-09-01T14:23:05.123000Z"; got != want {
		t.Errorf("FormatTime = %q, want %q", got, want)
	}
}

func TestEventDataSize(t *testing.T) {
	e := EventData{
		Type:    "abc",
		Payload: "xyz",
	}
	if got, want := e.Size(), 6; got != want {
		t.Errorf("Size() = %d, want %d", got, want)
	}

	// "é" is 2 bytes in UTF-8; Size must count bytes, not runes.
	e2 := EventData{Type: "é"}
	if got, want := e2.Size(), 2; got != want {
		t.Errorf("Size() = %d, want %d (multi-byte char counted as bytes)", got, want)
	}
}

func TestEventMarshalJSON(t *testing.T) {
	tm := time.Date(2026, 9, 1, 14, 23, 5, 123000000, time.UTC)
	e := Event{
		Sequence:  12346,
		WriteTime: tm,
		EventData: EventData{
			Type:        "user-created",
			ClientTime:  "2026-09-01T14:23:04.999999Z",
			Identifiers: IdentifierSet{{Name: "userId", Value: "123"}},
			Metadata:    MetadataSet{{Name: "tenantId", Value: "acme"}},
			Payload:     "...",
		},
	}
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"sequence":12346,"clientTime":"2026-09-01T14:23:04.999999Z","writeTime":"2026-09-01T14:23:05.123000Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}`
	if string(got) != want {
		t.Errorf("Marshal() = %s, want %s", got, want)
	}
}

func TestEventUnmarshalJSON(t *testing.T) {
	input := `{"sequence":12346,"clientTime":"2026-09-01T14:23:04.999999Z","writeTime":"2026-09-01T14:23:05.123456Z","type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"..."}`
	var e Event
	if err := json.Unmarshal([]byte(input), &e); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if e.Sequence != 12346 {
		t.Errorf("Sequence = %d, want 12346", e.Sequence)
	}
	wantTime := time.Date(2026, 9, 1, 14, 23, 5, 123456000, time.UTC)
	if !e.WriteTime.Equal(wantTime) {
		t.Errorf("WriteTime = %v, want %v", e.WriteTime, wantTime)
	}
	if e.ClientTime != "2026-09-01T14:23:04.999999Z" {
		t.Errorf("ClientTime = %q, want %q", e.ClientTime, "2026-09-01T14:23:04.999999Z")
	}
	if e.Type != "user-created" {
		t.Errorf("Type = %q, want %q", e.Type, "user-created")
	}
}

func TestEventUnmarshalJSONInvalidWriteTime(t *testing.T) {
	input := `{"sequence":1,"clientTime":"2026-09-01T14:23:05.123456Z","writeTime":"not-a-time","type":"t","identifiers":{},"metadata":{},"payload":""}`
	var e Event
	if err := json.Unmarshal([]byte(input), &e); err == nil {
		t.Fatalf("Unmarshal() error = nil, want error")
	}
}
