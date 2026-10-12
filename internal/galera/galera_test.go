package galera

import (
	"strings"
	"testing"
)

const clusterUUID = "5c3a1e0e-a8f1-11f0-9a3b-7e1c2d3f4a5b"

func TestParseGrastate(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     Position
		err      bool
	}{
		{"na een gewone stop", "# GALERA saved state\nversion: 2.1\nuuid:    " + clusterUUID + "\nseqno:   1234\nsafe_to_bootstrap: 1\n",
			Position{UUID: clusterUUID, Seqno: 1234, SafeToBootstrap: true}, false},
		{"na een crash", "# GALERA saved state\nversion: 2.1\nuuid:    " + clusterUUID + "\nseqno:   -1\nsafe_to_bootstrap: 0\n",
			Position{UUID: clusterUUID, Seqno: -1}, false},
		{"nieuwe node", "# GALERA saved state\nversion: 2.1\nuuid:    " + NoUUID + "\nseqno:   -1\nsafe_to_bootstrap: 0\n",
			Position{UUID: NoUUID, Seqno: -1}, false},
		{"leeg", "", Position{Seqno: -1}, true},
		{"kapotte seqno", "uuid: " + clusterUUID + "\nseqno: x\n", Position{UUID: clusterUUID, Seqno: -1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseGrastate(tc.in)
			if (err != nil) != tc.err || got != tc.want {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	if p, _ := ParseGrastate("uuid: " + clusterUUID + "\nseqno: -1\n"); p.Known() || p.String() != "onbekend" {
		t.Fatalf("seqno -1 is onbekend: %+v", p)
	}
	if p, _ := ParseGrastate("uuid: " + NoUUID + "\nseqno: 0\n"); p.Known() {
		t.Fatalf("de lege uuid is onbekend: %+v", p)
	}
}

func TestParseRecovered(t *testing.T) {
	// Zoals galera_recovery van MariaDB 11.8 het schrijft, en mysqld
	// --wsrep-recover in zijn log, met een GTID erachter.
	for _, in := range []string{
		"--wsrep_start_position=" + clusterUUID + ":1234\n",
		"2026-10-12 12:01:02 0 [Note] WSREP: Recovered position: " + clusterUUID + ":1234\n",
		"[Note] WSREP: Recovered position " + strings.ToUpper(clusterUUID) + ":1234,0-1-1234\n",
		"[Note] WSREP: Recovered position: " + clusterUUID + ":7\n--wsrep_start_position=" + clusterUUID + ":1234\n",
	} {
		p, err := ParseRecovered(in)
		if err != nil || p != (Position{UUID: clusterUUID, Seqno: 1234}) {
			t.Errorf("%q: %+v %v", in, p, err)
		}
	}
	if _, err := ParseRecovered("WSREP: Failed to recover position\n"); err == nil {
		t.Error("zonder positie: geen fout")
	}
}

func TestChoose(t *testing.T) {
	pos := func(seqno int64, safe bool) Position {
		return Position{UUID: clusterUUID, Seqno: seqno, SafeToBootstrap: safe}
	}
	for _, tc := range []struct {
		name  string
		cands []Candidate
		want  string
		err   string
	}{
		{"hoogste seqno", []Candidate{{"nc1", 1, pos(9, false)}, {"nc2", 2, pos(11, false)}, {"nc3", 3, pos(10, false)}}, "nc2", ""},
		{"gelijk: laatst gestopt", []Candidate{{"nc1", 1, pos(11, false)}, {"nc2", 2, pos(11, false)}, {"nc3", 3, pos(11, true)}}, "nc3", ""},
		{"gelijk: laagste volgnummer", []Candidate{{"nc3", 3, pos(11, false)}, {"nc2", 2, pos(11, false)}}, "nc2", ""},
		{"safe maar ouder", []Candidate{{"nc1", 1, pos(12, false)}, {"nc2", 2, pos(11, true)}}, "nc1", ""},
		{"onbekend", []Candidate{{"nc1", 1, pos(12, false)}, {"nc2", 2, pos(-1, false)}}, "", "de positie van nc2 is onbekend"},
		{"andere geschiedenis", []Candidate{{"nc1", 1, pos(12, false)}, {"nc2", 2, Position{UUID: "11111111-a8f1-11f0-9a3b-7e1c2d3f4a5b", Seqno: 3}}}, "",
			"nc1 en nc2 hebben een andere geschiedenis"},
		{"niemand", nil, "", "er is geen node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Choose(tc.cands)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("fout %v, verwacht %q", err, tc.err)
				}
				return
			}
			if err != nil || got.Name != tc.want {
				t.Fatalf("%+v %v, verwacht %s", got, err, tc.want)
			}
		})
	}
}
