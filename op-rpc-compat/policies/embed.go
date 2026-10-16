// Package policies embeds the reviewed RPC difference registry.
package policies

import "embed"

//go:embed accepted/registry.json
var FS embed.FS
