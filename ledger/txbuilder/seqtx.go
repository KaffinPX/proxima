package txbuilder

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

type (
	MakeSequencerTransactionParams struct {
		// sequencer name
		SeqName string
		// predecessor
		ChainInput *ledger.OutputWithChainID
		//
		StemInput *ledger.OutputWithID // it is branch tx if != nil
		// timestamp of the transaction
		Timestamp ledger.Time
		// minimum fee
		MinimumFee uint64
		// additional inputs to consume. Must be unlockable by chain
		// can contain sender commands to the sequencer
		AdditionalInputs []*ledger.OutputWithID
		// additional outputs to produce
		AdditionalOutputs []*ledger.Output
		// Endorsements
		Endorsements []*ledger.TransactionID
		// chain controller
		PrivateKey ed25519.PrivateKey
		//
		TotalSupply uint64
		// Inflate if true, it indicates to begin inflation constraints, if relevant. Otherwise 'continue' inflation constraints
		// and in branched are added automatically and with auto-calculated values
		Inflate bool
		// by default branch is always inflated. This option supresses it
		DoNotInflateBranch bool
		//
		ReturnInputLoader bool
	}

	// MilestoneData data which is on sequencer as 'or(..)' constraint. It is not enforced by the ledger, yet maintained
	// by the sequencer
	MilestoneData struct {
		Name         string // < 256
		MinimumFee   uint64
		ChainHeight  uint32
		BranchHeight uint32
	}
)

//func(i byte) (*ledger.Output, error)

func MakeSequencerTransaction(par MakeSequencerTransactionParams) ([]byte, error) {
	ret, _, err := MakeSequencerTransactionWithInputLoader(par)
	return ret, err
}

