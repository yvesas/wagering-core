package app

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/yvesas/wagering-core/internal/domain"
)

// The fakes below are in-memory and deliberately dumb. The use case is worth
// testing without a database: what it decides -- whether an opening
// transaction exists at all, which identities get minted, what goes in one
// commit -- is logic, and logic tested through a container and a pool is logic
// that gets tested once and then not again.

type fakeClock struct{ at time.Time }

func (c fakeClock) Now() time.Time { return c.at }

// movableClock is a clock a test can push forward.
//
// A frozen clock cannot exercise anything that waits: a schedule set in the
// future is never due, and a deadline is never passed. Sleeping instead would
// make the tests slow and, worse, timing-dependent.
type movableClock struct {
	mu sync.Mutex
	at time.Time
}

func newMovableClock(at time.Time) *movableClock { return &movableClock{at: at} }

func (c *movableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *movableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// fakeIDs hands out predictable identifiers and can be told to fail, because
// "the entropy source blew up halfway through" is a path with real
// consequences: it must not leave a half-written wallet behind.
type fakeIDs struct {
	counter  int
	failAt   int
	minted   []string
	failWith error
}

func (g *fakeIDs) next() (string, error) {
	g.counter++
	if g.failWith != nil && g.counter == g.failAt {
		return "", g.failWith
	}
	id := "id-" + strconv.Itoa(g.counter)
	g.minted = append(g.minted, id)
	return id, nil
}

func (g *fakeIDs) NewWalletID(context.Context) (domain.WalletID, error) {
	s, err := g.next()
	if err != nil {
		return domain.WalletID{}, err
	}
	return domain.ParseWalletID(s)
}

func (g *fakeIDs) NewTransactionID(context.Context) (domain.TransactionID, error) {
	s, err := g.next()
	if err != nil {
		return domain.TransactionID{}, err
	}
	return domain.ParseTransactionID(s)
}

func (g *fakeIDs) NewLedgerEntryID(context.Context) (domain.LedgerEntryID, error) {
	s, err := g.next()
	if err != nil {
		return domain.LedgerEntryID{}, err
	}
	return domain.ParseLedgerEntryID(s)
}

func (g *fakeIDs) NewEventID(context.Context) (string, error) { return g.next() }

// memoryStore records what a committed transaction wrote.
type memoryStore struct {
	wallets      []domain.Wallet
	transactions []domain.WagerTransaction
	entries      []domain.LedgerEntry
	inbox        []InboxMessage
	outbox       []OutboxRecord

	failOnInsertWallet error
	failOnInsertTx     error
	commits            int
	rollbacks          int
}

func (s *memoryStore) clone() *memoryStore {
	return &memoryStore{
		wallets:            append([]domain.Wallet(nil), s.wallets...),
		transactions:       append([]domain.WagerTransaction(nil), s.transactions...),
		entries:            append([]domain.LedgerEntry(nil), s.entries...),
		inbox:              append([]InboxMessage(nil), s.inbox...),
		outbox:             append([]OutboxRecord(nil), s.outbox...),
		failOnInsertWallet: s.failOnInsertWallet,
		failOnInsertTx:     s.failOnInsertTx,
	}
}

type memoryUnitOfWork struct{ store *memoryStore }

// Do works on a copy and only publishes it when the callback returns nil, which
// is the property the real one gets from the database. A fake that wrote
// straight through would let a test pass while the real rollback was broken.
//
// The copy carries what is already committed, so a callback reads its own
// writes and everyone else's -- the same thing a transaction sees.
func (u *memoryUnitOfWork) Do(ctx context.Context, fn func(context.Context, Repositories) error) error {
	staged := u.store.clone()
	if err := fn(ctx, &memoryRepositories{store: staged}); err != nil {
		u.store.rollbacks++
		return err
	}
	u.store.wallets = staged.wallets
	u.store.transactions = staged.transactions
	u.store.entries = staged.entries
	u.store.inbox = staged.inbox
	u.store.outbox = staged.outbox
	u.store.commits++
	return nil
}

// memoryQueries reads what is committed, outside any transaction.
type memoryQueries struct{ store *memoryStore }

func (q *memoryQueries) Wallets() WalletReader           { return &memoryWallets{store: q.store} }
func (q *memoryQueries) Ledger() LedgerReader            { return &memoryLedger{store: q.store} }
func (q *memoryQueries) Transactions() TransactionReader { return &memoryTx{store: q.store} }

type memoryRepositories struct{ store *memoryStore }

func (r *memoryRepositories) Wallets() WalletRepository           { return &memoryWallets{store: r.store} }
func (r *memoryRepositories) Ledger() LedgerRepository            { return &memoryLedger{store: r.store} }
func (r *memoryRepositories) Transactions() TransactionRepository { return &memoryTx{store: r.store} }
func (r *memoryRepositories) Inbox() InboxRepository              { return &memoryInbox{store: r.store} }
func (r *memoryRepositories) Outbox() OutboxRepository            { return &memoryOutbox{store: r.store} }

// memoryOutbox keeps the events a committed transaction produced.
type memoryOutbox struct{ store *memoryStore }

func (m *memoryOutbox) Append(_ context.Context, record OutboxRecord) error {
	for _, existing := range m.store.outbox {
		if existing.EventID == record.EventID {
			return NewConflict("outbox_events_pkey")
		}
	}
	m.store.outbox = append(m.store.outbox, record)
	return nil
}

func (m *memoryOutbox) ClaimDue(_ context.Context, now time.Time, limit int) ([]OutboxRecord, error) {
	var due []OutboxRecord
	for _, record := range m.store.outbox {
		if record.Published() || record.NextAttemptAt.After(now) {
			continue
		}
		due = append(due, record)
		if len(due) == limit {
			break
		}
	}
	return due, nil
}

func (m *memoryOutbox) MarkPublished(_ context.Context, eventID string, at time.Time) error {
	for i, record := range m.store.outbox {
		if record.EventID == eventID {
			m.store.outbox[i].PublishedAt = at
			return nil
		}
	}
	return ErrNotFound
}

func (m *memoryOutbox) Reschedule(_ context.Context, eventID string, nextAttemptAt time.Time, cause string) error {
	for i, record := range m.store.outbox {
		if record.EventID == eventID {
			m.store.outbox[i].Attempts++
			m.store.outbox[i].NextAttemptAt = nextAttemptAt
			m.store.outbox[i].LastError = cause
			return nil
		}
	}
	return ErrNotFound
}

// memoryInbox enforces the same uniqueness the schema does, because that
// constraint is the deduplication: a fake without it would let a test pass
// while the real duplicate delivery went straight through.
type memoryInbox struct{ store *memoryStore }

func (m *memoryInbox) Claim(_ context.Context, message InboxMessage) error {
	for _, seen := range m.store.inbox {
		if seen.ConsumerName == message.ConsumerName && seen.MessageID == message.MessageID {
			return NewConflict("inbox_messages_identity")
		}
	}
	m.store.inbox = append(m.store.inbox, message)
	return nil
}

func (m *memoryInbox) Complete(_ context.Context, consumerName, messageID string, at time.Time) error {
	for i, seen := range m.store.inbox {
		if seen.ConsumerName == consumerName && seen.MessageID == messageID {
			m.store.inbox[i].CompletedAt = at
			return nil
		}
	}
	return ErrNotFound
}

func (m *memoryInbox) Find(_ context.Context, consumerName, messageID string) (InboxMessage, error) {
	for _, seen := range m.store.inbox {
		if seen.ConsumerName == consumerName && seen.MessageID == messageID {
			return seen, nil
		}
	}
	return InboxMessage{}, ErrNotFound
}

type memoryWallets struct{ store *memoryStore }

func (m *memoryWallets) Insert(_ context.Context, w domain.Wallet) error {
	if m.store.failOnInsertWallet != nil {
		return m.store.failOnInsertWallet
	}
	m.store.wallets = append(m.store.wallets, w)
	return nil
}

// UpdateBalance honours the expected version, because a fake that ignored it
// would make the lost-update guard untestable at this level.
func (m *memoryWallets) UpdateBalance(_ context.Context, w domain.Wallet, expected int64) error {
	for i, stored := range m.store.wallets {
		if stored.ID() != w.ID() {
			continue
		}
		if stored.Version() != expected {
			return ErrVersionMismatch
		}
		m.store.wallets[i] = w
		return nil
	}
	return ErrNotFound
}

// FindByIDForUpdate has nothing to lock here: the fake is single-threaded and
// the unit of work already serialises. It exists so the use case can be tested
// against the same port the real one implements.
func (m *memoryWallets) FindByIDForUpdate(ctx context.Context, id domain.WalletID) (domain.Wallet, error) {
	return m.FindByID(ctx, id)
}

func (m *memoryWallets) FindByID(_ context.Context, id domain.WalletID) (domain.Wallet, error) {
	for _, w := range m.store.wallets {
		if w.ID() == id {
			return w, nil
		}
	}
	return domain.Wallet{}, ErrNotFound
}

func (m *memoryWallets) FindByPlayerAndCurrency(_ context.Context, player domain.PlayerID, currency domain.Currency) (domain.Wallet, error) {
	for _, w := range m.store.wallets {
		if w.PlayerID() == player && w.Currency() == currency {
			return w, nil
		}
	}
	return domain.Wallet{}, ErrNotFound
}

type memoryLedger struct{ store *memoryStore }

func (m *memoryLedger) Append(_ context.Context, e domain.LedgerEntry) error {
	m.store.entries = append(m.store.entries, e)
	return nil
}

func (m *memoryLedger) ListByWallet(context.Context, domain.WalletID, LedgerCursor, int) (LedgerPage, error) {
	return LedgerPage{}, nil
}

// SumByWallet folds the entries the way the SQL does: credits minus debits.
//
// It is a real implementation and not a stub because reconciliation is the one
// use case whose whole job is this sum -- a fake returning zero would let every
// reconciliation test pass against a wallet with money in it.
func (m *memoryLedger) SumByWallet(_ context.Context, wallet domain.WalletID, currency domain.Currency) (domain.Money, int, error) {
	total, err := domain.ZeroMoney(currency)
	if err != nil {
		return domain.Money{}, 0, err
	}
	count := 0

	for _, entry := range m.store.entries {
		if entry.WalletID() != wallet {
			continue
		}
		count++

		if entry.Direction() == domain.Credit {
			total, err = total.Add(entry.Amount())
		} else {
			total, err = total.Sub(entry.Amount())
		}
		if err != nil {
			return domain.Money{}, 0, err
		}
	}
	return total, count, nil
}

type memoryTx struct{ store *memoryStore }

// Insert enforces the same two uniqueness rules the schema does, because those
// constraints are the actual idempotency guarantee -- a fake without them would
// let a test pass while the real race was wide open.
func (m *memoryTx) Insert(_ context.Context, t domain.WagerTransaction) error {
	if m.store.failOnInsertTx != nil {
		return m.store.failOnInsertTx
	}
	for _, stored := range m.store.transactions {
		if stored.ProviderID() == t.ProviderID() && stored.ExternalID() == t.ExternalID() &&
			!t.ExternalID().IsZero() {
			return NewConflict("wager_transactions_business_identity")
		}
		if stored.ProviderID() == t.ProviderID() && stored.IdempotencyKey() == t.IdempotencyKey() &&
			!t.IdempotencyKey().IsZero() {
			return NewConflict(constraintIdempotencyKey)
		}
	}
	m.store.transactions = append(m.store.transactions, t)
	return nil
}

func (m *memoryTx) Update(_ context.Context, t domain.WagerTransaction) error {
	for i, stored := range m.store.transactions {
		if stored.ID() != t.ID() {
			continue
		}
		if stored.IsTerminal() {
			return ErrConflict
		}
		m.store.transactions[i] = t
		return nil
	}
	return ErrNotFound
}

func (m *memoryTx) FindByID(_ context.Context, id domain.TransactionID) (domain.WagerTransaction, error) {
	for _, t := range m.store.transactions {
		if t.ID() == id {
			return t, nil
		}
	}
	return domain.WagerTransaction{}, ErrNotFound
}

func (m *memoryTx) FindProcessedReversalOf(_ context.Context, reference domain.TransactionID) (domain.WagerTransaction, error) {
	for _, t := range m.store.transactions {
		if t.ResolvedReferenceID() == reference && t.Status() == domain.StatusProcessed {
			return t, nil
		}
	}
	return domain.WagerTransaction{}, ErrNotFound
}

func (m *memoryTx) ClaimDueReferences(_ context.Context, now time.Time, limit int) ([]domain.WagerTransaction, error) {
	var due []domain.WagerTransaction
	for _, t := range m.store.transactions {
		if t.Status() != domain.StatusPendingReference {
			continue
		}
		if t.ReferenceNextAttemptAt().After(now) {
			continue
		}
		due = append(due, t)
		if len(due) == limit {
			break
		}
	}
	return due, nil
}

func (m *memoryTx) FindByBusinessID(_ context.Context, provider domain.ProviderID, external domain.ExternalTransactionID) (domain.WagerTransaction, error) {
	for _, t := range m.store.transactions {
		if t.ProviderID() == provider && t.ExternalID() == external {
			return t, nil
		}
	}
	return domain.WagerTransaction{}, ErrNotFound
}

func newFixture() (*OpenWallet, *memoryStore, *fakeIDs) {
	store := &memoryStore{}
	ids := &fakeIDs{}
	clock := fakeClock{at: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
	return NewOpenWallet(&memoryUnitOfWork{store: store}, ids, clock), store, ids
}

func TestOpenWalletWithBalance(t *testing.T) {
	t.Parallel()
	uc, store, ids := newFixture()

	wallet, err := uc.Execute(callerContext(), OpenWalletCommand{
		PlayerID: "player-1",
		Amount:   "1000.00",
		Currency: "BRL",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if wallet.Balance().String() != "1000.00" {
		t.Errorf("balance = %s", wallet.Balance())
	}
	// The opening credit is part of creating the wallet, so there is no earlier
	// version for it to advance from.
	if wallet.Version() != 1 {
		t.Errorf("version = %d, want 1", wallet.Version())
	}

	if len(store.wallets) != 1 || len(store.transactions) != 1 || len(store.entries) != 1 {
		t.Fatalf("stored %d wallets, %d transactions, %d entries",
			len(store.wallets), len(store.transactions), len(store.entries))
	}
	if store.commits != 1 {
		t.Errorf("committed %d times, want once: all three writes are one commit", store.commits)
	}
	// Wallet, transaction, entry, and one id for each of the two events: the
	// opening completed, and the balance changed.
	if got := len(ids.minted); got != 5 {
		t.Errorf("minted %d ids, want 5", got)
	}
	if len(store.outbox) != 2 {
		t.Fatalf("recorded %d events, want 2", len(store.outbox))
	}

	if store.transactions[0].Kind() != domain.KindOpening {
		t.Errorf("kind = %q", store.transactions[0].Kind())
	}
	if store.transactions[0].Origin() != domain.OriginInternal {
		t.Errorf("origin = %q", store.transactions[0].Origin())
	}
}

func TestOpenWalletAtZeroWritesOnlyTheWallet(t *testing.T) {
	t.Parallel()
	uc, store, ids := newFixture()

	wallet, err := uc.Execute(callerContext(), OpenWalletCommand{
		PlayerID: "player-1",
		Amount:   "0.00",
		Currency: "BRL",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(store.transactions) != 0 {
		t.Errorf("an opening of nothing created %d transactions", len(store.transactions))
	}
	if len(store.entries) != 0 {
		t.Errorf("an opening of nothing created %d ledger entries", len(store.entries))
	}
	if wallet.Version() != 1 {
		t.Errorf("version = %d, want 1", wallet.Version())
	}
	// Only the wallet id. Minting the others would burn identities on records
	// that are never written -- and an opening of nothing produces no events,
	// because nothing happened to report.
	if got := len(ids.minted); got != 1 {
		t.Errorf("minted %d ids, want 1", got)
	}
	if len(store.outbox) != 0 {
		t.Errorf("an opening of nothing produced %d events", len(store.outbox))
	}
}

func TestOpenWalletRejectsBadInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cmd      OpenWalletCommand
		wantCode domain.Code
	}{
		{"empty player", OpenWalletCommand{PlayerID: "", Amount: "1.00", Currency: "BRL"}, domain.CodeInvalidIdentifier},
		{"lowercase currency", OpenWalletCommand{PlayerID: "p", Amount: "1.00", Currency: "brl"}, domain.CodeInvalidCurrency},
		{"unknown currency shape", OpenWalletCommand{PlayerID: "p", Amount: "1.00", Currency: "REAIS"}, domain.CodeInvalidCurrency},
		{"excess scale", OpenWalletCommand{PlayerID: "p", Amount: "1.000", Currency: "BRL"}, domain.CodeInvalidAmount},
		{"scientific notation", OpenWalletCommand{PlayerID: "p", Amount: "1e3", Currency: "BRL"}, domain.CodeInvalidAmount},
		{"empty amount", OpenWalletCommand{PlayerID: "p", Amount: "", Currency: "BRL"}, domain.CodeInvalidAmount},
		// Negative is rejected at the edge, not merely stored: external money
		// is never negative, whatever the caller says.
		{"negative", OpenWalletCommand{PlayerID: "p", Amount: "-1.00", Currency: "BRL"}, domain.CodeNegativeAmount},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uc, store, _ := newFixture()

			_, err := uc.Execute(callerContext(), tc.cmd)
			if err == nil {
				t.Fatal("want a rejection")
			}
			var de *domain.Error
			if !errors.As(err, &de) {
				t.Fatalf("error %v is not a domain rejection", err)
			}
			if de.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q (%v)", de.Code, tc.wantCode, err)
			}
			// Nothing reached the database, and nothing was committed.
			if store.commits != 0 || len(store.wallets) != 0 {
				t.Errorf("a rejected command wrote: %d commits, %d wallets", store.commits, len(store.wallets))
			}
		})
	}
}

func TestOpenWalletPropagatesAConflict(t *testing.T) {
	t.Parallel()
	store := &memoryStore{failOnInsertWallet: NewConflict("wallets_one_per_player_and_currency")}
	uc := NewOpenWallet(&memoryUnitOfWork{store: store}, &fakeIDs{},
		fakeClock{at: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)})

	_, err := uc.Execute(callerContext(), OpenWalletCommand{
		PlayerID: "player-1", Amount: "1.00", Currency: "BRL",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if store.rollbacks != 1 {
		t.Errorf("rolled back %d times, want once", store.rollbacks)
	}
	if store.commits != 0 {
		t.Errorf("committed despite the conflict")
	}
}

func TestOpenWalletDoesNotWriteWhenAnIDCannotBeMinted(t *testing.T) {
	t.Parallel()
	entropy := errors.New("entropy source exhausted")

	for name, failAt := range map[string]int{
		"wallet id":       1,
		"transaction id":  2,
		"ledger entry id": 3,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &memoryStore{}
			ids := &fakeIDs{failAt: failAt, failWith: entropy}
			uc := NewOpenWallet(&memoryUnitOfWork{store: store}, ids,
				fakeClock{at: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)})

			_, err := uc.Execute(callerContext(), OpenWalletCommand{
				PlayerID: "player-1", Amount: "10.00", Currency: "BRL",
			})
			if !errors.Is(err, entropy) {
				t.Fatalf("error = %v, want the entropy failure", err)
			}
			// Every id is minted before the transaction opens, so a failure
			// here cannot leave a half-written wallet behind.
			if store.commits != 0 || store.rollbacks != 0 {
				t.Errorf("touched the database: %d commits, %d rollbacks", store.commits, store.rollbacks)
			}
		})
	}
}

func TestOpenWalletUsesTheInjectedClock(t *testing.T) {
	t.Parallel()
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	store := &memoryStore{}
	uc := NewOpenWallet(&memoryUnitOfWork{store: store}, &fakeIDs{}, fakeClock{at: at})

	wallet, err := uc.Execute(callerContext(), OpenWalletCommand{
		PlayerID: "player-1", Amount: "10.00", Currency: "BRL",
	})
	if err != nil {
		t.Fatal(err)
	}
	// One reading of the clock for the whole operation: the wallet, the
	// transaction and the entry share an instant, which is what makes them
	// look like one event to anyone reading them later.
	if !wallet.CreatedAt().Equal(at) {
		t.Errorf("createdAt = %v, want %v", wallet.CreatedAt(), at)
	}
	if !store.entries[0].CreatedAt().Equal(at) {
		t.Errorf("the entry has a different instant from the wallet")
	}
	if !store.transactions[0].CreatedAt().Equal(at) {
		t.Errorf("the transaction has a different instant from the wallet")
	}
}
