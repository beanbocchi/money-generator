package newsgate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
)

// TestFrozenPrompt pins the system prompt, the schema and the model. Changing any of
// them breaks ADR-0027's pre-registered shadow period, which then has to restart. The
// hashes were checked against services/newsgate/newsgate.py's SYSTEM, SCHEMA (as
// sorted-key compact JSON) and MODEL.
func TestFrozenPrompt(t *testing.T) {
	sch, err := json.Marshal(schema())
	if err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct {
		value []byte
		want  string
	}{
		"System": {[]byte(System), "b4f98143b3879f2a96990c006bd7d8b4be731b3706a823e95247971ebb823350"},
		"schema": {sch, "f7fc6913008a8d5fdce8f8edf29a5990adf4bb9b74d95caa0da30a8c520272d5"},
		"Model":  {[]byte(Model), "dedfefd51120f28386c9dba78e46feb403523e23d67980cdf24514ee0b50b5ed"},
	} {
		if got := fmt.Sprintf("%x", sha256.Sum256(tt.value)); got != tt.want {
			t.Errorf("%s changed (sha256 %s): this ends ADR-0027's shadow period", name, got)
		}
	}
}
