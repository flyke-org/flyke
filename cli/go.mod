module github.com/flyke-org/flyke/cli

go 1.26.0

require (
	github.com/flyke-org/flyke/api v0.0.0
	github.com/flyke-org/flyke/flyke v0.0.0
	github.com/flyke-org/flyke/ui v0.0.0-00010101000000-000000000000
	github.com/spf13/cobra v1.10.2
	golang.org/x/term v0.38.0
)

require (
	connectrpc.com/connect v1.21.1 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.62.0 // indirect
)

replace (
	github.com/flyke-org/flyke/api => ../api
	github.com/flyke-org/flyke/flyke => ../flyke
	github.com/flyke-org/flyke/ui => ../ui
)
