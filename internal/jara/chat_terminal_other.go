//go:build !linux && !darwin

package jara

import (
	"fmt"
	"io"
	"os"
)

func chatInteractiveFiles(io.Reader, io.Writer) (*os.File, *os.File, bool) {
	return nil, nil, false
}

func chatTerminalLoop(*chatUI, *os.File, *os.File) error {
	return fmt.Errorf("interactive chat terminal is unavailable on this platform")
}