func MakeSequencerTransactionWithInputLoader(par MakeSequencerTransactionParams) ([]byte, func(i byte) (*ledger.Output, error), error) {
	var consumedOutputs []*ledger.Output
	if par.ReturnInputLoader {
		consumedOutputs = make([]*ledger.Output, 0)
	}
	errP := util.MakeErrFuncForPrefix("MakeSequencerTransaction")

	nIn := len(par.AdditionalInputs) + 1
	if par.StemInput != nil {
		nIn++
	}
	switch {
	case nIn > 256:
		return nil, nil, errP("too many inputs")
	case par.StemInput != nil && par.Timestamp.Tick() != 0:
		return nil, nil, errP("wrong timestamp for branch transaction: %s", par.Timestamp.String())
	case par.Timestamp.Slot() > par.ChainInput.ID.TimeSlot() && par.Timestamp.Tick() != 0 && len(par.Endorsements) == 0:
		return nil, nil, errP("cross-slot sequencer tx must endorse another sequencer tx: chain input ts: %s, target: %s",
			par.ChainInput.ID.Timestamp(), par.Timestamp)
	case !par.ChainInput.ID.SequencerFlagON() && par.StemInput == nil && len(par.Endorsements) == 0:
		return nil, nil, errP("chain predecessor is not a sequencer transaction -> endorsement of sequencer transaction is mandatory (unless making a branch)")
	}

	chainInConstraint, chainInConstraintIdx := par.ChainInput.Output.ChainConstraint()
	if chainInConstraintIdx == 0xff {
		return nil, nil, errP("not a chain output: %s", par.ChainInput.ID.StringShort())
	}

	txb := NewTransactionBuilder()
	// count sums
	additionalIn, additionalOut := uint64(0), uint64(0)
	for _, o := range par.AdditionalInputs {
		additionalIn += o.Output.Amount()
	}
	for _, o := range par.AdditionalOutputs {
		additionalOut += o.Amount()
	}
	chainInAmount := par.ChainInput.Output.Amount()

	// decide if to put 'inflation' constraint and what amount
	putInflationConstraint := false
	inflateBy := uint64(0)

	_, inInflationIdx := par.ChainInput.Output.InflationConstraint()
	continueInflationOnTheSlot :=
		inInflationIdx != 0xff && !par.ChainInput.ID.IsBranchTransaction() && par.Timestamp.Slot() == par.ChainInput.Timestamp().Slot()

	switch {
	case par.Timestamp.Tick() == 0 && !par.DoNotInflateBranch:
		// always put inflation constraint to the branch
		putInflationConstraint = true
		inflateBy = ledger.InflationAmountBranchFixed
	case par.Timestamp.Slot() == par.ChainInput.Timestamp().Slot():
		if continueInflationOnTheSlot {
			// there is an inflation constraint in the predecessor
			// will put with inflation amount 0 until next slot
			putInflationConstraint = true
		} else {
			// there is no inflation constraint in the predecessor on the same slot.
			// Put initial inflation constraint with calculated amount
			if par.Inflate {
				// but initial inflation amount
				inflateBy = chainInAmount / ledger.InflationAmountPerChainFraction
				putInflationConstraint = inflateBy > 0
			}
		}
	}
	// safe arithmetics
	if additionalIn > math.MaxUint64-chainInAmount || additionalIn+chainInAmount > math.MaxUint64-inflateBy {
		return nil, nil, errP("arithmetic overflow")
	}

	totalProducedAmount := chainInAmount + additionalIn + inflateBy
	chainOutAmount := totalProducedAmount - additionalOut

	// make chain input/output
	chainPredIdx, err := txb.ConsumeOutput(par.ChainInput.Output, par.ChainInput.ID)
	if err != nil {
		return nil, nil, errP(err)
	}
	if par.ReturnInputLoader {
		consumedOutputs = append(consumedOutputs, par.ChainInput.Output)
	}
	txb.PutSignatureUnlock(chainPredIdx)

	seqID := chainInConstraint.ID
	if chainInConstraint.IsOrigin() {
		seqID = ledger.OriginChainID(&par.ChainInput.ID)
	}

	var chainOutConstraintIdx, inflationOutConstraintIdx byte

	chainOut := ledger.NewOutput(func(o *ledger.Output) {
		o.PutAmount(chainOutAmount)
		o.PutLock(par.ChainInput.Output.Lock())
		// put chain constraint
		chainOutConstraint := ledger.NewChainConstraint(seqID, chainPredIdx, chainInConstraintIdx, 0)
		chainOutConstraintIdx, _ = o.PushConstraint(chainOutConstraint.Bytes())
		// put sequencer constraint
		sequencerConstraint := ledger.NewSequencerConstraint(chainOutConstraintIdx, totalProducedAmount)
		_, _ = o.PushConstraint(sequencerConstraint.Bytes())
		if putInflationConstraint {
			inflationConstraint := ledger.NewInflationConstraint(chainOutConstraintIdx, inflateBy)
			inflationOutConstraintIdx, _ = o.PushConstraint(inflationConstraint.Bytes())
		}

		outData := ParseMilestoneData(par.ChainInput.Output)
		if outData == nil {
			outData = &MilestoneData{
				Name:         par.SeqName,
				MinimumFee:   par.MinimumFee,
				BranchHeight: 0,
				ChainHeight:  0,
			}
		} else {
			outData.ChainHeight += 1
			if par.StemInput != nil {
				outData.BranchHeight += 1
			}
			outData.Name = par.SeqName
		}
		_, _ = o.PushConstraint(outData.AsConstraint().Bytes())
	})

	chainOutIndex, err := txb.ProduceOutput(chainOut)
	if err != nil {
		return nil, nil, errP(err)
	}
	// unlock chain input (chain constraint unlock + inflation (optionally)
	txb.PutUnlockParams(chainPredIdx, chainInConstraintIdx, ledger.NewChainUnlockParams(chainOutIndex, chainOutConstraintIdx, 0))
	if putInflationConstraint && continueInflationOnTheSlot {
		txb.PutUnlockParams(chainPredIdx, inInflationIdx, ledger.NewInflationConstraintUnlockParams(inflationOutConstraintIdx))
	}

	// make stem input/output if it is a branch transaction
	stemOutputIndex := byte(0xff)
	if par.StemInput != nil {
		_, err = txb.ConsumeOutput(par.StemInput.Output, par.StemInput.ID)
		if err != nil {
			return nil, nil, errP(err)
		}
		if par.ReturnInputLoader {
			consumedOutputs = append(consumedOutputs, par.StemInput.Output)
		}

		stemOut := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(par.StemInput.Output.Amount())
			o.WithLock(&ledger.StemLock{
				PredecessorOutputID: par.StemInput.ID,
			})
		})
		stemOutputIndex, err = txb.ProduceOutput(stemOut)
		if err != nil {
			return nil, nil, errP(err)
		}
	}

	// consume and unlock additional inputs/outputs
	// unlock additional inputs
	tsIn := ledger.MustNewLedgerTime(0, 0)
	for _, o := range par.AdditionalInputs {
		idx, err := txb.ConsumeOutput(o.Output, o.ID)
		if err != nil {
			return nil, nil, errP(err)
		}
		if par.ReturnInputLoader {
			consumedOutputs = append(consumedOutputs, o.Output)
		}
		switch lockName := o.Output.Lock().Name(); lockName {
		case ledger.AddressED25519Name:
			if err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0); err != nil {
				return nil, nil, err
			}
		case ledger.ChainLockName:
			txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0, chainInConstraintIdx))
		default:
			return nil, nil, errP("unsupported type of additional input: %s", lockName)
		}
		tsIn = ledger.MaxTime(tsIn, o.Timestamp())
	}

	if !ledger.ValidTimePace(tsIn, par.Timestamp) {
		return nil, nil, errP("timestamp inconsistent with inputs")
	}

	_, err = txb.ProduceOutputs(par.AdditionalOutputs...)
	if err != nil {
		return nil, nil, errP(err)
	}
	txb.PushEndorsements(par.Endorsements...)
	txb.TransactionData.Timestamp = par.Timestamp
	txb.TransactionData.SequencerOutputIndex = chainOutIndex
	txb.TransactionData.StemOutputIndex = stemOutputIndex
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(par.PrivateKey)

	inputLoader := func(i byte) (*ledger.Output, error) {
		panic("MakeSequencerTransactionWithInputLoader: par.ReturnInputLoader parameter must be set to true")
	}
	if par.ReturnInputLoader {
		inputLoader = func(i byte) (*ledger.Output, error) {
			return consumedOutputs[i], nil
		}
	}
	return txb.TransactionData.Bytes(), inputLoader, nil
}

