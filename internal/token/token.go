// Package token creates and compares the unguessable secrets that stand in
// for user accounts: a campaign's join link, a participant's private link and
// the organizer's management link.
package token

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
)

// Bytes of entropy per token. 16 bytes is 128 bit, which is far beyond
// guessable and still short enough to paste into a chat message.
const Bytes = 16

// New returns a fresh URL safe token.
func New() string {
	b := make([]byte, Bytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on any supported platform; if it ever
		// does we must not hand out a predictable token.
		panic("secret-santa: out of randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Equal compares two tokens in constant time so that response timing does not
// leak how much of a guess was correct.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
