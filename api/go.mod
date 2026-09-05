module velib-agent

go 1.26.5

// ⚠️ La directive « go » ci-dessus est la version MINIMALE du langage. Elle ne
// dit rien de la chaîne d'outils qui compile, et c'est ce qui a produit un écart
// a trois versions : go.mod disait 1.26.5, le Dockerfile construisait en 1.27,
// et la CI — qui lit ce fichier — se retrouvait sur 1.26.5.
//
// Or Go 1.26.5 porte sept vulnerabilites de bibliotheque standard, corrigees en
// 1.26.6. La CI les signalait alors que l'image livree, construite en 1.27, n'en
// avait aucune. Le scan disait vrai, il ne parlait simplement pas de la meme
// chose que ce qu'on livre.
//
// « toolchain » fait de ce fichier la source unique : setup-go la respecte, et
// le Dockerfile s'aligne dessus. Un test verifie que les deux restent d'accord.
toolchain go1.27.1

require (
	github.com/openai/openai-go v1.12.0
	golang.org/x/text v0.41.0
	trpc.group/trpc-go/trpc-agent-go/session/postgres v1.11.0
)

require (
	github.com/bmatcuk/doublestar/v4 v4.9.1 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.22.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.9.2 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/panjf2000/ants/v2 v2.10.0 // indirect
	github.com/tidwall/gjson v1.14.4 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.29.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.29.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.29.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/sdk v1.43.0 // indirect
	go.opentelemetry.io/proto/otlp v1.3.1 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260414002931-afd174a4e478 // indirect
	google.golang.org/grpc v1.82.1 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	trpc.group/trpc-go/trpc-agent-go/storage/postgres v0.8.0 // indirect
)

require (
	github.com/google/uuid v1.6.0
	go.opentelemetry.io/otel v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	trpc.group/trpc-go/trpc-a2a-go v0.2.6-0.20260721084546-18c8244d0acb // indirect
	trpc.group/trpc-go/trpc-agent-go v1.11.2
)
