#!/usr/bin/env bash
# Giving money back, including when the reversal arrives first.
#
#   make simulate SCENARIO=reversals

source "$(dirname "$0")/lib.sh"

ACME="$(token provider-acme)"
PLAYER="player-rev-$RUN_ID"
WALLET="$(open_wallet "$PLAYER" '100.00')"
note "wallet $WALLET"

step "A bet, then a refund of it"
submit "$ACME" "$(operation acme "rb-$RUN_ID" "$PLAYER" "$WALLET" BET '25.00')"
expect_status 200 'the bet'
expect_equal "$(balance_of "$WALLET")" '75.00' 'the balance after it'

# The direction is derived from what is being reversed, never sent by the
# client: a table of directions maintained by hand would be a second opinion
# about what a bet does.
submit "$ACME" "$(operation acme "rf-$RUN_ID" "$PLAYER" "$WALLET" REFUND '25.00' "rb-$RUN_ID")"
expect_status 200 'the refund'
expect_equal "$(balance_of "$WALLET")" '100.00' 'the money came back'

step "The same thing cannot be reversed twice"
# One reversal per operation, of any kind. "Not two of the same kind" would let
# a REFUND and a ROLLBACK hand back the same money twice.
submit "$ACME" "$(operation acme "rf2-$RUN_ID" "$PLAYER" "$WALLET" ROLLBACK '25.00' "rb-$RUN_ID")"
expect_status 422 'refused'
expect_json '.failureCode' 'ALREADY_REVERSED' 'and says why'
expect_equal "$(balance_of "$WALLET")" '100.00' 'the balance is untouched'

step "A reversal whose amount does not match"
submit "$ACME" "$(operation acme "rb2-$RUN_ID" "$PLAYER" "$WALLET" BET '30.00')"
expect_status 200 'a second bet'
submit "$ACME" "$(operation acme "rf3-$RUN_ID" "$PLAYER" "$WALLET" REFUND '10.00' "rb2-$RUN_ID")"
# Partial reversals are out of scope, so an amount that differs is a mistake
# rather than a partial refund.
expect_status 422 'refused'
expect_json '.failureCode' 'REFERENCE_AMOUNT_MISMATCH' 'and says which way'

step "A reversal that arrives before what it reverses"
# Out-of-order delivery is normal on a queue. The reversal is parked with its
# own schedule and deadline, and a worker takes it from there -- 202, because
# nothing has moved yet and it still might.
submit "$ACME" "$(operation acme "early-rf-$RUN_ID" "$PLAYER" "$WALLET" REFUND '15.00' "late-bet-$RUN_ID")"
expect_status 202 'accepted and parked'
expect_json '.status' 'PENDING_REFERENCE' 'waiting for its reference'
BEFORE="$(balance_of "$WALLET")"

step "and the bet it was waiting for turns up"
submit "$ACME" "$(operation acme "late-bet-$RUN_ID" "$PLAYER" "$WALLET" BET '15.00')"
expect_status 200 'the late bet lands'

resolved() {
  http GET "/providers/acme/wagering/transactions/early-rf-$RUN_ID" "$ACME"
  [[ "$(printf '%s' "$HTTP_BODY" | jq -r .status)" == 'PROCESSED' ]]
}
if wait_for 60 resolved; then
  pass 'the parked refund resolved itself'
else
  fail "the refund is still $(printf '%s' "$HTTP_BODY" | jq -r .status)"
fi

# The bet took 15.00 and the refund gave it back, so the balance ends where it
# started -- which is the only way to tell the two really paired up.
expect_equal "$(balance_of "$WALLET")" "$BEFORE" 'the pair cancelled out'

step "Everything still adds up"
http POST "/wallets/$WALLET/reconciliation" "$(token platform)"
expect_json '.consistent' 'true' 'the wallet reconciles'
note "entries: $(printf '%s' "$HTTP_BODY" | jq -r .entries)"
