package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseFilterLine(t *testing.T) {
	now := time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC)
	k, ok := parseFilterLine(`<134>1 2026-11-17T18:50:01+01:00 fw1.lan filterlog 812 - [meta sequenceId="4"] 96,,,fae5593,vtnet1,match,block,in,4,0x0,,64,0,0,DF,6,tcp,60,192.168.90.20,192.168.10.50,52144,445,0,S,1,,64240,,mss`, now)
	if !ok || k.Action != "block" || k.Interface != "vtnet1" || k.Proto != "tcp" || k.Src != "192.168.90.20" || k.Dst != "192.168.10.50" || k.Port != 445 || k.Rule != "fae5593" || !k.Hour.Equal(time.Date(2026, 11, 17, 17, 0, 0, 0, time.UTC)) {
		t.Fatalf("5424: %+v %v", k, ok)
	}
	k, ok = parseFilterLine(`Nov 17 18:50:01 fw1 filterlog[812]: 5,,,02f4bab0,vtnet0,match,pass,out,6,0x00,0x00000,64,udp,17,72,2a02::1,2a02::2,5353,53,72`, now)
	if !ok || k.Action != "pass" || k.Proto != "udp" || k.Src != "2a02::1" || k.Port != 53 || k.Dir != "out" {
		t.Fatalf("oud v6: %+v %v", k, ok)
	}
	k, ok = parseFilterLine(`<134>1 2026-11-17T18:50:01Z fw1 filterlog 812 - [meta] 7,,,abc,vtnet1,match,block,in,4,0x0,,64,0,0,none,1,icmp,84,10.0.0.1,10.0.0.2,request,1,2`, now)
	if !ok || k.Proto != "icmp" || k.Port != 0 {
		t.Fatalf("icmp: %+v %v", k, ok)
	}
	if _, ok := parseFilterLine("iets anders", now); ok {
		t.Fatal("geen filterlog toch gelezen")
	}
}

func TestOPNFilterLogRotation(t *testing.T) {
	c := &Collector{Root: t.TempDir()}
	dir := filepath.Join(c.Root, "var/log/filter")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `<134>1 2026-11-17T18:50:01Z fw1 filterlog 1 - [meta] 9,,,r1,vtnet1,match,block,in,4,0x0,,64,0,0,DF,6,tcp,60,10.0.0.9,10.0.0.10,1000,22,0,S` + "\n"
	day1 := filepath.Join(dir, "filter_20261117.log")
	if err := os.WriteFile(day1, []byte(strings.Repeat(line, 3)), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink("filter_20261117.log", filepath.Join(dir, "latest.log"))
	res, err := c.opnFilterLog()
	if err != nil || len(res.Flows) != 1 || res.Flows[0].Count != 3 {
		t.Fatalf("eerste keer: %v %+v", err, res)
	}
	// Twee regels erbij, dan een nieuwe dag met één regel.
	f, _ := os.OpenFile(day1, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(strings.Repeat(line, 2))
	_ = f.Close()
	if err := os.WriteFile(filepath.Join(dir, "filter_20261118.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "latest.log"))
	_ = os.Symlink("filter_20261118.log", filepath.Join(dir, "latest.log"))
	res, err = c.opnFilterLog()
	if err != nil || len(res.Flows) != 1 || res.Flows[0].Count != 3 {
		t.Fatalf("na rotatie: %v %+v", err, res)
	}
	if res, _ = c.opnFilterLog(); len(res.Flows) != 0 {
		t.Fatalf("dubbel geteld: %+v", res)
	}
}
