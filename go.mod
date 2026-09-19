module github.com/pacenote-sim/client-engineer

go 1.26.1

// The contract and the protocol are required by version: a checkout of this
// repository alone builds against the tagged releases.

require (
	github.com/pacenote-sim/clientplugin v0.1.0
	github.com/pacenote-sim/protocol v0.2.0
	github.com/stretchr/testify v1.12.1
)

require go.yaml.in/yaml/v3 v3.0.5 // indirect
