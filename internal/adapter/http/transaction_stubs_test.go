package http

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/yvesas/wagering-core/internal/app"
	"github.com/yvesas/wagering-core/internal/domain"
)

// TestMain silences the structured logger. These tests exercise the middleware
// on purpose, and its output would otherwise bury the one line that matters
// when something fails.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type stubSubmitter struct {
	result app.SubmitResult
	err    error
	got    app.SubmitCommand
}

func (s *stubSubmitter) Execute(_ context.Context, cmd app.SubmitCommand) (app.SubmitResult, error) {
	s.got = cmd
	return s.result, s.err
}

type stubTxReader struct {
	transaction domain.WagerTransaction
	err         error

	gotProvider string
	gotExternal string
}

func (s *stubTxReader) Get(context.Context, string) (domain.WagerTransaction, error) {
	return s.transaction, s.err
}

func (s *stubTxReader) GetByBusinessID(_ context.Context, provider, external string) (domain.WagerTransaction, error) {
	s.gotProvider, s.gotExternal = provider, external
	return s.transaction, s.err
}
