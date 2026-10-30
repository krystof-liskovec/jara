//go:build !linux

package jag

import "fmt"

func lockLifecycle() (func(), error) {
	return nil, fmt.Errorf("agent lifecycle management requires Linux")
}
