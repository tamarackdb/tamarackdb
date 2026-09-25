package document

import (
	"errors"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestDataValidate(t *testing.T) {
	tests := []struct {
		name    string
		doc     Data
		wantErr error
	}{
		{"write", Data{Type: "user-profile", ID: "123", Payload: strPtr("...")}, nil},
		{"delete", Data{Type: "user-profile", ID: "123"}, nil},
		{"missing type", Data{ID: "123", Payload: strPtr("...")}, ErrMissingType},
		{"missing id", Data{Type: "user-profile", Payload: strPtr("...")}, ErrMissingID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.doc.Validate()
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
