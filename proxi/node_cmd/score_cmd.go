package node_cmd

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initScoreCmd() *cobra.Command {
	scoreCmd := &cobra.Command{
		Use:   "score <txid_hex>",
		Short: `displays transaction inclusion score. In verbose mode displays details`,
		Args:  cobra.ExactArgs(1),
		Run:   runScoreCmd,
	}
	scoreCmd.InitDefaultHelpCmd()

	return scoreCmd
}

const slotSpan = 2

func runScoreCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	txid, err := ledger.TransactionIDFromHexString(args[0])
	glb.AssertNoError(err)
	glb.Infof("transaction ID: %s (%s)", txid.String(), args[0])

	inclusionThresholdNumerator, inclusionThresholdDenominator := glb.GetInclusionThreshold()
	fin := "strong"
	if glb.GetIsWeakFinality() {
		fin = "weak"
	}
	glb.Infof("Inclusion threshold: %d/%d, finality criterion: %s", inclusionThresholdNumerator, inclusionThresholdDenominator, fin)

	score, err := glb.GetClient().QueryTxInclusionScore(&txid, inclusionThresholdNumerator, inclusionThresholdDenominator, slotSpan)
	glb.AssertNoError(err)

	glb.Infof("   weak score: %d%%, strong score: %d%%, from slot %d to %d (%d)",
		score.WeakScore, score.StrongScore, score.EarliestSlot, score.LatestSlot, score.LatestSlot-score.EarliestSlot+1)

	if !glb.IsVerbose() {
		return
	}

	_, inclusion, err := glb.GetClient().QueryTxIDStatus(&txid, slotSpan)
	glb.AssertNoError(err)

	glb.Infof("------- inclusion details -------")
	glb.Infof("   slot span: %d, from: %s to %d", slotSpan, inclusion.EarliestSlot, inclusion.LatestSlot)

	for _, incl := range inclusion.Inclusion {
		in := "-"
		if incl.Included {
			in = "+"
		}
		glb.Infof(" %s %20s  %s  %s", in, util.GoTh(incl.RootRecord.LedgerCoverage), incl.BranchID.StringShort(), incl.RootRecord.SequencerID.StringShort())
	}
}
