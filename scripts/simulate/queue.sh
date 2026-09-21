#!/usr/bin/env bash
# The second entry port.
#
# The same operation, delivered by the queue instead of by HTTP, has to end in
# the same state. What differs is the shape of the envelope and the fact that
# nothing answers synchronously -- so every check here polls.
#
#   make simulate SCENARIO=queue

source "$(dirname "$0")/lib.sh"

PLAYER="player-queue-$RUN_ID"
WALLET="$(open_wallet "$PLAYER" '100.00')"
note "wallet $WALLET"

# The queue carries no token. What decides who may put a message on it is the
# broker's own access policy -- REQ-SEC-005, said plainly: whoever can write to
# the queue can act as any provider. Everything after that point is the same
# use case with the same rules.
step "One bet, over the queue"
BET="$(operation acme "q-bet-$RUN_ID" "$PLAYER" "$WALLET" BET '25.00')"
send_envelope "msg-$RUN_ID" "$BET" "$WALLET"

# Three visibility timeouts, and the number is the contract rather than
# impatience. Delivery is at-least-once with a 30s visibility timeout, so a
# delivery that is not completed in time comes round again -- and the queue
# path shares a connection pool with HTTP, so right after a burst of requests
# one cycle can be spent waiting. Allowing less than one redelivery would be
# asserting "the first attempt always lands", which nothing promises.
applied() { [[ "$(balance_of "$WALLET")" == '75.00' ]]; }
if wait_for 90 applied; then
  pass 'the queued bet was applied'
else
  fail "the queued bet never landed; balance is $(balance_of "$WALLET")"
fi

step "It is readable exactly like an HTTP one"
http GET "/providers/acme/wagering/transactions/q-bet-$RUN_ID" "$(token provider-acme)"
expect_status 200 'the operation exists'
expect_json '.status' 'PROCESSED' 'and is processed'
expect_json '.balanceAfter' '75.00' 'with the balance it observed'

step "The same message id again"
# At-least-once delivery is the contract, so this is routine rather than an
# error. The inbox absorbs it: one refused insert, and nothing else moves.
BEFORE="$(metric wagering_queue_duplicates_total)"
send_envelope "msg-$RUN_ID" "$BET" "$WALLET"

deduped() { [[ "$(metric wagering_queue_duplicates_total)" != "$BEFORE" ]]; }
if wait_for 90 deduped; then
  pass 'the redelivery was absorbed by the inbox'
else
  fail 'the duplicate counter never moved'
fi
expect_equal "$(balance_of "$WALLET")" '75.00' 'and the money did not move twice'

step "A new message id carrying the same operation"
# A different delivery of an operation already applied. The inbox has not seen
# this message, so it goes through -- and the business identity stops it.
BEFORE="$(metric 'wagering_operations_total{kind="BET",source="queue",status="PROCESSED"}')"
send_envelope "msg-again-$RUN_ID" "$BET" "$WALLET"

handled() { [[ "$(metric 'wagering_operations_total{kind="BET",source="queue",status="PROCESSED"}')" != "$BEFORE" ]]; }
if wait_for 90 handled; then
  pass 'the second delivery was handled'
else
  fail 'the second delivery never arrived'
fi
# It was handled and it moved nothing: the operation was already applied, and
# (provider, externalTransactionId) is what says so.
expect_equal "$(balance_of "$WALLET")" '75.00' 'still one debit'

step "An envelope the domain refuses"
# Redelivering this would repeat the same refusal until the dead-letter queue
# took it, so it is recorded as handled and dropped, loudly.
BEFORE="$(metric 'wagering_queue_discarded_total{reason="unusable"}')"
BAD="$(printf '%s' "$BET" | jq -c '.externalTransactionId = "bad-'"$RUN_ID"'" | .money.amount = "25.00.00"')"
send_envelope "msg-bad-$RUN_ID" "$BAD" "$WALLET"

discarded() { [[ "$(metric 'wagering_queue_discarded_total{reason="unusable"}')" != "$BEFORE" ]]; }
if wait_for 90 discarded; then
  pass 'the unusable envelope was discarded, with a reason'
else
  fail 'the discard counter never moved'
fi

step "Both ports are counted, and told apart"
note "http:  $(metric 'wagering_operations_total{kind="BET",source="http",status="PROCESSED"}')"
note "queue: $(metric 'wagering_operations_total{kind="BET",source="queue",status="PROCESSED"}')"

step "The wallet still agrees with its ledger"
http POST "/wallets/$WALLET/reconciliation" "$(token platform)"
expect_json '.consistent' 'true' 'it reconciles'
