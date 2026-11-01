module kogge.de/kogge

go 1.26.0

toolchain go1.27.3

require (
	codeberg.org/go-pdf/fpdf v0.12.0
	github.com/alecthomas/chroma/v2 v2.27.0
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/danielgtaylor/huma/v2 v2.39.1
	github.com/go-chi/chi/v5 v5.3.2
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/jackc/pgx/v5 v5.11.0
	github.com/pressly/goose/v3 v3.28.0
	github.com/prometheus/client_golang v1.24.1
	github.com/riverqueue/river v0.49.0
	github.com/riverqueue/river/riverdriver/riverpgxv5 v0.49.0
	github.com/riverqueue/river/rivertype v0.49.0
	github.com/santhosh-tekuri/jsonschema/v5 v5.3.1
	github.com/yuin/goldmark v1.8.6
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/crypto v0.57.0
	golang.org/x/image v0.47.0
	golang.org/x/net v0.60.0
	golang.org/x/oauth2 v0.37.0
	golang.org/x/sync v0.24.0
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.43.0
	golang.org/x/time v0.16.0
	rsc.io/pdf v0.1.1
	rsc.io/qr v0.2.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dlclark/regexp2/v2 v2.2.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/mfridman/interpolate v0.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/riverqueue/river/riverdriver v0.49.0 // indirect
	github.com/riverqueue/river/rivershared v0.49.0 // indirect
	github.com/sethvargo/go-retry v0.4.0 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.2.0 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

// Node packages may contain Go files; they are not part of Kogge.
ignore (
	./e2e/node_modules
	./web/node_modules
)
