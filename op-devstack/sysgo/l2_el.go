package sysgo

import (
	"os"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-devstack/stack"
)

type L2ELNode interface {
	hydrate(system stack.ExtensibleSystem)
	stack.Lifecycle
	UserRPC() string
	EngineRPC() string
	JWTPath() string
}

type L2ELConfig struct {
	SupervisorID  *stack.SupervisorID
	P2PAddr       string
	P2PPort       int
	P2PNodeKeyHex string
	StaticPeers   []string
	TrustedPeers  []string

	// Preconf configures the Mantle preconfirmation subsystem. It is only
	// honoured by the op-reth EL (WithOpReth translates it into --preconf.*
	// CLI flags); the op-geth EL ignores it. All-zero means preconf stays
	// disabled, matching op-reth's default.
	PreconfEnabled   bool
	PreconfAll       bool     // --preconf.all: every tx is preconf-eligible (skip whitelist)
	PreconfFrom      []string // --preconf.from: sender whitelist (unused when PreconfAll)
	PreconfTo        []string // --preconf.to: recipient whitelist (unused when PreconfAll)
	PreconfJournal   bool     // enable the on-disk journal (path chosen under the node tempdir)
	PreconfTimeoutMs int      // --preconf.timeout-ms; 0 = leave op-reth's default
}

func L2ELWithSupervisor(supervisorID stack.SupervisorID) L2ELOption {
	return L2ELOptionFn(func(p devtest.P, id stack.L2ELNodeID, cfg *L2ELConfig) {
		cfg.SupervisorID = &supervisorID
	})
}

// L2ELWithP2PConfig sets deterministic P2P identity and static peers for the L2 EL.
func L2ELWithP2PConfig(addr string, port int, nodeKeyHex string, staticPeers, trustedPeers []string) L2ELOption {
	return L2ELOptionFn(func(p devtest.P, id stack.L2ELNodeID, cfg *L2ELConfig) {
		cfg.P2PAddr = addr
		cfg.P2PPort = port
		cfg.P2PNodeKeyHex = nodeKeyHex
		cfg.StaticPeers = staticPeers
		cfg.TrustedPeers = trustedPeers
	})
}

// L2ELWithPreconf enables the Mantle preconf subsystem on the L2 EL. Only the
// op-reth EL honours this (op-geth ignores it). Pass all=true to skip the
// (from,to) whitelist, or supply from/to lists otherwise. journal=true enables
// the on-disk commitment journal (needed to exercise the reorg/restart replay
// paths). timeoutMs<=0 leaves op-reth's default RPC deadline.
func L2ELWithPreconf(all, journal bool, from, to []string, timeoutMs int) L2ELOption {
	return L2ELOptionFn(func(p devtest.P, id stack.L2ELNodeID, cfg *L2ELConfig) {
		cfg.PreconfEnabled = true
		cfg.PreconfAll = all
		cfg.PreconfFrom = from
		cfg.PreconfTo = to
		cfg.PreconfJournal = journal
		cfg.PreconfTimeoutMs = timeoutMs
	})
}

func DefaultL2ELConfig() *L2ELConfig {
	return &L2ELConfig{
		SupervisorID:  nil,
		P2PAddr:       "127.0.0.1",
		P2PPort:       0,
		P2PNodeKeyHex: "",
		StaticPeers:   nil,
		TrustedPeers:  nil,
	}
}

type L2ELOption interface {
	Apply(p devtest.P, id stack.L2ELNodeID, cfg *L2ELConfig)
}

// WithGlobalL2ELOption applies the L2ELOption to all L2ELNode instances in this orchestrator
func WithGlobalL2ELOption(opt L2ELOption) stack.Option[*Orchestrator] {
	return stack.BeforeDeploy(func(o *Orchestrator) {
		o.l2ELOptions = append(o.l2ELOptions, opt)
	})
}

type L2ELOptionFn func(p devtest.P, id stack.L2ELNodeID, cfg *L2ELConfig)

var _ L2ELOption = L2ELOptionFn(nil)

func (fn L2ELOptionFn) Apply(p devtest.P, id stack.L2ELNodeID, cfg *L2ELConfig) {
	fn(p, id, cfg)
}

// L2ELOptionBundle a list of multiple L2ELOption, to all be applied in order.
type L2ELOptionBundle []L2ELOption

var _ L2ELOption = L2ELOptionBundle(nil)

func (l L2ELOptionBundle) Apply(p devtest.P, id stack.L2ELNodeID, cfg *L2ELConfig) {
	for _, opt := range l {
		p.Require().NotNil(opt, "cannot Apply nil L2ELOption")
		opt.Apply(p, id, cfg)
	}
}

// WithL2ELNode adds the default type of L2 CL node.
// The default can be configured with DEVSTACK_L2EL_KIND.
// Tests that depend on specific types can use options like WithKonaNode and WithOpNode directly.
func WithL2ELNode(id stack.L2ELNodeID, opts ...L2ELOption) stack.Option[*Orchestrator] {
	switch os.Getenv("DEVSTACK_L2EL_KIND") {
	case "op-reth":
		return WithOpReth(id, opts...)
	default:
		return WithOpGeth(id, opts...)
	}
}

func WithExtL2Node(id stack.L2ELNodeID, elRPCEndpoint string) stack.Option[*Orchestrator] {
	return stack.AfterDeploy(func(orch *Orchestrator) {
		require := orch.P().Require()

		// Create L2 EL node with external RPC
		l2ELNode := &OpGeth{
			id:       id,
			userRPC:  elRPCEndpoint,
			readOnly: true,
		}
		require.True(orch.l2ELs.SetIfMissing(id, l2ELNode), "must not already exist")
	})
}
