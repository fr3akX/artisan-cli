package main

import (
	"context"
	"os"

	"github.com/fr3akX/artisan-cli/internal/command"
)

func main() {
	runtime := command.Runtime{
		In:     os.Stdin,
		Out:    os.Stdout,
		Err:    os.Stderr,
		Getenv: os.Getenv,
	}
	os.Exit(command.Run(context.Background(), os.Args[1:], runtime))
}