// ParseMilestoneData expected at index 4, otherwise nil
func ParseMilestoneData(o *ledger.Output) *MilestoneData {
	if o.NumConstraints() < 5 {
		return nil
	}
	ret, err := MilestoneDataFromConstraint(o.ConstraintAt(4))
	if err != nil {
		return nil
	}
	return ret
}

func (od *MilestoneData) AsConstraint() ledger.Constraint {
	dscrBin := []byte(od.Name)
	if len(dscrBin) > 255 {
		dscrBin = dscrBin[:256]
	}
	dscrBinStr := fmt.Sprintf("0x%s", hex.EncodeToString(dscrBin))
	chainIndexStr := fmt.Sprintf("u32/%d", od.ChainHeight)
	branchIndexStr := fmt.Sprintf("u32/%d", od.BranchHeight)
	minFeeStr := fmt.Sprintf("u64/%d", od.MinimumFee)

	src := fmt.Sprintf("or(%s)", strings.Join([]string{dscrBinStr, chainIndexStr, branchIndexStr, minFeeStr}, ","))
	_, _, bytecode, err := easyfl.CompileExpression(src)
	util.AssertNoError(err)

	constr, err := ledger.ConstraintFromBytes(bytecode)
	util.AssertNoError(err)

	return constr
}

func MilestoneDataFromConstraint(constr []byte) (*MilestoneData, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(constr)
	if err != nil {
		return nil, err
	}
	if sym != "or" {
		return nil, fmt.Errorf("sequencer.MilestoneDataFromConstraint: unexpected function '%s'", sym)
	}
	if len(args) != 4 {
		return nil, fmt.Errorf("sequencer.MilestoneDataFromConstraint: expected exactly 4 arguments, got %d", len(args))
	}
	dscrBin := easyfl.StripDataPrefix(args[0])
	chainIdxBin := easyfl.StripDataPrefix(args[1])
	branchIdxBin := easyfl.StripDataPrefix(args[2])
	minFeeBin := easyfl.StripDataPrefix(args[3])
	if len(chainIdxBin) != 4 || len(branchIdxBin) != 4 || len(minFeeBin) != 8 {
		return nil, fmt.Errorf("sequencer.MilestoneDataFromConstraint: unexpected argument sizes %d, %d, %d, %d",
			len(args[0]), len(args[1]), len(args[2]), len(args[3]))
	}
	return &MilestoneData{
		Name:         string(dscrBin),
		ChainHeight:  binary.BigEndian.Uint32(chainIdxBin),
		BranchHeight: binary.BigEndian.Uint32(branchIdxBin),
		MinimumFee:   binary.BigEndian.Uint64(minFeeBin),
	}, nil
}
