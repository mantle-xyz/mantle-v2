// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Script } from "forge-std/Script.sol";
import { console2 as console } from "forge-std/console2.sol";
import { IProxyAdmin } from "interfaces/universal/IProxyAdmin.sol";
import { Predeploys } from "src/libraries/Predeploys.sol";
import { LegacyMessagePasser } from "src/legacy/LegacyMessagePasser.sol";
import { DeployerWhitelist } from "src/legacy/DeployerWhitelist.sol";
import { GasPriceOracle } from "src/L2/GasPriceOracle.sol";
import { L1BlockNumber } from "src/legacy/L1BlockNumber.sol";
import { L1Block } from "src/L2/L1Block.sol";

interface IVersioned {
    function version() external view returns (string memory);
}

interface IGasPriceOracleState {
    function tokenRatio() external view returns (uint256);
    function owner() external view returns (address);
    function operator() external view returns (address);
    function isArsia() external view returns (bool);
}

interface IL1BlockState {
    function number() external view returns (uint64);
    function timestamp() external view returns (uint64);
    function basefee() external view returns (uint256);
    function hash() external view returns (bytes32);
    function sequenceNumber() external view returns (uint64);
    function batcherHash() external view returns (bytes32);
    function blobBaseFee() external view returns (uint256);
}

