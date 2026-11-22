package protocol

import "testing"

func TestMaskSecrets(t *testing.T) {
	cases := map[string]string{
		"mysqldump -uroot -pGeheim123 nextcloud > /tmp/x":    "mysqldump -uroot -p*** nextcloud > /tmp/x",
		"mariadb-dump -p'Ge heim' db":                        "mariadb-dump -p'***' db",
		"rsync -pav /a/ nc2:/a/":                             "rsync -pav /a/ nc2:/a/",
		"PGPASSWORD=s3cr3t pg_dump -h db app":                "PGPASSWORD=*** pg_dump -h db app",
		"curl -s https://jonas:hunter2@example.org/hook":     "curl -s https://jonas:***@example.org/hook",
		"curl -H 'Authorization: Bearer abc.def' https://x":  "curl -H 'Authorization: Bearer ***' https://x",
		"/usr/local/bin/backup --password=abc --target /mnt": "/usr/local/bin/backup --password=*** --target /mnt",
		"restic --password-file /root/.restic backup /srv":   "restic --password-file /root/.restic backup /srv",
		"export API_KEY=xyz; /opt/sync.sh":                   "export API_KEY=***; /opt/sync.sh",
		"cd / && run-parts --report /etc/cron.hourly":        "cd / && run-parts --report /etc/cron.hourly",
	}
	for in, want := range cases {
		if got := MaskSecrets(in); got != want {
			t.Errorf("MaskSecrets(%q) = %q, wil %q", in, got, want)
		}
	}
}
