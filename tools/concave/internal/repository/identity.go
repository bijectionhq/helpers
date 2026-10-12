package repository

import "encoding/json"

// JSONDigest uses Go's deterministic map-key encoding. Each artifact owner
// chooses its payload: an exact receipt, a review, or deterministic evidence.
func JSONDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return Digest(data), nil
}
