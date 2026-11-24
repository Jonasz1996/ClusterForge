package commands

import "testing"

func TestReadOnly(t *testing.T) {
	for script, want := range map[string]bool{
		"pvesm status":                      true,
		"df -h; free -m":                    true,
		"systemctl status nginx | head -20": true,
		"timeout 10 pvesm status":           true,
		"# kijk\nuptime\n":                  true,
		"systemctl restart nginx":           false,
		"apt-get upgrade -y":                false,
		"cat /etc/hosts > /tmp/x":           false,
		"echo $(rm -rf /)":                  false,
		"curl -s http://x":                  true,
		"curl -X POST http://x":             false,
		"pct list && pct stop 101":          false,
		"ip a":                              true,
		"ip link set eth0 down":             false,
		"":                                  false,
	} {
		if got := ReadOnly(script); got != want {
			t.Errorf("ReadOnly(%q) = %v, wil %v", script, got, want)
		}
	}
}
