package recipes

import (
	"errors"
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/internal/templates"
)

func zabbixComposition() (Composition, map[string]*Recipe) {
	r, _ := Parse(zabbix)
	c := Composition{
		Name: "zabbix", Title: "Zabbix met HA",
		Roles:    []CompRole{{Name: "server", Recipe: "zabbix", RecipeVersion: "1.0", Count: 3}},
		VIP:      &CompVIP{Role: "server"},
		Database: &CompDB{Role: "server", BufferPool: "1G", MaxConnections: 300, Extra: "max_allowed_packet = 64M\n"},
	}
	return c, map[string]*Recipe{RecipeKey("zabbix", "1.0"): r}
}

func renderAll(t *testing.T, tpl *templates.Template, params map[string]any) map[int][]templates.Step {
	t.Helper()
	values, err := tpl.Validate(params)
	if err != nil {
		t.Fatal(err)
	}
	c := templates.Context{Params: values, Cluster: templates.ClusterInfo{Name: "Zabbix", Slug: "zabbix", Environment: "lab"}}
	for i := 1; i <= 3; i++ {
		c.Nodes = append(c.Nodes, templates.NodeInfo{Hostname: "zabbix-server-0" + string(rune('0'+i)), Role: "server", Index: i,
			Address: "192.0.2." + string(rune('0'+i)), Prefix: 24, Interface: "eth0"})
	}
	out := map[int][]templates.Step{}
	for i := range c.Nodes {
		c.Node = &c.Nodes[i]
		steps, err := tpl.Steps("server", c)
		if err != nil {
			t.Fatal(err)
		}
		out[i+1] = steps
	}
	c.Node = nil
	db, err := tpl.RenderDBSteps(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(db) != 3 || db[1].Password != values["db_password"] || db[1].Password == "" {
		t.Errorf("db-stappen: %+v", db)
	}
	return out
}

func TestComposeZabbix(t *testing.T) {
	c, recs := zabbixComposition()
	out, err := Compose(c, "1.0.0", recs)
	if err != nil {
		t.Fatal(err)
	}
	tpl := out.Template
	if tpl.Name != "eigen-zabbix" || tpl.Database == nil || tpl.Database.Role != "server" || tpl.Database.Settings["buffer_pool"] != "1G" {
		t.Fatalf("template: %+v", tpl)
	}
	if !tpl.UsesRecipeSteps() {
		t.Error("repo_deb vraagt protocol 24")
	}
	steps := renderAll(t, tpl, map[string]any{"vip": "192.0.2.100", "vrid": 51, "zabbix_version": "7.0"})
	var kinds []string
	files := map[string]string{}
	for _, s := range steps[1] {
		kinds = append(kinds, s.Kind())
		if s.File != nil {
			files[s.File.Path] = s.File.Content
		}
		if s.RepoDeb != nil && !strings.Contains(s.RepoDeb.URL, "/zabbix/7.0/") {
			t.Errorf("versie: %s", s.RepoDeb.URL)
		}
		if s.IniSet != nil && s.IniSet.Key == "DBHost" && s.IniSet.Value != "192.0.2.100" {
			t.Errorf("DBHost: %s", s.IniSet.Value)
		}
		if s.IniSet != nil && s.IniSet.Key == "HANodeName" && s.IniSet.Value != "zabbix-server-01" {
			t.Errorf("HANodeName: %s", s.IniSet.Value)
		}
	}
	want := "file repo_deb package package ini_set ini_set ini_set ini_set file service service service package file file service"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("stappen:\n got %s\nwant %s", got, want)
	}
	if cnf := files[AppDBFile]; !strings.Contains(cnf, "max_allowed_packet = 64M") {
		t.Errorf("cnf: %q", cnf)
	}
	php := files["/etc/zabbix/web/zabbix.conf.php"]
	if !strings.Contains(php, "'192.0.2.100'") || !strings.Contains(php, "'3306'") || strings.Contains(php, "{{") {
		t.Errorf("zabbix.conf.php: %s", php)
	}
	check := files["/etc/keepalived/check-cluster.sh"]
	for _, part := range []string{"is-active --quiet zabbix-server", "'http://localhost/zabbix/')\" = 200", "cf-agent check db mariadb"} {
		if !strings.Contains(check, part) {
			t.Errorf("controle mist %q:\n%s", part, check)
		}
	}
	if k := files["/etc/keepalived/keepalived.conf"]; !strings.Contains(k, "192.0.2.2\n        192.0.2.3") {
		t.Errorf("keepalived:\n%s", k)
	}
	if out2, _ := Compose(c, "1.0.0", recs); string(out2.YAML) != string(out.YAML) {
		t.Error("dezelfde samenstelling geeft een andere template")
	}
	if _, err := ParseComposed(string(out.YAML)); err != nil {
		t.Error(err)
	}
}

