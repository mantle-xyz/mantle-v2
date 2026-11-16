// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Script } from "forge-std/Script.sol";
import { console2 as console } from "forge-std/console2.sol";
import { Proxy } from "src/universal/Proxy.sol";
import { ProxyAdmin } from "src/universal/ProxyAdmin.sol";
import { Predeploys } from "src/libraries/Predeploys.sol";
import { PreconfWhitelist } from "src/L2/PreconfWhitelist.sol";

/// @title DeployPreconfWhitelist
/// @notice Deploys `PreconfWhitelist` on L2 behind a `Proxy`, both through the CREATE2 factory, so
///         the proxy address depends only on the salt and the proxy admin. The same admin and salt
///         give the same address on every chain, whoever broadcasts.
///
///         The admin is a parameter and the script assumes nothing about it. It is baked into the
///         proxy's initcode, so it is part of the address. Initialization (`initialize`, through
///         `upgradeToAndCall` so nobody can take it first) needs that admin's authority:
///           - sender is the admin                             -> the script initializes
///           - admin is the L2 ProxyAdmin, sender is its owner -> the script initializes through it
///           - anything else                                   -> the script deploys, then prints
///                                                                the call the admin must make
///         Re-running with the same arguments resumes: deployed contracts are reused, and an
///         already initialized proxy is only checked.
///
///         The script holds no environment-specific values.
///
/// Entry points (run from packages/contracts-bedrock with `--sender`; pass `--evm-version osaka`,
/// since Mantle L2 does not run amsterdam rules and the default simulation overestimates gas):
///   predictEx(address admin, bytes32 salt)
///       proxy and implementation addresses, without sending anything
///   predict()
///       the same with the sender as admin and a fresh random salt, printed for reuse
///   runEx(address admin, bytes32 salt, address authorizedL1, (address,address)[] initRules)
///       deploy, and initialize when the sender holds the admin's authority
///   run(address authorizedL1, (address,address)[] initRules)
///       the same with the sender as admin and a fresh random salt, printed for reuse. Each call
///       draws a new salt, so it lands at a new address; resume with `runEx` and that salt
///   verify(address admin, bytes32 salt, address authorizedL1)
///       check a deployment against the expected admin and governance address
///
/// Usage. Without `--broadcast`, forge only simulates: it prints the addresses and gas, sends
/// nothing, and ends with "SIMULATION COMPLETE". With it, the transactions are sent; success ends
/// with "ONCHAIN EXECUTION COMPLETE & SUCCESSFUL" after a "verified proxy" line. Sign with one of
/// `--private-key <key>`, `--account <keystore name>` or `--ledger`, and set `--sender` to that
/// signer's address: the script decides from `msg.sender` whether it may initialize. Re-run
/// `runEx` with the same admin and salt to resume a deployment that stopped halfway.
///
///   S=scripts/deploy/DeployPreconfWhitelist.s.sol:DeployPreconfWhitelist
///   RULES='[(0x<from>,0x<to>),(0x<from>,0x0000000000000000000000000000000000000000)]'  # zero = wildcard
///
///   # preview the addresses
///   forge script $S --sig "predictEx(address,bytes32)" $ADMIN $SALT --evm-version osaka
///
///   # simulate; add --broadcast to deploy
///   forge script $S --sig "runEx(address,bytes32,address,(address,address)[])" \
///     $ADMIN $SALT $GOV "$RULES" \
///     --rpc-url $L2_RPC --sender $SENDER --private-key $PK --evm-version osaka
///
///   # deploy with the sender as admin and a random salt; record the printed salt
///   forge script $S --sig "run(address,(address,address)[])" $GOV "$RULES" \
///     --rpc-url $L2_RPC --sender $SENDER --private-key $PK --evm-version osaka --broadcast
///
///   # check the result
///   forge script $S --sig "verify(address,bytes32,address)" $ADMIN $SALT $GOV \
///     --rpc-url $L2_RPC --evm-version osaka
contract DeployPreconfWhitelist is Script {
    /// @notice A suggested salt, so that environments that do not pick their own share an address.
    bytes32 public constant DEFAULT_SALT = keccak256("mantle.preconf.whitelist");

    /// @notice `bytes32(uint256(keccak256('eip1967.proxy.admin')) - 1)`.
    bytes32 internal constant ADMIN_SLOT = 0xb53127684a568b3173ae13b9f8a6016e243e63b6e8ee1178d6a717850b5d6103;

    /// @notice `bytes32(uint256(keccak256('eip1967.proxy.implementation')) - 1)`.
    bytes32 internal constant IMPLEMENTATION_SLOT = 0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc;

    /// @notice Where the proxy and the implementation land for `_admin` and `_salt`.
    function predictEx(address _admin, bytes32 _salt) public pure returns (address proxy_, address implementation_) {
        proxy_ = vm.computeCreate2Address(_salt, keccak256(_proxyInitcode(_admin)), CREATE2_FACTORY);
        implementation_ =
            vm.computeCreate2Address(_salt, keccak256(type(PreconfWhitelist).creationCode), CREATE2_FACTORY);
    }

    /// @notice [`predictEx`] with `msg.sender` as admin and a random salt.
    function predict() public view returns (address proxy_, address implementation_, bytes32 salt_) {
        salt_ = _randomSalt();
        (proxy_, implementation_) = predictEx(msg.sender, salt_);
        console.log("admin          ", msg.sender);
        console.log("proxy          ", proxy_);
        console.log("implementation ", implementation_);
    }

    /// @notice [`runEx`] with `msg.sender` as admin and a random salt.
    function run(
        address _authorizedL1,
        PreconfWhitelist.Rule[] calldata _initRules
    )
        public
        returns (address proxy_, address implementation_, bytes32 salt_)
    {
        salt_ = _randomSalt();
        (proxy_, implementation_) = runEx(msg.sender, salt_, _authorizedL1, _initRules);
    }

    /// @notice Deploys whatever is missing and initializes the proxy if the sender can.
    /// @param _admin        Proxy admin. Fixed into the proxy address; chosen by the operator.
    /// @param _salt         CREATE2 salt, e.g. [`DEFAULT_SALT`].
    /// @param _authorizedL1 The L1 `PreconfWhitelistGov`, unaliased.
    /// @param _initRules    Initial rules, `[(from,to),...]`; a zero half is a wildcard.
    function runEx(
        address _admin,
        bytes32 _salt,
        address _authorizedL1,
        PreconfWhitelist.Rule[] calldata _initRules
    )
        public
        returns (address proxy_, address implementation_)
    {
        require(_admin != address(0), "DeployPreconfWhitelist: admin is the zero address");
        require(_authorizedL1 != address(0), "DeployPreconfWhitelist: authorizedL1 is the zero address");
        require(CREATE2_FACTORY.code.length > 0, "DeployPreconfWhitelist: no CREATE2 factory on this chain");

        (proxy_, implementation_) = predictEx(_admin, _salt);
        console.log("proxy          ", proxy_);
        console.log("implementation ", implementation_);
        console.log("admin          ", _admin);

        vm.startBroadcast(msg.sender);
        if (implementation_.code.length == 0) {
            new PreconfWhitelist{ salt: _salt }();
        }
        if (proxy_.code.length == 0) {
            new Proxy{ salt: _salt }(_admin);
        }
        vm.stopBroadcast();
        require(_slotAddress(proxy_, ADMIN_SLOT) == _admin, "DeployPreconfWhitelist: proxy admin differs");

        address current = _slotAddress(proxy_, IMPLEMENTATION_SLOT);
        if (current != address(0)) {
            console.log("already initialized; implementation", current);
            verify(_admin, _salt, _authorizedL1);
            return (proxy_, implementation_);
        }

        bytes memory init = abi.encodeCall(PreconfWhitelist.initialize, (_authorizedL1, _initRules));
        (address target, bytes memory data, bool canSend) = _initCall(_admin, proxy_, implementation_, init);
        if (!canSend) {
            console.log("sender cannot act for the admin; the admin must send:");
            console.log("  to  ", target);
            console.log("  data");
            console.logBytes(data);
            return (proxy_, implementation_);
        }

        vm.broadcast(msg.sender);
        (bool ok,) = target.call(data);
        require(ok, "DeployPreconfWhitelist: initialization failed");
        verify(_admin, _salt, _authorizedL1);
    }

    /// @notice Reverts unless the proxy for `_admin` and `_salt` is deployed, initialized, still
    ///         administered by `_admin`, and governed by `_authorizedL1`.
    function verify(address _admin, bytes32 _salt, address _authorizedL1) public view {
        (address proxy, address implementation) = predictEx(_admin, _salt);
        PreconfWhitelist wl = PreconfWhitelist(proxy);

        require(proxy.code.length > 0, "DeployPreconfWhitelist: proxy not deployed");
        require(_slotAddress(proxy, ADMIN_SLOT) == _admin, "DeployPreconfWhitelist: proxy admin differs");
        require(_slotAddress(proxy, IMPLEMENTATION_SLOT) != address(0), "DeployPreconfWhitelist: not initialized");
        require(wl.initialized(), "DeployPreconfWhitelist: not initialized");
        require(wl.authorizedL1() == _authorizedL1, "DeployPreconfWhitelist: authorizedL1 differs");
        require(wl.layoutVersion() == wl.LAYOUT_VERSION(), "DeployPreconfWhitelist: layoutVersion differs");

        console.log("verified proxy ", proxy);
        console.log("  implementation", _slotAddress(proxy, IMPLEMENTATION_SLOT));
        if (_slotAddress(proxy, IMPLEMENTATION_SLOT) != implementation) {
            console.log("  (not the implementation this source tree deploys at this salt)");
        }
        console.log("  rules         ", wl.rulesLength());
        console.log("  listsHash");
        console.logBytes32(wl.listsHash());
    }

    /// @notice The call that points the proxy at `_implementation` and initializes it in one step,
    ///         and whether `msg.sender` is allowed to make it.
    function _initCall(
        address _admin,
        address _proxy,
        address _implementation,
        bytes memory _init
    )
        internal
        view
        returns (address target_, bytes memory data_, bool canSend_)
    {
        if (_admin == Predeploys.PROXY_ADMIN) {
            target_ = Predeploys.PROXY_ADMIN;
            data_ = abi.encodeCall(ProxyAdmin.upgradeAndCall, (payable(_proxy), _implementation, _init));
            canSend_ = msg.sender == ProxyAdmin(Predeploys.PROXY_ADMIN).owner();
        } else {
            target_ = _proxy;
            data_ = abi.encodeCall(Proxy.upgradeToAndCall, (_implementation, _init));
            canSend_ = msg.sender == _admin;
        }
    }

    /// @notice A fresh salt, printed: the proxy address depends on it, and resuming or verifying the
    ///         deployment later needs it again.
    function _randomSalt() internal view returns (bytes32 salt_) {
        salt_ = bytes32(vm.randomUint());
        console.log("salt (record it; runEx/verify need it to find this deployment):");
        console.logBytes32(salt_);
    }

    function _proxyInitcode(address _admin) internal pure returns (bytes memory) {
        return abi.encodePacked(type(Proxy).creationCode, abi.encode(_admin));
    }

    function _slotAddress(address _proxy, bytes32 _slot) internal view returns (address) {
        return address(uint160(uint256(vm.load(_proxy, _slot))));
    }
}
