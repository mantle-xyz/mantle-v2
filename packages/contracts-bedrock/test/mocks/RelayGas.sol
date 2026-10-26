// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

/// @notice Consumes all forwarded gas until the test makes the target executable again.
contract RelayGasBurner {
    bool public burn = true;

    function stopBurning() external {
        burn = false;
    }

    fallback() external payable {
        _burnGas();
    }

    receive() external payable {
        _burnGas();
    }

    function _burnGas() internal view {
        if (burn) {
            assembly {
                invalid()
            }
        }
    }
}

/// @notice Separates a transaction's calldata floor from its nested call's execution budget.
contract RelayGasForwarder {
    function forward(address _target, uint256 _gas, bytes calldata _data) external payable returns (bool completed) {
        (completed,) = _target.call{ gas: _gas, value: msg.value }(_data);
    }
}