/// @title RepairPredeployVersions
/// @notice Repairs L2 predeploys whose implementation was etched at genesis without running its
///         constructor. Before the L2Genesis fix, LegacyMessagePasser, DeployerWhitelist,
///         GasPriceOracle, L1BlockNumber and L1Block were written that way, so their Semver
///         immutables are zero and `version()` returns "0.0.0". The runtime code is otherwise
///         identical to a constructed instance.
///
///         For every target proxy that reports "0.0.0", the script deploys a constructed
///         implementation from this source tree and points the proxy at it through the L2
///         ProxyAdmin. Proxies reporting any other version are skipped, so the script never
///         downgrades a contract. Proxy storage is untouched and checked before and after.
///
///         The script holds no environment-specific values. Chain RPC, sender, and the optional
///         reference code hashes are supplied by the environment runbook.
///
/// Entry points (run from packages/contracts-bedrock, ProxyAdmin owner EOA as --sender). Pass
/// `--evm-version osaka`: Mantle L2 executes Arsia/Osaka rules, and the default `amsterdam`
/// simulation overestimates gas.
///   run()                    deploy and upgrade every "0.0.0" target
///   run(bytes32[5])          same, but require each new implementation to match the given runtime
///                            code hash (order: LegacyMessagePasser, DeployerWhitelist,
///                            GasPriceOracle, L1BlockNumber, L1Block), e.g. read from a reference
///                            chain with `cast codehash`
///   verify()                 check that no target reports "0.0.0" and every implementation
///                            matches this source tree
contract RepairPredeployVersions is Script {
    string internal constant ZERO_VERSION = "0.0.0";
    uint256 internal constant TARGET_COUNT = 5;

    /// @notice Target proxies, in the order used by `run(bytes32[5])`.
    function targets() public pure returns (address[TARGET_COUNT] memory targets_) {
        targets_[0] = Predeploys.LEGACY_MESSAGE_PASSER;
        targets_[1] = Predeploys.DEPLOYER_WHITELIST;
        targets_[2] = Predeploys.GAS_PRICE_ORACLE;
        targets_[3] = Predeploys.L1_BLOCK_NUMBER;
        targets_[4] = Predeploys.L1_BLOCK_ATTRIBUTES;
    }

    /// @notice Repair without a reference check.
    function run() public returns (address[TARGET_COUNT] memory implementations_) {
        bytes32[TARGET_COUNT] memory none;
        implementations_ = _repair(none, false);
    }

    /// @notice Repair and require every new implementation to match the reference runtime code hash.
    function run(bytes32[TARGET_COUNT] memory _expectedCodehashes)
        public
        returns (address[TARGET_COUNT] memory implementations_)
    {
        for (uint256 i; i < TARGET_COUNT; i++) {
            require(_expectedCodehashes[i] != bytes32(0), "RepairPredeploys: empty reference code hash");
        }
        implementations_ = _repair(_expectedCodehashes, true);
    }

    /// @notice Check the active chain. Builds the implementations locally (not broadcast) and
    ///         compares versions and runtime code hashes with the deployed ones.
    function verify() public {
        address[TARGET_COUNT] memory t = targets();
        for (uint256 i; i < TARGET_COUNT; i++) {
            address impl = IProxyAdmin(Predeploys.PROXY_ADMIN).getProxyImplementation(t[i]);
            address built = _build(i);
            require(!_isZeroVersion(t[i]), "RepairPredeploys: target still reports 0.0.0");
            require(impl.codehash == built.codehash, "RepairPredeploys: implementation differs from source");
            require(
                keccak256(bytes(IVersioned(t[i]).version())) == keccak256(bytes(IVersioned(built).version())),
                "RepairPredeploys: version differs from source"
            );
            console.log("OK:", t[i], IVersioned(t[i]).version());
        }
    }

    function _repair(
        bytes32[TARGET_COUNT] memory _expected,
        bool _checkExpected
    )
        internal
        returns (address[TARGET_COUNT] memory impls_)
    {
        address proxyAdmin = Predeploys.PROXY_ADMIN;
        address owner = IProxyAdmin(proxyAdmin).owner();
        require(owner.code.length == 0, "RepairPredeploys: ProxyAdmin owner is a contract");
        (, address sender,) = vm.readCallers();
        require(sender == owner, "RepairPredeploys: sender is not ProxyAdmin owner");

        address[TARGET_COUNT] memory t = targets();
        bool[TARGET_COUNT] memory todo;
        uint256 count;
        for (uint256 i; i < TARGET_COUNT; i++) {
            todo[i] = _isZeroVersion(t[i]);
            if (todo[i]) count++;
            else console.log("Skip (not 0.0.0):", t[i], IVersioned(t[i]).version());
        }
        require(count > 0, "RepairPredeploys: nothing to repair");

        bytes32 gpoState = _gasPriceOracleState();
        bytes32 l1BlockState = _l1BlockState();

        vm.startBroadcast();
        for (uint256 i; i < TARGET_COUNT; i++) {
            if (!todo[i]) continue;
            impls_[i] = _build(i);
            require(!_isZeroVersion(impls_[i]), "RepairPredeploys: new implementation reports 0.0.0");
            if (_checkExpected) {
                require(impls_[i].codehash == _expected[i], "RepairPredeploys: reference code hash mismatch");
            }
            IProxyAdmin(proxyAdmin).upgrade(payable(t[i]), impls_[i]);
            console.log("Upgraded:", t[i], impls_[i]);
            console.logBytes32(impls_[i].codehash);
        }
        vm.stopBroadcast();

        // The upgrade only rewrites the EIP-1967 implementation slot; proxy state must be unchanged.
        require(IProxyAdmin(proxyAdmin).owner() == owner, "RepairPredeploys: owner changed");
        require(_gasPriceOracleState() == gpoState, "RepairPredeploys: GasPriceOracle state changed");
        require(_l1BlockState() == l1BlockState, "RepairPredeploys: L1Block state changed");
        for (uint256 i; i < TARGET_COUNT; i++) {
            require(!_isZeroVersion(t[i]), "RepairPredeploys: target still reports 0.0.0");
            if (todo[i]) {
                require(
                    IProxyAdmin(proxyAdmin).getProxyImplementation(t[i]) == impls_[i],
                    "RepairPredeploys: implementation not set"
                );
            }
        }
    }

    /// @notice Deploy the constructed implementation for target `_index`.
    function _build(uint256 _index) internal returns (address) {
        if (_index == 0) return address(new LegacyMessagePasser());
        if (_index == 1) return address(new DeployerWhitelist());
        if (_index == 2) return address(new GasPriceOracle());
        if (_index == 3) return address(new L1BlockNumber());
        if (_index == 4) return address(new L1Block());
        revert("RepairPredeploys: bad index");
    }

    function _isZeroVersion(address _target) internal view returns (bool) {
        return keccak256(bytes(IVersioned(_target).version())) == keccak256(bytes(ZERO_VERSION));
    }

    function _gasPriceOracleState() internal view returns (bytes32) {
        IGasPriceOracleState gpo = IGasPriceOracleState(Predeploys.GAS_PRICE_ORACLE);
        return keccak256(abi.encode(gpo.tokenRatio(), gpo.owner(), gpo.operator(), gpo.isArsia()));
    }

    function _l1BlockState() internal view returns (bytes32) {
        IL1BlockState l1Block = IL1BlockState(Predeploys.L1_BLOCK_ATTRIBUTES);
        return keccak256(
            abi.encode(
                l1Block.number(),
                l1Block.timestamp(),
                l1Block.basefee(),
                l1Block.hash(),
                l1Block.sequenceNumber(),
                l1Block.batcherHash(),
                l1Block.blobBaseFee()
            )
        );
    }
}
