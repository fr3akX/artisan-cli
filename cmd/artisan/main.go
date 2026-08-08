package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/fr3akX/artisan-cli/internal/command"
	"golang.org/x/term"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), handledSignals()...)
	defer stop()
	runtime := command.Runtime{
		In:           os.Stdin,
		Out:          os.Stdout,
		Err:          os.Stderr,
		Getenv:       os.Getenv,
		IsTerminal:   term.IsTerminal,
		ReadPassword: term.ReadPassword,
	}
	return command.Run(ctx, os.Args[1:], runtime)
}
