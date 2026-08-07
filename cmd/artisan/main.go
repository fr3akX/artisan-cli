package main

import (
	"context"
	"os"

	"github.com/fr3akX/artisan-cli/internal/command"
	"golang.org/x/term"
)

func main() {
	runtime := command.Runtime{
		In:           os.Stdin,
		Out:          os.Stdout,
		Err:          os.Stderr,
		Getenv:       os.Getenv,
		IsTerminal:   term.IsTerminal,
		ReadPassword: term.ReadPassword,
	}
	os.Exit(command.Run(context.Background(), os.Args[1:], runtime))
}
