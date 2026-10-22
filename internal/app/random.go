package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// tokenBytes is the length of a generated random filename stem.
const tokenBytes = 16

// randomToken returns a cryptographically random hex filename stem.
func randomToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("could not generate a random filename: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
