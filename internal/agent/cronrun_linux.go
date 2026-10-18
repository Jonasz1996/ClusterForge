package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// cronLock neemt het slot van een job, zonder te wachten.
func cronLock(p string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

// runCronCommand draait command met /bin/sh als name, met stdout en stderr
// naar out. Afloop van ctx stopt de hele procesgroep.
func runCronCommand(ctx context.Context, name, command string, out io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	env := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	u, err := user.Lookup(name)
	if err != nil {
		return -1, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if uid != os.Getuid() {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	env = append(env, "HOME="+u.HomeDir, "USER="+u.Username, "LOGNAME="+u.Username, "SHELL=/bin/sh")
	cmd.Env = env
	cmd.Dir = "/"
	if fi, err := os.Stat(u.HomeDir); err == nil && fi.IsDir() {
		cmd.Dir = u.HomeDir
	}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exitErr):
		if code := exitErr.ExitCode(); code >= 0 {
			return code, nil
		}
		return -1, nil
	}
	return -1, err
}
