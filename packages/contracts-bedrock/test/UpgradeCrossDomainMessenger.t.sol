// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Test } from "forge-std/Test.sol";
import {
    UpgradeL1CrossDomainMessenger,
    UpgradeL2CrossDomainMessenger
} from "scripts/upgrade/UpgradeCrossDomainMessenger.s.sol";
import { ProxyAdmin } from "src/universal/ProxyAdmin.sol";
import { Proxy } from "src/universal/Proxy.sol";
import { AddressManager } from "src/legacy/AddressManager.sol";
import { ResolvedDelegateProxy } from "src/legacy/ResolvedDelegateProxy.sol";
import { OptimismPortal } from "src/L1/OptimismPortal.sol";
import { L1CrossDomainMessenger } from "src/L1/L1CrossDomainMessenger.sol";
import { L2CrossDomainMessenger } from "src/L2/L2CrossDomainMessenger.sol";
import { CrossDomainMessenger } from "src/universal/CrossDomainMessenger.sol";

contract UpgradeCrossDomainMessenger_Test is Test {
    UpgradeL1CrossDomainMessenger internal l1Script;
    UpgradeL2CrossDomainMessenger internal l2Script;
    ProxyAdmin internal admin;
    address internal l1Proxy;
    address internal constant L2_PROXY = 0x4200000000000000000000000000000000000007;
    address internal portal;
    address internal token;
    address internal owner;
    bytes32 internal constant SUCCESS_HASH = keccak256("historical success");
    bytes32 internal constant FAILURE_HASH = keccak256("historical failure");

    function setUp() public {
        owner = tx.origin;
        vm.deal(owner, 100 ether);
        l1Script = new UpgradeL1CrossDomainMessenger();
        l2Script = new UpgradeL2CrossDomainMessenger();
        admin = new ProxyAdmin(owner);
        portal = makeAddr("portal");
        token = makeAddr("L1 MNT");
        vm.etch(portal, hex"00");
        vm.etch(token, hex"00");

        AddressManager manager = new AddressManager();
        l1Proxy = address(new ResolvedDelegateProxy(manager, "BVM_L1CrossDomainMessenger"));
        L1CrossDomainMessenger oldL1 = new L1CrossDomainMessenger(OptimismPortal(payable(portal)), token);
        manager.setAddress("BVM_L1CrossDomainMessenger", address(oldL1));
        manager.transferOwnership(address(admin));
        vm.startPrank(owner);
        admin.setAddressManager(manager);
        admin.setProxyType(l1Proxy, ProxyAdmin.ProxyType.RESOLVED);
        admin.setImplementationName(l1Proxy, "BVM_L1CrossDomainMessenger");
        vm.stopPrank();
        L1CrossDomainMessenger(l1Proxy).initialize();

        Proxy template = new Proxy(address(admin));
        vm.etch(L2_PROXY, address(template).code);
        vm.store(
            L2_PROXY, bytes32(uint256(keccak256("eip1967.proxy.admin")) - 1), bytes32(uint256(uint160(address(admin))))
        );
        L2CrossDomainMessenger oldL2 = new L2CrossDomainMessenger(l1Proxy, token);
        vm.prank(owner);
        admin.upgrade(payable(L2_PROXY), address(oldL2));
        L2CrossDomainMessenger(L2_PROXY).initialize();

        _seedHistory(l1Proxy);
        _seedHistory(L2_PROXY);
    }

    function _seedHistory(address _proxy) internal {
        // Seed nontrivial existing state at the audited Messenger layout, including both mappings.
        vm.store(_proxy, bytes32(uint256(206)), bytes32(uint256(37)));
        vm.store(_proxy, keccak256(abi.encode(SUCCESS_HASH, uint256(203))), bytes32(uint256(1)));
        vm.store(_proxy, keccak256(abi.encode(FAILURE_HASH, uint256(207))), bytes32(uint256(1)));
        vm.deal(_proxy, 7);
    }

    function _assertHistory(address _proxy) internal {
        CrossDomainMessenger messenger = CrossDomainMessenger(_proxy);
        assertEq(messenger.messageNonce(), (uint256(1) << 240) | 37);
        assertTrue(messenger.successfulMessages(SUCCESS_HASH));
        assertTrue(messenger.failedMessages(FAILURE_HASH));
        assertFalse(messenger.failedMessages(SUCCESS_HASH));
        assertEq(_proxy.balance, 7);
        assertEq(admin.owner(), owner);
        vm.expectRevert("Initializable: contract is already initialized");
        L1CrossDomainMessenger(_proxy).initialize();
    }

    function test_run_l1Resolved_preservesState() external {
        address old = admin.getProxyImplementation(l1Proxy);
        address implementation = l1Script.run(block.chainid, address(admin), l1Proxy);
        assertTrue(implementation != old);
        assertEq(admin.getProxyImplementation(l1Proxy), implementation);
        assertEq(address(L1CrossDomainMessenger(l1Proxy).PORTAL()), portal);
        _assertHistory(l1Proxy);
    }

    function test_run_l2ERC1967_preservesState() external {
        address implementation = l2Script.run(block.chainid, address(admin), L2_PROXY);
        assertEq(admin.getProxyImplementation(L2_PROXY), implementation);
        assertEq(L2CrossDomainMessenger(L2_PROXY).OTHER_MESSENGER(), l1Proxy);
        _assertHistory(L2_PROXY);
    }

    function test_deployAndPrepare_contractOwner_doesNotUpgrade() external {
        vm.prank(owner);
        admin.transferOwnership(address(this));
        address old = admin.getProxyImplementation(l1Proxy);
        address implementation = l1Script.deployImplementation(block.chainid, address(admin), l1Proxy);
        (address safe, address to, uint256 value, bytes memory data) =
            l1Script.prepareUpgrade(block.chainid, address(admin), l1Proxy, implementation);
        assertEq(safe, address(this));
        assertEq(to, address(admin));
        assertEq(value, 0);
        assertEq(data, abi.encodeCall(ProxyAdmin.upgrade, (payable(l1Proxy), implementation)));
        assertEq(admin.getProxyImplementation(l1Proxy), old);
        // Execute the generated CALL as the contract owner. This does not simulate Safe signatures.
        (bool success,) = to.call(data);
        assertTrue(success);
        l1Script.verify(block.chainid, address(admin), l1Proxy, implementation);
    }

    function test_run_wrongChain_revertsBeforeDeployment() external {
        uint64 nonce = vm.getNonce(owner);
        vm.expectRevert("MessengerUpgrade: wrong chain");
        l1Script.run(block.chainid + 1, address(admin), l1Proxy);
        assertEq(vm.getNonce(owner), nonce);
    }

    function test_run_wrongSender_revertsBeforeDeployment() external {
        vm.prank(owner);
        admin.transferOwnership(makeAddr("different owner"));
        uint64 nonce = vm.getNonce(owner);
        vm.expectRevert("MessengerUpgrade: sender is not ProxyAdmin owner");
        l1Script.run(block.chainid, address(admin), l1Proxy);
        vm.stopBroadcast();
        assertEq(vm.getNonce(owner), nonce);
    }

    function test_run_contractOwner_revertsBeforeDeployment() external {
        vm.prank(owner);
        admin.transferOwnership(address(this));
        vm.expectRevert("MessengerUpgrade: contract owner must execute prepared calldata");
        l1Script.run(block.chainid, address(admin), l1Proxy);
        vm.stopBroadcast();
    }

    function test_run_wrongAdmin_reverts() external {
        ProxyAdmin otherAdmin = new ProxyAdmin(owner);
        vm.expectRevert();
        l2Script.run(block.chainid, address(otherAdmin), L2_PROXY);
    }

    function test_upgrade_wrongPortal_reverts() external {
        address candidate =
            address(new L1CrossDomainMessenger(OptimismPortal(payable(makeAddr("other Portal"))), token));
        vm.expectRevert("MessengerUpgrade: incompatible immutables");
        l1Script.upgrade(block.chainid, address(admin), l1Proxy, candidate);
    }

    function test_upgrade_wrongToken_reverts() external {
        address candidate = address(new L1CrossDomainMessenger(OptimismPortal(payable(portal)), makeAddr("other MNT")));
        vm.expectRevert("MessengerUpgrade: incompatible immutables");
        l1Script.upgrade(block.chainid, address(admin), l1Proxy, candidate);
    }

    function test_upgrade_wrongL1Counterpart_reverts() external {
        address candidate = address(new L2CrossDomainMessenger(makeAddr("other L1 Messenger"), token));
        vm.expectRevert("MessengerUpgrade: incompatible immutables");
        l2Script.upgrade(block.chainid, address(admin), L2_PROXY, candidate);
    }

    function test_upgrade_emptyImplementation_reverts() external {
        vm.expectRevert("MessengerUpgrade: invalid implementation");
        l1Script.upgrade(block.chainid, address(admin), l1Proxy, address(0));
    }

    function test_verify_oldImplementation_reverts() external {
        address candidate = l2Script.deployImplementation(block.chainid, address(admin), L2_PROXY);
        vm.expectRevert("MessengerUpgrade: implementation mismatch");
        l2Script.verify(block.chainid, address(admin), L2_PROXY, candidate);
    }

    function test_upgrade_alreadyUpgraded_reverts() external {
        address candidate = l2Script.run(block.chainid, address(admin), L2_PROXY);
        vm.expectRevert("MessengerUpgrade: already upgraded");
        l2Script.upgrade(block.chainid, address(admin), L2_PROXY, candidate);
    }
}
