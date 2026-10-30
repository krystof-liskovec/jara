//go:build !linux

package jag

import (
	"fmt"
	"os"
)

func lockUpdateFile(f *os.File) (func(), error) { return nil, fmt.Errorf("host updates require Linux") }
