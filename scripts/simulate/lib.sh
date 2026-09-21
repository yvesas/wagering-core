#!/usr/bin/env bash
# Shared plumbing for the scenarios.
#
# These scripts are two things at once: a way to exercise a running system, and
# a readable example of how a provider integrates. The second is why everything
# here is curl and jq rather than a client library -- there is nothing to
# install and nothing to believe, only the HTTP a provider would send.
#
# Source it, do not run it.

set -euo pipefail

API_URL="${API_URL:-http://api:8080}"
METRICS_URL="${METRICS_URL:-http://api:9090}"
OIDC_URL="${OIDC_URL:-http://keycloak:8081/realms/wagering}"
CLIENT_SECRET="${CLIENT_SECRET:-local-dev-only}"
QUEUE_ENDPOINT="${QUEUE_ENDPOINT:-http://localstack:4566}"
QUEUE_NAME="${QUEUE_NAME:-wager-transactions.fifo}"

DB_HOST="${DB_HOST:-postgres}"
DB_NAME="${DB_NAME:-wagering}"
DB_USER="${DB_USER:-wagering}"
export PGPASSWORD="${DB_PASSWORD:-local-dev-only}"

# A suffix unique to this run. Every identifier these scenarios mint carries
# it, so the same scenario can be run again without colliding with what the
# last run left behind -- the ledger is append-only and nothing is ever cleaned
# up, which is the point.
RUN_ID="$(date +%H%M%S)-$RANDOM"

# --- reporting -------------------------------------------------------------

CHECKS_PASSED=0
CHECKS_FAILED=0

# The report.
#
# What a run prints is gone when the terminal scrolls, and comparing two runs
# needs something that outlived both. Markdown rather than JSON because the
# reader is a person: it pastes into a pull request, and a failure is legible
# without a tool.
#
# REPORT_BODY and REPORT_SUMMARY are set by all.sh when it drives several
# scenarios into one report. A scenario run on its own makes its own.
source "$(dirname "${BASH_SOURCE[0]}")/report.sh"

SCENARIO_NAME="$(basename "${BASH_SOURCE[1]:-scenario}" .sh)"
REPORT_STARTED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

REPORT_OWNED=0
if [[ -z "${REPORT_BODY:-}" ]]; then
  REPORT_OWNED=1
  REPORT_BODY="$(mktemp)"
  REPORT_SUMMARY="$(mktemp)"
fi
printf '\n## %s\n' "$SCENARIO_NAME" >> "$REPORT_BODY"

# record writes one line to the report. It is separate from printing so the
# terminal can keep its colours and the file can stay plain.
record() { printf '%s\n' "$*" >> "$REPORT_BODY"; }

if [[ -t 1 ]]; then
  GREEN=$'\e[32m'; RED=$'\e[31m'; DIM=$'\e[2m'; BOLD=$'\e[1m'; RESET=$'\e[0m'
else
  GREEN=''; RED=''; DIM=''; BOLD=''; RESET=''
fi

step() {
  printf '\n%s== %s ==%s\n' "$BOLD" "$*" "$RESET"
  record ""
  record "**${*}**"
  record ""
}

note() {
  printf '%s   %s%s\n' "$DIM" "$*" "$RESET"
  record "- _${*}_"
}

pass() {
  CHECKS_PASSED=$((CHECKS_PASSED + 1))
  printf '%s   ok%s  %s\n' "$GREEN" "$RESET" "$*"
  record "- ok — $*"
}

fail() {
  CHECKS_FAILED=$((CHECKS_FAILED + 1))
  printf '%s  NOT%s  %s\n' "$RED" "$RESET" "$*"
  record "- **NOT** — $*"
}

