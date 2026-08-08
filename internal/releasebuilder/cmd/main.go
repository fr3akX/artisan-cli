package main

import (
	"fmt"
	"os"

	"github.com/fr3akX/artisan-cli/internal/releasebuilder"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "Usage: releasebuilder VERSION COMMIT DESTINATION_LEAF")
		os.Exit(2)
	}
	root, err := os.Getwd()
	if err == nil {
		err = releasebuilder.Build(releasebuilder.Options{
			Root: root, Version: os.Args[1], Commit: os.Args[2], Destination: os.Args[3], Go: os.Getenv("GO"),
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
