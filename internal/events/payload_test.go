package events

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEncodePayloadHidesSecrets(t *testing.T) {
	type entry struct {
		Name           string `json:"name"`
		RootPassword   string `json:"root_password"`
		ClientSecret   string `json:"client_secret"`
		SecretRotation int    `json:"secret_rotation"`
	}
	b, err := encodePayload(map[string]any{
		"password":     "hunter22",
		"token_secret": Changed,
		"totp_secret":  map[string]any{"from": "AAAA", "to": "BBBB"},
		"private_key":  "-----BEGIN",
		"value_enc":    []byte{1, 2, 3},
		"code":         "123456",
		"token":        "zichtbaar",
		"nested":       []any{map[string]any{"db_password": "geheim"}},
		"typed":        []entry{{Name: "a", RootPassword: "geheim2", ClientSecret: "geheim3", SecretRotation: 7}},
		"empty_secret": "",
		"big":          int64(9007199254740993),
	}, Origin{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, leak := range []string{"hunter22", "AAAA", "BBBB", "BEGIN", "AQID", "geheim"} {
		if strings.Contains(s, leak) {
			t.Errorf("%q staat in %s", leak, s)
		}
	}
	for _, want := range []string{`"token_secret":"gewijzigd"`, `"code":"123456"`, `"token":"zichtbaar"`, `"secret_rotation":7`,
		`"empty_secret":""`, `"big":9007199254740993`, `"password":"[verborgen]"`} {
		if !strings.Contains(s, want) {
			t.Errorf("%s ontbreekt in %s", want, s)
		}
	}
}

func TestEncodePayloadTruncates(t *testing.T) {
	long := strings.Repeat("é", 3000)
	b, err := encodePayload(map[string]any{"error": long, "name": "web01"}, Origin{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if got := m["error"].(string); len(got) > maxString+len("…") || !strings.HasSuffix(got, "é…") {
		t.Errorf("string niet netjes ingekort: %d bytes", len(got))
	}

	big := map[string]any{"name": "web01"}
	for i := range 20 {
		big[string(rune('a'+i))] = strings.Repeat("x", 1500)
	}
	b, err = encodePayload(big, Origin{IP: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > maxPayload {
		t.Fatalf("payload van %d bytes", len(b))
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["truncated"] != true || m["name"] != "web01" || m["origin"] == nil {
		t.Fatalf("ingekorte payload mist velden: %v", m["truncated"])
	}
}

func TestOrigin(t *testing.T) {
	ctx := WithRequest(context.Background(), "203.0.113.5", []byte{0xde, 0xad, 0xbe, 0xef, 0x01})
	job := uuid.New()
	ctx = WithJob(ctx, job)
	b, err := encodePayload(nil, OriginFrom(ctx))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"origin":{"ip":"203.0.113.5","job_id":"` + job.String() + `","session":"deadbeef"}}`
	if string(b) != want {
		t.Fatalf("%s, wil %s", b, want)
	}
	// Een payload met een eigen origin houdt die.
	b, _ = encodePayload(map[string]any{"origin": "agent"}, OriginFrom(ctx))
	if string(b) != `{"origin":"agent"}` {
		t.Fatalf("eigen origin overschreven: %s", b)
	}
	if b, _ := encodePayload(nil, Origin{}); string(b) != "{}" {
		t.Fatalf("lege payload: %s", b)
	}
}
