// Package preconf implements the Mantle preconfirmation test scenarios as raw JSON-RPC,
// with no dependency on the op-geth fork's typed client. It reuses rpc_compat's rpc/tx
// building blocks (Tester, Builder, raw JSON-RPC client) and adds preconf-specific scenarios:
// geth<->reth response parity, failure-reason validation, and predicted-vs-actual block checks.
package preconf

import (
	_ "embed"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// Deterministic deploy addresses (funder nonce 0 / 1), matching the legacy suite + deploy.sh.
var (
	TestERC20Addr = common.HexToAddress("0x5FbDB2315678afecb367f032d93F642f64180aa3")
	TestPayAddr   = common.HexToAddress("0xe7f1725E7734CE288F8367e1Bb143E90bb3F0512")

	// Addr2 is the preconf-whitelisted native recipient (txpool.topreconfs); Addr3 is used only as
	// the ERC20 "sender" argument inside TestPay.transferTo calldata.
	Addr2 = common.HexToAddress("0x71920E3cb420fbD8Ba9a495E6f801c50375ea127")
	Addr3 = common.HexToAddress("0x918a3880A91308279C06A89415d01ae47d64eC29")

	// GasBurnerAddr is the deterministic CREATE address of GasBurner deployed from
	// gasBurnerDeployerKey at nonce 0. It must be added to txpool.topreconfs so the block-full
	// scenario can send preconf txs to it.
	GasBurnerAddr = common.HexToAddress("0xf0620ca0820DE5BcAc573f2DaD9243A1427d41f7")

	// WorstCaseAddr is the deterministic CREATE address of WorstCase (bn256-ECMUL loop, worst
	// gas-per-walltime) deployed from worstCaseDeployerKey at nonce 0. Added to txpool.topreconfs so
	// A8 can send it as a preconf whose ~190ms exec reliably exceeds a mid-range preconftimeout.
	WorstCaseAddr = common.HexToAddress("0xD603276cD86F8e2d63182C9bf33Fd871b94CD188")
)

// gasBurnerDeployerKey is a dedicated fresh key (addr 0xaf29…5620); its nonce-0 CREATE address is
// GasBurnerAddr. Deploying from a dedicated key keeps the burner address deterministic/whitelistable
// without depending on the funder's (non-zero) nonce.
const gasBurnerDeployerKey = "0xb0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"

// worstCaseDeployerKey is a dedicated fresh key (addr 0x4ee7…cAD0); its nonce-0 CREATE address is
// WorstCaseAddr.
const worstCaseDeployerKey = "0xc0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0"

// Compiled creation bytecode (solc --optimize), embedded so setup is self-contained (no solc/cast
// at runtime). Regenerate with:
//
//	solc --bin --optimize -o testdata src/op-geth/tests/mantletest/preconf/contracts/TestERC20.sol \
//	                                  src/op-geth/tests/mantletest/preconf/contracts/TestPay.sol
//
//go:embed testdata/TestERC20.bin
var testERC20Bin string

//go:embed testdata/TestPay.bin
var testPayBin string

//go:embed testdata/GasBurner.bin
var gasBurnerBin string

//go:embed testdata/WorstCase.bin
var worstCaseBin string

// TestERC20Bytecode returns the 0x-prefixed creation bytecode for TestERC20.
func TestERC20Bytecode() string { return "0x" + strings.TrimSpace(testERC20Bin) }

// TestPayBytecode returns the 0x-prefixed creation bytecode for TestPay.
func TestPayBytecode() string { return "0x" + strings.TrimSpace(testPayBin) }

// GasBurnerBytecode returns the 0x-prefixed creation bytecode for GasBurner (burns ~all gas sent).
func GasBurnerBytecode() string { return "0x" + strings.TrimSpace(gasBurnerBin) }

// WorstCaseBytecode returns the 0x-prefixed creation bytecode for WorstCase (bn256-precompile loop,
// worst gas-per-walltime; used by A11 latency-vs-timeout).
func WorstCaseBytecode() string { return "0x" + strings.TrimSpace(worstCaseBin) }

// transferTo(address,address,uint256) — TestPay's pay entrypoint (selector 0xa5f2a152).
var payTransferToSelector = []byte{0xa5, 0xf2, 0xa1, 0x52}

// PayTransferToCalldata builds calldata for TestPay.transferTo(sender, recipient, amount).
func PayTransferToCalldata(sender, recipient common.Address, amount *big.Int) []byte {
	out := make([]byte, 0, 4+32*3)
	out = append(out, payTransferToSelector...)
	out = append(out, common.LeftPadBytes(sender.Bytes(), 32)...)
	out = append(out, common.LeftPadBytes(recipient.Bytes(), 32)...)
	out = append(out, common.LeftPadBytes(amount.Bytes(), 32)...)
	return out
}

// TestERC20 selectors.
var (
	erc20ApproveSelector   = []byte{0x09, 0x5e, 0xa7, 0xb3} // approve(address,uint256)
	erc20MintSelector      = []byte{0x40, 0xc1, 0x0f, 0x19} // mint(address,uint256)
	erc20AllowanceSelector = []byte{0xdd, 0x62, 0xed, 0x3e} // allowance(address,address)
	erc20BalanceOfSelector = []byte{0x70, 0xa0, 0x82, 0x31} // balanceOf(address)
)

func addrUintCalldata(sel []byte, addr common.Address, amount *big.Int) []byte {
	out := make([]byte, 0, 4+32*2)
	out = append(out, sel...)
	out = append(out, common.LeftPadBytes(addr.Bytes(), 32)...)
	out = append(out, common.LeftPadBytes(amount.Bytes(), 32)...)
	return out
}

// ERC20ApproveCalldata: approve(spender, amount).
func ERC20ApproveCalldata(spender common.Address, amount *big.Int) []byte {
	return addrUintCalldata(erc20ApproveSelector, spender, amount)
}

// ERC20MintCalldata: mint(to, amount).
func ERC20MintCalldata(to common.Address, amount *big.Int) []byte {
	return addrUintCalldata(erc20MintSelector, to, amount)
}

// ERC20AllowanceCalldata: allowance(owner, spender).
func ERC20AllowanceCalldata(owner, spender common.Address) []byte {
	out := make([]byte, 0, 4+32*2)
	out = append(out, erc20AllowanceSelector...)
	out = append(out, common.LeftPadBytes(owner.Bytes(), 32)...)
	out = append(out, common.LeftPadBytes(spender.Bytes(), 32)...)
	return out
}

// ERC20BalanceOfCalldata: balanceOf(addr).
func ERC20BalanceOfCalldata(addr common.Address) []byte {
	out := make([]byte, 0, 4+32)
	out = append(out, erc20BalanceOfSelector...)
	out = append(out, common.LeftPadBytes(addr.Bytes(), 32)...)
	return out
}
