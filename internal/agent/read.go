package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"

	"github.com/Jonasz1996/clusterforge/pkg/protocol"
)

// De leesfuncties hieronder gebruiken apply en state.inspect allebei, zodat
// een controle precies ziet wat apply zou zien. Ze veranderen niets.

// errNoDpkg betekent dat deze node geen dpkg heeft.
var errNoDpkg = errors.New("deze node heeft geen dpkg")

// packageStates vraagt met één dpkg-query hoe de pakketten erbij staan. Een
// onbekend pakket staat er als niet geïnstalleerd.
func (a *Agent) packageStates(ctx context.Context, names []string) (map[string]protocol.PackageState, error) {
	for _, n := range names {
		if !packageName.MatchString(n) {
			return nil, fmt.Errorf("ongeldige pakketnaam %q", n)
		}
	}
	args := append([]string{"-W", `-f=${Package}\t${Status}\t${Version}\n`}, names...)
	out, err := a.exec(ctx, "dpkg-query", args...)
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errNoDpkg
	}
	// dpkg-query faalt ook als één pakket onbekend is; de bekende staan dan
	// toch in de uitvoer.
	states := make(map[string]protocol.PackageState, len(names))
	for _, n := range names {
		states[n] = protocol.PackageState{Name: n}
	}
	for line := range strings.Lines(string(out)) {
		f := strings.Split(strings.TrimRight(line, "\n"), "\t")
		if len(f) != 3 {
			continue
		}
		st, ok := states[f[0]]
		if !ok || st.Installed {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(f[1]), "install ok installed") {
			st.Installed, st.Version = true, f[2]
			states[f[0]] = st
		}
	}
	return states, nil
}

// unit leest hoe een systemd-unit erbij staat.
func (a *Agent) unit(ctx context.Context, name string) (loaded, enabled, active bool, err error) {
	out, err := a.exec(ctx, "systemctl", "show", name, "--property=LoadState,UnitFileState,ActiveState")
	if err != nil {
		return false, false, false, fmt.Errorf("systemctl show %s: %w", name, err)
	}
	props := map[string]string{}
	for line := range strings.Lines(string(out)) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	loaded = props["LoadState"] == "loaded"
	enabled = props["UnitFileState"] == "enabled" || props["UnitFileState"] == "enabled-runtime"
	active = props["ActiveState"] == "active" || props["ActiveState"] == "activating" || props["ActiveState"] == "reloading"
	return loaded, enabled, active, nil
}

// userExists vraagt met id of een gebruiker bestaat.
func (a *Agent) userExists(ctx context.Context, name string) (bool, error) {
	if !userName.MatchString(name) {
		return false, fmt.Errorf("ongeldige gebruikersnaam %q", name)
	}
	_, err := a.exec(ctx, "id", "-u", name)
	return err == nil, nil
}

// pathState beschrijft een bestand of map. Net als apply volgt hij een
// symlink. Met hash rekent hij de sha256 van een gewoon bestand tot
// protocol.MaxInspectHash bytes.
func (a *Agent) pathState(p string, hash bool) (protocol.PathState, error) {
	path, err := a.path(p)
	if err != nil {
		return protocol.PathState{}, err
	}
	var st protocol.PathState
	if li, err := os.Lstat(path); err == nil && li.Mode()&fs.ModeSymlink != 0 {
		st.Symlink, _ = os.Readlink(path)
	}
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Exists = true
	st.Mode = fmt.Sprintf("%04o", fi.Mode().Perm())
	st.ModTime = fi.ModTime().UTC()
	if uid, gid, ok := fileOwner(fi); ok {
		st.Owner, st.Group = userNameOf(uid), groupNameOf(gid)
	}
	switch {
	case fi.Mode().IsRegular():
		st.Type, st.Size = "file", fi.Size()
	case fi.IsDir():
		st.Type = "directory"
	default:
		st.Type = "other"
	}
	if hash && st.Type == "file" && st.Size <= protocol.MaxInspectHash {
		if st.SHA256, err = hashFile(path); err != nil {
			return st, err
		}
	}
	return st, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	// Groeit het bestand intussen, dan niet verder dan de grens.
	n, err := io.Copy(h, io.LimitReader(f, protocol.MaxInspectHash+1))
	if err != nil {
		return "", err
	}
	if n > protocol.MaxInspectHash {
		return "", nil
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func userNameOf(uid int) string {
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		return u.Username
	}
	return strconv.Itoa(uid)
}

func groupNameOf(gid int) string {
	if g, err := user.LookupGroupId(strconv.Itoa(gid)); err == nil {
		return g.Name
	}
	return strconv.Itoa(gid)
}
