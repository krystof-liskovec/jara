package main

import (
	"fmt"
	"gitea.liskovec.dev/krystof.liskovec/jag-cli/internal/jag"
	"os"
)

func main() {
	if err := jag.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "jag:", err)
		os.Exit(1)
	}
}