# summary runs on exit, whichever way the script leaves, and decides the exit
# code. A scenario that fell over halfway must not report success because the
# last line it reached happened to be a passing check.
summary() {
  local code=$?
  printf '\n%s---%s %d passed, %d failed\n' "$BOLD" "$RESET" "$CHECKS_PASSED" "$CHECKS_FAILED"
  printf '| %s | %d | %d |\n' "$SCENARIO_NAME" "$CHECKS_PASSED" "$CHECKS_FAILED" >> "$REPORT_SUMMARY"

  # Only a scenario running on its own finishes the report; when all.sh is
  # driving, it owns the file and writes it once at the end.
  if (( REPORT_OWNED )) && reportable; then
    local path
    path="$(report_path "$SCENARIO_NAME")"
    write_report "$path"
    printf '%s   report: %s%s\n' "$DIM" "$path" "$RESET"
  fi

  if (( CHECKS_FAILED > 0 )); then
    exit 1
  fi
  exit "$code"
}
trap summary EXIT

# --- assertions ------------------------------------------------------------

# expect_status <wanted> <what happened>
expect_status() {
  local want=$1 what=$2
  if [[ "$HTTP_STATUS" == "$want" ]]; then
    pass "$what -> $want"
  else
    fail "$what -> $HTTP_STATUS, wanted $want"
    note "$(printf '%s' "$HTTP_BODY" | head -c 400)"
  fi
}

# expect_json <jq filter> <wanted> <what>
expect_json() {
  local filter=$1 want=$2 what=$3
  local got
  got="$(printf '%s' "$HTTP_BODY" | jq -r "$filter" 2>/dev/null || echo '<unparseable>')"
  if [[ "$got" == "$want" ]]; then
    pass "$what -> $want"
  else
    fail "$what -> $got, wanted $want"
  fi
}

expect_equal() {
  local got=$1 want=$2 what=$3
  if [[ "$got" == "$want" ]]; then
    pass "$what -> $want"
  else
    fail "$what -> $got, wanted $want"
  fi
}

# --- credentials -----------------------------------------------------------

# token <client-id> mints an access token through the client credentials grant,
# which is the flow a machine uses: there is no user, no browser and no
# redirect. The realm has three clients -- see docs/security.md.
token() {
  local client=$1 response
  response="$(curl -sS -X POST "$OIDC_URL/protocol/openid-connect/token" \
    -d grant_type=client_credentials \
    -d "client_id=$client" \
    -d "client_secret=$CLIENT_SECRET")"

  local minted
  minted="$(printf '%s' "$response" | jq -r '.access_token // empty')"
  if [[ -z "$minted" ]]; then
    printf 'could not mint a token for %s: %s\n' "$client" "$response" >&2
    exit 1
  fi
  printf '%s' "$minted"
}

# --- http ------------------------------------------------------------------

# http <method> <path> <token> [body] [extra headers...]
#
# Leaves the answer in HTTP_STATUS and HTTP_BODY rather than printing it, so a
# scenario can assert on both without parsing its own output. An empty token
# sends no Authorization header at all, which is how the unauthenticated cases
# are written.
http() {
  local method=$1 path=$2 tok=${3:-} body=${4:-}
  shift 4 2>/dev/null || shift $#

  local args=(-sS -X "$method" -w $'\n%{http_code}')
  [[ -n "$tok" ]] && args+=(-H "Authorization: Bearer $tok")
  [[ -n "$body" ]] && args+=(-H 'Content-Type: application/json' --data "$body")
  local header
  for header in "$@"; do args+=(-H "$header"); done

  local out
  out="$(curl "${args[@]}" "$API_URL$path")"
  HTTP_STATUS="${out##*$'\n'}"
  HTTP_BODY="${out%$'\n'*}"
}

# --- domain shortcuts ------------------------------------------------------

# open_wallet <player> <amount> echoes the new wallet id. Opening mints the
# initial balance, so it is the internal service's alone.
open_wallet() {
  local player=$1 amount=$2
  http POST /wallets "$(token platform)" \
    "{\"playerId\":\"$player\",\"initialBalance\":{\"amount\":\"$amount\",\"currency\":\"BRL\"}}"

  if [[ "$HTTP_STATUS" != "201" ]]; then
    printf 'could not open a wallet: %s %s\n' "$HTTP_STATUS" "$HTTP_BODY" >&2
    exit 1
  fi
  printf '%s' "$HTTP_BODY" | jq -r .id
}

