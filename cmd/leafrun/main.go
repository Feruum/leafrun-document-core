package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/leafrun/leafrun-document-core/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.RunWithSources(ctx, os.Args[1:], os.Stdout, os.Stderr, providerSource)
	stop()
	os.Exit(code)
}
