package biz

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func testCodec(t *testing.T) *CursorCodec {
	t.Helper()
	key := make([]byte, 48)
	for i := range key {
		key[i] = byte(i + 1)
	}
	codec, err := NewCursorCodec(key)
	if err != nil {
		t.Fatalf("NewCursorCodec: %v", err)
	}
	return codec
}

func TestNewCursorCodecRejectsWeakKeys(t *testing.T) {
	for _, size := range []int{0, 31, 129} {
		if _, err := NewCursorCodec(make([]byte, size)); err == nil {
			t.Fatalf("NewCursorCodec accepted %d-byte key", size)
		}
	}
	for _, size := range []int{32, 128} {
		if _, err := NewCursorCodec(make([]byte, size)); err != nil {
			t.Fatalf("NewCursorCodec rejected %d-byte key: %v", size, err)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	codec := testCodec(t)
	binding := CursorBinding{Kind: CursorKindExecutions, TenantID: "tenant-a", FilterKey: CursorFilterKey("", "", "")}
	last := "477b197d-4ce3-4509-b2f6-a9213d930663"
	token, err := codec.Encode(binding, last)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// The token is an opaque signed blob, never the raw boundary id.
	if token == last || !strings.Contains(token, ".") {
		t.Fatalf("token is not an opaque signed cursor: %q", token)
	}
	body, _, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatalf("token missing signature separator: %q", token)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("payload not base64url: %v", err)
	}
	if strings.Contains(string(decoded), " ") {
		t.Fatalf("payload unexpectedly padded")
	}
	got, err := codec.Decode(token, binding)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != last {
		t.Fatalf("Decode = %q, want %q", got, last)
	}
}

func TestCursorEmptyBoundaryIsEmptyToken(t *testing.T) {
	codec := testCodec(t)
	binding := CursorBinding{Kind: CursorKindPresets, TenantID: "tenant-a"}
	token, err := codec.Encode(binding, "")
	if err != nil || token != "" {
		t.Fatalf("Encode(empty) = %q, %v; want \"\"", token, err)
	}
}

func TestCursorRejectsTamperAndBindingMismatch(t *testing.T) {
	codec := testCodec(t)
	binding := CursorBinding{Kind: CursorKindExecutions, TenantID: "tenant-a", FilterKey: CursorFilterKey("RUNNING")}
	token, err := codec.Encode(binding, "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Flip one byte of the signature.
	tampered := []byte(token)
	if tampered[len(tampered)-1] == 'A' {
		tampered[len(tampered)-1] = 'B'
	} else {
		tampered[len(tampered)-1] = 'A'
	}

	cases := map[string]struct {
		token   string
		binding CursorBinding
	}{
		"tampered signature": {string(tampered), binding},
		"cross tenant":       {token, CursorBinding{Kind: CursorKindExecutions, TenantID: "tenant-b", FilterKey: binding.FilterKey}},
		"cross kind":         {token, CursorBinding{Kind: CursorKindArtifacts, TenantID: binding.TenantID, FilterKey: binding.FilterKey}},
		"changed filter":     {token, CursorBinding{Kind: CursorKindExecutions, TenantID: binding.TenantID, FilterKey: CursorFilterKey("SUCCEEDED")}},
		"garbage":            {"not-a-cursor", binding},
		"missing signature":  {"Zm9v", binding},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := codec.Decode(tc.token, tc.binding); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("Decode = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func TestCursorRejectsUnicodeAndWhitespace(t *testing.T) {
	codec := testCodec(t)
	binding := CursorBinding{Kind: CursorKindInputVersions, TenantID: "tenant-a", FilterKey: CursorFilterKey("READY")}
	token, err := codec.Encode(binding, "22222222-2222-2222-2222-222222222222")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for name, mutated := range map[string]string{
		"leading space":  " " + token,
		"trailing space": token + " ",
		"newline":        token + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := codec.Decode(mutated, binding); !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("Decode = %v, want ErrInvalidCursor", err)
			}
		})
	}
}

func TestCursorCodecWithoutKeyFailsClosed(t *testing.T) {
	var codec *CursorCodec
	binding := CursorBinding{Kind: CursorKindPresets, TenantID: "tenant-a"}
	if _, err := codec.Decode("Zm9v.YmFy", binding); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("nil codec decode = %v, want ErrInvalidCursor", err)
	}
	if _, err := codec.Encode(binding, "11111111-1111-1111-1111-111111111111"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("nil codec encode = %v, want ErrInvalidCursor", err)
	}
}

func TestCursorFilterKeyIsStableAndCollisionResistant(t *testing.T) {
	if CursorFilterKey("a", "b") != CursorFilterKey("a", "b") {
		t.Fatal("filter key is not stable")
	}
	if CursorFilterKey("ab") == CursorFilterKey("a", "b") {
		t.Fatal("filter key collides across different part boundaries")
	}
	if CursorFilterKey("") == CursorFilterKey("", "") {
		t.Fatal("filter key ignores part count")
	}
}
