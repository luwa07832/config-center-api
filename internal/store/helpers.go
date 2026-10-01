package store

import (
	"bytes"
	"time"
)

// nowUTC stamps versions with a stable RFC3339 time so output stays deterministic across hosts.
func nowUTC() string {
	return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

func bytesReader(raw []byte) *bytes.Reader { return bytes.NewReader(raw) }
