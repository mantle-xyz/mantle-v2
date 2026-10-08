// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;
// Adversarial worst-case-per-gas: loop a bn256 precompile until gas ~exhausted.
// Used by A11 to measure max single-tx execution latency vs the preconf timeout.
// calldata sel(0)=ECMUL(0x07), sel(1)=ECPAIRING(0x08), sel(2)=MODEXP(0x05); empty → ECMUL.
contract WorstCase {
    fallback() external payable {
        assembly {
            let sel := calldataload(0)
            switch sel
            case 1 {
                mstore(0x00, 1) mstore(0x20, 2)
                mstore(0x40, 0x198e9393920d483a7260bfb731fb5d25f1aa493335a9e71297e485b7aef312c2)
                mstore(0x60, 0x1800deef121f1e76426a00665e5c4479674322d4f75edadd46debd5cd992f6ed)
                mstore(0x80, 0x090689d0585ff075ec9e99ad690c3395bc4b313370b38ef355acdadcd122975b)
                mstore(0xa0, 0x12c85ea5db8c6deb4aab71808dcb408fe3d1e7690c43d37b4ce6cc0166fa7daa)
                for {} gt(gas(), 90000) {} { pop(staticcall(gas(), 0x08, 0x00, 0xc0, 0x100, 0x20)) }
            }
            case 2 {
                mstore(0x00, 32) mstore(0x20, 32) mstore(0x40, 32)
                mstore(0x60, not(0)) mstore(0x80, not(0)) mstore(0xa0, not(0))
                for {} gt(gas(), 30000) {} { pop(staticcall(gas(), 0x05, 0x00, 0xc0, 0x100, 0x20)) }
            }
            default {
                mstore(0x00, 1) mstore(0x20, 2)
                mstore(0x40, 0x2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f2f)
                for {} gt(gas(), 30000) {} { pop(staticcall(gas(), 0x07, 0x00, 0x60, 0x80, 0x40)) }
            }
        }
    }
}
