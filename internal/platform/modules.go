package platform

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	nethttp "net/http"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver for migrations
	"go.uber.org/fx"

	httpadapter "github.com/yvesas/wagering-core/internal/adapter/http"
	"github.com/yvesas/wagering-core/internal/adapter/postgres"
	sqsadapter "github.com/yvesas/wagering-core/internal/adapter/sqs"
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
		referencePolicy,
		app.NewOpenWallet,
		app.NewSubmitTransaction,
		newReferenceWorker,
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

// QueueModule provisions the queues and builds the consumers.
var QueueModule = fx.Module("queue",
	fx.Provide(
		newQueueClient,
		newEventPublisher,
		publisherPolicy,
		newPublisher,
	),
)

// WorkersModule runs the background work.
var WorkersModule = fx.Module("workers",
	fx.Invoke(runReferenceWorker),
	fx.Invoke(runConsumers),
	fx.Invoke(runPublisher),
)

// Module is the whole application.
var Module = fx.Options(
	ConfigModule,
	DatabaseModule,
	AdaptersModule,
	UseCasesModule,
	QueueModule,
	HTTPModule,
	WorkersModule,
)

// newQueueClient reaches the broker and provisions the queues.
//
// It happens at start-up so a broker that is unreachable fails the boot rather
// than the first message, and it is idempotent so every replica can do it.
func newQueueClient(appCfg AppConfig, logger *slog.Logger) (*sqsadapter.Client, error) {
	client, err := sqsadapter.New(context.Background(), sqsadapter.Config{
		Endpoint:          appCfg.QueueEndpoint,
		Region:            appCfg.QueueRegion,
		QueueName:         appCfg.QueueName,
		DLQName:           appCfg.QueueDLQName,
		AccessKeyID:       appCfg.AWSAccessKeyID,
		SecretKey:         appCfg.AWSSecretAccessKey,
		VisibilityTimeout: appCfg.QueueVisibilityTimeout,
		WaitTime:          appCfg.QueueWaitTime,
		MaxReceiveCount:   appCfg.QueueMaxReceiveCount,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("queues ready",
		slog.String("queue", appCfg.QueueName),
		slog.String("deadLetter", appCfg.QueueDLQName))
	return client, nil
}

// newEventPublisher provisions the destination for outgoing events.
func newEventPublisher(appCfg AppConfig, logger *slog.Logger) (app.EventPublisher, error) {
	publisher, err := sqsadapter.NewEventPublisher(context.Background(), sqsadapter.Config{
		Endpoint:    appCfg.QueueEndpoint,
		Region:      appCfg.QueueRegion,
		AccessKeyID: appCfg.AWSAccessKeyID,
		SecretKey:   appCfg.AWSSecretAccessKey,
	}, appCfg.QueueEventsName)
	if err != nil {
		return nil, err
	}
	logger.Info("events destination ready", slog.String("queue", appCfg.QueueEventsName))
	return publisher, nil
}

func publisherPolicy(cfg AppConfig) app.PublisherPolicy {
	return app.PublisherPolicy{
		BatchSize:      cfg.PublisherBatchSize,
		Interval:       cfg.PublisherInterval,
		BaseBackoff:    cfg.PublisherBaseBackoff,
		PublishTimeout: cfg.PublisherTimeout,
	}
}

func newPublisher(uow app.UnitOfWork, publisher app.EventPublisher, clock app.Clock, policy app.PublisherPolicy, logger *slog.Logger) *app.Publisher {
	return app.NewPublisher(uow, publisher, clock, policy, logger)
}

// runPublisher starts the outbox publisher and stops it before the pool closes.
//
// Nothing waits for it on the way out beyond the deadline: an event that was
// not published is still in the outbox, and whoever runs next picks it up. That
// is the property the outbox buys.
func runPublisher(lc fx.Lifecycle, publisher *app.Publisher, logger *slog.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				publisher.Run(ctx)
			}()
			logger.Info("outbox publisher started")
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			select {
			case <-done:
				logger.Info("outbox publisher stopped")
			case <-stopCtx.Done():
				logger.Warn("outbox publisher did not stop in time")
			}
			return nil
		},
	})
}

// runConsumers starts the queue consumers and drains them on stop.
//
// Stopping cancels the context, which makes the in-flight Receive return at
// once; whatever a consumer already holds is finished by the RunOnce it is in.
// Anything that does not fit in the deadline has its visibility released, so
// another instance picks it up instead of waiting out the timeout.
func runConsumers(
	lc fx.Lifecycle,
	client *sqsadapter.Client,
	uow app.UnitOfWork,
	submit *app.SubmitTransaction,
	clock app.Clock,
	cfg AppConfig,
	logger *slog.Logger,
) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			for i := 0; i < cfg.QueueConsumers; i++ {
				consumer := app.NewConsumer(client, uow, submit, clock, app.ConsumerConfig{
					// One name for all the loops in this process: they are
					// interchangeable workers on one queue, not different
					// consumers of it. Naming them apart would let the same
					// message be handled once per loop.
					Name:      cfg.QueueName,
					BatchSize: cfg.QueueBatchSize,
				}, logger)

				wg.Add(1)
				go func() {
					defer wg.Done()
					consumer.Run(ctx)
				}()
			}
			logger.Info("queue consumers started", slog.Int("count", cfg.QueueConsumers))
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()

			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()

			select {
			case <-done:
				logger.Info("queue consumers stopped")
			case <-stopCtx.Done():
				// Mid-message when the deadline passed. Saying so matters: the
				// work is either committed or rolled back, and the message is
				// either deleted or coming back.
				logger.Warn("queue consumers did not stop in time")
			}
			return nil
		},
	})
}

// referencePolicy reads the wait settings for a reversal whose target has not
// arrived.
func referencePolicy(cfg AppConfig) app.ReferencePolicy {
	return app.ReferencePolicy{
		MaxAttempts: cfg.ReferenceMaxAttempts,
		BaseBackoff: cfg.ReferenceBaseBackoff,
		TTL:         cfg.ReferenceTTL,
	}
}

func newReferenceWorker(uow app.UnitOfWork, ids app.IDGenerator, clock app.Clock, policy app.ReferencePolicy, logger *slog.Logger) *app.ReferenceWorker {
	return app.NewReferenceWorker(uow, ids, clock, policy, logger)
}

// runReferenceWorker starts the worker and stops it before the pool closes.
//
// It registers after the server, so its OnStop runs first: the worker stops
// taking new pending items while requests are still draining, and both are done
// before the pool goes away.
func runReferenceWorker(lc fx.Lifecycle, worker *app.ReferenceWorker, logger *slog.Logger) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				worker.Run(ctx)
			}()
			logger.Info("pending reference worker started")
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			select {
			case <-done:
				logger.Info("pending reference worker stopped")
				return nil
			case <-stopCtx.Done():
				// It is mid-transaction and the deadline passed. Saying so
				// matters: the work will be picked up again by whoever is next,
				// because the pending state is in the database.
				logger.Warn("pending reference worker did not stop in time")
				return nil
			}
		},
	})
}

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
