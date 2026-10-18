//go:build !linux

package agent

import (
	"context"
	"errors"
	"io"
)

var errCronLinux = errors.New("beheerde cronjobs draaien alleen op Linux")

func cronLock(string) (func(), error) { return nil, errCronLinux }

func runCronCommand(context.Context, string, string, io.Writer) (int, error) { return -1, errCronLinux }

func runCronArgv(context.Context, string, []string, io.Writer) (int, error) { return -1, errCronLinux }
