// Command Free-Model-Router is the AFM gateway binary.
// Phase 0: boots config + logging, prints startup info, exits cleanly.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/konor123/Free-Model-Router/internal/app"
)

func main() {
	configPath := flag.String("config", "", "path to config file (default: OS config dir)")
	flag.Parse()

	cfg, err := app.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Free-Model-Router: load config: %v\n", err)
		os.Exit(1)
	}

	log, err := app.NewLogger(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Free-Model-Router: init logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx, cfg, log); err != nil {
		log.Error("afm exited with error: %v", err)
		os.Exit(1)
	}
	log.Info("afm shut down cleanly")
}
