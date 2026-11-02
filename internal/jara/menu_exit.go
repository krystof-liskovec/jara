package jara

import (
	"errors"
	"os/exec"
)

// ErrUIQuit exits every menu level. io.EOF means return to the parent menu.
var ErrUIQuit = errors.New("close interface")

func remoteMenuResult(err error) error {
	var exited *exec.ExitError
	if errors.As(err, &exited) && exited.ExitCode() == 130 {
		return ErrUIQuit
	}
	return err
}
