package diskwatch

import (
	"strings"
	"testing"
)

func TestLevel(t *testing.T) {
	st := Settings{WarnPct: 80, CritPct: 90, IgnoreMounts: []string{"/mnt/backup"}}
	cases := []struct {
		ratio float64
		mount string
		want  string
	}{
		{0.79, "/", LevelOK},
		{0.799, "/", LevelOK},
		{0.80, "/", LevelWarn},
		{0.90, "/", LevelCrit},
		{0.99, "/mnt/backup", LevelOK},
		{0.5, "", LevelUnknown},
	}
	for _, c := range cases {
		if got := st.Level(c.ratio, c.mount); got != c.want {
			t.Errorf("Level(%v, %q) = %s, wil %s", c.ratio, c.mount, got, c.want)
		}
	}
}

func TestParseScan(t *testing.T) {
	lines := []string{
		"CFDISK\tmount\t/\t10000000000\t8000000000\t2000000000",
		"CFDISK\tmount\t/boot/efi\t500000\t100\t400",
		"CFDISK\titem\tjournal\t1200000000",
		"CFDISK\titem\tapt\t300000000",
		"CFDISK\tdocker\tImages\t1.5GB (40%)",
		"CFDISK\tdocker\tBuild Cache\t500MB",
		"CFDISK\tdir\t4000000000\t/var/lib/ollama",
	}
	sc, err := parseScan(lines)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Mounts) != 1 || sc.Mounts[0].Used != 8000000000 {
		t.Fatalf("mounts %+v", sc.Mounts)
	}
	if len(sc.Items) != 3 || sc.Items[0].ID != "journal" || !sc.Items[0].Default || sc.Items[2].ID != "docker" || sc.Items[2].Default {
		t.Fatalf("items %+v", sc.Items)
	}
	if sc.Items[2].Bytes != 2000000000 {
		t.Fatalf("docker %d", sc.Items[2].Bytes)
	}
	if len(sc.Dirs) != 1 || sc.Dirs[0].Path != "/var/lib/ollama" {
		t.Fatalf("dirs %+v", sc.Dirs)
	}
	if _, err := parseScan([]string{"iets anders"}); err == nil {
		t.Fatal("geen fout zonder CFDISK-regels")
	}
}

func TestScripts(t *testing.T) {
	if s := scanScript("/; rm -rf /"); !strings.Contains(s, "du -xb -d 3 '/'") {
		t.Fatal("een vreemde mount moet / worden")
	}
	s := cleanupScript([]string{"journal", "apt"})
	if !strings.Contains(s, "journalctl --vacuum-size=200M") || !strings.Contains(s, "apt-get clean") || strings.Contains(s, "docker") {
		t.Fatal(s)
	}
	for id, e := range catalog {
		if e.def && !e.safe {
			t.Errorf("%s staat standaard aan maar is niet veilig", id)
		}
	}
}

func TestHuman(t *testing.T) {
	if got := Human(1_400_000_000); got != "1,4 GB" {
		t.Fatal(got)
	}
}
