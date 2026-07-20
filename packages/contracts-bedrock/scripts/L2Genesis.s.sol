// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

// Testing
import { EIP1967Helper } from "test/mocks/EIP1967Helper.sol";

// Scripts
import { Script } from "forge-std/Script.sol";
import { OutputMode, OutputModeUtils, MantleFork, MantleForkUtils } from "scripts/libraries/Config.sol";
import { SetPreinstalls } from "scripts/SetPreinstalls.s.sol";
import { DeployUtils } from "scripts/libraries/DeployUtils.sol";

// Libraries
import { Predeploys } from "src/libraries/Predeploys.sol";
import { Constants } from "src/libraries/Constants.sol";

// Interfaces
import { IFeeVault } from "interfaces/L2/IFeeVault.sol";
import { IOptimismMintableERC20Factory } from "interfaces/universal/IOptimismMintableERC20Factory.sol";
import { IOptimismMintableERC721Factory } from "interfaces/universal/IOptimismMintableERC721Factory.sol";
import { IProxy } from "interfaces/universal/IProxy.sol";
import { IGasPriceOracle } from "interfaces/L2/IGasPriceOracle.sol";
import { IL1Block } from "interfaces/L2/IL1Block.sol";
import { IL2CrossDomainMessenger } from "interfaces/L2/IL2CrossDomainMessenger.sol";
import { IL2ERC721Bridge } from "interfaces/L2/IL2ERC721Bridge.sol";
import { IL2StandardBridge } from "interfaces/L2/IL2StandardBridge.sol";
import { IL2ToL1MessagePasser } from "interfaces/L2/IL2ToL1MessagePasser.sol";
import { ISequencerFeeVault } from "interfaces/L2/ISequencerFeeVault.sol";

