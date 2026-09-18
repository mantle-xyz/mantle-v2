// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Script } from "forge-std/Script.sol";
import { console2 as console } from "forge-std/console2.sol";
import { IProxyAdmin } from "interfaces/universal/IProxyAdmin.sol";
import { L1CrossDomainMessenger } from "src/L1/L1CrossDomainMessenger.sol";
import { L2CrossDomainMessenger } from "src/L2/L2CrossDomainMessenger.sol";
import { OptimismPortal } from "src/L1/OptimismPortal.sol";
import { Predeploys } from "src/libraries/Predeploys.sol";

interface IUpgradeMessenger {
    function version() external view returns (string memory);
    function OTHER_MESSENGER() external view returns (address);
    function L1_MNT_ADDRESS() external view returns (address);
    function messageNonce() external view returns (uint256);
    function RELAY_RESERVED_GAS() external view returns (uint64);
    function RELAY_GAS_CHECK_BUFFER() external view returns (uint64);
    function RELAY_NEW_ACCOUNT_OVERHEAD() external view returns (uint64);
    function baseGas(bytes calldata message, uint32 minGasLimit) external view returns (uint64);
}

/// @notice Upgrade the existing Messenger proxy without reinitializing its storage.
/// @dev Select the L1 or L2 derived script explicitly. Each invocation operates on one chain.
///      run/upgrade require an EOA ProxyAdmin owner. For a Safe owner, deployImplementation,
///      prepareUpgrade and verify support separate deployment, Safe execution and verification.
abstract contract UpgradeCrossDomainMessenger is Script {
    /// @notice Deploy an implementation and broadcast its proxy upgrade as two transactions.
    /// @dev Requires the selected Forge sender to own ProxyAdmin before deploying anything.
    function run(uint256 _chainId, address _proxyAdmin, address _proxy) public returns (address implementation_) {
        _validateProxy(_chainId, _proxyAdmin, _proxy);
        vm.startBroadcast();
        _requireOwner(_proxyAdmin);
        implementation_ = _deploy(_proxy);
        vm.stopBroadcast();
        upgrade(_chainId, _proxyAdmin, _proxy, implementation_);
    }

    /// @notice Deploy only; proxy ownership may belong to a different account or a Safe.
    function deployImplementation(
        uint256 _chainId,
        address _proxyAdmin,
        address _proxy
    )
        public
        returns (address implementation_)
    {
        _validateProxy(_chainId, _proxyAdmin, _proxy);
        vm.startBroadcast();
        implementation_ = _deploy(_proxy);
        vm.stopBroadcast();
        _validateImplementation(_proxy, implementation_);
        console.log("New implementation:", implementation_);
        console.log("Runtime codehash:");
        console.logBytes32(implementation_.codehash);
    }

    /// @notice Upgrade to an already deployed implementation using the EOA ProxyAdmin owner.
    function upgrade(uint256 _chainId, address _proxyAdmin, address _proxy, address _implementation) public {
        _validateProxy(_chainId, _proxyAdmin, _proxy);
        _validateImplementation(_proxy, _implementation);
        address oldImplementation = IProxyAdmin(_proxyAdmin).getProxyImplementation(_proxy);
        require(oldImplementation != _implementation, "MessengerUpgrade: already upgraded");
        address owner = IProxyAdmin(_proxyAdmin).owner();
        bytes32 configuration = _configuration(_proxy);
        uint256 nonce = IUpgradeMessenger(_proxy).messageNonce();
        uint256 balance = _proxy.balance;

        console.log("Old implementation:", oldImplementation);
        console.log("New implementation:", _implementation);
        vm.startBroadcast();
        _requireOwner(_proxyAdmin);
        IProxyAdmin(_proxyAdmin).upgrade(payable(_proxy), _implementation);
        vm.stopBroadcast();

        verify(_chainId, _proxyAdmin, _proxy, _implementation);
        require(IProxyAdmin(_proxyAdmin).owner() == owner, "MessengerUpgrade: owner changed");
        require(_configuration(_proxy) == configuration, "MessengerUpgrade: configuration changed");
        require(IUpgradeMessenger(_proxy).messageNonce() == nonce, "MessengerUpgrade: nonce changed");
        require(_proxy.balance == balance, "MessengerUpgrade: balance changed");
    }

    /// @notice Return the single CALL to execute through ProxyAdmin's owner Safe. No broadcast.
    /// @dev Independently review the implementation codehash and simulate the full Safe transaction.
    function prepareUpgrade(
        uint256 _chainId,
        address _proxyAdmin,
        address _proxy,
        address _implementation
    )
        public
        view
        returns (address owner_, address to_, uint256 value_, bytes memory data_)
    {
        _validateProxy(_chainId, _proxyAdmin, _proxy);
        _validateImplementation(_proxy, _implementation);
        require(
            IProxyAdmin(_proxyAdmin).getProxyImplementation(_proxy) != _implementation,
            "MessengerUpgrade: already upgraded"
        );
        owner_ = IProxyAdmin(_proxyAdmin).owner();
        to_ = _proxyAdmin;
        value_ = 0;
        data_ = abi.encodeCall(IProxyAdmin.upgrade, (payable(_proxy), _implementation));
        console.log("Chain ID:", block.chainid);
        console.log("Owner / Safe:", owner_);
        console.log("To (CALL, value=0):", to_);
        console.logBytes(data_);
        console.log("Implementation runtime codehash:");
        console.logBytes32(_implementation.codehash);
    }

    /// @notice Read the actual proxy after receipt confirmation; version alone is insufficient.
    function verify(uint256 _chainId, address _proxyAdmin, address _proxy, address _implementation) public view {
        _validateProxy(_chainId, _proxyAdmin, _proxy);
        require(
            IProxyAdmin(_proxyAdmin).getProxyImplementation(_proxy) == _implementation,
            "MessengerUpgrade: implementation mismatch"
        );
        _validateImplementation(_proxy, _implementation);
        _validateGas(_proxy);
        console.log("Verified Messenger proxy:", _proxy);
        console.log("Implementation:", _implementation);
        console.log("Version:", IUpgradeMessenger(_proxy).version());
    }

    function _validateProxy(uint256 _chainId, address _proxyAdmin, address _proxy) internal view {
        require(block.chainid == _chainId, "MessengerUpgrade: wrong chain");
        require(_proxyAdmin.code.length != 0 && _proxy.code.length != 0, "MessengerUpgrade: missing contract");
        IProxyAdmin admin = IProxyAdmin(_proxyAdmin);
        // Use ProxyAdmin for both ERC1967 and AddressManager-backed RESOLVED proxies.
        // A local sload would read the script's own storage, not the target proxy's storage.
        require(admin.getProxyAdmin(payable(_proxy)) == _proxyAdmin, "MessengerUpgrade: wrong admin");
        require(admin.owner() != address(0), "MessengerUpgrade: missing owner");
        require(admin.getProxyImplementation(_proxy).code.length != 0, "MessengerUpgrade: missing implementation");
        require(IUpgradeMessenger(_proxy).L1_MNT_ADDRESS() != address(0), "MessengerUpgrade: missing MNT");
        require(IUpgradeMessenger(_proxy).OTHER_MESSENGER() != address(0), "MessengerUpgrade: missing counterpart");
        _validateLayer(_proxy);
    }

    function _requireOwner(address _proxyAdmin) internal view {
        address owner = IProxyAdmin(_proxyAdmin).owner();
        require(owner.code.length == 0, "MessengerUpgrade: contract owner must execute prepared calldata");
        (, address sender,) = vm.readCallers();
        require(sender == owner, "MessengerUpgrade: sender is not ProxyAdmin owner");
    }

    function _validateImplementation(address _proxy, address _implementation) internal view {
        require(
            _implementation != _proxy && _implementation.code.length != 0, "MessengerUpgrade: invalid implementation"
        );
        require(_configuration(_proxy) == _configuration(_implementation), "MessengerUpgrade: incompatible immutables");
        _validateGas(_implementation);
    }

    function _validateGas(address _messenger) internal view {
        IUpgradeMessenger messenger = IUpgradeMessenger(_messenger);
        require(keccak256(bytes(messenger.version())) == keccak256("1.6.0"), "MessengerUpgrade: wrong version");
        require(messenger.RELAY_RESERVED_GAS() == 150_000, "MessengerUpgrade: wrong reserved gas");
        require(messenger.RELAY_GAS_CHECK_BUFFER() == 150_000, "MessengerUpgrade: wrong check buffer");
        require(messenger.RELAY_NEW_ACCOUNT_OVERHEAD() == 185_000, "MessengerUpgrade: wrong account overhead");
        require(messenger.baseGas(hex"", 0) == 725_800, "MessengerUpgrade: wrong base gas");
    }

    function _configuration(address _messenger) internal view virtual returns (bytes32);
    function _validateLayer(address _proxy) internal view virtual;
    function _deploy(address _proxy) internal virtual returns (address);
}

