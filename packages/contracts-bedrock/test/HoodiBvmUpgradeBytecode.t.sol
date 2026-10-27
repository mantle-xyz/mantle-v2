// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Test } from "forge-std/Test.sol";
import { BVM_ETH } from "src/L2/BVM_ETH.sol";
import { LegacyERC20MNT } from "src/legacy/LegacyERC20MNT.sol";
import { Config } from "scripts/libraries/Config.sol";

/// @notice Pins the runtime code that the Mantle Hoodi `HoodiBvmUpgrade` hardfork writes. The
///         execution clients embed this code as constants (alloy-op-evm `block/hoodi_bvm_upgrade.rs`,
///         op-geth); if this test fails, the contracts changed and the embedded code must be
///         regenerated before any release.
contract HoodiBvmUpgradeBytecode_Test is Test {
    /// @notice Mantle Hoodi's L1 MNT token, the `LegacyERC20MNT` constructor argument.
    address internal constant HOODI_L1_MNT = 0xb57837B4D0d372bF49E69Bc36c36d9Cc9488bf18;

    bytes32 internal constant BVM_ETH_CODEHASH = 0xe84a8d4d3ed629a670b03e88dab806078de4b86beac8ca06addcff8dd4996290;
    bytes32 internal constant LEGACY_ERC20_MNT_CODEHASH =
        0xf165e4b851dc8d4e18dcf1d2bc59817d0129a39365c191e76bfd596b60513b45;

    /// @notice The pinned code hashes come from an optimized build. Profiles that disable the
    ///         optimizer (`lite`, used by `just test-dev`, and `cicoverage`) compile different
    ///         runtime code, so skip the hash comparison there.
    modifier onlyOptimizedBuild() {
        bytes32 profile = keccak256(bytes(Config.foundryProfile()));
        vm.skip(profile == keccak256("lite") || profile == keccak256("cicoverage"));
        _;
    }

    function test_bvmEthCodehash_matchesClients() external onlyOptimizedBuild {
        assertEq(address(new BVM_ETH()).codehash, BVM_ETH_CODEHASH);
    }

    function test_legacyErc20MntCodehash_matchesClients() external onlyOptimizedBuild {
        assertEq(address(new LegacyERC20MNT(HOODI_L1_MNT)).codehash, LEGACY_ERC20_MNT_CODEHASH);
    }

    /// @notice Etching only the runtime code (as the hardfork does) yields correct metadata.
    function test_etchedCode_metadata() external {
        address eth = 0xdEAddEaDdeadDEadDEADDEAddEADDEAddead1111;
        address mnt = 0xDeadDeAddeAddEAddeadDEaDDEAdDeaDDeAD0000;
        vm.etch(eth, address(new BVM_ETH()).code);
        vm.etch(mnt, address(new LegacyERC20MNT(HOODI_L1_MNT)).code);

        assertEq(BVM_ETH(eth).name(), "Ether");
        assertEq(BVM_ETH(eth).symbol(), "WETH");
        assertEq(BVM_ETH(eth).version(), "1.0.0");
        assertEq(BVM_ETH(eth).bridge(), 0x4200000000000000000000000000000000000010);
        assertEq(LegacyERC20MNT(mnt).name(), "Mantle Token");
        assertEq(LegacyERC20MNT(mnt).symbol(), "MNT");
        assertEq(LegacyERC20MNT(mnt).version(), "1.0.0");
        assertEq(LegacyERC20MNT(mnt).remoteToken(), HOODI_L1_MNT);
    }
}
