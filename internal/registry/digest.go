package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// digestOf computes the canonical sha256 digest of a manifest's bytes.
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// shortDigest abbreviates a digest for display: sha256:abcdef01... -> abcdef01.
// Values that are not digests are returned unchanged.
func shortDigest(d string) string {
	return ShortDigest(d)
}

// ShortDigest abbreviates a digest to its first 12 hex characters, dropping the
// algorithm prefix. Non-digest input is returned unchanged.
func ShortDigest(d string) string {
	if d == "" {
		return ""
	}
	if i := strings.Index(d, ":"); i >= 0 {
		d = d[i+1:]
	}
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
