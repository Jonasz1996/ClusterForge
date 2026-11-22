package dbsql

import (
	"strings"
	"testing"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

func TestEnsure(t *testing.T) {
	const pw = "Abcdefgh12345678xyz"
	for _, tc := range []struct {
		name string
		req  protocol.DBEnsureRequest
		want string
		err  string
	}{
		{
			name: "database met gebruiker",
			req:  protocol.DBEnsureRequest{Database: "next_cloud", User: "nextcloud", Hosts: []string{"10.0.20.186", "10.0.20.185"}, Password: pw, Grant: true},
			want: "CREATE DATABASE IF NOT EXISTS `next_cloud` CHARACTER SET utf8mb4;\n" +
				"CREATE USER IF NOT EXISTS 'nextcloud'@'10.0.20.186' IDENTIFIED BY '" + pw + "';\n" +
				"ALTER USER 'nextcloud'@'10.0.20.186' IDENTIFIED BY '" + pw + "';\n" +
				"GRANT ALL PRIVILEGES ON `next\\_cloud`.* TO 'nextcloud'@'10.0.20.186';\n" +
				"CREATE USER IF NOT EXISTS 'nextcloud'@'10.0.20.185' IDENTIFIED BY '" + pw + "';\n" +
				"ALTER USER 'nextcloud'@'10.0.20.185' IDENTIFIED BY '" + pw + "';\n" +
				"GRANT ALL PRIVILEGES ON `next\\_cloud`.* TO 'nextcloud'@'10.0.20.185';\n",
		},
		{
			name: "nieuw wachtwoord",
			req:  protocol.DBEnsureRequest{User: "nextcloud", Hosts: []string{"10.0.20.%"}, Password: pw},
			want: "CREATE USER IF NOT EXISTS 'nextcloud'@'10.0.20.%' IDENTIFIED BY '" + pw + "';\n" +
				"ALTER USER 'nextcloud'@'10.0.20.%' IDENTIFIED BY '" + pw + "';\n",
		},
		{name: "alleen de database", req: protocol.DBEnsureRequest{Database: "zabbix"}, want: "CREATE DATABASE IF NOT EXISTS `zabbix` CHARACTER SET utf8mb4;\n"},
		{name: "niets", req: protocol.DBEnsureRequest{}, err: "niets om aan te maken"},
		{name: "hoofdletters", req: protocol.DBEnsureRequest{Database: "NextCloud"}, err: "kleine letters"},
		{name: "backtick", req: protocol.DBEnsureRequest{Database: "a`b"}, err: "kleine letters"},
		{name: "systeemdatabase", req: protocol.DBEnsureRequest{Database: "mysql"}, err: "van MariaDB zelf"},
		{name: "root", req: protocol.DBEnsureRequest{User: "root", Hosts: []string{"localhost"}, Password: pw}, err: "van MariaDB zelf"},
		{name: "geen host", req: protocol.DBEnsureRequest{User: "app", Password: pw}, err: "minstens één host"},
		{name: "host met aanhalingsteken", req: protocol.DBEnsureRequest{User: "app", Hosts: []string{"x' OR '1"}, Password: pw}, err: "geen host"},
		{name: "kort wachtwoord", req: protocol.DBEnsureRequest{User: "app", Hosts: []string{"localhost"}, Password: "kort"}, err: "16 tot 128"},
		{name: "wachtwoord met aanhalingsteken", req: protocol.DBEnsureRequest{User: "app", Hosts: []string{"localhost"}, Password: "abcdefghijklmnop'"}, err: "16 tot 128"},
		{name: "rechten zonder database", req: protocol.DBEnsureRequest{User: "app", Hosts: []string{"localhost"}, Grant: true}, err: "zonder database"},
		{name: "wachtwoord zonder gebruiker", req: protocol.DBEnsureRequest{Database: "app", Password: pw}, err: "zonder gebruiker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Ensure(tc.req)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("fout %v, wil %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("kreeg\n%s\nwil\n%s", got, tc.want)
			}
		})
	}
}

func TestDrop(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  protocol.DBDropRequest
		want string
		err  string
	}{
		{name: "database", req: protocol.DBDropRequest{Database: "zabbix", Confirm: "zabbix"}, want: "DROP DATABASE IF EXISTS `zabbix`;\n"},
		{
			name: "gebruiker", req: protocol.DBDropRequest{User: "zabbix", Hosts: []string{"10.0.20.186", "localhost"}, Confirm: "zabbix"},
			want: "DROP USER IF EXISTS 'zabbix'@'10.0.20.186';\nDROP USER IF EXISTS 'zabbix'@'localhost';\n",
		},
		{name: "andere naam", req: protocol.DBDropRequest{Database: "zabbix", Confirm: "nextcloud"}, err: "niet de naam"},
		{name: "zonder bevestiging", req: protocol.DBDropRequest{User: "zabbix", Hosts: []string{"localhost"}}, err: "niet de naam"},
		{name: "systeemdatabase", req: protocol.DBDropRequest{Database: "mysql", Confirm: "mysql"}, err: "van MariaDB zelf"},
		{name: "root", req: protocol.DBDropRequest{User: "root", Hosts: []string{"localhost"}, Confirm: "root"}, err: "van MariaDB zelf"},
		{name: "allebei", req: protocol.DBDropRequest{Database: "a", User: "a", Hosts: []string{"localhost"}, Confirm: "a"}, err: "tegelijk"},
		{name: "niets", req: protocol.DBDropRequest{Confirm: "x"}, err: "niets om weg te halen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Drop(tc.req)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("fout %v, wil %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("kreeg\n%s\nwil\n%s", got, tc.want)
			}
		})
	}
}
