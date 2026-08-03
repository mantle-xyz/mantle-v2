package presets

import (
	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/dsl"
	"github.com/ethereum-optimism/optimism/op-devstack/shim"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
	"github.com/ethereum-optimism/optimism/op-devstack/stack/match"
	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
	"github.com/ethereum-optimism/optimism/op-supervisor/supervisor/types"
)

type MantleSingleChainMultiNode struct {
	MantleMinimal

	L2ELB *dsl.L2ELNode
	L2CLB *dsl.L2CLNode
}

func WithMantleSingleChainMultiNode() stack.CommonOption {
	return stack.MakeCommon(sysgo.DefaultMantleSingleChainMultiNodeSystem(&sysgo.DefaultMantleSingleChainMultiNodeSystemIDs{}))
}

func NewMantleSingleChainMultiNode(t devtest.T) *MantleSingleChainMultiNode {
	preset := NewMantleSingleChainMultiNodeWithoutCheck(t)
	// Ensure the follower node is in sync with the sequencer before starting tests
	dsl.CheckAll(t,
		preset.L2CLB.MatchedFn(preset.L2CL, types.CrossSafe, 30),
		preset.L2CLB.MatchedFn(preset.L2CL, types.LocalUnsafe, 30),
	)
	return preset
}

func NewMantleSingleChainMultiNodeWithoutCheck(t devtest.T) *MantleSingleChainMultiNode {
	system := shim.NewSystem(t)
	orch := Orchestrator()
	orch.Hydrate(system)
	mantleMinimal := mantleMinimalFromSystem(t, system, orch)
	l2 := system.L2Network(match.Assume(t, match.L2ChainA))
	verifierCL := l2.L2CLNode(match.Assume(t,
		match.And(
			match.Not(match.WithSequencerActive(t.Ctx())),
			match.Not[stack.L2CLNodeID, stack.L2CLNode](mantleMinimal.L2CL.ID()),
		)))
	verifierEL := l2.L2ELNode(match.Assume(t,
		match.And(
			match.EngineFor(verifierCL),
			match.Not[stack.L2ELNodeID, stack.L2ELNode](mantleMinimal.L2EL.ID()))))
	preset := &MantleSingleChainMultiNode{
		MantleMinimal: *mantleMinimal,
		L2ELB:         dsl.NewL2ELNode(verifierEL, orch.ControlPlane()),
		L2CLB:         dsl.NewL2CLNode(verifierCL, orch.ControlPlane()),
	}
	return preset
}

func WithMantleSingleChainMultiNodeWithoutP2P() stack.CommonOption {
	return stack.MakeCommon(sysgo.DefaultMantleSingleChainMultiNodeSystemWithoutP2P(&sysgo.DefaultMantleSingleChainMultiNodeSystemIDs{}))
}

type MantleSingleChainMultiNodeWithTestSeq struct {
	MantleSingleChainMultiNode

	TestSequencer *dsl.TestSequencer
}

func NewMantleSingleChainMultiNodeWithTestSeq(t devtest.T) *MantleSingleChainMultiNodeWithTestSeq {
	system := shim.NewSystem(t)
	orch := Orchestrator()
	orch.Hydrate(system)
	mantleMinimal := mantleMinimalFromSystem(t, system, orch)
	l2 := system.L2Network(match.Assume(t, match.L2ChainA))
	verifierCL := l2.L2CLNode(match.Assume(t,
		match.And(
			match.Not(match.WithSequencerActive(t.Ctx())),
			match.Not[stack.L2CLNodeID, stack.L2CLNode](mantleMinimal.L2CL.ID()),
		)))
	verifierEL := l2.L2ELNode(match.Assume(t,
		match.And(
			match.EngineFor(verifierCL),
			match.Not[stack.L2ELNodeID, stack.L2ELNode](mantleMinimal.L2EL.ID()))))
	preset := &MantleSingleChainMultiNode{
		MantleMinimal: *mantleMinimal,
		L2ELB:         dsl.NewL2ELNode(verifierEL, orch.ControlPlane()),
		L2CLB:         dsl.NewL2CLNode(verifierCL, orch.ControlPlane()),
	}
	out := &MantleSingleChainMultiNodeWithTestSeq{
		MantleSingleChainMultiNode: *preset,
		TestSequencer:              dsl.NewTestSequencer(system.TestSequencer(match.Assume(t, match.FirstTestSequencer))),
	}
	return out
}

func WithNewMantleSingleChainMultiNodeWithTestSeq() stack.CommonOption {
	return stack.MakeCommon(sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqSystem(&sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs{}))
}

// WithNewMantleSingleChainMultiNodeWithTestSeqPreconf is the same topology as
// WithNewMantleSingleChainMultiNodeWithTestSeq but with the Mantle preconf
// subsystem enabled on the L2 EL(s). Hydrate it with
// NewMantleSingleChainMultiNodeWithTestSeq. Intended to run under
// DEVSTACK_L2EL_KIND=op-reth (only op-reth honours preconf).
func WithNewMantleSingleChainMultiNodeWithTestSeqPreconf() stack.CommonOption {
	return stack.MakeCommon(sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqPreconfSystem(&sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs{}))
}

// WithNewMantleSingleChainMultiNodeWithTestSeqPreconfNoJournal is like
// WithNewMantleSingleChainMultiNodeWithTestSeqPreconf but starts preconf with
// the on-disk commitment journal DISABLED, for the TC-RG4 degraded-path reorg
// test (reorg-reverted commitments may be dropped rather than replayed).
// Hydrate it with NewMantleSingleChainMultiNodeWithTestSeq, and run under
// DEVSTACK_L2EL_KIND=op-reth.
func WithNewMantleSingleChainMultiNodeWithTestSeqPreconfNoJournal() stack.CommonOption {
	return stack.MakeCommon(sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqPreconfNoJournalSystem(&sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs{}))
}

// WithNewMantleSingleChainMultiNodeWithTestSeqPreconfWhitelist is the preconf
// journal-ON test-seq topology but with a RESTRICTED (from,to) whitelist (vs
// all=true), so a non-whitelisted, tip-ordered pool tx can be co-submitted. Used
// by the journal-on ordering-inversion reorg test. Hydrate it with
// NewMantleSingleChainMultiNodeWithTestSeq, run under DEVSTACK_L2EL_KIND=op-reth.
// The whitelisted (sender,recipient) pair is sysgo.PreconfWhitelist{Sender,Recipient}Addr.
func WithNewMantleSingleChainMultiNodeWithTestSeqPreconfWhitelist() stack.CommonOption {
	return stack.MakeCommon(sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqPreconfWhitelistSystem(&sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs{}))
}

// WithNewMantleSingleChainMultiNodeWithTestSeqPreconfWhitelistNoJournal is the
// journal-OFF counterpart of WithNewMantleSingleChainMultiNodeWithTestSeqPreconfWhitelist,
// for the TC-RG4 degraded-path ordering characterization.
func WithNewMantleSingleChainMultiNodeWithTestSeqPreconfWhitelistNoJournal() stack.CommonOption {
	return stack.MakeCommon(sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqPreconfWhitelistNoJournalSystem(&sysgo.DefaultMantleSingleChainMultiNodeWithTestSeqSystemIDs{}))
}
