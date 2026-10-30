package main

import (
	"fmt"
	"os"

	"krystof.liskovec/jara/internal/jara"
)

func main() {
	if err := jara.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "jara:", err)
		os.Exit(1)
	}
}
