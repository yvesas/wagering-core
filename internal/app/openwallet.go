package app

import (
	"context"
	"fmt"

	"github.com/yvesas/wagering-core/internal/domain"
)

// OpenWalletCommand is what the edge hands in: strings, exactly as they arrived.
//
// They stay strings on purpose. Parsing is the first thing the use case does,
// through the domain's own constructors, so there is one place where external
// input becomes a domain type and one place that can reject it.
type OpenWalletCommand struct {
	PlayerID string
	Amount   string
	Currency string
}

// OpenWallet creates a wallet and, when it opens with money, the opening credit
// that goes with it.
type OpenWallet struct {
	uow   UnitOfWork
	ids   IDGenerator
	clock Clock
}

// NewOpenWallet takes plain constructor arguments, which is what lets the use
// case be built by fx.Provide and by three lines in a test alike. See
// docs/adr/0004-fx-only-at-the-edge.md.
func NewOpenWallet(uow UnitOfWork, ids IDGenerator, clock Clock) *OpenWallet {
	return &OpenWallet{uow: uow, ids: ids, clock: clock}
}

// Execute opens the wallet.
//
// The wallet, the OPENING transaction and the ledger entry are written in one
// transaction. There is no intermediate PENDING: the opening depends on nothing
// and is committed with the wallet, so there is no window in which it is
// accepted but not yet applied, and nothing for another instance to resume.
func (uc *OpenWallet) Execute(ctx context.Context, cmd OpenWalletCommand) (domain.Wallet, error) {
	playerID, err := domain.ParsePlayerID(cmd.PlayerID)
	if err != nil {
		return domain.Wallet{}, err
	}
	currency, err := domain.ParseCurrency(cmd.Currency)
	if err != nil {
		return domain.Wallet{}, err
	}

	// ParseExternalMoney, not ParseMoney: this is money arriving from outside,
	// and outside never sends a negative opening balance.
	initial, err := domain.ParseExternalMoney(cmd.Amount, currency)
	if err != nil {
		return domain.Wallet{}, err
	}

	walletID, err := uc.ids.NewWalletID(ctx)
	if err != nil {
		return domain.Wallet{}, fmt.Errorf("minting a wallet id: %w", err)
	}

	at := uc.clock.Now()
	params := domain.OpenWalletParams{
		ID:             walletID,
		PlayerID:       playerID,
		InitialBalance: initial,
		CreatedAt:      at,
	}

	// The opening identities are only minted when there is an opening to
	// identify. A wallet that starts at zero creates no transaction and no
	// entry, so asking for ids here would burn two identities on nothing.
	if initial.IsPositive() {
		if params.OpeningTransactionID, err = uc.ids.NewTransactionID(ctx); err != nil {
			return domain.Wallet{}, fmt.Errorf("minting a transaction id: %w", err)
		}
		if params.OpeningEntryID, err = uc.ids.NewLedgerEntryID(ctx); err != nil {
			return domain.Wallet{}, fmt.Errorf("minting a ledger entry id: %w", err)
		}
	}

	opening, err := domain.OpenWallet(params)
	if err != nil {
		return domain.Wallet{}, err
	}

	var openingTx domain.WagerTransaction
	if initial.IsPositive() {
		openingTx, err = domain.NewOpeningTransaction(domain.OpeningTransactionParams{
			ID:        params.OpeningTransactionID,
			WalletID:  walletID,
			PlayerID:  playerID,
			Money:     initial,
			CreatedAt: at,
		})
		if err != nil {
			return domain.Wallet{}, err
		}
	}

	err = uc.uow.Do(ctx, func(ctx context.Context, repos Repositories) error {
		if err := repos.Wallets().Insert(ctx, opening.Wallet); err != nil {
			return err
		}
		if openingTx.IsInitialised() {
			if err := repos.Transactions().Insert(ctx, openingTx); err != nil {
				return err
			}
		}
		for _, entry := range opening.Entries {
			if err := repos.Ledger().Append(ctx, entry); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.Wallet{}, err
	}

	return opening.Wallet, nil
}