func TestComposeOnceAndNodes(t *testing.T) {
	src := "RECIPE app 1.0\nFROM debian:13\nAPT nginx\nONCE RUN CREATES /x touch /x\nFILE /etc/peers <<EOF\n{{ role.nodes }}|{{ cluster.nodes }}\nEOF\n"
	r, d := Parse(src)
	if r == nil {
		t.Fatal(d)
	}
	c := Composition{Name: "app", Title: "App", Roles: []CompRole{{Name: "server", Recipe: "app", RecipeVersion: "1.0", Count: 3}}}
	out, err := Compose(c, "1.0.0", map[string]*Recipe{RecipeKey("app", "1.0"): r})
	if err != nil {
		t.Fatal(err)
	}
	ctx := templates.Context{Params: map[string]any{}, Cluster: templates.ClusterInfo{Slug: "app"}}
	for i := 1; i <= 3; i++ {
		ctx.Nodes = append(ctx.Nodes, templates.NodeInfo{Hostname: "n" + string(rune('0'+i)), Role: "server", Index: i, Address: "10.0.0." + string(rune('0'+i))})
	}
	for i := range ctx.Nodes {
		ctx.Node = &ctx.Nodes[i]
		steps, err := out.Template.Steps("server", ctx)
		if err != nil {
			t.Fatal(err)
		}
		if want := map[bool]int{true: 3, false: 2}[i == 0]; len(steps) != want {
			t.Errorf("node %d: %d stappen", i+1, len(steps))
		}
		if f := steps[len(steps)-1].File; f == nil || f.Content != "10.0.0.1 10.0.0.2 10.0.0.3|10.0.0.1 10.0.0.2 10.0.0.3\n" {
			t.Errorf("peers: %+v", f)
		}
	}
}

func TestComposeErrors(t *testing.T) {
	c, recs := zabbixComposition()
	for name, change := range map[string]func(*Composition){
		"database": func(c *Composition) { c.Database = nil },
		"name":     func(c *Composition) { c.Name = "Zabbix!" },
		"roles.0.recipe": func(c *Composition) {
			c.Roles[0].RecipeVersion = "9.9"
		},
		"database.extra":       func(c *Composition) { c.Database.Extra = "wsrep_on = OFF" },
		"database.buffer_pool": func(c *Composition) { c.Database.BufferPool = "1K" },
	} {
		cc := c
		cc.Roles = append([]CompRole{}, c.Roles...)
		d := *c.Database
		cc.Database = &d
		change(&cc)
		_, err := Compose(cc, "1.0.0", recs)
		var ce *ComposeError
		if !errors.As(err, &ce) || ce.Field != name {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Zonder VIP verbindt de app met de database op de eigen node.
	cc0 := c
	cc0.VIP = nil
	out, err := Compose(cc0, "1.0.0", recs)
	if err != nil || !strings.Contains(string(out.YAML), "value: localhost") {
		t.Errorf("zonder VIP: %v", err)
	}
	// De app op andere nodes dan de database zonder VIP weet niet waar de
	// database is.
	app, _ := Parse("RECIPE app 1.0\nFROM debian:13\nNEEDS database mariadb\nSET /etc/app.ini host={{ db.host }}\n")
	cc := Composition{Name: "x", Title: "X", Roles: []CompRole{
		{Name: "app", Recipe: "app", RecipeVersion: "1.0", Count: 1},
		{Name: "db", Recipe: "leeg", RecipeVersion: "1.0", Count: 3},
	}, Database: &CompDB{Role: "db"}}
	leeg, _ := Parse("RECIPE leeg 1.0\nFROM debian:13\nAPT htop\n")
	_, err = Compose(cc, "1.0.0", map[string]*Recipe{RecipeKey("app", "1.0"): app, RecipeKey("leeg", "1.0"): leeg})
	var ce *ComposeError
	if !errors.As(err, &ce) || ce.Field != "vip.role" {
		t.Errorf("db op andere rol: %v", err)
	}
	// Met het VIP op de databaserol verbindt de app via het VIP.
	cc.VIP = &CompVIP{Role: "db"}
	out, err = Compose(cc, "1.0.0", map[string]*Recipe{RecipeKey("app", "1.0"): app, RecipeKey("leeg", "1.0"): leeg})
	if err != nil || !strings.Contains(string(out.YAML), "value: '{{ .params.vip }}'") {
		t.Errorf("VIP op db: %v", err)
	}
	// Het schema laden kan alleen op een node met de database.
	z, _ := Parse(zabbix)
	cc.Roles[0] = CompRole{Name: "app", Recipe: "zabbix", RecipeVersion: "1.0", Count: 1}
	_, err = Compose(cc, "1.0.0", map[string]*Recipe{RecipeKey("zabbix", "1.0"): z, RecipeKey("leeg", "1.0"): leeg})
	if !errors.As(err, &ce) || ce.Field != "roles.0.recipe" || !strings.Contains(ce.Msg, "IMPORT") {
		t.Errorf("import op andere rol: %v", err)
	}
}

func TestMysqldConfig(t *testing.T) {
	cnf, warns, err := mysqldConfig([]DBSetting{{Key: "max_allowed_packet", Value: "16M", Min: true}, {Key: "max_allowed_packet", Value: "64M", Min: true}}, "innodb_log_file_size=512M\nmax_allowed_packet = 32M")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cnf, "max_allowed_packet = 32M\ninnodb_log_file_size = 512M\n") || len(warns) != 1 {
		t.Errorf("cnf %q, warns %v", cnf, warns)
	}
	if _, _, err := mysqldConfig([]DBSetting{{Key: "a", Value: "1"}, {Key: "a", Value: "2"}}, ""); err == nil {
		t.Error("twee waarden zonder MIN")
	}
}
