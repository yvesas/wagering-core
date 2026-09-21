# A client, with the two tools a client needs.
#
# It is not the service and shares nothing with it: no Go, no binary, no source.
# What it has is curl, jq and a shell -- which is exactly what the scenarios in
# scripts/simulate are written against, so that they stay readable as examples
# of how a provider integrates.
FROM alpine:3.21

# psql is here for one scenario, and it earns its place: a drift between the
# balance and the ledger can only be produced by writing behind the domain's
# back, because every path through the real code writes both in one commit.
# Without it, the reconciliation scenario could only show the happy answer.
RUN apk add --no-cache bash curl jq postgresql17-client

WORKDIR /scenarios

# The scenarios arrive as a mounted volume, so editing one and running it again
# costs nothing. Baking them in would mean a rebuild between every attempt,
# which is the opposite of what a simulator is for.
ENTRYPOINT ["/bin/bash"]
