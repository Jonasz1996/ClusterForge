package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const npmLine = `[17/Nov/2026:08:00:%02d +0000] - %d %d - GET https app.jbogaert.be "/x" [Client %s] [Length 100] [Gzip -] [Sent-to 10.0.0.5] "curl/8" "-"`

func npmLogLine(sec, status int, ip string) string {
	return fmt.Sprintf(npmLine, sec, status, status, ip) + "\n"
}

func addToFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func TestNPMTraffic(t *testing.T) {
	a, data, _ := npmFixture(t)
	a.NPMSnapshotDir = filepath.Join(t.TempDir(), "snap")
	ctx := context.Background()
	logDir := filepath.Join(data, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Twee open verbindingen op 443 en één op 81 (telt niet).
	tcp := "  sl  local_address rem_address   st\n" +
		"   0: 0100007F:01BB 0200007F:D431 01 x\n" +
		"   1: 0100007F:01BB 0200007F:D432 01 x\n" +
		"   2: 0100007F:0051 0200007F:D433 01 x\n" +
		"   3: 0100007F:0050 0200007F:D434 0A x\n"
	writeFile(t, a.Root, "/proc/4242/net/tcp", tcp)
	log7 := filepath.Join(logDir, "proxy-host-7_access.log")
	addToFile(t, log7, npmLogLine(1, 200, "1.2.3.4")+npmLogLine(2, 401, "5.6.7.8")+npmLogLine(3, 403, "5.6.7.8")+"rommel\n")
	addToFile(t, filepath.Join(logDir, "fallback_access.log"), npmLogLine(1, 404, "9.9.9.9"))

	res := a.web(ctx, protocol.WebRequest{Op: protocol.WebOpNPMTraffic})
	tr := res.Traffic
	if res.Error != "" || tr == nil {
		t.Fatalf("%+v", res)
	}
	if tr.Lines != 3 || tr.Skipped != 1 || tr.Connections != 2 || len(tr.Minutes) != 1 {
		t.Fatalf("eerste keer: %+v", tr)
	}
	m := tr.Minutes[0]
	if m.HostID != 7 || m.Requests != 3 || m.S2xx != 1 || m.S4xx != 2 || m.Bytes != 300 {
		t.Fatalf("minuut: %+v", m)
	}
	if len(tr.IPs) != 2 || tr.IPs[0].IP != "5.6.7.8" || tr.IPs[0].Denied != 2 {
		t.Fatalf("IP's: %+v", tr.IPs)
	}

	// Niets nieuw: niets dubbel.
	if r := a.web(ctx, protocol.WebRequest{Op: protocol.WebOpNPMTraffic}).Traffic; r.Lines != 0 {
		t.Fatalf("dubbel geteld: %+v", r)
	}
	// Een halve regel wacht tot hij af is.
	addToFile(t, log7, npmLogLine(10, 502, "1.2.3.4")+`[17/Nov/2026:08:00:11 +0000] - 200 200 - GET`)
	if r := a.web(ctx, protocol.WebRequest{Op: protocol.WebOpNPMTraffic}).Traffic; r.Lines != 1 || r.Minutes[0].S5xx != 1 {
		t.Fatalf("na aanvullen: %+v", r)
	}
	addToFile(t, log7, ` https app.jbogaert.be "/" [Client 1.2.3.4] [Length 5] [Gzip -] [Sent-to x] "-" "-"`+"\n")
	if r := a.web(ctx, protocol.WebRequest{Op: protocol.WebOpNPMTraffic}).Traffic; r.Lines != 1 || r.Skipped != 0 {
		t.Fatalf("halve regel: %+v", r)
	}

	// Rotatie: wat nog in het oude bestand stond, telt mee.
	addToFile(t, log7, npmLogLine(20, 200, "1.2.3.4"))
	if err := os.Rename(log7, log7+".1"); err != nil {
		t.Fatal(err)
	}
	addToFile(t, log7, npmLogLine(21, 200, "1.2.3.4")+npmLogLine(22, 200, "1.2.3.4"))
	if r := a.web(ctx, protocol.WebRequest{Op: protocol.WebOpNPMTraffic}).Traffic; r.Lines != 3 {
		t.Fatalf("na rotatie: %+v", r)
	}
}

func TestParseTrafficLine(t *testing.T) {
	ts, st, ip, size, ok := parseTrafficLine([]byte(`[17/Nov/2026:09:15:42 +0100] HIT 304 304 - GET https cloud.jbogaert.be "/a b" [Client 2001:db8::1] [Length 0] [Gzip -] [Sent-to 192.168.10.185] "Mozilla" "-"`))
	if !ok || st != 304 || ip != "2001:db8::1" || size != 0 || ts.Hour() != 8 {
		t.Fatalf("%v %d %s %d %v", ts, st, ip, size, ok)
	}
}
