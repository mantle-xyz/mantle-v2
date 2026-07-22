// Package evmsfi covers design-doc case §3-5, TestL2EVM_NoSFIBehavior: the Mantle L2's EVM
// keeps Arsia semantics and adopts none of the Glamsterdam SFI EIPs, while the L1 it consumes
// runs Glamsterdam.
//
// The four sub-cases were previously four separate packages (evmgas, evmaccesslist,
// evmblockgas, evmopcodes). They are merged here because their TestMains were byte-identical,
// so the split bought nothing and cost three extra system bring-ups per run — and because the
// doc describes §3-5 as ONE case. Keeping four package-level Test functions meant the name the
// doc specifies existed nowhere in the tree, which is how the mapping between doc and code
// silently drifted.
//
// Each sub-case pins an L2-internal gas or opcode property that Glamsterdam would change if it
// leaked in. The L1 is the environment, not the discriminator: every assertion below is gated
// on the L2 fork and would hold under any L1. What makes them meaningful is that they assert
// MEASURED quantities — exact gas, exact opcode outcomes — rather than the absence of
// something, so a repricing that leaked in has nowhere to hide.
package evm

import "testing"

// TestL2EVM_NoSFIBehavior is design-doc case §3-5.
//
//   - 7976: the calldata floor stays at the Arsia rate (40 gas per non-zero byte), not the
//     raised Glamsterdam cost.
//   - 7981: access-list entries stay at 2400 gas/address and 1900 gas/storage-key, not the
//     raised 3680 / 3948.
//   - 7778: a storage-clear refund is still credited back to the block gas pool, so the block
//     header's GasUsed equals the cumulative receipt gas rather than exceeding it.
//   - 8024: DUPN / SWAPN / EXCHANGE remain undefined opcodes and abort the call.
//
// (The doc also listed EIP-8037 under this case. It is a Draft that never entered Glamsterdam
// and has no op-geth implementation, so there is nothing to assert; it was struck from the doc
// the same way EIP-7825 was struck from §2.)
func TestL2EVM_NoSFIBehavior(gt *testing.T) {
	gt.Run("7976_CalldataGasStaysArsia", runCalldataGasStaysArsia)
	gt.Run("7981_AccessListGasStaysArsia", runAccessListGasStaysArsia)
	gt.Run("7778_BlockGasRefundCredited", runBlockGasRefundCredited)
	gt.Run("8024_NoNewOpcodes", runNoNewOpcodes)
}
