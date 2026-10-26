package rollup

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum-optimism/optimism/op-core/forks"
	"github.com/ethereum-optimism/optimism/op-service/eth"
)

// TestMantleEngineAPIVersions_NeverAmsterdam pins the op-node -> op-geth Engine API
// method-version selection for a Mantle L2 while its L1 is upgraded to Glamsterdam
// (Amsterdam EL). Phase-1 requirement (design §3-4): the L2 must keep the Limb-era
// Engine API tuple and must NEVER negotiate the Ethereum Amsterdam tuple.
//
// This is the real version-selection test that the existing l2engineversions
// coverage only approximates via block headers. It exercises the actual selector
// functions in op-node/rollup/types.go:
//   - (*Config).ForkchoiceUpdatedVersion  (types.go:721)
//   - (*Config).NewPayloadVersion         (types.go:741)
//   - (*Config).GetPayloadVersion         (types.go:753)
//
// Discriminating values (Mantle Arsia/Elysium  vs  Ethereum Amsterdam):
//
//	ForkchoiceUpdated : Arsia => engine_forkchoiceUpdatedV3 (eth.FCUV3)
//	                    Amsterdam would be   engine_forkchoiceUpdatedV4 (eth.FCUV4)
//	NewPayload        : Arsia => engine_newPayloadV4        (eth.NewPayloadV4)
//	                    Amsterdam would be   engine_newPayloadV5        (eth.NewPayloadV5)
//	GetPayload        : Arsia => engine_getPayloadV5 (Limb) (eth.GetPayloadV5)
//	                    Amsterdam would be   engine_getPayloadV6        (eth.GetPayloadV6)
//
// If the L2 had adopted an Amsterdam SFI EIP that bumped an engine method, the
// selector would return the V4/V5/V6 method for these Mantle-fork timestamps and
// the require.Equal assertions below would FAIL. The require.NotEqual lines make
// the "never Amsterdam" guard explicit and self-documenting.
func TestMantleEngineAPIVersions_NeverAmsterdam(t *testing.T) {
	// Two production-shaped configs: the current L2 rule (Arsia) and the next
	// scheduled L2 fork (Elysium). Neither may adopt Amsterdam engine methods.
	// MantleActivateAtGenesis sets the target fork and every prior Mantle fork
	// (incl. Limb, which gates GetPayloadV5) to timestamp 0, and AlignOpWithMantle
	// maps the OP forks (Ecotone/Isthmus/Jovian) onto the same activation, matching
	// how a real Mantle rollup.Config is derived.
	for _, target := range []forks.MantleForkName{forks.MantleArsia, forks.MantleElysium} {
		t.Run(string(target), func(t *testing.T) {
			cfg := randConfig()
			cfg.MantleActivateAtGenesis(target)
			require.NoError(t, cfg.AlignOpWithMantle())

			// Any post-activation timestamp; forks are at genesis (0) here.
			const ts = uint64(1_000_000)
			require.True(t, cfg.IsMantleArsia(ts), "Arsia must be active for %s config", target)
			require.True(t, cfg.IsMantleLimb(ts), "Limb must be active so GetPayload resolves to V5")

			attr := &eth.PayloadAttributes{Timestamp: eth.Uint64Quantity(ts)}

			// ForkchoiceUpdated: V3, never the Amsterdam V4.
			require.Equal(t, eth.FCUV3, cfg.ForkchoiceUpdatedVersion(attr),
				"Mantle L2 must use engine_forkchoiceUpdatedV3")
			require.NotEqual(t, eth.FCUV4, cfg.ForkchoiceUpdatedVersion(attr),
				"Mantle L2 must NEVER negotiate Amsterdam engine_forkchoiceUpdatedV4")

			// NewPayload: V4, never the Amsterdam V5.
			require.Equal(t, eth.NewPayloadV4, cfg.NewPayloadVersion(ts),
				"Mantle L2 must use engine_newPayloadV4")
			require.NotEqual(t, eth.NewPayloadV5, cfg.NewPayloadVersion(ts),
				"Mantle L2 must NEVER negotiate Amsterdam engine_newPayloadV5")

			// GetPayload: V5 (Limb), never the Amsterdam V6.
			require.Equal(t, eth.GetPayloadV5, cfg.GetPayloadVersion(ts),
				"Mantle L2 must use engine_getPayloadV5 (Limb)")
			require.NotEqual(t, eth.GetPayloadV6, cfg.GetPayloadVersion(ts),
				"Mantle L2 must NEVER negotiate Amsterdam engine_getPayloadV6")
		})
	}
}

// TestExecutionPayloadEnvelope_NoAmsterdamFields locks down the wire type that the
// V5 GetPayload response deserializes into (op-service/eth/types.go:233). Amsterdam's
// engine_getPayloadV6 envelope carries a block access list (BAL, EIP-7928) and an
// ePBS slotNumber; the Mantle V5 envelope must carry neither. A reflective field
// scan fails loudly if either field is ever added to the shared eth types, which
// would mean op-node had started to model Amsterdam getPayloadV6 data.
func TestExecutionPayloadEnvelope_NoAmsterdamFields(t *testing.T) {
	// Collect field names of the envelope and the payload it wraps.
	var names []string
	collect := func(rt reflect.Type) {
		for i := 0; i < rt.NumField(); i++ {
			names = append(names, rt.Field(i).Name)
		}
	}
	collect(reflect.TypeOf(eth.ExecutionPayloadEnvelope{}))
	collect(reflect.TypeOf(eth.ExecutionPayload{}))

	// The V5 envelope is exactly {ParentBeaconBlockRoot, ExecutionPayload}.
	require.ElementsMatch(t,
		[]string{"ParentBeaconBlockRoot", "ExecutionPayload"},
		fieldNamesOf(reflect.TypeOf(eth.ExecutionPayloadEnvelope{})),
		"V5 ExecutionPayloadEnvelope must stay {ParentBeaconBlockRoot, ExecutionPayload}")

	// No Amsterdam getPayloadV6 fields anywhere in the payload/envelope types.
	forbidden := []string{"accesslist", "blockaccesslist", "bal", "slotnumber", "slot"}
	for _, n := range names {
		low := strings.ToLower(n)
		for _, bad := range forbidden {
			require.NotEqual(t, bad, low,
				"Amsterdam getPayloadV6 field %q must not appear on Mantle L2 engine types", n)
		}
	}
}

func fieldNamesOf(rt reflect.Type) []string {
	out := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		out = append(out, rt.Field(i).Name)
	}
	return out
}
