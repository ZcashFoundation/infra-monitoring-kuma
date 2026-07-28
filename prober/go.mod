module github.com/ZcashFoundation/infra-monitoring-kuma/prober

go 1.21

require (
	github.com/miekg/dns v1.1.58
	github.com/zcashfoundation/dnsseeder v0.5.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/btcsuite/btcd v0.22.0-beta // indirect
	github.com/btcsuite/btclog v0.0.0-20170628155309-84c8d2346e9f // indirect
	github.com/btcsuite/btcutil v1.0.3-0.20201208143702-a53e38424cce // indirect
	github.com/btcsuite/go-socks v0.0.0-20170105172521-4720035b7bfd // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/decred/dcrd/lru v1.0.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	golang.org/x/crypto v0.18.0 // indirect
	golang.org/x/mod v0.14.0 // indirect
	golang.org/x/net v0.20.0 // indirect
	golang.org/x/sys v0.16.0 // indirect
	golang.org/x/tools v0.17.0 // indirect
)

// dnsseeder's Zcash p2p handshake depends on a Zcash fork of btcd. We must
// mirror its replace directive or the version/verack would speak Bitcoin, not
// Zcash. Keep in sync with dnsseeder/go.mod.
replace github.com/btcsuite/btcd => github.com/ZcashFoundation/btcd v0.22.0-beta.0.20220607000607-40dc9492aa42
