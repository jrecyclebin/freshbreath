// Package utils is the home for small helpers that more than one package
// needs. Anything here must be dependency-free (standard library only) so
// every layer — db, formats, server — can import it without cycles.
package utils

import (
	crand "crypto/rand"
)

// nonceAlphabet is the 64-symbol alphabet for fresh-minted nonces: 0-9,
// A-Za-z, and -_. It packs 6 bits per symbol, so a 10-char nonce carries
// ~60 bits of entropy — enough for every current use (app nonces, pending
// states, refresh-family IDs, act-ticket IDs, elicitation IDs). 256 % 64
// == 0, so byte%64 picks each symbol without bias. Existing 48-hex nonces
// minted before this change stay valid and untouched; only new mints are
// short. Revisit only if a nonce ever faces an unlimited-time online
// attacker.
const nonceAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_"

// GenNonce mints a 10-char crypto-random ID (~60 bits) over the compact
// alphabet. It is the one generator for every opaque identifier the server
// mints: app nonces, act tickets, refresh-family IDs, MCP elicitation
// state, OAuth flow state. Uniqueness is not guaranteed — probabilistic
// collision on 60 bits is vanishingly rare, but callers that must retry on
// a store collision should check and re-mint (see db.CreateApp).
func GenNonce() string {
	b := make([]byte, 10)
	if _, err := crand.Read(b); err != nil {
		panic(err)
	}
	out := make([]byte, 10)
	for i, v := range b {
		out[i] = nonceAlphabet[int(v)%64]
	}
	return string(out)
}
