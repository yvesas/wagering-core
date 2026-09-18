package platform

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	nethttp "net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver for migrations
	"go.uber.org/fx"

	httpadapter "github.com/yvesas/wagering-core/internal/adapter/http"
	"github.com/yvesas/wagering-core/internal/adapter/postgres"
	"github.com/yvesas/wagering-core/internal/adapter/system"
	"github.com/yvesas/wagering-core/internal/app"
)

// This file is the only place in the codebase that knows Fx exists, along with
// cmd/. Everything it wires is an ordinary constructor, which is what lets the
// same code be built by three lines in a test.
// See docs/adr/0004-fx-only-at-the-edge.md.

// ConfigModule reads the settings and builds the logger.
var ConfigModule = fx.Module("config",
	fx.Provide(
		AppConfigFromEnv,
		DatabaseConfigFromEnv,
		NewLogger,
	),
)

// DatabaseModule owns the pool, and owns closing it.
var DatabaseModule = fx.Module("database",
	fx.Provide(
		newPool,
		postgres.NewUnitOfWork,
		postgres.NewQueries,
		postgres.NewProbe,
	),
)

// AdaptersModule provides the ports that are not storage.
var AdaptersModule = fx.Module("adapters",
	fx.Provide(
		system.NewClock,
		system.NewIDGenerator,
	),
)

// UseCasesModule provides the application layer. Nothing here mentions Fx --
// these are the same constructors a test calls directly.
var UseCasesModule = fx.Module("usecases",
	fx.Provide(
		app.NewOpenWallet,
		app.NewSubmitTransaction,
		app.NewWalletQueries,
		app.NewTransactionQueries,
	),
)

// HTTPModule builds the server and runs it.
var HTTPModule = fx.Module("http",
	fx.Provide(
		newWalletHandler,
		newTransactionHandler,
		newHealthHandler,
		httpadapter.Routes,
		httpadapter.Handler,
		newServer,
	),
	fx.Invoke(runServer),
)

// Module is the whole application.
var Module = fx.Options(
	ConfigModule,
	DatabaseModule,
	AdaptersModule,
	UseCasesModule,
	HTTPModule,
)

// newPool opens the pool, applies migrations and closes the pool on stop.
//
// Migrations run at start-up from every replica. goose takes an advisory lock,
// so the race is resolved by the database rather than by hoping only one
// process starts at a time.
func newPool(lc fx.Lifecycle, appCfg AppConfig, dbCfg DatabaseConfig, logger *slog.Logger) (*pgxpool.Pool, error) {
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, postgres.PoolConfig{
		DSN:       dbCfg.DSN(),
		SafeLabel: dbCfg.Redacted(),
		MaxConns:  appCfg.DBMaxConns,
	})
	if err != nil {
		return nil, err
	}

	migrationDB, err := sql.Open("pgx", dbCfg.DSN())
	if err != nil {
		pool.Close()
		return nil, err
	}
	defer migrationDB.Close()

	if err := postgres.Migrate(ctx, migrationDB); err != nil {
		pool.Close()
		return nil, err
	}
	logger.Info("database ready", slog.String("dsn", dbCfg.Redacted()))

	// Registering OnStop here, at construction, is what puts the pool's
	// shutdown *after* the server's: Fx runs OnStop in reverse registration
	// order, and the server is built from this pool, so it registers later.
	// If the pool closed first, every request the drain exists to finish would
	// fail on a closed connection in its last millisecond.
	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			logger.Info("closing the database pool")
			pool.Close()
			return nil
		},
	})

	return pool, nil
}

// newWalletHandler binds the concrete use cases to the small interfaces the
// handler declares. The assignment is the whole binding -- no fx.As, no
// annotation, just Go satisfying an interface.
func newWalletHandler(open *app.OpenWallet, queries *app.WalletQueries) *httpadapter.WalletHandler {
	return httpadapter.NewWalletHandler(open, queries)
}

func newTransactionHandler(submit *app.SubmitTransaction, queries *app.TransactionQueries) *httpadapter.TransactionHandler {
	return httpadapter.NewTransactionHandler(submit, queries)
}

func newHealthHandler(probe *postgres.Probe) *httpadapter.HealthHandler {
	return httpadapter.NewHealthHandler(probe)
}

func newServer(cfg AppConfig, handler nethttp.Handler) *nethttp.Server {
	return httpadapter.NewServer(httpadapter.ServerConfig{
		Addr:            cfg.HTTPAddr,
		ShutdownTimeout: cfg.ShutdownTimeout,
	}, handler)
}

// runServer starts listening and drains on stop.
func runServer(lc fx.Lifecycle, server *nethttp.Server, cfg AppConfig, logger *slog.Logger) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			// The listener is opened here rather than inside ListenAndServe so
			// a port already in use fails the start. Left to the goroutine, the
			// process would report "started" and serve nothing.
			listener, err := net.Listen("tcp", server.Addr)
			if err != nil {
				return err
			}
			logger.Info("http server listening", slog.String("addr", listener.Addr().String()))

			go func() {
				if err := server.Serve(listener); err != nil && !errors.Is(err, nethttp.ErrServerClosed) {
					logger.Error("http server stopped", slog.String("error", err.Error()))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			// Shutdown stops accepting and waits for in-flight requests. The
			// deadline is what keeps a drain from becoming a process that never
			// exits -- and a process that does not exit is a deploy that does
			// not finish.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
			defer cancel()

			logger.Info("draining the http server",
				slog.Duration("timeout", cfg.ShutdownTimeout))

			if err := server.Shutdown(ctx); err != nil {
				// Close is the blunt version: the deadline passed and something
				// is still holding a connection. Reporting it matters, because
				// it means a request was cut off.
				logger.Warn("drain did not finish in time; closing",
					slog.String("error", err.Error()))
				return server.Close()
			}
			return nil
		},
	})
}
