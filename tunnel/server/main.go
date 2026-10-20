package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // embed the IANA database so admin.timezone works in minimal images

	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := NewCommand()
	if err := cmd.Run(ctx, os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		logger := logging.New("server", logging.WithWriter(os.Stderr))
		logger.Error("server command failed", err, logging.Fields{
			"event": "server.exit_error",
		})
		os.Exit(1)
	}
}
