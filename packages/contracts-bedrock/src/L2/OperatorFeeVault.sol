// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Semver } from "../universal/Semver.sol";
import { FeeVault } from "../universal/FeeVault.sol";

/// @custom:proxied true
/// @custom:predeploy 0x420000000000000000000000000000000000001B
/// @title OperatorFeeVault
/// @notice The OperatorFeeVault accumulates the operator portion of the transaction fees.
contract OperatorFeeVault is FeeVault, Semver {
    /// @custom:semver 1.1.0
    ///
    /// @param _recipient Address that will receive the accumulated fees.
    constructor(address _recipient) FeeVault(_recipient, 10 ether) Semver(1, 1, 0) { }
}
