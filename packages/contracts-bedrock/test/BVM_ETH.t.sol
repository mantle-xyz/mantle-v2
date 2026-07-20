// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { BVMETH_Initializer } from "./CommonTest.t.sol";
import { Predeploys } from "src/libraries/Predeploys.sol";

// BVM_ETH_Test is for testing functionality BVM_ETH .
contract BVM_ETH_Test is BVMETH_Initializer {
    function setUp() public override {
        super.setUp();
    }

    function test_mint_reverts() external {
        vm.prank(alice);
        vm.expectRevert(
            "BVM_ETH: mint is disabled by normal contract calling. BVM_ETH mint can only be triggered in deposit transaction execution, similar to MNT mint on L2."
        );
        l2ETH.mint(alice, 1000);
    }

    function test_metadataAndBridgeConfig_succeeds() external view {
        assertEq(l2ETH.name(), "Ether");
        assertEq(l2ETH.symbol(), "WETH");
        assertEq(l2ETH.bridge(), Predeploys.L2_STANDARD_BRIDGE);
        assertEq(l2ETH.remoteToken(), address(0));
    }
}
