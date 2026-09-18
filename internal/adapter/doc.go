// Package adapter groups the implementations of the ports declared in
// internal/app: inbound HTTP and queue consumption, outbound PostgreSQL and the
// event destination.
//
// The edge is where an external payload becomes a domain type, and that happens
// in exactly one layer. Past it, no code re-parses a string or re-validates a
// format.
package adapter
