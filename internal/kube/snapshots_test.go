package kube

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Jonasz1996/clusterforge/internal/kube/kubefake"
	"github.com/Jonasz1996/clusterforge/internal/store"
)

func fromFake(t *testing.T, sp kubefake.SnapshotSpec) Snapshot {
	t.Helper()
	b, err := json.Marshal(kubefake.SnapshotFile(sp))
	if err != nil {
		t.Fatal(err)
	}
	var f etcdSnapshotFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return snapshotFromFile(uuid.Nil, f)
}

func TestSnapshotFromFile(t *testing.T) {
	at := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	sn := fromFake(t, kubefake.SnapshotSpec{Node: "leren-server-01", File: "etcd-snapshot-leren-server-01-1791763200", Taken: at, Size: 6283296})
	if sn.Name != "local-etcd-snapshot-leren-server-01-1791763200" || sn.File != "etcd-snapshot-leren-server-01-1791763200" || sn.Node != "leren-server-01" ||
		!sn.Ready || sn.S3 || sn.SizeBytes == nil || *sn.SizeBytes != 6283296 || sn.TakenAt == nil || !sn.TakenAt.Equal(at) {
		t.Fatalf("lokaal: %+v", sn)
	}
	if note := RestoreNote(store.EnvironmentLab, "leren-server-01", sn); note != "" {
		t.Fatalf("kan terug: %q", note)
	}
	for env, init := range map[store.Environment]string{store.EnvironmentProd: "leren-server-01", store.EnvironmentTest: "leren-server-02", store.EnvironmentLab: ""} {
		if RestoreNote(env, init, sn) == "" {
			t.Errorf("%s met %q kan niet terug", env, init)
		}
	}
	s3 := fromFake(t, kubefake.SnapshotSpec{Node: "leren-server-01", File: "on-demand-leren-server-01-1", Taken: at, S3: true})
	if !s3.S3 || !strings.Contains(RestoreNote(store.EnvironmentLab, "leren-server-01", s3), "S3") {
		t.Fatalf("s3: %+v", s3)
	}
	bad := fromFake(t, kubefake.SnapshotSpec{Node: "leren-server-01", File: "etcd-snapshot-x", Taken: at, Error: "no space left on device"})
	if bad.Ready || bad.Error != "no space left on device" {
		t.Fatalf("fout: %+v", bad)
	}
	// Een bestand buiten de map met snapshots kan niet terug.
	sn.Location = "file:///tmp/" + sn.File
	if RestoreNote(store.EnvironmentLab, "leren-server-01", sn) == "" {
		t.Fatal("buiten de map")
	}

	for file, want := range map[string]string{
		"etcd-snapshot-leren-server-01-1": "scheduled", "clusterforge-leren-server-01-1": "clusterforge",
		"clusterforge-voor-terugzetten-leren-server-01-1": "clusterforge", "on-demand-leren-server-01-1": "on_demand", "backup-1": "on_demand",
	} {
		if got := SnapshotKind(file); got != want {
			t.Errorf("%s: %s", file, got)
		}
	}
}

func TestSnapshotFreshness(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	at := func(h int) *time.Time { v := now.Add(-time.Duration(h) * time.Hour); return &v }
	items := []Snapshot{
		{Node: "b", File: "1", Ready: true, TakenAt: at(30)},
		{Node: "a", File: "2", Ready: true, TakenAt: at(13)},
		{Node: "a", File: "3", Ready: false, TakenAt: at(1), Error: "kapot"},
		{Node: "a", File: "4", Ready: true, TakenAt: at(25)},
	}
	if f := Fresh(items, "", now); f != "ok" {
		t.Fatalf("vers: %s", f)
	}
	if f := Fresh(items[:1], "", now); f != "stale" {
		t.Fatalf("oud: %s", f)
	}
	if f := Fresh(items[2:3], "", now); f != "none" {
		t.Fatalf("alleen kapot: %s", f)
	}
	if f := Fresh(nil, "k3s meldt geen snapshots", now); f != "unknown" {
		t.Fatalf("niet te lezen: %s", f)
	}
	per := PerServer(items)
	if len(per) != 2 || per[0].Node != "a" || per[0].Count != 3 || !per[0].Newest.Equal(*at(13)) || per[1].Count != 1 {
		t.Fatalf("per server: %+v", per)
	}
}
