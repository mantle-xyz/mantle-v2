// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Predeploys } from "../libraries/Predeploys.sol";
import { OptimismMintableERC20 } from "../universal/OptimismMintableERC20.sol";
/// @custom:legacy
/// @custom:proxied
/// @custom:predeploy 0xDeadDeAddeAddEAddeadDEaDDEAdDeaDDeAD0000
/// @title LegacyERC20MNT
/// @notice LegacyERC20MNT is a legacy contract that held MNT balances before the Bedrock upgrade.
///         All MNT balances held within this contract were migrated to the state trie as part of
///         the Bedrock upgrade. Functions within this contract that mutate state were already
///         disabled as part of the EVM equivalence upgrade.

contract LegacyERC20MNT is OptimismMintableERC20 {
    /// @notice Initializes the contract as an Optimism Mintable ERC20.
    /// @dev name and symbol are constant overrides rather than constructor storage, so etching the
    ///      constructed runtime code at the predeploy address is sufficient (no storage copy needed).
    constructor(address _l1mnt) OptimismMintableERC20(Predeploys.L2_STANDARD_BRIDGE, _l1mnt, "", "") { }

    /// @notice Returns the token name.
    function name() public pure override returns (string memory) {
        return "Mantle Token";
    }

    /// @notice Returns the token symbol.
    function symbol() public pure override returns (string memory) {
        return "MNT";
    }

    /// @notice Returns the MNT balance of the target account. Overrides the base behavior of the
    ///         contract to preserve the invariant that the balance within this contract always
    ///         matches the balance in the state trie.
    ///
    /// @param _who Address of the account to query.
    ///
    /// @return The MNT balance of the target account.
    function balanceOf(address _who) public view virtual override returns (uint256) {
        return _who.balance;
    }

    /// @custom:blocked
    /// @notice Mints some amount of MNT.
    function mint(address, uint256) public virtual override {
        revert("LegacyERC20MNT: mint is disabled");
    }

    /// @custom:blocked
    /// @notice Burns some amount of MNT.
    function burn(address, uint256) public virtual override {
        revert("LegacyERC20MNT: burn is disabled");
    }

    /// @custom:blocked
    /// @notice Transfers some amount of MNT.
    function transfer(address, uint256) public virtual override returns (bool) {
        revert("LegacyERC20MNT: transfer is disabled");
    }

    /// @custom:blocked
    /// @notice Approves a spender to spend some amount of MNT.
    function approve(address, uint256) public virtual override returns (bool) {
        revert("LegacyERC20MNT: approve is disabled");
    }

    /// @custom:blocked
    /// @notice Transfers funds from some sender account.
    function transferFrom(address, address, uint256) public virtual override returns (bool) {
        revert("LegacyERC20MNT: transferFrom is disabled");
    }

    /// @custom:blocked
    /// @notice Increases the allowance of a spender.
    function increaseAllowance(address, uint256) public virtual override returns (bool) {
        revert("LegacyERC20MNT: increaseAllowance is disabled");
    }

    /// @custom:blocked
    /// @notice Decreases the allowance of a spender.
    function decreaseAllowance(address, uint256) public virtual override returns (bool) {
        revert("LegacyERC20MNT: decreaseAllowance is disabled");
    }
}
