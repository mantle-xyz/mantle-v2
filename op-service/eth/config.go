package eth

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
)

// L1ChainConfigByChainID returns the chain config for the given chain ID,
// if it is in the set of known chain IDs (Mainnet, Sepolia, Holesky, Hoodi).
// If the chain ID is not known, it returns nil.
func L1ChainConfigByChainID(chainID ChainID) *params.ChainConfig {
	switch chainID {
	case ChainIDFromBig(params.MainnetChainConfig.ChainID):
		return params.MainnetChainConfig
	case ChainIDFromBig(params.SepoliaChainConfig.ChainID):
		return params.SepoliaChainConfig
	case ChainIDFromBig(params.HoleskyChainConfig.ChainID):
		return params.HoleskyChainConfig
	case ChainIDFromBig(params.HoodiChainConfig.ChainID):
		return params.HoodiChainConfig
	default:
		return nil
	}
}

func MantleArsiaL1ChainConfigByChainID(chainID ChainID) *params.ChainConfig {
	switch chainID {
	case ChainIDFromBig(params.MainnetChainConfig.ChainID):
		mainnetChainConfig := &params.ChainConfig{
			ChainID:                 big.NewInt(1),
			HomesteadBlock:          big.NewInt(1_150_000),
			DAOForkBlock:            big.NewInt(1_920_000),
			DAOForkSupport:          true,
			EIP150Block:             big.NewInt(2_463_000),
			EIP155Block:             big.NewInt(2_675_000),
			EIP158Block:             big.NewInt(2_675_000),
			ByzantiumBlock:          big.NewInt(4_370_000),
			ConstantinopleBlock:     big.NewInt(7_280_000),
			PetersburgBlock:         big.NewInt(7_280_000),
			IstanbulBlock:           big.NewInt(9_069_000),
			MuirGlacierBlock:        big.NewInt(9_200_000),
			BerlinBlock:             big.NewInt(12_244_000),
			LondonBlock:             big.NewInt(12_965_000),
			ArrowGlacierBlock:       big.NewInt(13_773_000),
			GrayGlacierBlock:        big.NewInt(15_050_000),
			TerminalTotalDifficulty: params.MainnetTerminalTotalDifficulty, // 58_750_000_000_000_000_000_000
			ShanghaiTime:            newUint64(1681338455),
			CancunTime:              newUint64(1710338135),
			PragueTime:              newUint64(1746612311),
			DepositContractAddress:  common.HexToAddress("0x00000000219ab540356cbb839cbe05303d7705fa"),
			Ethash:                  new(params.EthashConfig),
			BlobScheduleConfig: &params.BlobScheduleConfig{
				Cancun: params.DefaultCancunBlobConfig,
				Prague: params.DefaultPragueBlobConfig,
			},
		}
		return mainnetChainConfig
	default:
		return nil
	}
}

func newUint64(val uint64) *uint64 { return &val }
