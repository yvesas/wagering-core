#!/usr/bin/env bash
# The same wallet, hit from many directions at once.
#
# The Go suite proves this with three processes and a race detector. What this
# adds is a dial: change N and watch what the numbers do. A wallet is the unit
# of contention, so N here is pressure on exactly one row.
#
#   make simulate SCENARIO=concurrency
#   N=50 make simulate SCENARIO=concurrency

source "$(dirname "$0")/lib.sh"

N="${N:-20}"
ACME="$(token provider-acme)"

step "$N copies of one bet, in parallel"
PLAYER="player-same-$RUN_ID"
WALLET="$(open_wallet "$PLAYER" '100.00')"
BET="$(operation acme "same-$RUN_ID" "$PLAYER" "$WALLET" BET '25.00')"

for _ in $(seq 1 "$N"); do
  ( submit "$ACME" "$BET" >/dev/null 2>&1 ) &
done
wait

# One operation, whatever the parallelism. The uniqueness constraint on
# (provider, externalTransactionId) is what serialises this, and it is the
# database that decides -- not a lock any one process holds.
expect_equal "$(balance_of "$WALLET")" '75.00' "$N identical bets moved money once"

http GET "/wallets/$WALLET/ledger" "$(token platform)"
expect_json '[.entries[] | select(.direction=="DEBIT")] | length' '1' 'one debit in the ledger'

step "Two bets racing for a balance that fits only one"
PLAYER="player-race-$RUN_ID"
WALLET="$(open_wallet "$PLAYER" '100.00')"

# 80.00 twice against 100.00. Both are legitimate and only one can win; the
# other has to be refused, not queued and not half-applied.
( submit "$ACME" "$(operation acme "race-a-$RUN_ID" "$PLAYER" "$WALLET" BET '80.00')" >/tmp/race-a 2>&1 ) &
( submit "$ACME" "$(operation acme "race-b-$RUN_ID" "$PLAYER" "$WALLET" BET '80.00')" >/tmp/race-b 2>&1 ) &
wait

expect_equal "$(balance_of "$WALLET")" '20.00' 'exactly one bet was applied'
http GET "/wallets/$WALLET/ledger" "$(token platform)"
expect_json '[.entries[] | select(.direction=="DEBIT")] | length' '1' 'and one debit'

step "Different wallets do not wait for each other"
# There is no global lock. Two wallets are two rows, and two rows move at the
# same time -- which is the whole reason the lock is per wallet.
PLAYER_A="player-par-a-$RUN_ID"; WALLET_A="$(open_wallet "$PLAYER_A" '100.00')"
PLAYER_B="player-par-b-$RUN_ID"; WALLET_B="$(open_wallet "$PLAYER_B" '100.00')"

( submit "$ACME" "$(operation acme "par-a-$RUN_ID" "$PLAYER_A" "$WALLET_A" BET '30.00')" >/dev/null 2>&1 ) &
( submit "$ACME" "$(operation acme "par-b-$RUN_ID" "$PLAYER_B" "$WALLET_B" BET '30.00')" >/dev/null 2>&1 ) &
wait

expect_equal "$(balance_of "$WALLET_A")" '70.00' 'the first wallet moved'
expect_equal "$(balance_of "$WALLET_B")" '70.00' 'and so did the second'

step "Every wallet still agrees with its own ledger"
for wallet in "$WALLET" "$WALLET_A" "$WALLET_B"; do
  http POST "/wallets/$wallet/reconciliation" "$(token platform)"
  expect_json '.consistent' 'true' "${wallet:0:8}… reconciles"
done

step "What the contention cost"
# These two together say how much of the disputing was absorbed by the retry
# and how much reached a client as a 409.
note "retries:    $(metric wagering_transaction_retries_total)"
note "contention: $(metric wagering_wallet_contention_total)"
