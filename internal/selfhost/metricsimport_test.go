package selfhost

import "testing"

func TestParseImport(t *testing.T) {
	got := parseImport([]string{
		"127.0.0.1 klaar 1700000000 1700000240",
		"192.168.113.161 bezig 1700000000 535822336",
		"192.168.113.162 mislukt 1700000000 1700000010 curl: (7) Failed to connect",
		"onzin",
	})
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	if got[0].State != "klaar" || got[0].Ended.Sub(*got[0].Started).Minutes() != 4 {
		t.Errorf("klaar: %+v", got[0])
	}
	if got[1].State != "bezig" || got[1].Bytes != 535822336 || got[1].Ended != nil {
		t.Errorf("bezig: %+v", got[1])
	}
	if got[2].Reason != "curl: (7) Failed to connect" {
		t.Errorf("mislukt: %+v", got[2])
	}
}
