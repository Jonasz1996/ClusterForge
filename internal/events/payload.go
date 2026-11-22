package events

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const (
	// Hidden vervangt de waarde van een geheim.
	Hidden = "[verborgen]"
	// Changed is de markering dat een geheim gewijzigd is; die mag blijven.
	Changed = "gewijzigd"
	// maxString en maxPayload houden een regel leesbaar en het logboek klein.
	maxString  = 2 << 10
	maxPayload = 16 << 10
	shortened  = "[ingekort]"
)

// secretKey is true voor een veld waarvan de waarde nooit in een event mag.
// Gewone namen als code, token, content en value blijven zichtbaar; een
// module met geheime inhoud laat die zelf weg.
func secretKey(k string) bool {
	k = strings.ToLower(k)
	switch k {
	case "password", "totp_secret", "private_key", "value_enc":
		return true
	}
	return strings.HasSuffix(k, "_password") || strings.HasSuffix(k, "_secret")
}

// encodePayload maakt de JSON van een event: geheimen verborgen, lange
// strings en een te grote payload ingekort, en de herkomst onder origin.
func encodePayload(p map[string]any, o Origin) ([]byte, error) {
	var m map[string]any
	if len(p) > 0 {
		// Via JSON, zodat ook structs en getypte lijsten nagekeken worden.
		b, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if err := d.Decode(&m); err != nil {
			return nil, err
		}
		m = clean(m).(map[string]any)
	}
	if op := o.payload(); op != nil {
		if m == nil {
			m = map[string]any{}
		}
		if _, ok := m["origin"]; !ok {
			m["origin"] = op
		}
	}
	if len(m) == 0 {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	for len(b) > maxPayload {
		// Het grootste veld eerst, tot de regel past.
		big, size := "", 0
		for k, v := range m {
			if k == "origin" || v == shortened {
				continue
			}
			vb, _ := json.Marshal(v)
			if len(vb) > size {
				big, size = k, len(vb)
			}
		}
		if big == "" {
			break
		}
		m[big] = shortened
		m["truncated"] = true
		if b, err = json.Marshal(m); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func clean(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if secretKey(k) && val != nil && val != Changed && val != "" {
				out[k] = Hidden
				continue
			}
			out[k] = clean(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = clean(val)
		}
		return out
	case string:
		if len(x) > maxString {
			cut := maxString
			for cut > 0 && !utf8.RuneStart(x[cut]) {
				cut--
			}
			return x[:cut] + "…"
		}
		return x
	}
	return v
}
