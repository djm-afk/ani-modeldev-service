package biz

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// ErrInvalidCursor signals a list cursor that failed integrity or binding
// checks: malformed, tampered, or reused across a different tenant, resource
// kind or filter set. Callers map it to InvalidArgument; it never degrades to
// "first page", so a tampered cursor cannot silently shift the window.
var ErrInvalidCursor = errors.New("INVALID_CURSOR")

const (
	// cursorVersion is the payload version. A mismatch is rejected fail-closed
	// so a future payload change invalidates old cursors explicitly.
	cursorVersion = 1
	// cursorMaxTokenLen bounds the decoded payload, mirroring the transport's
	// 2048-byte cursor limit and rejecting oversized blobs before JSON parsing.
	cursorMaxTokenLen = 2048
	cursorMinKeyLen   = 32
	cursorMaxKeyLen   = 128
)

// CursorKind identifies the list a cursor belongs to. Cursors are not
// interchangeable across kinds.
type CursorKind string

const (
	CursorKindExecutions    CursorKind = "executions"
	CursorKindPresets       CursorKind = "presets"
	CursorKindInputVersions CursorKind = "input-versions"
	CursorKindArtifacts     CursorKind = "artifacts"
)

// CursorBinding is the request context a cursor is bound to. Every field is
// compared on decode; any mismatch yields ErrInvalidCursor. FilterKey is a
// stable fingerprint of the effective list filters, so the same filter set
// replayed reproduces it while a changed filter set is rejected.
type CursorBinding struct {
	Kind      CursorKind
	TenantID  string
	FilterKey string
}

// cursorPayload is the signed cursor body. Field names are single characters to
// keep the token short; clients never parse it.
type cursorPayload struct {
	V int    `json:"v"`
	K string `json:"k"`
	T string `json:"t"`
	F string `json:"f"`
	I string `json:"i"`
}

// CursorCodec signs and verifies list pagination cursors with HMAC-SHA256.
// Token format: base64url(payload) + "." + base64url(mac). The boundary value
// bound into the payload is the previous page's last keyset key (item id).
type CursorCodec struct {
	key []byte
}

// NewCursorCodec validates and copies the signing key. A key outside 32..128
// bytes is rejected: pagination must not run without a usable secret.
func NewCursorCodec(key []byte) (*CursorCodec, error) {
	if len(key) < cursorMinKeyLen || len(key) > cursorMaxKeyLen {
		return nil, errors.New("cursor signing key must be 32..128 bytes")
	}
	return &CursorCodec{key: append([]byte(nil), key...)}, nil
}

// Encode binds the boundary to the request context and returns a signed cursor.
// An empty boundary means "no next page" and produces an empty token.
func (c *CursorCodec) Encode(binding CursorBinding, last string) (string, error) {
	if last == "" {
		return "", nil
	}
	if c == nil || len(c.key) == 0 {
		return "", ErrInvalidCursor
	}
	body, err := json.Marshal(cursorPayload{
		V: cursorVersion, K: string(binding.Kind), T: binding.TenantID, F: binding.FilterKey, I: last,
	})
	if err != nil {
		return "", ErrInvalidCursor
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	if len(encoded) > cursorMaxTokenLen {
		return "", ErrInvalidCursor
	}
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Decode verifies the signature and binding and returns the raw boundary key.
// Any failure returns ErrInvalidCursor; it never falls back to the first page.
func (c *CursorCodec) Decode(token string, binding CursorBinding) (string, error) {
	if c == nil || len(c.key) == 0 {
		return "", ErrInvalidCursor
	}
	if len(token) == 0 || len(token) > cursorMaxTokenLen {
		return "", ErrInvalidCursor
	}
	left, right, ok := strings.Cut(token, ".")
	if !ok {
		return "", ErrInvalidCursor
	}
	body, err := base64.RawURLEncoding.DecodeString(left)
	if err != nil || base64.RawURLEncoding.EncodeToString(body) != left {
		return "", ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(right)
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != right {
		return "", ErrInvalidCursor
	}
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte(left))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", ErrInvalidCursor
	}
	var payload cursorPayload
	if json.Unmarshal(body, &payload) != nil || payload.V != cursorVersion {
		return "", ErrInvalidCursor
	}
	if payload.K != string(binding.Kind) || payload.T != binding.TenantID || payload.F != binding.FilterKey || payload.I == "" {
		return "", ErrInvalidCursor
	}
	return payload.I, nil
}

// CursorFilterKey builds a stable fingerprint for a list's effective filters.
// Parts are joined with a unit separator so distinct filter values cannot
// collide; it is hashed so the raw filter text never leaves the service.
func CursorFilterKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return strconv.FormatUint(uint64(len(parts)), 10) + ":" + base64.RawURLEncoding.EncodeToString(sum[:])
}
