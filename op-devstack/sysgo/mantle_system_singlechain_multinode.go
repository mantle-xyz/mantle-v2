package sysgo

import (
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

type DefaultMantleSingleChainMultiNodeSystemIDs struct {
	DefaultMinimalSystemIDs

	L2CLB stack.L2CLNodeID
	L2ELB stack.L2ELNodeID
}

type DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs struct {
	DefaultMantleSingleChainMultiNodeSystemIDs

	TestSequencer stack.TestSequencerID
}

func NewDefaultMantleSingleChainMultiNodeSystemIDs(l1ID, l2ID eth.ChainID) DefaultMantleSingleChainMultiNodeSystemIDs {
	minimal := NewDefaultMinimalSystemIDs(l1ID, l2ID)
	return DefaultMantleSingleChainMultiNodeSystemIDs{
		DefaultMinimalSystemIDs: minimal,
		L2CLB:                   stack.NewL2CLNodeID("b", l2ID),
		L2ELB:                   stack.NewL2ELNodeID("b", l2ID),
	}
}

func NewMantleDefaultSingleChainMultiNodeWithTestSeqSystemIDs(l1ID, l2ID eth.ChainID) DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs {
	return DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs{
		DefaultMantleSingleChainMultiNodeSystemIDs: NewDefaultMantleSingleChainMultiNodeSystemIDs(l1ID, l2ID),
		TestSequencer: "dev",
	}
}

func DefaultMantleSingleChainMultiNodeSystem(dest *DefaultMantleSingleChainMultiNodeSystemIDs) stack.Option[*Orchestrator] {
	ids := NewDefaultMantleSingleChainMultiNodeSystemIDs(DefaultL1ID, DefaultL2AID)

	opt := stack.Combine[*Orchestrator]()
	opt.Add(DefaultMantleMinimalSystem(&dest.DefaultMinimalSystemIDs))

	opt.Add(WithL2ELNode(ids.L2ELB))
	opt.Add(WithL2CLNode(ids.L2CLB, ids.L1CL, ids.L1EL, ids.L2ELB))

	// P2P connect L2CL nodes
	opt.Add(WithL2CLP2PConnection(ids.L2CL, ids.L2CLB))
	opt.Add(WithL2ELP2PConnection(ids.L2EL, ids.L2ELB))

	opt.Add(stack.Finally(func(orch *Orchestrator) {
		*dest = ids
	}))
	return opt
}

func DefaultMantleSingleChainMultiNodeWithTestSeqSystem(dest *DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs) stack.Option[*Orchestrator] {
	ids := NewMantleDefaultSingleChainMultiNodeWithTestSeqSystemIDs(DefaultL1ID, DefaultL2AID)
	opt := stack.Combine[*Orchestrator]()
	opt.Add(DefaultMantleSingleChainMultiNodeSystem(&dest.DefaultMantleSingleChainMultiNodeSystemIDs))

	opt.Add(WithTestSequencer(ids.TestSequencer, ids.L1CL, ids.L2CL, ids.L1EL, ids.L2EL))

	opt.Add(stack.Finally(func(orch *Orchestrator) {
		*dest = ids
	}))
	return opt
}

// DefaultMantleSingleChainMultiNodeWithTestSeqPreconfSystem is the Mantle
// test-sequencer topology with the preconf subsystem enabled on every L2 EL.
// Preconf is turned on via a global L2EL option applied before deploy, so it
// covers the sequencer EL (wired inside DefaultMantleMinimalSystem) as well as
// the verifier. Only the op-reth EL honours preconf, so this system is meant
// to be run with DEVSTACK_L2EL_KIND=op-reth; under op-geth the flags are
// ignored and the topology behaves like the non-preconf variant.
func DefaultMantleSingleChainMultiNodeWithTestSeqPreconfSystem(dest *DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs) stack.Option[*Orchestrator] {
	opt := stack.Combine[*Orchestrator]()
	// all=true (skip whitelist, no need to know the test EOA at launch) +
	// journal=true (exercise the commitment-replay path across a reorg).
	opt.Add(WithGlobalL2ELOption(L2ELWithPreconf(true, true, nil, nil, 0)))
	opt.Add(DefaultMantleSingleChainMultiNodeWithTestSeqSystem(dest))
	return opt
}

// DefaultMantleSingleChainMultiNodeWithTestSeqPreconfNoJournalSystem mirrors
// DefaultMantleSingleChainMultiNodeWithTestSeqPreconfSystem but launches the
// preconf subsystem with the on-disk commitment journal DISABLED
// (journal=false). It exists to exercise the TC-RG4 "reorg + journal off"
// degraded path: without the journal, a reorg-reverted commitment is only
// re-injected via a plain FIFO-membership check (no Replay identification), so
// some commitments MAY be legitimately dropped instead of replayed. As with the
// journal-on variant, only op-reth honours the flags.
func DefaultMantleSingleChainMultiNodeWithTestSeqPreconfNoJournalSystem(dest *DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs) stack.Option[*Orchestrator] {
	opt := stack.Combine[*Orchestrator]()
	// all=true (skip whitelist, no need to know the test EOA at launch) +
	// journal=false (degraded replay path — the whole point of TC-RG4).
	opt.Add(WithGlobalL2ELOption(L2ELWithPreconf(true, false, nil, nil, 0)))
	opt.Add(DefaultMantleSingleChainMultiNodeWithTestSeqSystem(dest))
	return opt
}

// Fixed identities baked into the RESTRICTED-whitelist preconf systems below.
//
// The ordering-inversion reorg tests (preconf/reorg_ordering*) need a
// tip-ordered NON-preconf tx to contrast against, which the `all=true` systems
// above cannot provide (under --preconf.all every tx is preconf). With a
// restricted whitelist `is_preconf_tx` requires BOTH from AND to to match
// (crates/preconf/src/config.rs), and the whitelist is a launch flag — so the
// eligible (sender,recipient) pair must be known before the node starts. These
// fixed values are that pair; the test signs the preconf tx with
// PreconfWhitelistSenderPrivHex (whose address is PreconfWhitelistSenderAddr)
// and sends it to PreconfWhitelistRecipientAddr. A normal tx to any OTHER
// recipient is not preconf-eligible and stays in the tip-ordered pool.
//
// The sender key is a well-known dev key (Anvil account #2). The devstack HD
// wallet uses a fresh RANDOM mnemonic per run, so this fixed address cannot
// collide with the funder-derived accounts.
const (
	// PreconfWhitelistSenderPrivHex is the private key (no 0x prefix) of the
	// whitelisted preconf sender; the test loads it to sign the preconf tx.
	PreconfWhitelistSenderPrivHex = "5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
	// PreconfWhitelistSenderAddr is the address of PreconfWhitelistSenderPrivHex,
	// passed as --preconf.from.
	PreconfWhitelistSenderAddr = "0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC"
	// PreconfWhitelistRecipientAddr is the whitelisted preconf recipient,
	// passed as --preconf.to. No private key is needed — it is only a transfer
	// target.
	PreconfWhitelistRecipientAddr = "0x00000000000000000000000000000000000000A1"
)

// DefaultMantleSingleChainMultiNodeWithTestSeqPreconfWhitelistSystem mirrors the
// preconf journal-ON system but with a RESTRICTED (from,to) whitelist instead of
// all=true, so a non-whitelisted tx exists as a tip-ordered pool tx. Used by the
// journal-on ordering-inversion reorg test to prove a reverted preconf tx
// re-lands via the carryover path (ahead of a higher-tip normal tx) rather than
// via reth-native pool re-injection. Only op-reth honours the flags.
func DefaultMantleSingleChainMultiNodeWithTestSeqPreconfWhitelistSystem(dest *DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs) stack.Option[*Orchestrator] {
	opt := stack.Combine[*Orchestrator]()
	opt.Add(WithGlobalL2ELOption(L2ELWithPreconf(
		false, true,
		[]string{PreconfWhitelistSenderAddr},
		[]string{PreconfWhitelistRecipientAddr},
		0,
	)))
	opt.Add(DefaultMantleSingleChainMultiNodeWithTestSeqSystem(dest))
	return opt
}

// DefaultMantleSingleChainMultiNodeWithTestSeqPreconfWhitelistNoJournalSystem is
// the journal-OFF counterpart of the restricted-whitelist system above, for the
// TC-RG4 degraded-path ordering characterization (journal off ⇒ a reverted
// commitment falls back to a plain FIFO-membership check and MAY be dropped).
func DefaultMantleSingleChainMultiNodeWithTestSeqPreconfWhitelistNoJournalSystem(dest *DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs) stack.Option[*Orchestrator] {
	opt := stack.Combine[*Orchestrator]()
	opt.Add(WithGlobalL2ELOption(L2ELWithPreconf(
		false, false,
		[]string{PreconfWhitelistSenderAddr},
		[]string{PreconfWhitelistRecipientAddr},
		0,
	)))
	opt.Add(DefaultMantleSingleChainMultiNodeWithTestSeqSystem(dest))
	return opt
}

func DefaultMantleSingleChainMultiNodeSystemWithoutP2P(dest *DefaultMantleSingleChainMultiNodeSystemIDs) stack.Option[*Orchestrator] {
	ids := NewDefaultMantleSingleChainMultiNodeSystemIDs(DefaultL1ID, DefaultL2AID)

	opt := stack.Combine[*Orchestrator]()
	opt.Add(DefaultMantleMinimalSystem(&dest.DefaultMinimalSystemIDs))

	opt.Add(WithL2ELNode(ids.L2ELB))
	opt.Add(WithL2CLNode(ids.L2CLB, ids.L1CL, ids.L1EL, ids.L2ELB))
	opt.Add(WithL2MetricsDashboard())

	opt.Add(stack.Finally(func(orch *Orchestrator) {
		*dest = ids
	}))
	return opt
}
