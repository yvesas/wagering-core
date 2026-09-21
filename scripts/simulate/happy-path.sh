#!/usr/bin/env bash
# The path a provider walks on its first day: open a wallet, place a bet, win,
# lose, and read it all back.
#
#   make simulate SCENARIO=happy-path

source "$(dirname "$0")/lib.sh"

ACME="$(token provider-acme)"
PLAYER="player-happy-$RUN_ID"

step "Open a wallet with 100.00"
WALLET="$(open_wallet "$PLAYER" '100.00')"
note "wallet $WALLET"

step "A bet of 25.00"
submit "$ACME" "$(operation acme "bet-$RUN_ID" "$PLAYER" "$WALLET" BET '25.00')"
expect_status 200 'the bet is accepted'
expect_json '.status' 'PROCESSED' 'the status'
expect_json '.balance.amount' '75.00' 'the balance it observed'
BET_ID="$(printf '%s' "$HTTP_BODY" | jq -r .transactionId)"

step "A win of 40.00"
submit "$ACME" "$(operation acme "win-$RUN_ID" "$PLAYER" "$WALLET" WIN '40.00')"
expect_status 200 'the win is accepted'
expect_json '.balance.amount' '115.00' 'the balance after the win'

step "A loss, which moves no money"
# LOSS records how a round ended. It is the case that separates "the operation
# finished" from "the balance changed", and it is why those are two events.
submit "$ACME" "$(operation acme "loss-$RUN_ID" "$PLAYER" "$WALLET" LOSS '0.00')"
expect_status 200 'the loss is accepted'
expect_json '.balance.amount' '115.00' 'the balance is untouched'

step "Read the bet back, by our id and by the provider's own"
http GET "/wagering/transactions/$BET_ID" "$ACME"
expect_status 200 'by transaction id'
expect_json '.kind' 'BET' 'the kind'
expect_json '.balanceAfter' '75.00' 'the balance it was processed against'

http GET "/providers/acme/wagering/transactions/bet-$RUN_ID" "$ACME"
expect_status 200 "by the provider's own id"
expect_json '.transactionId' "$BET_ID" 'the same operation'

step "The ledger, as the platform"
http GET "/wallets/$WALLET/ledger" "$(token platform)"
expect_status 200 'the ledger reads'
# The opening credit, the bet and the win. The loss moved nothing, so it has no
# entry -- a ledger line is a movement, not a record that something happened.
expect_json '.entries | length' '3' 'three entries, and the loss is not one'
expect_json '[.entries[].direction] | join(",")' 'CREDIT,DEBIT,CREDIT' 'in order'

step "The balance agrees with the ledger"
expect_equal "$(balance_of "$WALLET")" '115.00' 'the stored balance'
http POST "/wallets/$WALLET/reconciliation" "$(token platform)"
expect_json '.consistent' 'true' 'the wallet reconciles'
