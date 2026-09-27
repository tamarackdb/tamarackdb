package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// file is every section a TOML configuration file may hold: [server] for
// tamarackdb-server, [backup] for tamarackdb-backup, or both. Each binary
// reads only its own section, but the file is checked as a whole, so a
// misspelled key in either section is caught.
type file struct {
	Server Config       `toml:"server"`
	Backup BackupConfig `toml:"backup"`
}

// readFile reads and decodes the configuration file at path. found is
// false when the file doesn't exist, which isn't an error: the caller
// falls back to the environment and defaults. An unknown key, or a key
// outside any section, is an error: it would otherwise be ignored without
// a word, and the setting it meant to change would silently keep its
// default.
func readFile(path string) (f file, data []byte, found bool, err error) {
	data, err = os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return file{}, nil, false, nil
	case err != nil:
		return file{}, nil, false, fmt.Errorf("config: read %s: %w", path, err)
	}
	if err := decodeStrict(data, &f); err != nil {
		return file{}, nil, false, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return f, data, true, nil
}

// decodeStrict decodes data into v, rejecting any key v has no field for,
// and names every such key in the error.
func decodeStrict(data []byte, v any) error {
	err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(v)
	var missing *toml.StrictMissingError
	if !errors.As(err, &missing) {
		return err
	}
	keys := make([]string, len(missing.Errors))
	for i, e := range missing.Errors {
		row, _ := e.Position()
		keys[i] = fmt.Sprintf("%s (line %d)", strings.Join(e.Key(), "."), row)
	}
	return fmt.Errorf("unknown key: %s", strings.Join(keys, ", "))
}

// validationError wraps a Validate error with the file it came from, or
// with nothing when there is no file, so the message never names a file
// that doesn't exist.
func validationError(path string, found bool, err error) error {
	if found {
		return fmt.Errorf("config: %s: %w", path, err)
	}
	return fmt.Errorf("config: %w", err)
}
