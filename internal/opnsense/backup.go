package opnsense

// Back-ups van de OPNsense-configuratie in ClusterForge (fase 34 M5): elke
// dag en na elke wijziging de volledige config.xml van elke firewall,
// versleuteld met de masterkey. Een ongewijzigde configuratie geeft geen
// nieuwe rij; de nieuwste KeepBackups per firewall blijven.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/jobs"
	"github.com/Jonasz1996/clusterforge/internal/store"
	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

const (
	KindBackup = "opnsense.backup"
	// KeepBackups is het aantal back-ups per firewall.
	KeepBackups = 30
	// BackupEvery is hoe oud de laatste back-up mag zijn.
	BackupEvery = 24 * time.Hour
)

// Backup is één back-up, zonder inhoud.
type Backup struct {
	ID      uuid.UUID `json:"id"`
	NodeID  uuid.UUID `json:"node_id"`
	TakenAt time.Time `json:"taken_at"`
	Reason  string    `json:"reason"`
	SHA256  string    `json:"sha256"`
	Size    int32     `json:"size"`
	Version string    `json:"version,omitempty"`
}

func backupAAD(node uuid.UUID, nonce string) []byte {
	return []byte("opnsense-backup:" + node.String() + ":" + nonce)
}

// backupNode haalt de configuratie van één firewall en bewaart ze; het
// antwoord zegt of ze nieuw was.
func (s *Service) backupNode(ctx context.Context, id uuid.UUID, reason string) (string, error) {
	if s.Box == nil {
		return "", errors.New("deze server heeft geen masterkey; zonder bewaart ClusterForge geen back-ups met wachtwoorden")
	}
	data, err := s.request(ctx, id, protocol.PVERequest{Op: protocol.OPNOpBackup}, false)
	if err != nil {
		return "", err
	}
	var xml string
	if err := json.Unmarshal(data, &xml); err != nil {
		return "", err
	}
	if !strings.Contains(xml, "<opnsense") {
		return "", errors.New("de agent gaf geen config.xml")
	}
	sum := sha256.Sum256([]byte(xml))
	hash := hex.EncodeToString(sum[:])
	last, err := s.q.LatestOpnsenseBackup(ctx, id)
	switch {
	case err == nil && last.Sha256 == hash:
		if err := s.q.TouchOpnsenseBackup(ctx, store.TouchOpnsenseBackupParams{ID: last.ID, Reason: reason}); err != nil {
			return "", err
		}
		return "ongewijzigd", nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	version := ""
	if f, err := s.Firewall(ctx, id); err == nil && f.Facts != nil {
		version = f.Facts.Version
	}
	nonce := uuid.NewString()
	if _, err := s.q.InsertOpnsenseBackup(ctx, store.InsertOpnsenseBackupParams{
		NodeID: id, Reason: reason, Sha256: hash, Size: int32(len(xml)), Version: version,
		KeyID: s.Box.KeyID, Nonce: nonce, Data: s.Box.Seal([]byte(xml), backupAAD(id, nonce)),
	}); err != nil {
		return "", err
	}
	if err := s.q.PruneOpnsenseBackups(ctx, store.PruneOpnsenseBackupsParams{NodeID: id, Limit: KeepBackups}); err != nil {
		s.log.Warn("oude OPNsense-back-ups opruimen mislukt", "err", err)
	}
	return "nieuw", nil
}

// backupStep bewaart een back-up van elke firewall; een mislukking laat de
// taak niet mislukken.
func (s *Service) backupStep(ctx context.Context, j *jobs.Job, ids []uuid.UUID, reason string) {
	_ = j.Step(ctx, "Back-up in ClusterForge", func(ctx context.Context, st *jobs.Step) error {
		for _, id := range ids {
			name := id.String()
			if f, err := s.Firewall(ctx, id); err == nil {
				name = f.Hostname
			}
			res, err := s.backupNode(ctx, id, reason)
			if err != nil {
				st.Logf("%s: back-up mislukt (%v)", name, err)
				continue
			}
			st.Logf("%s: %s", name, res)
		}
		return nil
	})
}

type backupParams struct {
	Nodes []uuid.UUID `json:"nodes"`
}

// StartBackup maakt nu een back-up van elke firewall van het cluster.
func (s *Service) StartBackup(ctx context.Context, actor events.Actor, id uuid.UUID) (store.Job, error) {
	if s.Box == nil {
		return store.Job{}, ValidationError{"deze server heeft geen masterkey; zonder bewaart ClusterForge geen back-ups met wachtwoorden"}
	}
	c, err := s.ClusterOf(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	var p backupParams
	for _, n := range c.Nodes {
		if n.Why != "" {
			return store.Job{}, ValidationError{n.Why}
		}
		p.Nodes = append(p.Nodes, n.NodeID)
	}
	return s.enqueue(ctx, actor, c, KindBackup, "Back-up van "+c.Name, id, p)
}

func (s *Service) runBackup(ctx context.Context, j *jobs.Job) error {
	var p backupParams
	if err := j.Decode(&p); err != nil {
		return err
	}
	return j.Step(ctx, "Back-up in ClusterForge", func(ctx context.Context, st *jobs.Step) error {
		failed := 0
		for _, id := range p.Nodes {
			res, err := s.backupNode(ctx, id, "met de hand")
			if err != nil {
				failed++
				st.Logf("%s: mislukt (%v)", id, err)
				continue
			}
			if f, err := s.Firewall(ctx, id); err == nil {
				st.Logf("%s: %s", f.Hostname, res)
			}
		}
		if failed > 0 {
			return fmt.Errorf("%d van %d back-ups mislukten", failed, len(p.Nodes))
		}
		return nil
	})
}

// Backups geeft de back-ups van een firewall, nieuwste eerst.
func (s *Service) Backups(ctx context.Context, id uuid.UUID) ([]Backup, error) {
	if _, err := s.Firewall(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.q.ListOpnsenseBackups(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]Backup, 0, len(rows))
	for _, r := range rows {
		out = append(out, Backup{ID: r.ID, NodeID: r.NodeID, TakenAt: r.TakenAt, Reason: r.Reason, SHA256: r.Sha256, Size: r.Size, Version: r.Version})
	}
	return out, nil
}

// OpenBackup ontsleutelt een back-up om te downloaden; de bestandsnaam is
// zoals OPNsense ze zelf noemt.
func (s *Service) OpenBackup(ctx context.Context, actor events.Actor, id uuid.UUID) (string, []byte, error) {
	b, err := s.q.GetOpnsenseBackup(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	if s.Box == nil {
		return "", nil, ValidationError{"deze server heeft geen masterkey meer; de back-up is niet te lezen"}
	}
	data, err := s.Box.Open(b.Data, backupAAD(b.NodeID, b.Nonce), b.KeyID)
	if err != nil {
		return "", nil, fmt.Errorf("de back-up is niet te lezen: %w", err)
	}
	host := b.NodeID.String()
	if f, err := s.Firewall(ctx, b.NodeID); err == nil {
		host = f.Hostname
	}
	s.event(ctx, actor, b.NodeID, "opnsense.backup_downloaded", map[string]any{"backup": id.String(), "taken_at": b.TakenAt, "firewall": host})
	name := fmt.Sprintf("config-%s-%s.xml", host, b.TakenAt.UTC().Format("20060102150405"))
	return name, data, nil
}

// Loop maakt elk uur de back-ups die ouder zijn dan BackupEvery.
func (s *Service) Loop(ctx context.Context) {
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.dailyBackups(ctx)
		t.Reset(time.Hour)
	}
}

func (s *Service) dailyBackups(ctx context.Context) {
	if s.Box == nil {
		return
	}
	fws, err := s.Firewalls(ctx)
	if err != nil {
		s.log.Warn("OPNsense-back-ups: firewalls lezen mislukt", "err", err)
		return
	}
	for _, f := range fws {
		if f.Why != "" || f.Facts == nil {
			continue
		}
		last, err := s.q.LatestOpnsenseBackup(ctx, f.NodeID)
		if err == nil && s.Now().Sub(last.TakenAt) < BackupEvery {
			continue
		}
		if _, err := s.backupNode(ctx, f.NodeID, "dagelijks"); err != nil {
			s.log.Warn("OPNsense-back-up mislukt", "firewall", f.Hostname, "err", err)
		}
	}
}
