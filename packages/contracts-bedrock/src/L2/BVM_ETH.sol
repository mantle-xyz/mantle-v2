// SPDX-License-Identifier: MIT
pragma solidity ^0.8.9;

/* Library Imports */
import { Predeploys } from "../libraries/Predeploys.sol";

/* Contract Imports */
import { OptimismMintableERC20 } from "../universal/OptimismMintableERC20.sol";

/// @title BVM_ETH
/// @dev The ETH predeploy provides an ERC20 interface for ETH deposited to Layer 2. Note that
/// unlike on Layer 1, Layer 2 accounts do not have a balance field.
contract BVM_ETH is OptimismMintableERC20 {
    /**
     *
     * Constructor *
     *
     */
    constructor() OptimismMintableERC20(Predeploys.L2_STANDARD_BRIDGE, address(0), "", "") { }

    /// @notice Returns the token name.
    function name() public pure override returns (string memory) {
        return "Ether";
    }

    /// @notice Returns the token symbol.
    function symbol() public pure override returns (string memory) {
        return "WETH";
    }

    /// @notice Allows the StandardBridge on this network to mint tokens.
    /// @dev This function is disabled and will always revert. BVM_ETH mint can only be triggered in deposit transaction
    /// execution.
    function mint(address, uint256) public virtual override {
        revert(
            "BVM_ETH: mint is disabled by normal contract calling. BVM_ETH mint can only be triggered in deposit transaction execution, similar to MNT mint on L2."
        );
    }

    /// @notice A modifier that only allows the L2_TO_L1_MESSAGE_PASSER to call
    modifier onlyL2Passer() {
        require(
            msg.sender == Predeploys.L2_TO_L1_MESSAGE_PASSER, "OptimismMintableERC20: only L2MessagePasser can burn"
        );
        _;
    }

    /// @notice Allows the StandardBridge on this network to burn tokens.
    ///
    /// @param _from   Address to burn tokens from.
    /// @param _amount Amount of tokens to burn.
    function burn(address _from, uint256 _amount) external virtual override onlyL2Passer {
        _burn(_from, _amount);
        emit Burn(_from, _amount);
    }
}
