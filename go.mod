module github.com/outofforest/osman

go 1.23

// rsc.io is unreliable, take it from github.
replace (
	rsc.io/binaryregexp => github.com/rsc/binaryregexp v0.2.0
	rsc.io/quote/v3 => github.com/rsc/quote/v3 v3.1.0
	rsc.io/sampler => github.com/rsc/sampler v1.3.1
)

require (
	github.com/beevik/etree v1.5.0
	github.com/digitalocean/go-libvirt v0.0.0-20221205150000-2939327a8519
	github.com/google/nftables v0.2.0
	github.com/google/uuid v1.6.0
	github.com/outofforest/go-zfs/v3 v3.1.14
	github.com/outofforest/ioc/v2 v2.5.2
	github.com/outofforest/isolator v0.12.1
	github.com/outofforest/logger v0.5.5
	github.com/outofforest/parallel v0.2.3
	github.com/outofforest/run v0.8.0
	github.com/pkg/errors v0.9.1
	github.com/ridge/must v0.6.0
	github.com/spf13/cobra v1.8.1
	github.com/vishvananda/netlink v1.3.0
	golang.org/x/sys v0.28.0
	libvirt.org/go/libvirtxml v1.10009.0
)

require (
	github.com/google/go-cmp v0.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/josharian/native v1.1.0 // indirect
	github.com/mdlayher/netlink v1.7.2 // indirect
	github.com/mdlayher/socket v0.5.0 // indirect
	github.com/outofforest/libexec v0.3.9 // indirect
	github.com/samber/lo v1.47.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/vishvananda/netns v0.0.4 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	golang.org/x/net v0.23.0 // indirect
	golang.org/x/sync v0.8.0 // indirect
	golang.org/x/text v0.18.0 // indirect
)