# operation builds the body a provider sends.
operation() {
  local provider=$1 external=$2 player=$3 wallet=$4 kind=$5 amount=$6 reference=${7:-}
  local body
  body="$(jq -nc \
    --arg p "$provider" --arg e "$external" --arg pl "$player" \
    --arg w "$wallet" --arg k "$kind" --arg a "$amount" \
    --arg r "$RUN_ID" \
    '{providerId:$p, externalTransactionId:$e, playerId:$pl, walletId:$w,
      roundId:("round-"+$r), gameId:"fortune-chimp", kind:$k,
      money:{amount:$a, currency:"BRL"}}')"

  if [[ -n "$reference" ]]; then
    body="$(printf '%s' "$body" | jq -c --arg ref "$reference" \
      '. + {referenceExternalTransactionId:$ref}')"
  fi
  printf '%s' "$body"
}

# submit sends an operation. The idempotency key is the provider's own, and the
# server never invents one: see docs/api.md.
submit() {
  local tok=$1 body=$2 key=${3:-}
  if [[ -z "$key" ]]; then
    key="$(printf '%s' "$body" | jq -r '.providerId + ":" + .externalTransactionId')"
  fi
  http POST /wagering/transactions "$tok" "$body" "Idempotency-Key: $key"
}

# balance_of echoes a wallet's stored balance.
balance_of() {
  local wallet=$1
  http GET "/wallets/$wallet" "$(token platform)"
  printf '%s' "$HTTP_BODY" | jq -r '.balance.amount'
}

# --- the queue -------------------------------------------------------------

# The emulator does not verify signatures, so a dummy Authorization header is
# enough and no AWS client has to be installed. A real broker would refuse
# this, which is the point of REQ-SEC-005: what guards the queue is the
# broker's own access policy, not anything in the envelope.
sqs() {
  local target=$1 payload=$2
  curl -sS -X POST "$QUEUE_ENDPOINT/" \
    -H 'Content-Type: application/x-amz-json-1.0' \
    -H "X-Amz-Target: AmazonSQS.$target" \
    -H 'Authorization: AWS4-HMAC-SHA256 Credential=test/20260101/us-east-1/sqs/aws4_request, SignedHeaders=host, Signature=simulated' \
    --data "$payload"
}

queue_url() {
  printf '%s/000000000000/%s' "$QUEUE_ENDPOINT" "$QUEUE_NAME"
}

# send_envelope puts one operation on the queue, in the shape the producer
# sends. The idempotency key rides in the body here, where HTTP puts it in a
# header -- that is the only difference between the two ports.
# The broker's own deduplication id is deliberately unique per call, even when
# the messageId repeats. FIFO drops a repeated deduplication id inside a
# five-minute window, so reusing it would mean the redelivery never reaches us
# -- and a scenario about the inbox absorbing duplicates would be testing SQS.
send_envelope() {
  local message_id=$1 body=$2 group=$3
  local envelope
  envelope="$(jq -nc --arg id "$message_id" --argjson data "$body" \
    --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{messageId:$id, type:"WagerTransactionRequested", occurredAt:$at,
      data:($data + {idempotencyKey:($data.providerId + ":" + $data.externalTransactionId)})}')"

  sqs SendMessage "$(jq -nc \
    --arg q "$(queue_url)" --arg b "$envelope" --arg g "$group" \
    --arg d "$message_id-$RANDOM-$SECONDS" \
    '{QueueUrl:$q, MessageBody:$b, MessageGroupId:$g, MessageDeduplicationId:$d}')" >/dev/null
}

# --- waiting ---------------------------------------------------------------

# wait_for runs a command until it succeeds or the deadline passes. Polling,
# not sleeping: the queue path is asynchronous and a fixed sleep is either a
# slow scenario or a flaky one.
wait_for() {
  local seconds=$1; shift
  local deadline=$(( SECONDS + seconds ))
  while (( SECONDS < deadline )); do
    if "$@"; then return 0; fi
    sleep 0.2
  done
  return 1
}

metric() {
  local series=$1
  curl -sS "$METRICS_URL/metrics" | awk -v s="$series" 'index($0, s) == 1 { print $NF; exit }'
}
