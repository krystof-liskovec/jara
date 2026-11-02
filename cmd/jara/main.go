package main

import (
	"errors"
	"fmt"
	"os"

	"krystof.liskovec/jara/internal/jara"
)

func main() {
	if err := jara.Run(os.Args[1:]); err != nil {
		if errors.Is(err, jara.ErrUIQuit) {
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "jara:", err)
		os.Exit(1)
	}
}
