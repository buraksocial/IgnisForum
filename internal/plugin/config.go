package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
)

const MaxConfigBytes = 4096

var configKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// NormalizeConfig accepts a small flat object of public plugin options. Settings
// do not grant capabilities. No secret should be put here: a plugin can include
// its configuration in a public notice. Empty objects clear the configuration.
func NormalizeConfig(raw []byte) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > MaxConfigBytes {
		return nil, errors.New("plugin configuration exceeds 4096 bytes")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("plugin configuration must be an object")
	}
	settings := make(map[string]any)
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok || !configKeyPattern.MatchString(key) {
			return nil, errors.New("invalid plugin configuration key")
		}
		if _, exists := settings[key]; exists {
			return nil, errors.New("duplicate plugin configuration key")
		}
		var value any
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		switch v := value.(type) {
		case bool:
		case string:
			if len(v) > 256 {
				return nil, errors.New("plugin configuration string exceeds 256 bytes")
			}
		case json.Number:
			n, err := strconv.ParseInt(string(v), 10, 64)
			if err != nil || n < -1000000 || n > 1000000 {
				return nil, errors.New("plugin configuration number must be an integer between -1000000 and 1000000")
			}
			value = n
		default:
			return nil, errors.New("plugin configuration values must be strings, booleans or integers")
		}
		settings[key] = value
		if len(settings) > 32 {
			return nil, errors.New("plugin configuration has more than 32 keys")
		}
	}
	if end, err := dec.Token(); err != nil || end != json.Delim('}') {
		return nil, errors.New("invalid plugin configuration object")
	}
	if dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing plugin configuration data")
	}
	if len(settings) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(settings)
	// HTML-sensitive characters can expand on encoding; bound the actual ABI
	// payload as well as the submitted text.
	if len(encoded) > MaxConfigBytes {
		return nil, errors.New("normalized plugin configuration exceeds 4096 bytes")
	}
	return encoded, err
}
