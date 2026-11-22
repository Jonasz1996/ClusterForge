package agent

// Databases en gebruikers in PostgreSQL: lezen wie er is met welke rechten
// op de databases, en een database of gebruiker aanmaken of weghalen. De
// opdrachten komen uit dbsql en gaan via stdin naar psql, als postgres via
// de socket. Een wachtwoord gaat nooit naar PostgreSQL: de agent rekent de
// SCRAM-verifier zelf uit, zoals psql met \password doet, zodat het ook
// niet in het log van PostgreSQL kan staan.

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/agent/dbsql"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// scramIterations is wat PostgreSQL zelf gebruikt (scram_iterations).
const scramIterations = 4096

// scramVerifier is wat PostgreSQL van een wachtwoord bewaart bij
// scram-sha-256: SCRAM-SHA-256$<iteraties>:<salt>$<StoredKey>:<ServerKey>
// (RFC 5802 en 7677). Een wachtwoord van ClusterForge is alleen letters
// en cijfers, dus SASLprep verandert er niets aan.
func scramVerifier(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, scramIterations, sha256.Size)
	if err != nil {
		return "", err
	}
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	stored := sha256.Sum256(mac(salted, "Client Key"))
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", scramIterations, b64(salt), b64(stored[:]), b64(mac(salted, "Server Key"))), nil
}

// needPG zegt waarom er op deze node niets met PostgreSQL kan.
func (a *Agent) needPG(ctx context.Context) error {
	units, err := a.dbUnits(ctx)
	if err != nil {
		return fmt.Errorf("systemctl show: %w", err)
	}
	if _, ok := pgUnit(units); !ok {
		return errors.New("op deze node staat geen PostgreSQL")
	}
	return nil
}

func (a *Agent) pgUsers(ctx context.Context) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}, Users: []protocol.DBUser{}}
	if err := a.needPG(ctx); err != nil {
		res.Error = err.Error()
		return res
	}
	rows, err := a.psql(ctx, "", dbsql.PGRoles)
	if err != nil {
		res.Error = "gebruikers: " + dbError(err)
		return res
	}
	index := map[string]int{}
	flags := []string{"SUPERUSER", "CREATEDB", "CREATEROLE", "REPLICATION"}
	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		u := protocol.DBUser{Name: r[0]}
		for i, f := range flags {
			if r[i+1] == "t" {
				u.Global = append(u.Global, f)
			}
		}
		index[u.Name] = len(res.Users)
		res.Users = append(res.Users, u)
	}
	if rows, err = a.psql(ctx, "", dbsql.PGDatabaseGrants); err != nil {
		res.Error = "rechten per database: " + dbError(err)
		return res
	}
	for _, r := range rows {
		if len(r) < 3 {
			continue
		}
		i, ok := index[r[1]]
		if !ok {
			continue
		}
		u := &res.Users[i]
		j := slices.IndexFunc(u.Databases, func(g protocol.DBGrant) bool { return g.Database == r[0] })
		if j < 0 {
			j = len(u.Databases)
			u.Databases = append(u.Databases, protocol.DBGrant{Database: r[0]})
		}
		if !slices.Contains(u.Databases[j].Privileges, r[2]) {
			u.Databases[j].Privileges = append(u.Databases[j].Privileges, r[2])
		}
	}
	return res
}

func (a *Agent) pgEnsure(ctx context.Context, req *protocol.DBEnsureRequest) protocol.DBResponse {
	if req == nil {
		return protocol.DBResponse{Error: "ensure zonder inhoud", Servers: []protocol.DBServer{}}
	}
	verifier := ""
	if req.Password != "" {
		v, err := scramVerifier(req.Password)
		if err != nil {
			return protocol.DBResponse{Error: "wachtwoord: " + err.Error(), Servers: []protocol.DBServer{}}
		}
		verifier = v
	}
	sql, err := dbsql.PGEnsure(*req, verifier)
	if err != nil {
		return protocol.DBResponse{Error: err.Error(), Servers: []protocol.DBServer{}}
	}
	if req.Restore {
		// Wat een vorig terugzetten liet staan, gaat mee weg.
		sql = dbsql.PGDropExpired + sql
	}
	return a.pgWrite(ctx, sql, verifier)
}

func (a *Agent) pgDrop(ctx context.Context, req *protocol.DBDropRequest) protocol.DBResponse {
	if req == nil {
		return protocol.DBResponse{Error: "drop zonder inhoud", Servers: []protocol.DBServer{}}
	}
	sql, err := dbsql.PGDrop(*req)
	if err != nil {
		return protocol.DBResponse{Error: err.Error(), Servers: []protocol.DBServer{}}
	}
	return a.pgWrite(ctx, sql, "")
}

// pgArgs is psql als postgres via de socket, dat stopt bij de eerste fout.
var pgArgs = []string{"-u", "postgres", "--", "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", "postgres"}

// pgWrite voert opdrachten uit via stdin. Een fout is alleen de regel met
// ERROR, zonder secret.
func (a *Agent) pgWrite(ctx context.Context, sql, secret string) protocol.DBResponse {
	res := protocol.DBResponse{Servers: []protocol.DBServer{}}
	if err := a.needPG(ctx); err != nil {
		res.Error = err.Error()
		return res
	}
	out, err := a.runStdin(ctx, []byte(sql), "runuser", pgArgs...)
	if err == nil {
		return res
	}
	res.Error = pgError(out, err, secret)
	return res
}

// pgError is de regel met ERROR of FATAL uit de uitvoer van psql, zonder
// het voorvoegsel psql:<stdin>:3:, en zonder secret.
func pgError(out []byte, err error, secret string) string {
	msg := err.Error()
	for line := range strings.SplitSeq(string(out), "\n") {
		if i := strings.Index(line, "ERROR:"); i >= 0 {
			msg = strings.TrimSpace(line[i:])
			break
		}
		if i := strings.Index(line, "FATAL:"); i >= 0 {
			msg = strings.TrimSpace(line[i:])
		}
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if secret != "" {
		msg = strings.ReplaceAll(msg, secret, "********")
	}
	return clip(msg, 500)
}
