#!/usr/bin/env bash
# What a caller cannot do, and what it learns by trying.
#
# The realm has two providers so isolation can be tried rather than asserted:
# rival must not reach anything acme submitted, and must not be able to tell
# whether there was anything to reach.
#
#   make simulate SCENARIO=security

source "$(dirname "$0")/lib.sh"

ACME="$(token provider-acme)"
RIVAL="$(token provider-rival)"
PLATFORM="$(token platform)"

PLAYER="player-sec-$RUN_ID"
WALLET="$(open_wallet "$PLAYER" '100.00')"
note "wallet $WALLET"

step "No credential at all"
# Registered routes only. An unregistered method on a known path answers 405
# from the router before any handler runs -- which is correct, and says nothing
# except which methods that path has.
for path in "/wallets/$WALLET" "/wallets/$WALLET/ledger" "/wagering/transactions/any-id"; do
  http GET "$path" ''
  expect_status 401 "GET $path without a token"
done

http POST /wallets '' '{}'
expect_status 401 'POST /wallets without a token'

http POST /wagering/transactions '' '{}' 'Idempotency-Key: none'
expect_status 401 'POST /wagering/transactions without a token'
expect_json '.code' 'UNAUTHENTICATED' 'and says only that'

http POST "/wallets/$WALLET/reconciliation" ''
expect_status 401 'POST a reconciliation without a token'

step "The challenge, and nothing else"
# Expired, signed by an unknown key and minted for another audience are all the
# same answer out here. Telling them apart says which part of a forgery to fix.
CHALLENGE="$(curl -sSI -X POST "$API_URL/wallets" | tr -d '\r' | grep -i '^www-authenticate:' || true)"
if [[ "$CHALLENGE" == *'Bearer realm="wagering-core"'* ]]; then
  pass 'the 401 carries the RFC 6750 challenge'
else
  fail "the 401 has no usable challenge: ${CHALLENGE:-<none>}"
fi

step "A token that nobody signed"
http GET "/wallets/$WALLET" 'not.a.token'
expect_status 401 'a made-up token'

step "acme places a bet"
submit "$ACME" "$(operation acme "sec-$RUN_ID" "$PLAYER" "$WALLET" BET '25.00')"
expect_status 200 'accepted'
BET_ID="$(printf '%s' "$HTTP_BODY" | jq -r .transactionId)"

step "rival cannot submit as acme"
submit "$RIVAL" "$(operation acme "sec-$RUN_ID" "$PLAYER" "$WALLET" BET '25.00')"
expect_status 403 'refused'
expect_json '.code' 'FORBIDDEN' 'the code'
# The message names rival's own provider, never the one it asked for. Echoing
# that back would answer "does acme exist?" for the price of one wrong request.
if [[ "$(printf '%s' "$HTTP_BODY" | jq -r .message)" == *acme* ]]; then
  fail 'the refusal echoes the provider that was asked for'
else
  pass 'the refusal names only the caller'
fi

step "rival cannot read what acme submitted"
http GET "/wagering/transactions/$BET_ID" "$RIVAL"
expect_status 404 "by acme's transaction id"
HIDDEN="$HTTP_BODY"

http GET "/wagering/transactions/no-such-transaction-$RUN_ID" "$RIVAL"
expect_status 404 'and one that never existed'
# The same answer, not a similar one. A 403 here -- or a differently worded
# 404 -- would confirm that there is something to refuse.
expect_equal "$(printf '%s' "$HIDDEN" | jq -r .code)" \
             "$(printf '%s' "$HTTP_BODY" | jq -r .code)" 'the two answers are the same'

http GET "/providers/acme/wagering/transactions/sec-$RUN_ID" "$RIVAL"
expect_status 404 "by acme's own business id"

step "and the owner still reads it"
http GET "/wagering/transactions/$BET_ID" "$ACME"
expect_status 200 'acme reads its own operation'

step "A provider cannot touch a wallet"
# Opening one mints the initial balance: it is the only operation here that
# creates money rather than moving it.
http POST /wallets "$ACME" \
  "{\"playerId\":\"p2-$RUN_ID\",\"initialBalance\":{\"amount\":\"1.00\",\"currency\":\"BRL\"}}"
expect_status 403 'opening a wallet'

http GET "/wallets/$WALLET" "$ACME"
expect_status 403 'reading a wallet'

http GET "/wallets/$WALLET/ledger" "$ACME"
expect_status 403 'reading a ledger'

http POST "/wallets/$WALLET/reconciliation" "$ACME"
expect_status 403 'running a reconciliation'

step "and the platform can"
http GET "/wallets/$WALLET" "$PLATFORM"
expect_status 200 'the internal credential reads the wallet'

step "Health answers without a credential"
# A load balancer has no token, and the answer is one word about the process.
http GET /health/live ''
expect_status 200 'liveness'
http GET /health/ready ''
expect_status 200 'readiness'
