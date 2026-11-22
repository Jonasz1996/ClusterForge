package agent

// Het schema van een app laden uit een bestand op de node (IMPORT in een
// recept, fase 9). De agent laadt via de socket als root, zoals de andere
// databaseverzoeken, en alleen als de database nog leeg is of Unless geen
// rij geeft: een tweede uitrol laadt niets opnieuw.

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func (a *Agent) dbImport(ctx context.Context, req *protocol.DBImportRequest) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	fail := func(err error) protocol.DBResponse {
		res.Error = err.Error()
		return res
	}
	if req == nil {
		return fail(errors.New("import zonder inhoud"))
	}
	if err := dbsql.CheckDatabase(req.Database); err != nil {
		return fail(err)
	}
	if !strings.HasSuffix(req.Path, ".sql") && !strings.HasSuffix(req.Path, ".sql.gz") {
		return fail(fmt.Errorf("%s is geen .sql of .sql.gz", req.Path))
	}
	if u := strings.TrimSpace(req.Unless); u != "" && (!strings.HasPrefix(strings.ToUpper(u), "SELECT ") || strings.Contains(u, ";")) {
		return fail(errors.New("unless is één SELECT zonder ;"))
	}
	p, err := a.path(req.Path)
	if err != nil {
		return fail(err)
	}
	if err := a.needMaria(ctx); err != nil {
		return fail(err)
	}
	f, err := os.Open(p)
	if err != nil {
		return fail(fmt.Errorf("%s: %w", req.Path, err))
	}
	defer func() { _ = f.Close() }()

	// Is er al iets?
	check := fmt.Sprintf("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '%s';\n", req.Database)
	if req.Unless != "" {
		check = "USE `" + req.Database + "`;\n" + strings.TrimSpace(req.Unless) + ";\n"
	}
	out, err := a.mariaQuery(ctx, check)
	switch {
	case err != nil && req.Unless != "" && strings.Contains(string(out), "ERROR 1146"):
		// De tabel uit Unless bestaat nog niet: laden.
	case err != nil:
		return fail(fmt.Errorf("controle vooraf: %s", errorLine(out, err)))
	case req.Unless != "" && strings.TrimSpace(string(out)) != "":
		res.Import = &protocol.DBImportResult{Skipped: "unless gaf een rij: het schema is er al"}
		return res
	case req.Unless == "" && strings.TrimSpace(string(out)) != "0":
		res.Import = &protocol.DBImportResult{Skipped: "de database heeft al tabellen"}
		return res
	}

	if req.TrustFunctions {
		before, err := a.mariaQuery(ctx, "SELECT @@GLOBAL.log_bin_trust_function_creators;\n")
		if err != nil {
			return fail(fmt.Errorf("log_bin_trust_function_creators lezen: %s", errorLine(before, err)))
		}
		if strings.TrimSpace(string(before)) != "1" {
			if out, err := a.mariaQuery(ctx, "SET GLOBAL log_bin_trust_function_creators = 1;\n"); err != nil {
				return fail(fmt.Errorf("log_bin_trust_function_creators aanzetten: %s", errorLine(out, err)))
			}
			defer func() {
				// Een eigen context: ook na een afgebroken import gaat het
				// weer uit.
				ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer cancel()
				if out, err := a.mariaQuery(ctx2, "SET GLOBAL log_bin_trust_function_creators = 0;\n"); err != nil {
					a.Log.Error("log_bin_trust_function_creators uitzetten mislukt", "err", errorLine(out, err))
				}
			}()
		}
	}

	var in io.Reader = f
	if strings.HasSuffix(req.Path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", filepath.Base(req.Path), err))
		}
		defer func() { _ = gz.Close() }()
		in = gz
	}
	var discard tailWriter
	if err := a.stream(ctx, in, &discard, "mariadb", "--batch", "--default-character-set=utf8mb4", req.Database); err != nil {
		return fail(fmt.Errorf("laden in %s: %w", req.Database, err))
	}
	res.Import = &protocol.DBImportResult{Imported: true}
	return res
}

// mariaQuery voert sql uit via stdin, zonder kolomnamen.
func (a *Agent) mariaQuery(ctx context.Context, sql string) ([]byte, error) {
	args := []string{"--batch", "--skip-column-names"}
	out, err := a.runStdin(ctx, []byte(sql), "mariadb", args...)
	if err != nil && notInstalled(err) {
		out, err = a.runStdin(ctx, []byte(sql), "mysql", args...)
	}
	return out, err
}

// errorLine is de regel met ERROR uit de uitvoer, of de fout zelf.
func errorLine(out []byte, err error) string {
	for line := range bytes.SplitSeq(out, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("ERROR")) {
			return string(line)
		}
	}
	return err.Error()
}
