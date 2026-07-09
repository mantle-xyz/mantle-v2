package beaconspec

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum-optimism/optimism/op-service/client"
	"github.com/ethereum-optimism/optimism/op-service/sources"
	"github.com/ethereum-optimism/optimism/op-service/testlog"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/require"
)

// TestL1Beacon_ConfigSpec_RealCL asserts a REAL post-Gloas L1 beacon (Prysm/Lighthouse) serves
// /eth/v1/config/spec with a usable SECONDS_PER_SLOT that matches the real L1 block spacing — the
// value op-node/kona use to map L1 timestamps to beacon slots when fetching blobs. The
// sysgo/fakebeacon suite cannot check this against a real CL (fakebeacon is a fork-agnostic mock),
// so it complements realclblob's blob-fetch check with the spec value the fetch depends on.
//
// Not a CI test: it skips unless L1_EL_URL + L1_BEACON_URL point at a real post-Gloas geth+beacon
// (e.g. rde running the glamsterdam profile). Same convention as realclblob.
func TestL1Beacon_ConfigSpec_RealCL(t *testing.T) {
	elURL := os.Getenv("L1_EL_URL")
	beaconURL := os.Getenv("L1_BEACON_URL")
	if elURL == "" || beaconURL == "" {
		t.Skip("set L1_EL_URL and L1_BEACON_URL to a real post-Gloas geth+beacon")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	logger := testlog.Logger(t, log.LevelInfo)

	beacon := sources.NewBeaconHTTPClient(client.NewBasicHTTPClient(beaconURL, logger))
	cfg, err := beacon.ConfigSpec(ctx)
	require.NoError(t, err, "real L1 beacon must serve /eth/v1/config/spec")
	secondsPerSlot := uint64(cfg.Data.SecondsPerSlot)
	require.Greater(t, secondsPerSlot, uint64(0), "SECONDS_PER_SLOT must be usable (>0)")

	el, err := ethclient.DialContext(ctx, elURL)
	require.NoError(t, err, "dial L1 EL")
	defer el.Close()
	head, err := el.HeaderByNumber(ctx, nil)
	require.NoError(t, err, "read L1 head header")
	require.Greater(t, head.Number.Uint64(), uint64(0), "need an L1 block past genesis")
	parent, err := el.HeaderByNumber(ctx, new(big.Int).Sub(head.Number, big.NewInt(1)))
	require.NoError(t, err, "read L1 parent header")
	require.Greater(t, head.Time, parent.Time, "L1 block timestamps must be strictly increasing")
	require.Equal(t, head.Time-parent.Time, secondsPerSlot,
		"beacon SECONDS_PER_SLOT must equal the real L1 block time")
	t.Logf("real beacon SECONDS_PER_SLOT=%d matches L1 block time", secondsPerSlot)
}
