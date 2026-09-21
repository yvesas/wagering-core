#!/usr/bin/env bash
# What happens when the same thing is sent twice, and when two different things
# are sent under one key.
#
# This is the scenario worth reading if you are integrating: a retry after a
# timeout is normal, and the answer it gets has to be the one the first attempt
# produced -- not a second bet.
#
#   make simulate SCENARIO=idempotency

source "$(dirname "$0")/lib.sh"

ACME="$(token provider-acme)"
PLAYER="player-idem-$RUN_ID"
WALLET="$(open_wallet "$PLAYER" '100.00')"
note "wallet $WALLET"

BET="$(operation acme "bet-$RUN_ID" "$PLAYER" "$WALLET" BET '25.00')"

step "Send the bet"
submit "$ACME" "$BET"
expect_status 200 'accepted'
expect_json '.idempotentReplay' 'false' 'not a replay'
FIRST_ID="$(printf '%s' "$HTTP_BODY" | jq -r .transactionId)"

step "Send it again, byte for byte -- the client timed out and retried"
submit "$ACME" "$BET"
expect_status 200 'accepted again'
expect_json '.idempotentReplay' 'true' 'reported as a replay'
expect_json '.transactionId' "$FIRST_ID" 'the same operation, not a new one'
expect_equal "$(balance_of "$WALLET")" '75.00' 'the balance moved once'

step "Move the balance, then replay again"
submit "$ACME" "$(operation acme "win-$RUN_ID" "$PLAYER" "$WALLET" WIN '10.00')"
expect_status 200 'a win lands in between'
expect_equal "$(balance_of "$WALLET")" '85.00' 'the balance now'

submit "$ACME" "$BET"
# The replay answers with the balance the original processing observed, not the
# one the wallet holds now. Answering 85.00 here would tell the provider its
# bet had just been applied again.
expect_json '.balance.amount' '75.00' 'the replay answers the original balance'
expect_equal "$(balance_of "$WALLET")" '85.00' 'and the wallet did not move'

step "The same key, different content"
submit "$ACME" "$(operation acme "bet-$RUN_ID" "$PLAYER" "$WALLET" BET '99.00')" \
  "acme:bet-$RUN_ID"
expect_status 409 'refused'
# The generic conflict, not a named constraint: this one never reaches the
# database. The use case finds the stored operation, sees a different payload
# hash and refuses -- the uniqueness index would have said the same thing, one
# round trip later.
expect_json '.code' 'CONFLICT' 'and says it conflicts'

step "The same operation under a different key"
# The pair (provider, externalTransactionId) identifies the operation. A new
# key does not make it a new bet, and accepting it would be the easiest double
# debit there is.
submit "$ACME" "$BET" "acme:a-brand-new-key-$RUN_ID"
expect_status 409 'refused'
expect_equal "$(balance_of "$WALLET")" '85.00' 'still one debit'

step "A rejection is a stored outcome, and replays as one"
submit "$ACME" "$(operation acme "toobig-$RUN_ID" "$PLAYER" "$WALLET" BET '500.00')"
expect_status 422 'refused for insufficient funds'
expect_json '.status' 'REJECTED' 'the status is recorded'
expect_json '.failureCode' 'INSUFFICIENT_FUNDS' 'with its code'

submit "$ACME" "$(operation acme "toobig-$RUN_ID" "$PLAYER" "$WALLET" BET '500.00')"
# The stored refusal is the answer. A resend that suddenly returned 200 would
# tell the provider its bet went through.
expect_status 422 'the replay is refused the same way'
expect_json '.idempotentReplay' 'true' 'and knows it is a replay'
