package multistate

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

func TestSerialization(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		rr := RootRecord{
			Root:           ledger.RandomVCommitment(),
			SequencerID:    ledger.RandomChainID(),
			LedgerCoverage: LedgerCoverage{0, 1337},
		}
		bin := rr.Bytes()
		rrBack, err := RootRecordFromBytes(bin)
		require.NoError(t, err)
		require.True(t, ledger.CommitmentModel.EqualCommitments(rr.Root, rrBack.Root))
		require.EqualValues(t, rr.SequencerID, rrBack.SequencerID)
		require.EqualValues(t, rr.LedgerCoverage, rrBack.LedgerCoverage)
		require.EqualValues(t, LedgerCoverage{0, 1337}, rrBack.LedgerCoverage)
		require.True(t, rr.LedgerCoverage.LatestDelta() == 0)
	})
	t.Run("with panic", func(t *testing.T) {
		rr := RootRecord{
			Root:           ledger.RandomVCommitment(),
			SequencerID:    ledger.RandomChainID(),
			LedgerCoverage: LedgerCoverage{42, 1337},
		}
		util.RequirePanicOrErrorWith(t, func() error {
			rr.Bytes()
			return nil
		}, "r.LedgerCoverage.LatestDelta() == 0")
	})
}
