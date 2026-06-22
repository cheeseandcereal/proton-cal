//go:build integration

// Package integration is the opt-in live integration test suite: it
// exercises the real Proton Calendar API end to end (create, decrypt,
// update, recur, delete) against a dedicated test calendar.
//
// The package is excluded from normal builds and `go test ./...` by the
// "integration" build tag; run it via `make integration`. See README.md in
// this directory for prerequisites and safety notes.
package integration
