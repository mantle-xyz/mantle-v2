// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Test } from "forge-std/Test.sol";
import {
    RepairPredeployVersions,
    IVersioned,
    IGasPriceOracleState
} from "scripts/upgrade/RepairPredeployVersions.s.sol";
import { ProxyAdmin } from "src/universal/ProxyAdmin.sol";
import { Proxy } from "src/universal/Proxy.sol";
import { Predeploys } from "src/libraries/Predeploys.sol";
import { LegacyMessagePasser } from "src/legacy/LegacyMessagePasser.sol";
import { DeployerWhitelist } from "src/legacy/DeployerWhitelist.sol";
import { GasPriceOracle } from "src/L2/GasPriceOracle.sol";
import { L1BlockNumber } from "src/legacy/L1BlockNumber.sol";
import { L1Block } from "src/L2/L1Block.sol";

/// @notice Rebuilds the pre-fix genesis layout (proxies pointing at constructor-less implementations)
///         and checks that the repair script upgrades exactly the "0.0.0" predeploys.
contract RepairPredeployVersions_Test is Test {
    RepairPredeployVersions internal script;
    address internal owner;
    address internal gpoOwner = makeAddr("gpo owner");

    string[5] internal names =
        ["LegacyMessagePasser", "DeployerWhitelist", "GasPriceOracle", "L1BlockNumber", "L1Block"];

    function setUp() public {
        owner = tx.origin;
        vm.deal(owner, 100 ether);
        script = new RepairPredeployVersions();

        ProxyAdmin admin = new ProxyAdmin(owner);
        vm.etch(Predeploys.PROXY_ADMIN, address(admin).code);
        vm.store(Predeploys.PROXY_ADMIN, bytes32(0), _word(owner));

        bytes memory proxyCode = address(new Proxy(Predeploys.PROXY_ADMIN)).code;
        address[5] memory t = script.targets();
        for (uint256 i; i < t.length; i++) {
            // Pre-fix genesis: implementation runtime code etched without its constructor.
            address impl = Predeploys.predeployToCodeNamespace(t[i]);
            vm.etch(impl, vm.getDeployedCode(string.concat(names[i], ".sol:", names[i])));
            vm.etch(t[i], proxyCode);
            vm.store(t[i], bytes32(uint256(keccak256("eip1967.proxy.admin")) - 1), _word(Predeploys.PROXY_ADMIN));
            vm.store(t[i], bytes32(uint256(keccak256("eip1967.proxy.implementation")) - 1), _word(impl));
        }

        // GasPriceOracle storage as written by L2Genesis: tokenRatio, owner, operator.
        vm.store(Predeploys.GAS_PRICE_ORACLE, bytes32(uint256(0)), bytes32(uint256(4083)));
        vm.store(Predeploys.GAS_PRICE_ORACLE, bytes32(uint256(1)), _word(gpoOwner));
        vm.store(Predeploys.GAS_PRICE_ORACLE, bytes32(uint256(2)), _word(gpoOwner));
    }

    /// @notice Runtime code hashes of constructed implementations, standing in for a reference chain.
    function _reference() internal returns (bytes32[5] memory hashes_) {
        hashes_[0] = address(new LegacyMessagePasser()).codehash;
        hashes_[1] = address(new DeployerWhitelist()).codehash;
        hashes_[2] = address(new GasPriceOracle()).codehash;
        hashes_[3] = address(new L1BlockNumber()).codehash;
        hashes_[4] = address(new L1Block()).codehash;
    }

    function test_preFixLayout_reportsZeroVersion() external view {
        address[5] memory t = script.targets();
        for (uint256 i; i < t.length; i++) {
            assertEq(IVersioned(t[i]).version(), "0.0.0", names[i]);
        }
    }

    function test_run_succeeds() external {
        address[5] memory impls = script.run();
        _assertRepaired(impls);
        script.verify();
    }

    function test_runWithReference_succeeds() external {
        bytes32[5] memory ref = _reference();
        address[5] memory impls = script.run(ref);
        _assertRepaired(impls);
        for (uint256 i; i < impls.length; i++) {
            assertEq(impls[i].codehash, ref[i], names[i]);
        }
    }

    function test_runWithReference_mismatch_reverts() external {
        bytes32[5] memory ref = _reference();
        ref[2] = ref[4];
        vm.expectRevert("RepairPredeploys: reference code hash mismatch");
        script.run(ref);
    }

    function test_runWithReference_empty_reverts() external {
        bytes32[5] memory ref;
        vm.expectRevert("RepairPredeploys: empty reference code hash");
        script.run(ref);
    }

    function test_run_skipsHealthyTargets() external {
        // L1Block already points at a constructed implementation (e.g. replaced by a network upgrade).
        address healthy = address(new L1Block());
        vm.prank(owner);
        ProxyAdmin(Predeploys.PROXY_ADMIN).upgrade(payable(Predeploys.L1_BLOCK_ATTRIBUTES), healthy);

        address[5] memory impls = script.run();
        assertEq(impls[4], address(0));
        assertEq(ProxyAdmin(Predeploys.PROXY_ADMIN).getProxyImplementation(Predeploys.L1_BLOCK_ATTRIBUTES), healthy);
        address[5] memory t = script.targets();
        for (uint256 i; i < 4; i++) {
            assertTrue(keccak256(bytes(IVersioned(t[i]).version())) != keccak256("0.0.0"), names[i]);
            assertTrue(impls[i] != address(0), names[i]);
        }
    }

    function test_run_twice_reverts() external {
        script.run();
        vm.expectRevert("RepairPredeploys: nothing to repair");
        script.run();
    }

    function test_run_notOwner_reverts() external {
        vm.store(Predeploys.PROXY_ADMIN, bytes32(0), _word(makeAddr("other owner")));
        vm.expectRevert("RepairPredeploys: sender is not ProxyAdmin owner");
        script.run();
    }

    function test_verify_beforeRepair_reverts() external {
        vm.expectRevert("RepairPredeploys: target still reports 0.0.0");
        script.verify();
    }

    function _assertRepaired(address[5] memory _impls) internal view {
        address[5] memory t = script.targets();
        for (uint256 i; i < t.length; i++) {
            assertEq(ProxyAdmin(Predeploys.PROXY_ADMIN).getProxyImplementation(t[i]), _impls[i], names[i]);
            assertEq(IVersioned(t[i]).version(), IVersioned(_impls[i]).version(), names[i]);
            assertTrue(keccak256(bytes(IVersioned(t[i]).version())) != keccak256("0.0.0"), names[i]);
        }
        IGasPriceOracleState gpo = IGasPriceOracleState(Predeploys.GAS_PRICE_ORACLE);
        assertEq(gpo.tokenRatio(), 4083);
        assertEq(gpo.owner(), gpoOwner);
        assertEq(gpo.operator(), gpoOwner);
        assertEq(ProxyAdmin(Predeploys.PROXY_ADMIN).owner(), owner);
    }

    function _word(address _addr) internal pure returns (bytes32) {
        return bytes32(uint256(uint160(_addr)));
    }
}