/// @notice Forge entry point for the L1 Messenger. Constructor arguments come from its existing proxy.
contract UpgradeL1CrossDomainMessenger is UpgradeCrossDomainMessenger {
    function _configuration(address _messenger) internal view override returns (bytes32) {
        L1CrossDomainMessenger messenger = L1CrossDomainMessenger(_messenger);
        return keccak256(abi.encode(messenger.PORTAL(), messenger.L1_MNT_ADDRESS(), messenger.OTHER_MESSENGER()));
    }

    function _validateLayer(address _proxy) internal view override {
        L1CrossDomainMessenger messenger = L1CrossDomainMessenger(_proxy);
        require(address(messenger.PORTAL()).code.length != 0, "MessengerUpgrade: missing Portal");
        require(messenger.L1_MNT_ADDRESS().code.length != 0, "MessengerUpgrade: missing L1 token");
        require(
            messenger.OTHER_MESSENGER() == Predeploys.L2_CROSS_DOMAIN_MESSENGER,
            "MessengerUpgrade: wrong L2 counterpart"
        );
    }

    function _deploy(address _proxy) internal override returns (address) {
        L1CrossDomainMessenger messenger = L1CrossDomainMessenger(_proxy);
        return address(new L1CrossDomainMessenger(OptimismPortal(messenger.PORTAL()), messenger.L1_MNT_ADDRESS()));
    }
}

/// @notice Forge entry point for the L2 predeploy. L1 addresses are not required to have L2 code.
contract UpgradeL2CrossDomainMessenger is UpgradeCrossDomainMessenger {
    function _configuration(address _messenger) internal view override returns (bytes32) {
        L2CrossDomainMessenger messenger = L2CrossDomainMessenger(_messenger);
        return keccak256(abi.encode(messenger.OTHER_MESSENGER(), messenger.L1_MNT_ADDRESS()));
    }

    function _validateLayer(address _proxy) internal pure override {
        require(_proxy == Predeploys.L2_CROSS_DOMAIN_MESSENGER, "MessengerUpgrade: wrong L2 predeploy");
    }

    function _deploy(address _proxy) internal override returns (address) {
        L2CrossDomainMessenger messenger = L2CrossDomainMessenger(_proxy);
        return address(new L2CrossDomainMessenger(messenger.OTHER_MESSENGER(), messenger.L1_MNT_ADDRESS()));
    }
}
