package agent

import "testing"

func TestParseDpkg(t *testing.T) {
	out := []byte("ii \topenssl\t3.5.1-1+deb13u1\tamd64\topenssl\t3.5.1-1+deb13u1\n" +
		"ii \tlibssl3t64\t3.5.1-1+deb13u1\tamd64\topenssl\t3.5.1-1+deb13u1\n" +
		"rc \toud\t1.0\tamd64\toud\t1.0\n" +
		"hi \tbash\t5.2.37-2\tamd64\t\t\n" +
		"kapotte regel\n")
	pkgs, trunc := parseDpkg(out)
	if trunc || len(pkgs) != 3 {
		t.Fatalf("pakketten = %+v, afgekapt %v", pkgs, trunc)
	}
	if pkgs[1].Source != "openssl" || pkgs[1].Name != "libssl3t64" {
		t.Errorf("bron = %+v", pkgs[1])
	}
	if pkgs[2].Source != "bash" || pkgs[2].SourceVersion != "5.2.37-2" {
		t.Errorf("terugval op het pakket zelf = %+v", pkgs[2])
	}
}
