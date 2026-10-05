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

func TestDerive(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	a1, a2, other := b.Derive(PurposeFile), b.Derive(PurposeFile), b.Derive("iets anders")
	if len(a1) != 32 || !bytes.Equal(a1, a2) || bytes.Equal(a1, other) || bytes.Equal(a1, key) {
		t.Fatal("afgeleide sleutels kloppen niet")
	}
	b2, _ := New(bytes.Repeat([]byte{8}, 32))
	if bytes.Equal(b2.Derive(PurposeFile), a1) {
		t.Fatal("een andere masterkey geeft dezelfde sleutel")
	}
	var none *Box
	if none.Derive(PurposeFile) != nil {
		t.Fatal("zonder masterkey hoort er geen sleutel te zijn")
	}
	f1, f2 := Fingerprint(a1, []byte("auth_pass geheim12")), Fingerprint(a1, []byte("auth_pass geheim13"))
	if len(f1) != 64 || f1 == f2 || f1 != Fingerprint(a1, []byte("auth_pass geheim12")) || f1 == Fingerprint(other, []byte("auth_pass geheim12")) {
		t.Fatal("vingerafdrukken kloppen niet")
	}
}
