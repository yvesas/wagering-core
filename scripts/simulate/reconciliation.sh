#!/usr/bin/env bash
# The second opinion, and what it looks like when it disagrees.
#
# Producing a drift takes writing to the database behind the domain's back,
# because every path through the real code writes the balance and the ledger
# entry in one commit. That is the point: the check exists for the day
# something we did not foresee does exactly this.
#
#   make simulate SCENARIO=reconciliation

source "$(dirname "$0")/lib.sh"

command -v psql >/dev/null || { echo 'this scenario needs psql' >&2; exit 1; }

ACME="$(token provider-acme)"
PLATFORM="$(token platform)"
PLAYER="player-rec-$RUN_ID"
WALLET="$(open_wallet "$PLAYER" '100.00')"
note "wallet $WALLET"

submit "$ACME" "$(operation acme "rec-$RUN_ID" "$PLAYER" "$WALLET" BET '25.00')" >/dev/null
note 'one bet of 25.00'

step "A healthy wallet"
http POST "/wallets/$WALLET/reconciliation" "$PLATFORM"
expect_status 200 'the check runs'
expect_json '.consistent' 'true' 'and agrees'
expect_json '.storedBalance.amount' '75.00' 'the stored balance'
expect_json '.rebuiltBalance.amount' '75.00' 'rebuilt from the ledger'
expect_json '.difference.amount' '0.00' 'no difference'
# The opening credit and the bet. The opening is an ordinary ledger entry,
# which is why "including the opening" needs no special case.
expect_json '.entries' '2' 'two entries'

step "Now break it, the only way it can be broken"
psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -q -c \
  "UPDATE wallets SET balance_minor = 4000 WHERE id = '$WALLET'"
note 'balance set to 40.00 with the ledger untouched'

DRIFTS_BEFORE="$(metric 'wagering_reconciliations_total{result="drift"}')"

http POST "/wallets/$WALLET/reconciliation" "$PLATFORM"
# 200, not 409. The caller asked whether the two agree, and answering "they do
# not" is this endpoint succeeding -- a non-2xx would make every monitor treat
# a working check as a broken request.
expect_status 200 'the check still succeeds'
expect_json '.consistent' 'false' 'and disagrees'
expect_json '.storedBalance.amount' '40.00' 'what the wallet says'
expect_json '.rebuiltBalance.amount' '75.00' 'what the ledger says'
# The sign is the first thing an investigation asks: money missing, or money
# invented.
expect_json '.difference.amount' '-35.00' 'and by how much, with the sign'

step "It is reported in three places, not one"
expect_equal "$(metric 'wagering_reconciliations_total{result="drift"}')" \
             "$(awk -v n="$DRIFTS_BEFORE" 'BEGIN{print n+1}')" 'the drift counter moved'
note 'the third is a log line at ERROR -- look for "wallet balance does not match its ledger"'
note "  docker compose logs api | grep '$WALLET'"

step "Nothing was corrected"
# A reconciliation that fixed what it found would destroy the evidence of how
# the two came apart. A correction is a new ledger entry, raised by a person.
expect_equal "$(balance_of "$WALLET")" '40.00' 'the balance is still wrong'
http GET "/wallets/$WALLET/ledger" "$PLATFORM"
expect_json '.entries | length' '2' 'and the ledger is still untouched'

step "Put it back"
psql -h "$DB_HOST" -U "$DB_USER" -d "$DB_NAME" -q -c \
  "UPDATE wallets SET balance_minor = 7500 WHERE id = '$WALLET'"
http POST "/wallets/$WALLET/reconciliation" "$PLATFORM"
expect_json '.consistent' 'true' 'it reconciles again'