/// @title L2Genesis
/// @notice Generates the genesis state for the Mantle L2 network.
///         The following safety invariants are used when setting state:
///         1. `vm.getDeployedBytecode` can only be used with `vm.etch` when there are no side
///         effects in the constructor and no immutables in the bytecode.
///         2. A contract must be deployed using the `new` syntax if there are immutables in the code.
///         Any other side effects from the init code besides setting the immutables must be cleaned up afterwards.
contract L2Genesis is Script {
    struct Input {
        uint256 l1ChainID;
        uint256 l2ChainID;
        address payable l1CrossDomainMessengerProxy;
        address payable l1StandardBridgeProxy;
        address payable l1ERC721BridgeProxy;
        address l1MNTAddress;
        address opChainProxyAdminOwner;
        address sequencerFeeVaultRecipient;
        address baseFeeVaultRecipient;
        address l1FeeVaultRecipient;
        address operatorFeeVaultRecipient;
        address gasPriceOracleOwner;
        uint256 gasPriceOracleTokenRatio;
        uint256 mantleFork;
        bool fundDevAccounts;
    }

    using MantleForkUtils for MantleFork;
    using OutputModeUtils for OutputMode;

    uint256 internal constant PRECOMPILE_COUNT = 256;
    uint80 internal constant DEV_ACCOUNT_FUND_AMT = 10_000 ether;
    uint256 internal constant DEFAULT_GAS_PRICE_ORACLE_TOKEN_RATIO = 3000;

    /// @notice Default Anvil dev accounts. Only funded if `cfg.fundDevAccounts == true`.
    /// Also known as "test test test test test test test test test test test junk" mnemonic accounts,
    /// on path "m/44'/60'/0'/0/i" (where i is the account index).
    address[30] internal devAccounts = [
        0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266, // 0
        0x70997970C51812dc3A010C7d01b50e0d17dc79C8, // 1
        0x3C44CdDdB6a900fa2b585dd299e03d12FA4293BC, // 2
        0x90F79bf6EB2c4f870365E785982E1f101E93b906, // 3
        0x15d34AAf54267DB7D7c367839AAf71A00a2C6A65, // 4
        0x9965507D1a55bcC2695C58ba16FB37d819B0A4dc, // 5
        0x976EA74026E726554dB657fA54763abd0C3a0aa9, // 6
        0x14dC79964da2C08b23698B3D3cc7Ca32193d9955, // 7
        0x23618e81E3f5cdF7f54C3d65f7FBc0aBf5B21E8f, // 8
        0xa0Ee7A142d267C1f36714E4a8F75612F20a79720, // 9
        0xBcd4042DE499D14e55001CcbB24a551F3b954096, // 10
        0x71bE63f3384f5fb98995898A86B02Fb2426c5788, // 11
        0xFABB0ac9d68B0B445fB7357272Ff202C5651694a, // 12
        0x1CBd3b2770909D4e10f157cABC84C7264073C9Ec, // 13
        0xdF3e18d64BC6A983f673Ab319CCaE4f1a57C7097, // 14
        0xcd3B766CCDd6AE721141F452C550Ca635964ce71, // 15
        0x2546BcD3c84621e976D8185a91A922aE77ECEc30, // 16
        0xbDA5747bFD65F08deb54cb465eB87D40e51B197E, // 17
        0xdD2FD4581271e230360230F9337D5c0430Bf44C0, // 18
        0x8626f6940E2eb28930eFb4CeF49B2d1F2C9C1199, // 19
        0x09DB0a93B389bEF724429898f539AEB7ac2Dd55f, // 20
        0x02484cb50AAC86Eae85610D6f4Bf026f30f6627D, // 21
        0x08135Da0A343E492FA2d4282F2AE34c6c5CC1BbE, // 22
        0x5E661B79FE2D3F6cE70F5AAC07d8Cd9abb2743F1, // 23
        0x61097BA76cD906d2ba4FD106E757f7Eb455fc295, // 24
        0xDf37F81dAAD2b0327A0A50003740e1C935C70913, // 25
        0x553BC17A05702530097c3677091C5BB47a3a7931, // 26
        0x87BdCE72c06C21cd96219BD8521bDF1F42C78b5e, // 27
        0x40Fc963A729c542424cD800349a7E4Ecc4896624, // 28
        0x9DCCe783B6464611f38631e6C851bf441907c710 // 29
    ];

    /// @notice Main entry point for L2 genesis generation.
    function run(Input memory _input) public {
        address deployer = makeAddr("deployer");
        vm.startPrank(deployer);
        vm.chainId(_input.l2ChainID);

        dealEthToPrecompiles();
        setPredeployProxies(_input);
        setPredeployImplementations(_input);
        setPreinstalls();

        if (_input.fundDevAccounts) {
            fundDevAccounts();
        }
        vm.stopPrank();
        vm.deal(deployer, 0);
        vm.resetNonce(deployer);

        // Activate Mantle fork if specified
        MantleFork fork = MantleFork(_input.mantleFork);

        if (fork == MantleFork.NONE) {
            return;
        }

        if (fork == MantleFork.MANTLE_EVEREST) {
            return;
        }

        if (fork == MantleFork.MANTLE_EUBOEA) {
            return;
        }

        if (fork == MantleFork.MANTLE_SKADI) {
            return;
        }

        if (fork == MantleFork.MANTLE_LIMB) {
            return;
        }

        activateMantleArsia();

        if (fork == MantleFork.MANTLE_ARSIA) {
            return;
        }
    }

    /// @notice Give all of the precompiles 1 wei
    function dealEthToPrecompiles() internal {
        for (uint256 i; i < PRECOMPILE_COUNT; i++) {
            vm.deal(address(uint160(i)), 1);
        }
    }

    /// @notice Set up the accounts that correspond to the predeploys.
    ///         The Proxy bytecode should be set. All proxied predeploys should have
    ///         the 1967 admin slot set to the ProxyAdmin predeploy. All defined predeploys
    ///         should have their implementations set.
    ///         Warning: the predeploy accounts have contract code, but 0 nonce value, contrary
    ///         to the expected nonce of 1 per EIP-161. This is because the legacy go genesis
    //          script didn't set the nonce and we didn't want to change that behavior when
    ///         migrating genesis generation to Solidity.
    function setPredeployProxies(Input memory _input) internal {
        bytes memory code = vm.getDeployedCode("Proxy.sol:Proxy");

        uint160 prefix = uint160(0x420) << 148;

        for (uint256 i = 0; i < Predeploys.PREDEPLOY_COUNT; i++) {
            address addr = address(prefix | uint160(i));
            if (Predeploys.notProxied(addr)) {
                continue;
            }

            vm.etch(addr, code);
            EIP1967Helper.setAdmin(addr, Predeploys.PROXY_ADMIN);

            if (Predeploys.isSupportedPredeploy(addr, _input.mantleFork)) {
                address implementation = Predeploys.predeployToCodeNamespace(addr);
                EIP1967Helper.setImplementation(addr, implementation);
            }
        }

        // // Set proxies for contracts that are proxied
        // address[15] memory proxiedPredeploys = [
        //     Predeploys.L2_TO_L1_MESSAGE_PASSER,
        //     Predeploys.DEPLOYER_WHITELIST,
        //     Predeploys.L2_CROSS_DOMAIN_MESSENGER,
        //     Predeploys.GAS_PRICE_ORACLE,
        //     Predeploys.L2_STANDARD_BRIDGE,
        //     Predeploys.SEQUENCER_FEE_WALLET,
        //     Predeploys.OPTIMISM_MINTABLE_ERC20_FACTORY,
        //     Predeploys.L1_BLOCK_NUMBER,
        //     Predeploys.L2_ERC721_BRIDGE,
        //     Predeploys.L1_BLOCK_ATTRIBUTES,
        //     Predeploys.OPTIMISM_MINTABLE_ERC721_FACTORY,
        //     Predeploys.PROXY_ADMIN,
        //     Predeploys.BASE_FEE_VAULT,
        //     Predeploys.L1_FEE_VAULT,
        //     Predeploys.LEGACY_MESSAGE_PASSER
        // ];

        // bytes32 adminSlot = bytes32(uint256(keccak256("eip1967.proxy.admin")) - 1);
        // bytes32 implSlot = bytes32(uint256(keccak256("eip1967.proxy.implementation")) - 1);

        // for (uint256 i = 0; i < proxiedPredeploys.length; i++) {
        //     address addr = proxiedPredeploys[i];
        //     vm.etch(addr, code);
        //     // Set admin to ProxyAdmin
        //     vm.store(addr, adminSlot, bytes32(uint256(uint160(Predeploys.PROXY_ADMIN))));

        //     // Set implementation to code namespace
        //     address implementation = _predeployToCodeNamespace(addr);
        //     vm.store(addr, implSlot, bytes32(uint256(uint160(implementation))));
        // }
    }

    /// @notice Sets all the implementations for the predeploy proxies. For contracts without proxies,
    ///      sets the deployed bytecode at their expected predeploy address.
    function setPredeployImplementations(Input memory _input) internal {
        setLegacyMessagePasser(); // 0
        // 01: legacy, not used in OP-Stack
        setDeployerWhitelist(); // 2
        // 3,4,5: legacy, not used in OP-Stack.
        setBVM_ETH(); // BVM_ETH - Mantle specific
        setL2CrossDomainMessenger(_input); // 7
        setGasPriceOracle(_input); // f
        setL2StandardBridge(_input); // 10
        setSequencerFeeVault(_input); // 11
        setOptimismMintableERC20Factory(); // 12
        setL1BlockNumber(); // 13
        setL2ERC721Bridge(_input); // 14
        setL1Block(); // 15
        setL2ToL1MessagePasser(_input); // 16
        setOptimismMintableERC721Factory(_input); // 17
        setProxyAdmin(_input); // 18
        setBaseFeeVault(_input); // 19
        setL1FeeVault(_input); // 1A
        setOperatorFeeVault(_input); // 1B
    }

    function setProxyAdmin(Input memory _input) internal {
        // Note the ProxyAdmin implementation itself is behind a proxy that owns itself.
        address impl = _setImplementationCode(Predeploys.PROXY_ADMIN);

        bytes32 _ownerSlot = bytes32(0);

        // there is no initialize() function, so we just set the storage manually.
        vm.store(Predeploys.PROXY_ADMIN, _ownerSlot, bytes32(uint256(uint160(_input.opChainProxyAdminOwner))));
        // update the proxy to not be uninitialized (although not standard initialize pattern)
        vm.store(impl, _ownerSlot, bytes32(uint256(uint160(_input.opChainProxyAdminOwner))));
    }

    function setL2ToL1MessagePasser(Input memory _input) internal {
        // L2ToL1MessagePasser has an immutable L1_MNT_ADDRESS
        address vault = DeployUtils.create1({
            _name: "L2ToL1MessagePasser",
            _args: DeployUtils.encodeConstructor(
                abi.encodeCall(IL2ToL1MessagePasser.__constructor__, (_input.l1MNTAddress))
            )
        });

        address impl = Predeploys.predeployToCodeNamespace(Predeploys.L2_TO_L1_MESSAGE_PASSER);
        vm.etch(impl, address(vault).code);

        /// Reset so its not included state dump
        vm.etch(address(vault), "");
        vm.resetNonce(address(vault));
    }

    /// @notice This predeploy is following the safety invariant #1.
    function setL2CrossDomainMessenger(Input memory _input) internal {
        // L2CrossDomainMessenger has immutables
        address messenger = DeployUtils.create1({
            _name: "L2CrossDomainMessenger",
            _args: DeployUtils.encodeConstructor(
                abi.encodeCall(
                    IL2CrossDomainMessenger.__constructor__,
                    (address(_input.l1CrossDomainMessengerProxy), _input.l1MNTAddress)
                )
            )
        });

        address impl = Predeploys.predeployToCodeNamespace(Predeploys.L2_CROSS_DOMAIN_MESSENGER);
        vm.etch(impl, address(messenger).code);

        // Initialize xDomainMsgSender to DEFAULT_L2_SENDER (0xdead) - slot 204
        // This is required for relayMessage to work, otherwise the re-entrancy check fails
        vm.store(
            Predeploys.L2_CROSS_DOMAIN_MESSENGER,
            bytes32(uint256(204)),
            bytes32(uint256(uint160(Constants.DEFAULT_L2_SENDER)))
        );

        /// Reset so its not included state dump
        vm.etch(address(messenger), "");
        vm.resetNonce(address(messenger));
    }

    /// @notice This predeploy is following the safety invariant #1.
    function setL2StandardBridge(Input memory _input) internal {
        // L2StandardBridge has immutables
        address bridge = DeployUtils.create1({
            _name: "L2StandardBridge",
            _args: DeployUtils.encodeConstructor(
                abi.encodeCall(
                    IL2StandardBridge.__constructor__, (payable(_input.l1StandardBridgeProxy), _input.l1MNTAddress)
                )
            )
        });

        address impl = Predeploys.predeployToCodeNamespace(Predeploys.L2_STANDARD_BRIDGE);
        vm.etch(impl, address(bridge).code);

        /// Reset so its not included state dump
        vm.etch(address(bridge), "");
        vm.resetNonce(address(bridge));
    }

    /// @notice This predeploy is following the safety invariant #1.
    function setL2ERC721Bridge(Input memory _input) internal {
        // L2ERC721Bridge has immutables
        address bridge = DeployUtils.create1({
            _name: "L2ERC721Bridge",
            _args: DeployUtils.encodeConstructor(
                abi.encodeCall(
                    IL2ERC721Bridge.__constructor__,
                    (address(Predeploys.L2_CROSS_DOMAIN_MESSENGER), payable(_input.l1ERC721BridgeProxy))
                )
            )
        });

        address impl = Predeploys.predeployToCodeNamespace(Predeploys.L2_ERC721_BRIDGE);
        vm.etch(impl, address(bridge).code);

        /// Reset so its not included state dump
        vm.etch(address(bridge), "");
        vm.resetNonce(address(bridge));
    }

    /// @notice This predeploy is following the safety invariant #2,
    function setSequencerFeeVault(Input memory _input) internal {
        address vault = DeployUtils.create1({
            _name: "SequencerFeeVault",
            _args: DeployUtils.encodeConstructor(
                abi.encodeCall(ISequencerFeeVault.__constructor__, (_input.sequencerFeeVaultRecipient))
            )
        });

        address impl = Predeploys.predeployToCodeNamespace(Predeploys.SEQUENCER_FEE_WALLET);
        vm.etch(impl, address(vault).code);

        /// Reset so its not included state dump
        vm.etch(address(vault), "");
        vm.resetNonce(address(vault));
    }

    /// @notice This predeploy is following the safety invariant #1.
    function setOptimismMintableERC20Factory() internal {
        // OptimismMintableERC20Factory has immutables
        address factory = DeployUtils.create1({
            _name: "OptimismMintableERC20Factory",
            _args: DeployUtils.encodeConstructor(
                abi.encodeCall(IOptimismMintableERC20Factory.__constructor__, (Predeploys.L2_STANDARD_BRIDGE))
            )
        });

        address impl = Predeploys.predeployToCodeNamespace(Predeploys.OPTIMISM_MINTABLE_ERC20_FACTORY);
        vm.etch(impl, address(factory).code);

        /// Reset so its not included state dump
        vm.etch(address(factory), "");
        vm.resetNonce(address(factory));
    }

    /// @notice This predeploy is following the safety invariant #2,
    function setOptimismMintableERC721Factory(Input memory _input) internal {
        address factory = DeployUtils.create1({
            _name: "OptimismMintableERC721Factory",
            _args: DeployUtils.encodeConstructor(
                abi.encodeCall(
                    IOptimismMintableERC721Factory.__constructor__, (Predeploys.L2_ERC721_BRIDGE, _input.l1ChainID)
                )
            )
        });

        address impl = Predeploys.predeployToCodeNamespace(Predeploys.OPTIMISM_MINTABLE_ERC721_FACTORY);
        vm.etch(impl, address(factory).code);

        /// Reset so its not included state dump
        vm.etch(address(factory), "");
        vm.resetNonce(address(factory));
    }

    /// @notice This predeploy is following the safety invariant #1.
    function setL1Block() internal {
        // Note: L1 block attributes are set to 0.
        // Before the first user-tx the state is overwritten with actual L1 attributes.
        _setImplementationCode(Predeploys.L1_BLOCK_ATTRIBUTES);
    }

    /// @notice This predeploy is following the safety invariant #1.
    function setGasPriceOracle(Input memory _input) internal {
        _setImplementationCode(Predeploys.GAS_PRICE_ORACLE);

        // Set owner and operator directly in storage
        // owner is at slot 1, operator is at slot 2
        bytes32 ownerSlot = bytes32(uint256(1));
        bytes32 operatorSlot = bytes32(uint256(2));

        // Set in proxy (storage is only used in proxy, not in implementation)
        vm.store(Predeploys.GAS_PRICE_ORACLE, ownerSlot, bytes32(uint256(uint160(_input.gasPriceOracleOwner))));
        vm.store(Predeploys.GAS_PRICE_ORACLE, operatorSlot, bytes32(uint256(uint160(_input.gasPriceOracleOwner))));

        // Set initial token ratio
        uint256 tokenRatio = _input.gasPriceOracleTokenRatio;
        if (tokenRatio == 0) {
            tokenRatio = DEFAULT_GAS_PRICE_ORACLE_TOKEN_RATIO;
        }
        vm.store(Predeploys.GAS_PRICE_ORACLE, bytes32(uint256(0)), bytes32(tokenRatio));
    }

    /// @notice This predeploy is following the safety invariant #1.
    function setDeployerWhitelist() internal {
        _setImplementationCode(Predeploys.DEPLOYER_WHITELIST);
    }

    /// @notice This predeploy is following the safety invariant #1.
    function setL1BlockNumber() internal {
        _setImplementationCode(Predeploys.L1_BLOCK_NUMBER);
    }

    /// @notice This predeploy is following the safety invariant #1.
    function setLegacyMessagePasser() internal {
        _setImplementationCode(Predeploys.LEGACY_MESSAGE_PASSER);
    }

    /// @notice Sets BVM_ETH at its predeploy address (Mantle specific)
    function setBVM_ETH() internal {
        // BVM_ETH has immutables. Deploy a real instance so the constructor patches
        // them into the runtime code, then etch that code at the predeploy address.
        // name()/symbol() are constant overrides and do not require storage copying.
        address bvmEth = DeployUtils.create1({ _name: "BVM_ETH.sol:BVM_ETH", _args: "" });
        vm.etch(Predeploys.BVM_ETH, address(bvmEth).code);
        vm.etch(bvmEth, "");
        vm.resetNonce(bvmEth);
    }

    /// @notice This predeploy is following the safety invariant #2.
    function setBaseFeeVault(Input memory _input) internal {
        address vault = DeployUtils.create1({
            _name: "BaseFeeVault",
            _args: DeployUtils.encodeConstructor(abi.encodeCall(IFeeVault.__constructor__, (_input.baseFeeVaultRecipient)))
        });

        address impl = Predeploys.predeployToCodeNamespace(Predeploys.BASE_FEE_VAULT);
        vm.etch(impl, address(vault).code);

        /// Reset so its not included state dump
        vm.etch(address(vault), "");
        vm.resetNonce(address(vault));
    }

    /// @notice This predeploy is following the safety invariant #2.
    function setL1FeeVault(Input memory _input) internal {
        address vault = DeployUtils.create1({
            _name: "L1FeeVault",
            _args: DeployUtils.encodeConstructor(abi.encodeCall(IFeeVault.__constructor__, (_input.l1FeeVaultRecipient)))
        });

        address impl = Predeploys.predeployToCodeNamespace(Predeploys.L1_FEE_VAULT);
        vm.etch(impl, address(vault).code);

        /// Reset so its not included state dump
        vm.etch(address(vault), "");
        vm.resetNonce(address(vault));
    }

    /// @notice This predeploy is following the safety invariant #2.
    function setOperatorFeeVault(Input memory _input) internal {
        address vault = DeployUtils.create1({
            _name: "OperatorFeeVault",
            _args: DeployUtils.encodeConstructor(
                abi.encodeCall(IFeeVault.__constructor__, (_input.operatorFeeVaultRecipient))
            )
        });

        address impl = Predeploys.predeployToCodeNamespace(Predeploys.OPERATOR_FEE_VAULT);
        vm.etch(impl, address(vault).code);

        /// Reset so its not included state dump
        vm.etch(address(vault), "");
        vm.resetNonce(address(vault));
    }

    /// @notice Sets all the preinstalls.
    function setPreinstalls() internal {
        address tmpSetPreinstalls = address(uint160(uint256(keccak256("SetPreinstalls"))));
        vm.etch(tmpSetPreinstalls, vm.getDeployedCode("SetPreinstalls.s.sol:SetPreinstalls"));
        SetPreinstalls(tmpSetPreinstalls).setPreinstalls();
        vm.etch(tmpSetPreinstalls, "");
    }

    /// @notice Sets the bytecode in state
    function _setImplementationCode(address _addr) internal returns (address) {
        string memory cname = Predeploys.getName(_addr);
        address impl = Predeploys.predeployToCodeNamespace(_addr);
        vm.etch(impl, vm.getDeployedCode(string.concat(cname, ".sol:", cname)));
        return impl;
    }

    /// @notice Activate Mantle Arsia network upgrade.
    ///         This calls setArsia() on the GasPriceOracle predeploy.
    function activateMantleArsia() internal {
        vm.prank(IL1Block(Predeploys.L1_BLOCK_ATTRIBUTES).DEPOSITOR_ACCOUNT());
        IGasPriceOracle(Predeploys.GAS_PRICE_ORACLE).setArsia();
    }

    /// @notice Funds the default dev accounts with ether
    function fundDevAccounts() internal {
        for (uint256 i; i < devAccounts.length; i++) {
            vm.deal(devAccounts[i], DEV_ACCOUNT_FUND_AMT);
        }
    }

    // /// @notice Returns the name of the predeploy at the given address.
    // function _getPredeployName(address _addr) internal pure returns (string memory) {
    //     if (_addr == Predeploys.LEGACY_MESSAGE_PASSER) return "LegacyMessagePasser";
    //     if (_addr == Predeploys.DEPLOYER_WHITELIST) return "DeployerWhitelist";
    //     if (_addr == Predeploys.L2_CROSS_DOMAIN_MESSENGER) return "L2CrossDomainMessenger";
    //     if (_addr == Predeploys.GAS_PRICE_ORACLE) return "GasPriceOracle";
    //     if (_addr == Predeploys.L2_STANDARD_BRIDGE) return "L2StandardBridge";
    //     if (_addr == Predeploys.SEQUENCER_FEE_WALLET) return "SequencerFeeVault";
    //     if (_addr == Predeploys.OPTIMISM_MINTABLE_ERC20_FACTORY) return "OptimismMintableERC20Factory";
    //     if (_addr == Predeploys.L1_BLOCK_NUMBER) return "L1BlockNumber";
    //     if (_addr == Predeploys.L2_ERC721_BRIDGE) return "L2ERC721Bridge";
    //     if (_addr == Predeploys.L1_BLOCK_ATTRIBUTES) return "L1Block";
    //     if (_addr == Predeploys.L2_TO_L1_MESSAGE_PASSER) return "L2ToL1MessagePasser";
    //     if (_addr == Predeploys.OPTIMISM_MINTABLE_ERC721_FACTORY) return "OptimismMintableERC721Factory";
    //     if (_addr == Predeploys.PROXY_ADMIN) return "ProxyAdmin";
    //     if (_addr == Predeploys.BASE_FEE_VAULT) return "BaseFeeVault";
    //     if (_addr == Predeploys.L1_FEE_VAULT) return "L1FeeVault";
    //     if (_addr == Predeploys.BVM_ETH) return "BVM_ETH";
    //     revert("Predeploy name not found");
    // }

    // /// @notice Returns the predeploy implementation address for the given predeploy.
    // function _predeployToCodeNamespace(address _addr) internal pure returns (address) {
    //     uint160 prefix = uint160(0xc0D3C0d3C0d3C0D3c0d3C0d3c0D3C0d3c0d30000);
    //     return address(prefix | uint160(_addr));
    // }
}
