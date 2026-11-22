package assistant

import "testing"

func TestMask(t *testing.T) {
	for in, want := range map[string]string{
		"verbinden met password=hunter2 naar db":                                           "verbinden met password=[geheim] naar db",
		`DB_PASSWORD: "s3cr3t met spatie"`:                                                 `DB_PASSWORD: [geheim]`,
		`{"apiKey":"abc123","user":"jonas"}`:                                               `{"apiKey":[geheim],"user":"jonas"}`,
		"client_secret = x9; next=1":                                                       "client_secret = [geheim]; next=1",
		"Authorization: Bearer abcdefgh.ijklmnop.qrstuvw":                                  "Authorization: Bearer [geheim]",
		"dial postgres://app:geheim@db:5432/shop failed":                                   "dial postgres://app:[geheim]@db:5432/shop failed",
		"token eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2lnbg":                                "token [geheim]",
		"env:\n  - name: DB_PASSWORD\n    value: hunter2\n  - name: LOG\n    value: debug": "env:\n  - name: DB_PASSWORD\n    value: [geheim]\n  - name: LOG\n    value: debug",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----\nklaar":      "[geheim]\nklaar",
		`{"env":[{"name":"API_TOKEN","value":"x\"y"},{"name":"LOG","value":"debug"}]}`:     `{"env":[{"name":"API_TOKEN","value":"[geheim]"},{"name":"LOG","value":"debug"}]}`,
		// Wat geen geheim is, blijft staan.
		"tests passed: 5":                                     "tests passed: 5",
		"tokenizer: klaar":                                    "tokenizer: klaar",
		"secretKeyRef:\n  name: web-db\n  key: url":           "secretKeyRef:\n  name: web-db\n  key: url",
		"GET /healthz 200 in 3ms":                             "GET /healthz 200 in 3ms",
		"volumes:\n- secret:\n    secretName: web-tls":        "volumes:\n- secret:\n    secretName: web-tls",
		"Back-off restarting failed container web in pod web": "Back-off restarting failed container web in pod web",
	} {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q)\n = %q\nwant %q", in, got, want)
		}
	}
}
