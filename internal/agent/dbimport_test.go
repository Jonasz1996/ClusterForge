package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/agent/agenttest"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// fakeMaria speelt de mariadb-client voor dbImport na.
type fakeMaria struct {
	mu       sync.Mutex
	tables   int
	trust    string
	unlessOK bool
	failLoad bool
	queries  []string
	loaded   string
}

func (m *fakeMaria) execStdin(_ context.Context, stdin []byte, _ string, _ ...string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := string(stdin)
	m.queries = append(m.queries, strings.TrimSpace(q))
	switch {
	case strings.Contains(q, "information_schema.tables"):
		return []byte(strconv.Itoa(m.tables) + "\n"), nil
	case strings.Contains(q, "SELECT @@GLOBAL.log_bin_trust_function_creators"):
		return []byte(m.trust + "\n"), nil
	case strings.Contains(q, "SET GLOBAL log_bin_trust_function_creators = 1"):
		m.trust = "1"
		return nil, nil
	case strings.Contains(q, "SET GLOBAL log_bin_trust_function_creators = 0"):
		m.trust = "0"
		return nil, nil
	case strings.HasPrefix(q, "USE "):
		if m.tables == 0 {
			return []byte("ERROR 1146 (42S02) at line 2: Table 'zabbix.config' doesn't exist\n"), errors.New("exit status 1")
		}
		if m.unlessOK {
			return []byte("1\n"), nil
		}
		return nil, nil
	}
	return nil, errors.New("onbekende query " + q)
}

func (m *fakeMaria) stream(_ context.Context, in io.Reader, _ io.Writer, name string, args ...string) error {
	b, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failLoad {
		return errors.New("mariadb: ERROR 1064 at line 3")
	}
	m.loaded = string(b)
	m.tables = 3
	return nil
}

func importAgent(t *testing.T) (*Agent, *fakeMaria) {
	t.Helper()
	a, h := applyAgent(t)
	h.SetUnit("mariadb", agenttest.Unit{Enabled: true, Active: true})
	m := &fakeMaria{trust: "0"}
	a.ExecStdin, a.Stream = m.execStdin, m.stream
	dir := filepath.Join(a.Root, "usr/share/zabbix/sql-scripts/mysql")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write([]byte("CREATE TABLE config (id int);\n"))
	_ = w.Close()
	if err := os.WriteFile(filepath.Join(dir, "server.sql.gz"), gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return a, m
}

func TestDBImport(t *testing.T) {
	a, m := importAgent(t)
	req := &protocol.DBImportRequest{Database: "zabbix", Path: "/usr/share/zabbix/sql-scripts/mysql/server.sql.gz", TrustFunctions: true, Unless: "SELECT 1 FROM config"}
	res := a.dbImport(context.Background(), req)
	if res.Error != "" || res.Import == nil || !res.Import.Imported {
		t.Fatalf("eerste keer: %+v", res)
	}
	if m.loaded != "CREATE TABLE config (id int);\n" || m.trust != "0" {
		t.Fatalf("geladen %q, trust %s", m.loaded, m.trust)
	}
	// Tweede keer: unless geeft een rij.
	m.unlessOK = true
	res = a.dbImport(context.Background(), req)
	if res.Error != "" || res.Import == nil || res.Import.Imported || !strings.Contains(res.Import.Skipped, "unless") {
		t.Fatalf("tweede keer: %+v", res)
	}

	// Zonder unless: alleen in een lege database.
	req.Unless, req.TrustFunctions = "", false
	if res := a.dbImport(context.Background(), req); res.Import == nil || res.Import.Imported {
		t.Fatalf("niet leeg: %+v", res)
	}

	// Een mislukte import zet trust toch terug.
	a2, m2 := importAgent(t)
	m2.failLoad = true
	res = a2.dbImport(context.Background(), &protocol.DBImportRequest{Database: "zabbix", Path: "/usr/share/zabbix/sql-scripts/mysql/server.sql.gz", TrustFunctions: true})
	if res.Error == "" || m2.trust != "0" {
		t.Fatalf("mislukt: %+v trust %s", res, m2.trust)
	}

	for _, bad := range []*protocol.DBImportRequest{
		{Database: "zabbix", Path: "/etc/shadow"},
		{Database: "zab;bix", Path: "/x.sql"},
		{Database: "zabbix", Path: "/x.sql", Unless: "DROP TABLE config"},
		{Database: "zabbix", Path: "/x.sql", Unless: "SELECT 1; DROP TABLE config"},
		{Database: "zabbix", Path: "/bestaat/niet.sql"},
	} {
		if res := a.dbImport(context.Background(), bad); res.Error == "" {
			t.Errorf("%+v: geen fout", bad)
		}
	}
}
