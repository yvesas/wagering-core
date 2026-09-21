# The service, built and run.
#
# Two stages: the toolchain never reaches the image that runs in production.
# A Go toolchain is ~300MB of compiler and source that an attacker with a shell
# would be delighted to find.

# --- build -----------------------------------------------------------------

FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first, in their own layer. They change far less often than the
# code, so an ordinary edit reuses this layer instead of downloading the module
# graph again.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 is what makes the binary static, and static is what lets the
# final stage be a base image with no libc to match. Everything this service
# links -- pgx, the AWS SDK, the JWT library -- is pure Go, so nothing is lost.
#
# -trimpath keeps the build machine's paths out of the binary; without it, a
# panic prints someone's home directory. -s -w drop the symbol table and DWARF,
# which is most of the size and nothing the service uses at run time.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/api ./cmd/api

# --- run -------------------------------------------------------------------

FROM alpine:3.21

# Alpine rather than distroless or scratch, and it is a trade.
#
# Distroless would be smaller and carry less surface. What it does not carry is
# a shell -- so the compose healthcheck would need a binary written for it, and
# nobody could open a shell in a container that is misbehaving. For a service
# meant to be read and run by hand, being able to look inside is worth the
# difference. A deployment that cares more about surface than about inspection
# should change this line; nothing above it has to change.
#
# ca-certificates is not optional: the identity provider and AWS are reached
# over TLS, and without the root store every one of those calls fails with an
# error that reads like a bug in our code.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 -h /app wagering

WORKDIR /app
COPY --from=build /out/api /app/api

# Not root. Nothing this process does needs it, and the ports it binds are all
# above 1024.
USER wagering

EXPOSE 8080 9090

# The readiness endpoint, through busybox's own wget -- no extra package, and
# no shell pipeline to get wrong. It checks the port the service actually
# serves, which is the only definition of "up" worth having.
#
# start-period covers the migrations, which run at start-up from every replica.
HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=5 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/health/ready || exit 1

ENTRYPOINT ["/app/api"]
