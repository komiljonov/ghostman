module github.com/komiljonov/ghostman

// The language baseline this project targets is Go 1.22 (net/http ServeMux
// method+path patterns). The floor below is dictated by the dependencies:
// goose v3.27 requires 1.25.7 and pgx v5.10 requires 1.25.0. With the default
// GOTOOLCHAIN=auto, older local installs fetch a matching toolchain on demand.
go 1.25.7

require (
	github.com/caarlos0/env/v11 v11.4.1
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.10.0
	github.com/pressly/goose/v3 v3.27.3
	golang.org/x/crypto v0.55.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/sethvargo/go-retry v0.4.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)
