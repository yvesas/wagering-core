// Command api serves the HTTP API.
//
// It builds the dependency graph and hands the lifecycle to fx: start validates
// configuration and dependencies, stop drains in-flight requests and then
// closes the pool, in that order.
package main

import (
	"log/slog"
	"os"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/yvesas/wagering-core/internal/platform"
)

// shutdownGrace bounds the whole stop sequence: the server's drain plus
// closing the pool. APP_SHUTDOWN_TIMEOUT governs the drain itself.
const shutdownGrace = 60 * time.Second

func main() {
	app := fx.New(
		platform.Module,

		// fx's own stop timeout has to sit clearly above the server's drain.
		// Both default to fifteen seconds, and left that way the two deadlines
		// fire together: fx gives up at the same instant the drain would have
		// finished, and the log blames the wrong one.
		fx.StopTimeout(shutdownGrace),

		// fx logs its own graph events. Routing them through slog keeps one
		// JSON format in the output instead of two, which matters the first
		// time a log aggregator tries to parse the startup lines.
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger}
		}),
	)

	app.Run()

	// Run returns after the stop hooks. A failure there means something did not
	// release cleanly, and exiting zero would hide it from whatever supervises
	// this process.
	if err := app.Err(); err != nil {
		slog.Error("shutdown failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
