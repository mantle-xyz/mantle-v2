package main

import (
	"os"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
