package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
)

func TestBox(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	ct := b.Seal([]byte("geheim"), []byte("proxmox:1"))
	if bytes.Contains(ct, []byte("geheim")) {
		t.Fatal("plaintext in ciphertext")
	}
	pt, err := b.Open(ct, []byte("proxmox:1"), b.KeyID)
	if err != nil || string(pt) != "geheim" {
		t.Fatalf("Open = %q, %v", pt, err)
	}
	if _, err := b.Open(ct, []byte("proxmox:2"), b.KeyID); err == nil {
		t.Error("andere aad werd aanvaard")
	}
	if _, err := b.Open(ct, []byte("proxmox:1"), "andere"); !errors.Is(err, ErrWrongKey) {
		t.Errorf("andere sleutel: %v", err)
	}
	if _, err := b.Open(ct[:5], nil, b.KeyID); err == nil {
		t.Error("te kort werd aanvaard")
	}
}

func TestParseKey(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	for _, s := range []string{base64.StdEncoding.EncodeToString(key), hex.EncodeToString(key) + "\n"} {
		got, err := ParseKey(s)
		if err != nil || !bytes.Equal(got, key) {
			t.Errorf("ParseKey(%q) = %v, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "kort", base64.StdEncoding.EncodeToString(key[:16])} {
		if _, err := ParseKey(s); err == nil {
			t.Errorf("ParseKey(%q) aanvaard", s)
		}
	}
}
