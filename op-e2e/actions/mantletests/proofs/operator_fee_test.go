package proofs

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum-optimism/optimism/op-chain-ops/genesis"
	"github.com/ethereum-optimism/optimism/op-core/predeploys"
	actionsHelpers "github.com/ethereum-optimism/optimism/op-e2e/actions/helpers"
	"github.com/ethereum-optimism/optimism/op-e2e/actions/mantletests/proofs/helpers"
	"github.com/ethereum-optimism/optimism/op-e2e/mantlebindings"
	"github.com/ethereum-optimism/optimism/op-node/rollup/derive"
	"github.com/ethereum-optimism/optimism/op-service/eth"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

func Test_ProgramAction_OperatorFeeConsistency(gt *testing.T) {
	type testCase int64

	const (
		NormalTx testCase = iota
		DepositTx
		StateRefund
		NotEnoughFundsInBatchMissingOpFee
		IsthmusTransitionBlock
	)

	testStorageUpdateContractAddress := common.HexToAddress("0xffffffff")
	// contract TestSetter {
	//   uint x;
	//   function set(uint _x) public { x = _x; }
	// }
	// The deployed bytecode below is from the contract above
	testStorageUpdateContractCode := common.FromHex("0x6080604052348015600e575f80fd5b50600436106026575f3560e01c806360fe47b114602a575b5f80fd5b60406004803603810190603c9190607d565b6042565b005b805f8190555050565b5f80fd5b5f819050919050565b605f81604f565b81146068575f80fd5b50565b5f813590506077816058565b92915050565b5f60208284031215608f57608e604b565b5b5f609a84828501606b565b9150509291505056fea26469706673582212201712a1e6e9c5e2ba1f8f7403f5d6e00090c6fa2b70c632beea4be8009331bd2064736f6c63430008190033")

	runJovianDerivationTest := func(gt *testing.T, testCfg *helpers.TestCfg[testCase]) {
		t := actionsHelpers.NewDefaultTesting(gt)
		deployConfigOverrides := func(dp *genesis.DeployConfig) {
			dp.UseCustomGasToken = true
			dp.GasPayingTokenName = "MNT"
			dp.GasPayingTokenSymbol = "MNT"
			dp.NativeAssetLiquidityAmount = (*hexutil.Big)(new(big.Int).Mul(big.NewInt(2000), big.NewInt(1e18)))
			// Set tokenRatio to 1 to ensure L1 fee calculation uses ratio of 1:1
			// (contract defaults to 3000 if set to 0, which would significantly affect fee calculations)
			dp.GasPriceOracleTokenRatio = 1
		}

		var testOperatorFeeScalar uint32
		var testOperatorFeeConstant uint64
		if testCfg.Custom == NotEnoughFundsInBatchMissingOpFee {
			testOperatorFeeScalar = 0
			testOperatorFeeConstant = 0xffff
		} else {
			testOperatorFeeScalar = 100e6
			testOperatorFeeConstant = 500
		}

		if testCfg.Custom == StateRefund {
			testCfg.Allocs = actionsHelpers.DefaultAlloc
			testCfg.Allocs.L2Alloc = make(map[common.Address]types.Account)
			testCfg.Allocs.L2Alloc[testStorageUpdateContractAddress] = types.Account{
				Code:    testStorageUpdateContractCode,
				Nonce:   1,
				Balance: new(big.Int),
			}
		}

		if testCfg.Custom == IsthmusTransitionBlock {
			deployConfigOverrides = func(dp *genesis.DeployConfig) {
				// Include common Mantle config
				dp.UseCustomGasToken = true
				dp.GasPayingTokenName = "MNT"
				dp.GasPayingTokenSymbol = "MNT"
				dp.NativeAssetLiquidityAmount = (*hexutil.Big)(new(big.Int).Mul(big.NewInt(2000), big.NewInt(1e18)))
				// Set tokenRatio to 1 to ensure L1 fee calculation uses ratio of 1:1
				dp.GasPriceOracleTokenRatio = 1
				// IsthmusTransitionBlock specific: Arsia activates at offset 13
				dp.L1PragueTimeOffset = ptr(hexutil.Uint64(0))
				dp.L2GenesisMantleArsiaTimeOffset = ptr(hexutil.Uint64(13))
			}
		}

		env := helpers.NewL2ProofEnv(t, testCfg, helpers.NewTestParams(), helpers.NewBatcherCfg(
			func(c *actionsHelpers.BatcherCfg) {
				c.ForceSubmitSingularBatch = true
			},
		), deployConfigOverrides)

		balanceAt := func(a common.Address) *big.Int {
			t.Helper()
			bal, err := env.Engine.EthClient().BalanceAt(t.Ctx(), a, nil)
			require.NoError(t, err)
			return bal
		}

		getCurrentBalances := func() (alice *big.Int, l1FeeVault *big.Int, baseFeeVault *big.Int, sequencerFeeVault *big.Int, operatorFeeVault *big.Int) {
			alice = balanceAt(env.Alice.Address())
			l1FeeVault = balanceAt(predeploys.L1FeeVaultAddr)
			baseFeeVault = balanceAt(predeploys.BaseFeeVaultAddr)
			sequencerFeeVault = balanceAt(predeploys.SequencerFeeVaultAddr)
			operatorFeeVault = balanceAt(predeploys.OperatorFeeVaultAddr)

			return alice, l1FeeVault, baseFeeVault, sequencerFeeVault, operatorFeeVault
		}

		setStorageInUpdateContractTo := func(value byte) {
			t.Helper()
			input := common.RightPadBytes(common.FromHex("0x60fe47b1"), 36)
			input[35] = value
			env.Sequencer.ActL2StartBlock(t)
			env.Alice.L2.ActResetTxOpts(t)
			env.Alice.L2.ActSetTxToAddr(&testStorageUpdateContractAddress)(t)
			env.Alice.L2.ActSetTxCalldata(input)(t)
			env.Alice.L2.ActMakeTx(t)
			env.Engine.ActL2IncludeTx(env.Alice.Address())(t)
			env.Sequencer.ActL2EndBlock(t)
			r := env.Alice.L2.LastTxReceipt(t)
			require.Equal(t, types.ReceiptStatusSuccessful, r.Status, "tx unsuccessful")
		}

		// For IsthmusTransitionBlock, we set operator fee AFTER MantleArsia activation
		// For other cases, set operator fee at the beginning
		if testCfg.Custom != IsthmusTransitionBlock {
			sysCfgContract, err := mantlebindings.NewSystemConfig(env.Sd.RollupCfg.L1SystemConfigAddress, env.Miner.EthClient())
			require.NoError(t, err)

			sysCfgOwner, err := bind.NewKeyedTransactorWithChainID(env.Dp.Secrets.Deployer, env.Sd.RollupCfg.L1ChainID)
			require.NoError(t, err)

			// Update the operator fee parameters
			_, err = sysCfgContract.SetOperatorFeeScalars(sysCfgOwner, testOperatorFeeScalar, testOperatorFeeConstant)
			require.NoError(t, err)

			env.Miner.ActL1StartBlock(12)(t)
			env.Miner.ActL1IncludeTx(env.Dp.Addresses.Deployer)(t)
			l1BlockWithOpFee := env.Miner.ActL1EndBlock(t)
			t.Logf("Set operator fee in L1 block: number=%d, hash=%s", l1BlockWithOpFee.Number(), l1BlockWithOpFee.Hash().Hex())

			// sequence L2 blocks, and submit with new batcher
			env.Sequencer.ActL1HeadSignal(t)
			env.Sequencer.ActBuildToL1Head(t)
			env.BatchAndMineMantle(t)

			env.Sequencer.ActL1HeadSignal(t)
		}

		var aliceInitialBalance *big.Int
		var baseFeeVaultInitialBalance *big.Int
		var l1FeeVaultInitialBalance *big.Int
		var sequencerFeeVaultInitialBalance *big.Int
		var operatorFeeVaultInitialBalance *big.Int

		var receipt *types.Receipt

		switch testCfg.Custom {
		case NormalTx, IsthmusTransitionBlock:
			aliceInitialBalance, l1FeeVaultInitialBalance, baseFeeVaultInitialBalance, sequencerFeeVaultInitialBalance, operatorFeeVaultInitialBalance = getCurrentBalances()

			require.Equal(t, operatorFeeVaultInitialBalance.Sign(), 0)

			if testCfg.Custom == IsthmusTransitionBlock {
				// For Isthmus transition block test:
				// Check if Isthmus activation is also Jovian activation
				// If so, we need to:
				// 1. Build empty Jovian/Isthmus activation block (no user txs allowed)
				// 2. Build next block with user transaction (which should have operator fee)

				// Get Isthmus activation time
				isArsiaTime := *env.Sd.RollupCfg.MantleArsiaTime
				isArsiaActivation := env.Sd.RollupCfg.IsMantleArsiaActivationBlock(isArsiaTime)

				if isArsiaActivation {
					// Build empty blocks until we reach Arsia activation block
					for env.Sequencer.L2Unsafe().Time < isArsiaTime {
						env.Sequencer.ActL2EmptyBlock(t)
					}

					// Verify we're at the activation block
					require.Equal(t, isArsiaTime, env.Sequencer.L2Unsafe().Time)
					require.True(t, env.Sd.RollupCfg.IsMantleArsiaActivationBlock(env.Sequencer.L2Unsafe().Time))
					t.Logf("Reached MantleArsia activation block: L2 time=%d", env.Sequencer.L2Unsafe().Time)

					// Batch and mine the activation block to L1 first
					env.BatchAndMineMantle(t)

					sysCfgContract, err := mantlebindings.NewSystemConfig(env.Sd.RollupCfg.L1SystemConfigAddress, env.Miner.EthClient())
					require.NoError(t, err)

					sysCfgOwner, err := bind.NewKeyedTransactorWithChainID(env.Dp.Secrets.Deployer, env.Sd.RollupCfg.L1ChainID)
					require.NoError(t, err)

					// Update the operator fee parameters
					_, err = sysCfgContract.SetOperatorFeeScalars(sysCfgOwner, testOperatorFeeScalar, testOperatorFeeConstant)
					require.NoError(t, err)

					env.Miner.ActL1StartBlock(12)(t)
					env.Miner.ActL1IncludeTx(env.Dp.Addresses.Deployer)(t)
					l1BlockWithOpFee := env.Miner.ActL1EndBlock(t)
					t.Logf("Set operator fee in L1 block: number=%d, hash=%s", l1BlockWithOpFee.Number(), l1BlockWithOpFee.Hash().Hex())

					// Sync L2 to the new L1 head so it picks up the operator fee config
					env.Sequencer.ActL1HeadSignal(t)
					env.Sequencer.ActBuildToL1Head(t)

					// Update initial balances after operator fee is set
					aliceInitialBalance, l1FeeVaultInitialBalance, baseFeeVaultInitialBalance, sequencerFeeVaultInitialBalance, operatorFeeVaultInitialBalance = getCurrentBalances()

					// Now build the next block with a user transaction
					// This block should have operator fee applied
					env.Sequencer.ActL2PipelineFull(t) // Ensure pipeline is idle before building
					env.Sequencer.ActL2StartBlock(t)
					env.Alice.L2.ActResetTxOpts(t)
					env.Alice.L2.ActSetTxToAddr(&env.Dp.Addresses.Bob)(t)
					env.Alice.L2.ActMakeTx(t)
					env.Engine.ActL2IncludeTx(env.Alice.Address())(t)
					env.Sequencer.ActL2EndBlock(t)
				} else {
					// Not a Jovian activation block, can include user tx in activation block
					// Build empty blocks until we're one block before activation
					for env.Sequencer.L2Unsafe().Time+2 < isArsiaTime {
						env.Sequencer.ActL2EmptyBlock(t)
					}

					// Now build the activation block with a user transaction
					env.Sequencer.ActL2StartBlock(t)
					env.Alice.L2.ActResetTxOpts(t)
					env.Alice.L2.ActSetTxToAddr(&env.Dp.Addresses.Bob)(t)
					env.Alice.L2.ActMakeTx(t)
					env.Engine.ActL2IncludeTxIgnoreForcedEmpty(env.Alice.Address())(t)
					env.Sequencer.ActL2EndBlock(t)

					require.True(t, env.Sd.RollupCfg.IsMantleArsiaActivationBlock(env.Sequencer.L2Unsafe().Time))
				}
			} else {
				// Normal tx case
				env.Sequencer.ActL2StartBlock(t)
				env.Alice.L2.ActResetTxOpts(t)
				env.Alice.L2.ActSetTxToAddr(&env.Dp.Addresses.Bob)(t)
				env.Alice.L2.ActMakeTx(t)
				env.Engine.ActL2IncludeTx(env.Alice.Address())(t)
				env.Sequencer.ActL2EndBlock(t)
			}

		case StateRefund:
			setStorageInUpdateContractTo(1)
			rSet := env.Alice.L2.LastTxReceipt(t)
			require.Equal(t, uint64(43696), rSet.GasUsed)
			aliceInitialBalance, l1FeeVaultInitialBalance, baseFeeVaultInitialBalance, sequencerFeeVaultInitialBalance, operatorFeeVaultInitialBalance = getCurrentBalances()
			setStorageInUpdateContractTo(0)
			rUnset := env.Alice.L2.LastTxReceipt(t)
			// we assert on the exact gas used to show that a refund is happening
			require.Equal(t, uint64(21784), rUnset.GasUsed)

		case DepositTx:
			// Mantle-specific deposit transaction (6 parameters)
			// Get portal address from deploy config
			portalAddr := env.Dp.DeployConfig.OptimismPortalProxy
			t.Logf("OptimismPortal address: %s", portalAddr.Hex())

			// Create Mantle OptimismPortal binding
			mantlePortal, err := mantlebindings.NewOptimismPortal(portalAddr, env.Miner.EthClient())
			require.NoError(t, err, "failed to create Mantle OptimismPortal binding")

			// Check if portal is paused
			isPaused, err := mantlePortal.Paused(&bind.CallOpts{})
			require.NoError(t, err, "failed to check if portal is paused")
			t.Logf("OptimismPortal paused status: %v", isPaused)
			require.False(t, isPaused, "OptimismPortal should not be paused")

			// Check Alice's L1 balance
			aliceL1Balance, err := env.Miner.EthClient().BalanceAt(context.Background(), env.Dp.Addresses.Alice, nil)
			require.NoError(t, err, "failed to get Alice's L1 balance")
			t.Logf("Alice's L1 balance: %s", aliceL1Balance.String())

			// Check SystemConfig ResourceConfig
			systemConfigAddr := env.Sd.RollupCfg.L1SystemConfigAddress
			systemConfig, err := mantlebindings.NewSystemConfig(systemConfigAddr, env.Miner.EthClient())
			require.NoError(t, err, "failed to create SystemConfig binding")
			resourceConfig, err := systemConfig.ResourceConfig(&bind.CallOpts{})
			require.NoError(t, err, "failed to get ResourceConfig")
			t.Logf("ResourceConfig: %+v", resourceConfig)

			// Check L1 MNT address
			l1MNTAddr, err := mantlePortal.L1MNTADDRESS(&bind.CallOpts{})
			require.NoError(t, err, "failed to get L1 MNT address")
			t.Logf("L1 MNT address: %s", l1MNTAddr.Hex())
			require.NotEqual(t, common.Address{}, l1MNTAddr, "L1 MNT address should not be zero")

			// Prepare deposit parameters
			toAddr := env.Dp.Addresses.Bob
			depositGas := uint64(2e6)

			// Create L1 transaction options using Alice's private key
			aliceTxOpts, err := bind.NewKeyedTransactorWithChainID(env.Dp.Secrets.Alice, env.Sd.RollupCfg.L1ChainID)
			require.NoError(t, err)
			// Note: msg.value must be >= _ethTxValue to ensure L2 tx has enough ETH
			ethValue := big.NewInt(1)    // 1 wei to send
			aliceTxOpts.Value = ethValue // ETH sent to portal on L1 (msg.value)
			aliceTxOpts.NoSend = true    // Don't auto-send, we'll send manually

			// Execute deposit transaction (Mantle 7-parameter version)
			// Solidity: function depositTransaction(uint256 _ethTxValue, uint256 _mntValue, address _to, uint256 _mntTxValue, uint64 _gasLimit, bool _isCreation, bytes _data) payable
			// Parameters:
			//   _ethTxValue: ETH value to use in the L2 transaction (must be <= msg.value)
			//   _mntValue: MNT to transfer from L1 sender to portal
			//   _to: target address on L2
			//   _mntTxValue: MNT value to send in L2 transaction
			//   _gasLimit: minimum L2 gas limit
			//   _isCreation: whether this is a contract creation
			//   _data: calldata for L2 transaction
			tx, err := mantlePortal.DepositTransaction(
				aliceTxOpts,
				ethValue,      // _ethTxValue: ETH value in L2 transaction (must match msg.value)
				big.NewInt(0), // _mntValue: MNT to transfer from L1 to portal
				toAddr,        // _to: target address on L2
				big.NewInt(0), // _mntTxValue: MNT value to send in L2 transaction
				depositGas,    // _gasLimit: minimum L2 gas limit
				false,         // _isCreation: not a contract creation
				[]byte{},      // _data: empty calldata
			)
			require.NoError(t, err, "failed to create deposit tx")

			// Send the deposit transaction on L1
			err = env.Miner.EthClient().SendTransaction(t.Ctx(), tx)
			require.NoError(t, err, "must send deposit tx")

			// Mine the L1 block containing the deposit
			env.Miner.ActL1StartBlock(12)(t)
			env.Miner.ActL1IncludeTx(env.Alice.Address())(t)
			env.Miner.ActL1EndBlock(t)

			// Sync sequencer and build enough blocks to adopt latest L1 origin
			env.Sequencer.ActL1HeadSignal(t)
			env.Sequencer.ActBuildToL1HeadUnsafe(t)

			// Get the deposit receipt on L2
			// We need to manually reconstruct the deposit from the L1 receipt
			l1Receipt, err := env.Miner.EthClient().TransactionReceipt(t.Ctx(), tx.Hash())
			require.NoError(t, err)
			require.Equal(t, types.ReceiptStatusSuccessful, l1Receipt.Status)

			// Reconstruct the L2 deposit transaction
			reconstructedDep, err := derive.UnmarshalDepositLogEvent(l1Receipt.Logs[0])
			require.NoError(t, err, "Could not reconstruct L2 Deposit")
			l2Tx := types.NewTx(reconstructedDep)

			// Get L2 receipt
			receipt, err = env.Engine.EthClient().TransactionReceipt(t.Ctx(), l2Tx.Hash())
			require.NoError(t, err)
			require.Equal(t, types.ReceiptStatusSuccessful, receipt.Status)
			aliceInitialBalance, l1FeeVaultInitialBalance, baseFeeVaultInitialBalance, sequencerFeeVaultInitialBalance, operatorFeeVaultInitialBalance = getCurrentBalances()

			// regular Deposit, in new L1 block (using direct portal call like above)
			toAddr2 := env.Dp.Addresses.Bob
			depositGas2 := uint64(2e6)
			ethValue2 := big.NewInt(0) // No ETH value for this deposit

			aliceTxOpts2, err := bind.NewKeyedTransactorWithChainID(env.Dp.Secrets.Alice, env.Sd.RollupCfg.L1ChainID)
			require.NoError(t, err)
			aliceTxOpts2.Value = ethValue2 // ETH sent to portal on L1 (msg.value)
			aliceTxOpts2.NoSend = true     // Don't auto-send, we'll send manually

			// Execute deposit transaction (Mantle 7-parameter version)
			tx2, err := mantlePortal.DepositTransaction(
				aliceTxOpts2,
				ethValue2,     // _ethTxValue: ETH value in L2 transaction
				big.NewInt(0), // _mntValue: MNT to transfer from L1 to portal
				toAddr2,       // _to: target address on L2
				big.NewInt(0), // _mntTxValue: MNT value to send in L2 transaction
				depositGas2,   // _gasLimit: minimum L2 gas limit
				false,         // _isCreation: not a contract creation
				[]byte{},      // _data: empty calldata
			)
			require.NoError(t, err, "failed to create second deposit tx")

			// Send the deposit transaction on L1
			err = env.Miner.EthClient().SendTransaction(t.Ctx(), tx2)
			require.NoError(t, err, "must send second deposit tx")

			// Mine the L1 block containing the deposit
			env.Miner.ActL1StartBlock(12)(t)
			env.Miner.ActL1IncludeTx(env.Alice.Address())(t)
			env.Miner.ActL1EndBlock(t)

			// sync sequencer build enough blocks to adopt latest L1 origin
			env.Sequencer.ActL1HeadSignal(t)
			env.Sequencer.ActBuildToL1HeadUnsafe(t)

			// Check deposit status and get L2 receipt manually (since we didn't use ActDeposit)
			depositL1Receipt := env.Alice.L1.CheckReceipt(t, true, tx2.Hash())
			require.NotNil(t, depositL1Receipt, "L1 deposit receipt should exist")
			require.Greater(t, len(depositL1Receipt.Logs), 0, "L1 deposit receipt should have logs")

			// Reconstruct the L2 deposit transaction from L1 receipt
			reconstructedDep, err = derive.UnmarshalDepositLogEvent(depositL1Receipt.Logs[0])
			require.NoError(t, err, "Could not reconstruct L2 Deposit")
			l2DepositTx := types.NewTx(reconstructedDep)

			// Get L2 receipt
			receipt = env.Alice.L2.CheckReceipt(t, true, l2DepositTx.Hash())
			require.NotNil(t, receipt, "L2 deposit receipt should exist")
		case NotEnoughFundsInBatchMissingOpFee:
			pkey, err := crypto.GenerateKey()
			require.NoError(t, err)
			address := crypto.PubkeyToAddress(pkey.PublicKey)

			// Send `address` just enough ETH to cover the gas costs of the transaction, sans the operator fee.
			// Since we're just doing a simple call to `Bob`, that should be 21000 gas at the current gas price
			// plus the L1 data fee.

			// Craft a transaction from Alice -> Bob (just to compute L1 cost, not to send.)
			env.Alice.L2.ActResetTxOpts(t)
			env.Alice.L2.ActSetTxToAddr(&env.Dp.Addresses.Bob)(t)
			tx := env.Alice.L2.MakeTransaction(t)

			rlp, err := tx.MarshalBinary()
			require.NoError(t, err, "failed to RLP encode transaction")

			unsafeHeader := env.Engine.L2Chain().CurrentHeader()
			unsafeBlock := env.Engine.L2Chain().GetBlockByHash(unsafeHeader.Hash())
			nextBaseFee := eip1559.CalcBaseFee(
				env.Sd.L2Cfg.Config,
				unsafeHeader,
			)

			l1BlockInfo, err := derive.L1BlockInfoFromBytes(env.Sd.RollupCfg, unsafeHeader.Time, unsafeBlock.Transactions()[0].Data())
			require.NoError(t, err)

			daCost := fjordL1Cost(l1BlockInfo, types.NewRollupCostData(rlp))
			// Calculate gas fee only (baseFee * gasLimit), excluding L1 data fee
			// Note: L1Fee is 0 in Mantle after certain upgrades, so we only need gas fee
			// to test that the address has enough for gas but not for gas + operator fee
			gasFeeOnly := new(big.Int).Mul(nextBaseFee, big.NewInt(int64(params.TxGas)))

			// Include an L2 tx, from Bob -> mock signer
			// Send only enough to cover gas fee, not operator fee
			env.Bob.L2.ActResetTxOpts(t)
			env.Bob.L2.ActSetTxToAddr(&address)(t)
			env.Bob.L2.ActSetTxValue(gasFeeOnly)(t)
			env.Bob.L2.ActMakeTx(t)

			env.Sequencer.ActL2StartBlock(t)
			env.Engine.ActL2IncludeTx(env.Bob.Address())(t)
			env.Sequencer.ActL2EndBlock(t)
			env.Bob.L2.ActCheckReceiptStatusOfLastTx(true)(t)

			// Ensure the mock signer received the funds
			require.Equal(t, gasFeeOnly, balanceAt(address))

			// DEBUG: Log the expected fee and address balance
			t.Logf("DEBUG: nextBaseFee = %v", nextBaseFee)
			t.Logf("DEBUG: daCost (L1 data fee from fjordL1Cost, without tokenRatio) = %v", daCost)
			t.Logf("DEBUG: l1BlockInfo.BaseFee = %v", l1BlockInfo.BaseFee)
			t.Logf("DEBUG: l1BlockInfo.BlobBaseFee = %v", l1BlockInfo.BlobBaseFee)
			t.Logf("DEBUG: l1BlockInfo.BaseFeeScalar = %v", l1BlockInfo.BaseFeeScalar)
			t.Logf("DEBUG: l1BlockInfo.BlobBaseFeeScalar = %v", l1BlockInfo.BlobBaseFeeScalar)
			// Check tokenRatio from state - this is the key for L1Fee calculation
			tokenRatioFromState := env.Engine.L2Chain().CurrentBlock().BaseFee
			statedb, _ := env.Engine.L2Chain().StateAt(env.Engine.L2Chain().CurrentBlock())
			if statedb != nil {
				tokenRatioSlot := statedb.GetState(common.HexToAddress("0x420000000000000000000000000000000000000F"), common.Hash{})
				t.Logf("DEBUG: tokenRatio from GasPriceOracle state (slot 0) = %v", tokenRatioSlot.Big())
			}
			_ = tokenRatioFromState
			t.Logf("DEBUG: gas fee only (nextBaseFee * TxGas) = %v", gasFeeOnly)
			t.Logf("DEBUG: address = %v", address)
			t.Logf("DEBUG: address balance after Bob's transfer = %v", balanceAt(address))
			t.Logf("DEBUG: testOperatorFeeConstant = %v", testOperatorFeeConstant)
			t.Logf("DEBUG: testOperatorFeeScalar = %v", testOperatorFeeScalar)
			expectedTotalCost := new(big.Int).Add(gasFeeOnly, new(big.Int).SetUint64(testOperatorFeeConstant))
			t.Logf("DEBUG: expected total cost with operator fee = %v", expectedTotalCost)

			// Buffer the L2 block we just included
			env.Batcher.ActL2BatchBuffer(t)

			aliceInitialBalance, l1FeeVaultInitialBalance, baseFeeVaultInitialBalance, sequencerFeeVaultInitialBalance, operatorFeeVaultInitialBalance = getCurrentBalances()

			// Craft a transaction from Alice -> Bob
			// Use the same gas price as used in expectedFeePreIsthmus calculation,
			// so that when re-signed by `address`, the total cost (gas + operator fee)
			// exceeds the balance that `address` received.
			env.Alice.L2.ActResetTxOpts(t)
			env.Alice.L2.ActSetTxToAddr(&env.Dp.Addresses.Bob)(t)
			env.Alice.L2.ActSetTxGasLimit(params.TxGas)(t)
			env.Alice.L2.ActSetGasFeeCap(nextBaseFee)(t)
			env.Alice.L2.ActSetGasTipCap(nextBaseFee)(t)
			env.Alice.L2.ActMakeTx(t)

			// Include an L2 tx, from Alice -> Bob
			env.Sequencer.ActL2StartBlock(t)
			env.Engine.ActL2IncludeTx(env.Alice.Address())(t)
			env.Sequencer.ActL2EndBlock(t)
			env.Alice.L2.ActCheckReceiptStatusOfLastTx(true)(t)

			// Instruct the batcher to submit a faulty channel, with Alice's tx re-signed by a new private key.
			// This key will have 0 balance.
			env.Batcher.ActL2BatchBuffer(t, actionsHelpers.WithBlockModifier(func(block *types.Block) *types.Block {
				txs := block.Transactions()

				// Skip over any L2 blocks that don't contain user-space txs.
				if len(txs) == 1 {
					return block
				}

				// Re-sign Alice's tx with a random key.
				require.NoError(t, err, "error generating random private key")
				signer := types.LatestSigner(env.Sd.L2Cfg.Config)
				newSignedTx, err := types.SignTx(txs[1], signer, pkey)
				require.NoError(t, err, "error re-signing Alice's transaction")

				// Replace Alice's tx with the re-signed one.
				txs[1] = newSignedTx
				return block
			}))
			env.Batcher.ActL2ChannelClose(t)
			env.Batcher.ActL2BatchSubmitMantle(t)

			// Include the batcher transaction.
			env.Miner.ActL1StartBlock(12)(t)
			env.Miner.ActL1IncludeTxByHash(env.Batcher.LastSubmitted.Hash())(t)
			env.Miner.ActL1EndBlock(t)
		}

		aliceFinalBalance, l1FeeVaultFinalBalance, baseFeeVaultFinalBalance, sequencerFeeVaultFinalBalance, operatorFeeVaultFinalBalance := getCurrentBalances()
		l2UnsafeHead := env.Engine.L2Chain().CurrentHeader()

		if receipt == nil {
			receipt = env.Alice.L2.LastTxReceipt(t)
		}

		// Determine if we should expect operator fee based on:
		// 1. DepositTx: never has operator fee
		// 2. IsthmusTransitionBlock with Jovian activation: tx is AFTER activation, should have operator fee
		// 3. IsthmusTransitionBlock without Jovian activation: tx is IN activation block, no operator fee
		shouldHaveOperatorFee := true
		if testCfg.Custom == DepositTx {
			shouldHaveOperatorFee = false
		} else if testCfg.Custom == IsthmusTransitionBlock {
			// Check if Isthmus activation was also Jovian activation
			// If so, the user tx is in the block AFTER activation and should have operator fee
			isMantleArsia := *env.Sd.RollupCfg.MantleArsiaTime
			isArsiaActivation := env.Sd.RollupCfg.IsMantleArsiaActivationBlock(isMantleArsia)
			shouldHaveOperatorFee = isArsiaActivation
		}

		if !shouldHaveOperatorFee {
			require.Nil(t, receipt.OperatorFeeScalar)
			require.Nil(t, receipt.OperatorFeeConstant)

			// Nothing should has been sent to operator fee vault
			require.Equal(t, operatorFeeVaultInitialBalance, operatorFeeVaultFinalBalance)
		} else if env.Sd.RollupCfg.IsMantleArsia(l2UnsafeHead.Time) {
			// Check that the operator fee was applied
			t.Logf("Receipt OperatorFeeScalar: %v (expected: %v)", *receipt.OperatorFeeScalar, testOperatorFeeScalar)
			t.Logf("Receipt OperatorFeeConstant: %v (expected: %v)", *receipt.OperatorFeeConstant, testOperatorFeeConstant)
			require.Equal(t, testOperatorFeeScalar, uint32(*receipt.OperatorFeeScalar))
			require.Equal(t, testOperatorFeeConstant, *receipt.OperatorFeeConstant)

			// Check that the operator fee sent to the vault is correct
			// Determine which formula to use based on whether Jovian is active
			var expectedOperatorFee *big.Int
			if env.Sd.RollupCfg.IsJovian(l2UnsafeHead.Time) {
				// Jovian formula: (gasUsed * operatorFeeScalar * 100) + operatorFeeConstant
				expectedOperatorFee = new(big.Int).Add(
					new(big.Int).Mul(
						new(big.Int).Mul(
							new(big.Int).SetUint64(receipt.GasUsed),
							new(big.Int).SetUint64(uint64(testOperatorFeeScalar)),
						),
						new(big.Int).SetUint64(100),
					),
					new(big.Int).SetUint64(testOperatorFeeConstant),
				)
			} else {
				// Isthmus formula: (gasUsed * operatorFeeScalar / 1e6) + operatorFeeConstant
				expectedOperatorFee = new(big.Int).Add(
					new(big.Int).Div(
						new(big.Int).Mul(
							new(big.Int).SetUint64(receipt.GasUsed),
							new(big.Int).SetUint64(uint64(testOperatorFeeScalar)),
						),
						new(big.Int).SetUint64(1e6),
					),
					new(big.Int).SetUint64(testOperatorFeeConstant),
				)
			}

			require.Equal(t,
				expectedOperatorFee,
				new(big.Int).Sub(operatorFeeVaultFinalBalance, operatorFeeVaultInitialBalance),
			)
		}

		if testCfg.Custom == DepositTx {
			require.Equal(t, aliceInitialBalance, aliceFinalBalance, "Alice's balance shouldn't have changed")
		} else {
			require.True(t, aliceFinalBalance.Cmp(aliceInitialBalance) < 0, "Alice's balance should decrease")
		}

		// Check that no Ether has been minted or burned
		finalTotalBalance := new(big.Int).Add(
			aliceFinalBalance,
			new(big.Int).Add(
				new(big.Int).Add(
					new(big.Int).Sub(l1FeeVaultFinalBalance, l1FeeVaultInitialBalance),
					new(big.Int).Sub(sequencerFeeVaultFinalBalance, sequencerFeeVaultInitialBalance),
				),
				new(big.Int).Add(
					new(big.Int).Sub(operatorFeeVaultFinalBalance, operatorFeeVaultInitialBalance),
					new(big.Int).Sub(baseFeeVaultFinalBalance, baseFeeVaultInitialBalance),
				),
			),
		)

		require.Equal(t, aliceInitialBalance, finalTotalBalance)

		// The NotEnoughFundsInBatchMissingOpFee case is special, as it submits its own invalid batch.
		if testCfg.Custom != NotEnoughFundsInBatchMissingOpFee {
			env.BatchAndMineMantle(t)
		}
		env.Sequencer.ActL1HeadSignal(t)
		env.Sequencer.ActL2PipelineFull(t)

		l2SafeHead := env.Engine.L2Chain().CurrentSafeBlock()

		if testCfg.Custom == NotEnoughFundsInBatchMissingOpFee {
			// DEBUG: Log derivation results
			t.Logf("DEBUG: l2UnsafeHead = %v (number: %v)", eth.HeaderBlockID(l2UnsafeHead), l2UnsafeHead.Number)
			t.Logf("DEBUG: l2SafeHead = %v (number: %v)", eth.HeaderBlockID(l2SafeHead), l2SafeHead.Number)

			safeHeadBlock := env.Engine.L2Chain().GetBlockByHash(l2SafeHead.Hash())
			t.Logf("DEBUG: safeHeadBlock tx count = %d", len(safeHeadBlock.Transactions()))
			signer := types.LatestSigner(env.Sd.L2Cfg.Config)
			for i, tx := range safeHeadBlock.Transactions() {
				sender, _ := types.Sender(signer, tx)
				t.Logf("DEBUG: safeHeadBlock tx[%d] hash=%v, type=%v, sender=%v, gasPrice=%v, gasLimit=%v, value=%v",
					i, tx.Hash(), tx.Type(), sender, tx.GasPrice(), tx.Gas(), tx.Value())
			}
			t.Logf("DEBUG: Alice address = %v", env.Alice.Address())

			// Check the receipt of the second transaction
			if len(safeHeadBlock.Transactions()) > 1 {
				tx := safeHeadBlock.Transactions()[1]
				receipts := env.Engine.L2Chain().GetReceiptsByHash(safeHeadBlock.Hash())
				if len(receipts) > 1 {
					r := receipts[1]
					t.Logf("DEBUG: tx[1] receipt gasUsed=%v, effectiveGasPrice=%v, status=%v",
						r.GasUsed, r.EffectiveGasPrice, r.Status)
					t.Logf("DEBUG: tx[1] receipt L1GasUsed=%v, L1GasPrice=%v, L1Fee=%v",
						r.L1GasUsed, r.L1GasPrice, r.L1Fee)
					t.Logf("DEBUG: tx[1] receipt operatorFeeScalar=%v, operatorFeeConstant=%v",
						r.OperatorFeeScalar, r.OperatorFeeConstant)
				}
				// Check sender balance in safe block state
				statedb, _ := env.Engine.L2Chain().StateAt(safeHeadBlock.Header())
				if statedb != nil {
					sender, _ := types.Sender(signer, tx)
					t.Logf("DEBUG: sender %v balance after tx = %v", sender, statedb.GetBalance(sender))
				}
			}

			// The unsafe block prior to derivation should be different from the safe block after derivation. The
			// batcher posted the block but with a different transaction, signed by a key that has no balance. This
			// should cause a reorg in the unsafe chain, and the original block should be reduced to deposits only
			// if Isthmus is active.
			require.NotEqual(t, eth.HeaderBlockID(l2SafeHead), eth.HeaderBlockID(l2UnsafeHead), "derivation should not lead to the same block")

			reorgedUnsafe := env.Engine.L2Chain().CurrentHeader()
			require.Equal(t, eth.HeaderBlockID(l2SafeHead), eth.HeaderBlockID(reorgedUnsafe), "reorged unsafe block is the same")

			if env.Sd.RollupCfg.IsMantleArsia(l2SafeHead.Time) {
				require.Equal(t, 1, len(safeHeadBlock.Transactions()))

				// Ensure that the logs contain a mention of the block being replaced _due to the signer not having enough
				// balance_. The address has 21000 wei (gas fee for TxGas only), but needs more for gasLimit + operator fee + L1 fee.
				// Note: The exact "want" value depends on L1 fee (affected by tokenRatio, baseFeeScalar, etc.),
				// so we only verify the error type and that the address has exactly 21000 wei.
				require.NotNil(t, env.Logs.FindLog(testlog.NewAttributesContainsFilter("err", "insufficient funds for gas * price + value")))
				require.NotNil(t, env.Logs.FindLog(testlog.NewAttributesContainsFilter("err", "have 21000 want")))
			} else {
				require.Equal(t, 2, len(safeHeadBlock.Transactions()))
			}
		} else {
			require.Equal(t, eth.HeaderBlockID(l2SafeHead), eth.HeaderBlockID(l2UnsafeHead), "derivation leads to the same block")
		}

		//env.RunFaultProofProgramFromGenesis(t, l2SafeHead.Number.Uint64(), testCfg.CheckResult, testCfg.InputParams...)
	}

	matrix := helpers.NewMatrix[testCase]()
	matrix.AddDefaultTestCasesWithName("NormalTx", NormalTx, helpers.NewForkMatrix(helpers.MantleArsia), runJovianDerivationTest)
	matrix.AddDefaultTestCasesWithName("DepositTx", DepositTx, helpers.NewForkMatrix(helpers.MantleArsia), runJovianDerivationTest)
	matrix.AddDefaultTestCasesWithName("StateRefund", StateRefund, helpers.NewForkMatrix(helpers.MantleArsia), runJovianDerivationTest)
	matrix.AddDefaultTestCasesWithName("NotEnoughFundsInBatchMissingOpFee", NotEnoughFundsInBatchMissingOpFee, helpers.NewForkMatrix(helpers.MantleArsia), runJovianDerivationTest)
	matrix.AddDefaultTestCasesWithName("IsthmusTransitionBlock", IsthmusTransitionBlock, helpers.NewForkMatrix(helpers.MantleLimb), runJovianDerivationTest)
	matrix.Run(gt)
}

func fjordL1Cost(l1BlockInfo *derive.L1BlockInfo, rollupCostData types.RollupCostData) *big.Int {
	costFunc := types.NewL1CostFuncFjord(
		l1BlockInfo.BaseFee,
		l1BlockInfo.BlobBaseFee,
		new(big.Int).SetUint64(uint64(l1BlockInfo.BaseFeeScalar)),
		new(big.Int).SetUint64(uint64(l1BlockInfo.BlobBaseFeeScalar)))

	fee, _ := costFunc(rollupCostData)
	return fee
}

func ptr[T any](v T) *T {
	return &v
}
