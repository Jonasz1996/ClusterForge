package sealbox

import (
	"encoding/base64"
	"testing"
)

func TestSealOpen(t *testing.T) {
	sk, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	pk, err := PublicKey(base64.StdEncoding.EncodeToString(sk.PublicKey().Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Seal(pk, []byte(`{"username":"jonas"}`), []byte("cf-inlog:a.jbogaert.be"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(sk, s, []byte("cf-inlog:a.jbogaert.be"))
	if err != nil || string(got) != `{"username":"jonas"}` {
		t.Fatalf("open: %q %v", got, err)
	}
	if _, err := Open(sk, s, []byte("cf-inlog:b.jbogaert.be")); err == nil {
		t.Fatal("ander domein mag niet openen")
	}
	other, _ := GenerateKey()
	if _, err := Open(other, s, []byte("cf-inlog:a.jbogaert.be")); err == nil {
		t.Fatal("andere sleutel mag niet openen")
	}
}
