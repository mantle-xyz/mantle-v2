// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Test } from "forge-std/Test.sol";
import { L2Genesis } from "scripts/L2Genesis.s.sol";
import { MantleFork } from "scripts/libraries/Config.sol";
import { Predeploys } from "src/libraries/Predeploys.sol";
import { OptimismMintableERC20 } from "src/universal/OptimismMintableERC20.sol";

interface IVersionedPredeploy {
    function version() external view returns (string memory);
}

/// @notice Runs the L2 genesis script and checks the resulting predeploy state.
contract L2Genesis_Test is Test {
    L2Genesis internal genesis;
    address internal l1MNT = makeAddr("L1 MNT");

    function setUp() public {
        genesis = new L2Genesis();
        genesis.run(
            L2Genesis.Input({
                l1ChainID: 560048,
                l2ChainID: 50002,
                l1CrossDomainMessengerProxy: payable(makeAddr("L1CrossDomainMessenger")),
                l1StandardBridgeProxy: payable(makeAddr("L1StandardBridge")),
                l1ERC721BridgeProxy: payable(makeAddr("L1ERC721Bridge")),
                l1MNTAddress: l1MNT,
                opChainProxyAdminOwner: makeAddr("ProxyAdmin owner"),
                sequencerFeeVaultRecipient: makeAddr("SequencerFeeVault recipient"),
                baseFeeVaultRecipient: makeAddr("BaseFeeVault recipient"),
                l1FeeVaultRecipient: makeAddr("L1FeeVault recipient"),
                operatorFeeVaultRecipient: makeAddr("OperatorFeeVault recipient"),
                gasPriceOracleOwner: makeAddr("GasPriceOracle owner"),
                gasPriceOracleTokenRatio: 0,
                mantleFork: uint256(MantleFork.MANTLE_ELYSIUM),
                fundDevAccounts: false
            })
        );
    }

    /// @notice Every Semver predeploy must report the version its constructor sets. "0.0.0" means the
    ///         implementation was etched without running its constructor (the Hoodi genesis bug).
    function test_predeployVersions_succeeds() external view {
        _assertVersion(Predeploys.LEGACY_MESSAGE_PASSER, "1.0.0");
        _assertVersion(Predeploys.DEPLOYER_WHITELIST, "1.0.0");
        _assertVersion(Predeploys.GAS_PRICE_ORACLE, "1.1.0");
        _assertVersion(Predeploys.L1_BLOCK_NUMBER, "1.0.0");
        _assertVersion(Predeploys.L1_BLOCK_ATTRIBUTES, "1.1.0");
        _assertVersion(Predeploys.L2_TO_L1_MESSAGE_PASSER, "1.0.0");
        _assertVersion(Predeploys.L2_CROSS_DOMAIN_MESSENGER, "1.6.0");
        _assertVersion(Predeploys.L2_STANDARD_BRIDGE, "1.1.0");
        _assertVersion(Predeploys.L2_ERC721_BRIDGE, "1.1.0");
        _assertVersion(Predeploys.SEQUENCER_FEE_WALLET, "1.1.0");
        _assertVersion(Predeploys.OPTIMISM_MINTABLE_ERC20_FACTORY, "1.1.0");
        _assertVersion(Predeploys.OPTIMISM_MINTABLE_ERC721_FACTORY, "1.2.0");
        _assertVersion(Predeploys.BASE_FEE_VAULT, "1.1.0");
        _assertVersion(Predeploys.L1_FEE_VAULT, "1.1.0");
        _assertVersion(Predeploys.OPERATOR_FEE_VAULT, "1.1.0");
        _assertVersion(Predeploys.BVM_ETH, "1.0.0");
        _assertVersion(Predeploys.LEGACY_ERC20_MNT, "1.0.0");
    }

    function test_bvmEth_succeeds() external view {
        OptimismMintableERC20 token = OptimismMintableERC20(Predeploys.BVM_ETH);
        assertEq(token.name(), "Ether");
        assertEq(token.symbol(), "WETH");
        assertEq(token.decimals(), 18);
        assertEq(token.bridge(), Predeploys.L2_STANDARD_BRIDGE);
        assertEq(token.remoteToken(), address(0));
    }

    function test_legacyERC20MNT_succeeds() external view {
        assertGt(Predeploys.LEGACY_ERC20_MNT.code.length, 0);
        OptimismMintableERC20 token = OptimismMintableERC20(Predeploys.LEGACY_ERC20_MNT);
        assertEq(token.name(), "Mantle Token");
        assertEq(token.symbol(), "MNT");
        assertEq(token.decimals(), 18);
        assertEq(token.bridge(), Predeploys.L2_STANDARD_BRIDGE);
        assertEq(token.remoteToken(), l1MNT);
    }

    /// @notice Temporary deployments used to construct implementations must not leak into the state dump.
    function test_temporaryDeployments_cleanedUp() external {
        address deployer = makeAddr("deployer");
        assertEq(vm.getNonce(deployer), 0);
        for (uint64 i; i < 32; i++) {
            assertEq(vm.computeCreateAddress(deployer, i).code.length, 0);
        }
    }

    function _assertVersion(address _predeploy, string memory _expected) internal view {
        assertEq(IVersionedPredeploy(_predeploy).version(), _expected, vm.toString(_predeploy));
    }
}
