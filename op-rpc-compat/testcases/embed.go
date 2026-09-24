package testcases

import "embed"

// FS contains the default RPC testcase corpus and known differences.
//
//go:embed *.json
var FS embed.FS
