//go:build !linux

package agent

import (
	"errors"
	"os"
	"os/exec"
)

func startPTY(*exec.Cmd, int, int) (*os.File, error) {
	return nil, errors.New("een terminal kan alleen op Linux")
}

func resizePTY(*os.File, int, int) error { return nil }
