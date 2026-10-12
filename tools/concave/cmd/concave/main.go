package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"concave/internal/command"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(command.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
