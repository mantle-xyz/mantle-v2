package fakepos

import (
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils/geth"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

type Config struct {
	ChainConfig types.BlockType
	// L1ChainConfig carries the L1 fork schedule so the builder can select the
	// correct engine-API version (V2..V6) and populate fork-specific payload
	// attributes (e.g. Amsterdam's SlotNumber/TargetGasLimit) per built block.
	L1ChainConfig     *params.ChainConfig
	Backend           Blockchain
	EngineAPI         geth.EngineAPI
	Beacon            Beacon
	FinalizedDistance uint64
	SafeDistance      uint64
	BlockTime         uint64
}
