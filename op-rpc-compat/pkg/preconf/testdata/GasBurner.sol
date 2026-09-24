// SPDX-License-Identifier: MIT
pragma solidity ^0.8.18;

// GasBurner consumes essentially all gas provided to it: any call hits the fallback, which loops
// SSTORE (a new cold slot each iteration ~22.1k gas) until it runs out of gas. A tx sent with gas
// limit G therefore *uses* ~G (no refund), which is what lets a few such txs exhaust a block's gas
// pool and trigger preconf "block full".
contract GasBurner {
    fallback() external payable {
        for (uint256 i = 0; ; i++) {
            assembly {
                sstore(i, add(i, 1))
            }
        }
    }
}
