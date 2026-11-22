package publish

import (
	"strings"
	"testing"
)

func TestThemeRender(t *testing.T) {
	th := Theme{CSSDefault: "https://css.example.test/{naam}.css", Hosts: map[int]string{7: NoCSS, 8: "https://x.test/eigen.css"}}
	if got := th.cssFor(1, "proxmox50.example.test"); got != "https://css.example.test/proxmox.css" {
		t.Fatalf("css = %q", got)
	}
	if th.cssFor(7, "a.example.test") != "" || th.cssFor(8, "a.example.test") != "https://x.test/eigen.css" {
		t.Fatal("css per host")
	}
	auth := th.render(true, "nc1.example.test", "https://x.test/nc.css", "http://10.0.0.5:9000")
	if !strings.Contains(auth, "proxy_pass http://10.0.0.5:9000/outpost.goauthentik.io;") || !strings.Contains(auth, `href="https://x.test/nc.css"`) ||
		!strings.HasPrefix(auth, themeBegin) || !hasSnippet(auth) {
		t.Fatalf("auth = %s", auth)
	}
	plain := th.render(false, "nc1.example.test", "", "")
	if strings.Contains(plain, "sub_filter '") || hasSnippet(plain) || !locationRoot.MatchString(plain) {
		t.Fatalf("plain = %s", plain)
	}

	// Eigen regels blijven, een eigen 'location /' maakt plaats.
	cur := "client_max_body_size 0;\n\n" + AuthentikSnippet("http://10.0.0.5:9000")
	adv, replaced, ok := th.themedAdvanced(cur, 1, "app.example.test", true, "http://10.0.0.5:9000")
	if !ok || replaced != "" || !strings.HasPrefix(adv, "client_max_body_size 0;") || strings.Contains(adv, snippetBegin) || themeBlock(adv) == "" {
		t.Fatalf("adv = %s", adv)
	}
	if ownAdvanced(adv) != "client_max_body_size 0;" {
		t.Fatalf("own = %q", ownAdvanced(adv))
	}
	manual := "location / {\n  proxy_pass $forward_scheme://$server:$port;\n}"
	if _, replaced, _ := th.themedAdvanced(manual, 1, "app.example.test", false, ""); replaced == "" {
		t.Fatal("eigen location / niet vervangen")
	}
	// Eigen Advanced bewerken houdt het themablok.
	kept, err := keepSnippet(adv, "gzip on;")
	if err != nil || themeBlock(kept) == "" || !strings.HasPrefix(kept, "gzip on;") {
		t.Fatalf("kept = %q %v", kept, err)
	}
	if _, err := keepSnippet(adv, manual); err == nil {
		t.Fatal("location / naast het sjabloon")
	}
	if checkTemplate("location / { auth_request /x; }", false) == nil || checkTemplate("location / {", true) == nil {
		t.Fatal("sjablooncontrole")
	}
	if checkCSS("javascript:alert(1)") == nil || checkCSS("https://css.example.test/{naam}.css") != nil {
		t.Fatal("csscontrole")
	}
}

func TestAdvancedFixes(t *testing.T) {
	services := []ServiceRow{{Key: "a", Proto: "tcp", Web: true, Target: "192.168.10.50", Port: 8006, Kind: "https", Name: "proxmox", Label: "proxmox"}}
	idx := map[string]int{"192.168.103.244:9000": 0}
	adv := "location / {\n proxy_pass https://192.168.0.50:8006;\n}\nlocation /outpost.goauthentik.io {\n proxy_pass http://192.168.0.9:9000/outpost.goauthentik.io;\n}\nlocation /x { proxy_pass http://192.168.103.244:9000/x; }\nlocation /y { proxy_pass $forward_scheme://$server:$port; }"
	fixes := advancedFixes(adv, "proxmox.example.test", idx, services, "http://192.168.103.244:9000")
	if len(fixes) != 2 || fixes[0].New != "https://192.168.10.50:8006" || fixes[1].New != "http://192.168.103.244:9000" {
		t.Fatalf("fixes = %+v", fixes)
	}
	out := applyAdvFixes(adv, fixes)
	if strings.Contains(out, "192.168.0.") || !strings.Contains(out, "proxy_pass http://192.168.103.244:9000/outpost.goauthentik.io;") || !strings.Contains(out, "proxy_pass https://192.168.10.50:8006;") {
		t.Fatalf("out = %s", out)
	}
	// Een gok (alleen dezelfde poort) geeft geen vervanging.
	if f := advancedFixes("proxy_pass http://10.9.9.9:8006;", "iets.example.test", idx, services, ""); len(f) != 0 {
		t.Fatalf("gok = %+v", f)
	}
}
