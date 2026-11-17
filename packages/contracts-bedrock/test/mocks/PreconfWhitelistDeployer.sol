// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Proxy } from "src/universal/Proxy.sol";
import { PreconfWhitelist } from "src/L2/PreconfWhitelist.sol";

/// @title PreconfWhitelistDeployer
/// @dev Testing library that deploys `PreconfWhitelist` the way it ships: a fresh implementation
///      behind a `Proxy`, seeded by `upgradeToAndCall` in the same call, then handed to `_admin`.
library PreconfWhitelistDeployer {
    function deploy(
        address _admin,
        address _authorizedL1,
        PreconfWhitelist.Rule[] memory _initRules
    )
        internal
        returns (PreconfWhitelist)
    {
        Proxy proxy = new Proxy(address(this));
        proxy.upgradeToAndCall(
            address(new PreconfWhitelist()), abi.encodeCall(PreconfWhitelist.initialize, (_authorizedL1, _initRules))
        );
        proxy.changeAdmin(_admin);
        return PreconfWhitelist(address(proxy));
    }
}
