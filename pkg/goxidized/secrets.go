package goxidized

import (
	"encoding/json"
)

const redactedMarker = "[REDACTED]"

type SecretString struct {
	value string
}

func NewSecretString(value string) SecretString {
	return SecretString{value: value}
}

func (s SecretString) String() string {
	return redactedMarker
}

func (s SecretString) GoString() string {
	return redactedMarker
}

func (s SecretString) Reveal() string {
	return s.value
}

func (s SecretString) IsZero() bool {
	return s.value == ""
}

func (s SecretString) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedMarker)
}

type SecretBytes struct {
	value []byte
}

func NewSecretBytes(value []byte) SecretBytes {
	cp := append([]byte(nil), value...)
	return SecretBytes{value: cp}
}

func (s SecretBytes) String() string {
	return redactedMarker
}

func (s SecretBytes) GoString() string {
	return redactedMarker
}

func (s SecretBytes) Reveal() []byte {
	return append([]byte(nil), s.value...)
}

func (s SecretBytes) IsZero() bool {
	return len(s.value) == 0
}

func (s SecretBytes) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedMarker)
}
