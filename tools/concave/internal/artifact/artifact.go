// Package artifact reads bounded, unambiguous JSON for policy and saved evidence.
package artifact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const MaxBytes = 128 << 20

// ReadBytes rejects nonregular and oversized artifacts before reading their contents.
// The reader limit also covers growth after the size check.
func ReadBytes(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact %q is not a regular file", path)
	}
	if info.Size() > MaxBytes {
		return nil, fmt.Errorf("artifact %q exceeds 128 MiB", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact %q is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("artifact %q exceeds 128 MiB", path)
	}
	return data, nil
}

func Read(path string, value any) ([]byte, error) {
	data, err := ReadBytes(path)
	if err != nil {
		return nil, err
	}
	if err := Decode(data, value); err != nil {
		return nil, fmt.Errorf("artifact %q: %w", path, err)
	}
	return data, nil
}

func Decode(data []byte, v any) error {
	if err := uniqueKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return fmt.Errorf("expected one JSON document")
	}
	return nil
}

// Reject duplicate object keys before typed decoding: an artifact has one meaning.
func uniqueKeys(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate or invalid JSON key %v", key)
				}
				seen[name] = true
				if err = uniqueKeys(d); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := uniqueKeys(d); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	return nil
}
