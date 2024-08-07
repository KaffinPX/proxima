package multistate

import (
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

type (
	// Updatable is an updatable ledger state, with the particular root
	// Suitable for chained updates
	// Not-thread safe, should be used individual instance for each parallel update.
	// DB (store) is updated atomically with all mutations in one DB transaction
	Updatable struct {
		trie  *immutable.TrieUpdatable
		store global.StateStore
	}

	// Readable is a read-only ledger state, with the particular root
	// It is thread-safe. The state itself is read-only, but trie cache needs write-lock with every call
	Readable struct {
		mutex *sync.Mutex
		trie  *immutable.TrieReader
	}

	// RootRecord is a persistent data stored in the DB partition with each state root
	// It contains deterministic values for that state
	RootRecord struct {
		Root        common.VCommitment
		SequencerID ledger.ChainID
		// Note: LedgerCoverage, SlotInflation and Supply are deterministic values calculated from the ledger past cone
		// Each node calculates them itself, and they must be equal on each
		LedgerCoverage uint64
		// SlotInflation: total inflation delta from previous root. It is a sum of individual transaction inflation values
		// of the previous slot/past cone. It includes the branch tx inflation itself and does not include inflation of the previous branch
		SlotInflation uint64
		// Supply: total supply at this root (including the branch itself, excluding prev branch).
		// It is the sum of the Supply of the previous branch and SlotInflation of the current
		Supply uint64
		// Number of new transactions in the slot of the branch
		NumTransactions uint32
		// TODO probably there's a need for other deterministic values, such as total number of outputs, of transactions, of chains
	}

	RootRecordJSONAble struct {
		Root           string `json:"root"`
		SequencerID    string `json:"sequencer_id"`
		LedgerCoverage uint64 `json:"ledger_coverage"`
		SlotInflation  uint64 `json:"slot_inflation"`
		Supply         uint64 `json:"supply"`
	}

	BranchData struct {
		RootRecord
		Stem            *ledger.OutputWithID
		SequencerOutput *ledger.OutputWithID
	}
)

// partitions of the state store on the trie
const (
	PartitionLedgerState = byte(iota)
	PartitionAccounts
	PartitionChainID
	PartitionCommittedTransactionID
)

func LedgerIdentityBytesFromStore(store global.StateStore) []byte {
	rr := FetchAnyLatestRootRecord(store)
	trie, err := immutable.NewTrieReader(ledger.CommitmentModel, store, rr.Root, 0)
	util.AssertNoError(err)
	return trie.Get(nil)
}

// NewReadable creates read-only ledger state with the given root
func NewReadable(store common.KVReader, root common.VCommitment, clearCacheAtSize ...int) (*Readable, error) {
	trie, err := immutable.NewTrieReader(ledger.CommitmentModel, store, root, clearCacheAtSize...)
	if err != nil {
		return nil, err
	}
	return &Readable{
		mutex: &sync.Mutex{},
		trie:  trie,
	}, nil
}

func MustNewReadable(store common.KVReader, root common.VCommitment, clearCacheAtSize ...int) *Readable {
	ret, err := NewReadable(store, root, clearCacheAtSize...)
	util.AssertNoError(err)
	return ret
}

// NewUpdatable creates updatable state with the given root. After updated, the root changes.
// Suitable for chained updates of the ledger state
func NewUpdatable(store global.StateStore, root common.VCommitment) (*Updatable, error) {
	trie, err := immutable.NewTrieUpdatable(ledger.CommitmentModel, store, root)
	if err != nil {
		return nil, err
	}
	return &Updatable{
		trie:  trie,
		store: store,
	}, nil
}

func MustNewUpdatable(store global.StateStore, root common.VCommitment) *Updatable {
	ret, err := NewUpdatable(store, root)
	util.AssertNoError(err)
	return ret
}

func (r *Readable) GetUTXO(oid *ledger.OutputID) ([]byte, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return r._getUTXO(oid)
}

func (r *Readable) _getUTXO(oid *ledger.OutputID) ([]byte, bool) {
	ret := common.MakeReaderPartition(r.trie, PartitionLedgerState).Get(oid[:])
	if len(ret) == 0 {
		return nil, false
	}
	return ret, true
}

func (r *Readable) HasUTXO(oid *ledger.OutputID) bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return common.MakeReaderPartition(r.trie, PartitionLedgerState).Has(oid[:])
}

// KnowsCommittedTransaction transaction IDs are purged after some time, so the result may be
func (r *Readable) KnowsCommittedTransaction(txid *ledger.TransactionID) bool {
	return common.MakeReaderPartition(r.trie, PartitionCommittedTransactionID).Has(txid[:])
}

func (r *Readable) GetIDsLockedInAccount(addr ledger.AccountID) ([]ledger.OutputID, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if len(addr) > 255 {
		return nil, fmt.Errorf("accountID length should be <= 255")
	}
	ret := make([]ledger.OutputID, 0)
	var oid ledger.OutputID
	var err error

	accountPrefix := common.Concat(PartitionAccounts, byte(len(addr)), addr)
	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		oid, err = ledger.OutputIDFromBytes(k[len(accountPrefix):])
		if err != nil {
			return false
		}
		ret = append(ret, oid)
		return true
	})

	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *Readable) GetUTXOsLockedInAccount(addr ledger.AccountID) ([]*ledger.OutputDataWithID, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if len(addr) > 255 {
		return nil, fmt.Errorf("accountID length should be <= 255")
	}
	accountPrefix := common.Concat(PartitionAccounts, byte(len(addr)), addr)

	ret := make([]*ledger.OutputDataWithID, 0)
	var err error
	var found bool
	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		o := &ledger.OutputDataWithID{}
		o.ID, err = ledger.OutputIDFromBytes(k[len(accountPrefix):])
		if err != nil {
			return false
		}
		o.OutputData, found = r._getUTXO(&o.ID)
		if !found {
			// skip this output ID
			return true
		}
		ret = append(ret, o)
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, err
}

func (r *Readable) GetUTXOForChainID(id *ledger.ChainID) (*ledger.OutputDataWithID, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return r._getUTXOForChainID(id)
}

func (r *Readable) _getUTXOForChainID(id *ledger.ChainID) (*ledger.OutputDataWithID, error) {
	if len(id) != ledger.ChainIDLength {
		return nil, fmt.Errorf("GetUTXOForChainID: chainID length must be %d-bytes long", ledger.ChainIDLength)
	}
	outID := common.MakeReaderPartition(r.trie, PartitionChainID).Get(id[:])
	if len(outID) == 0 {
		return nil, ErrNotFound
	}
	oid, err := ledger.OutputIDFromBytes(outID)
	if err != nil {
		return nil, err
	}
	outData, found := r._getUTXO(&oid)
	if !found {
		return nil, ErrNotFound
	}
	return &ledger.OutputDataWithID{
		ID:         oid,
		OutputData: outData,
	}, nil
}

func (r *Readable) GetStem() (ledger.Slot, []byte) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	accountPrefix := common.Concat(PartitionAccounts, byte(len(ledger.StemAccountID)), ledger.StemAccountID)

	var found bool
	var retSlot ledger.Slot
	var retBytes []byte

	// we iterate one element. Stem output ust always be present in the state
	count := 0
	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		util.Assertf(count == 0, "inconsistency: must be exactly 1 index record for stem output")
		count++
		id, err := ledger.OutputIDFromBytes(k[len(accountPrefix):])
		util.AssertNoError(err)
		retSlot = id.Slot()
		retBytes, found = r._getUTXO(&id)
		util.Assertf(found, "can't find stem output")
		return true
	})
	return retSlot, retBytes
}

func (r *Readable) MustLedgerIdentityBytes() []byte {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return r.trie.Get(nil)
}

// IterateKnownCommittedTransactions iterates transaction IDs in the state. Optionally, iteration is restricted
// for a slot. In that case first iterates non-sequencer transactions, the sequencer transactions
func (r *Readable) IterateKnownCommittedTransactions(fun func(txid *ledger.TransactionID, slot ledger.Slot) bool, txidSlot ...ledger.Slot) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	var prefixSeq, prefixNoSeq []byte
	if len(txidSlot) > 0 {
		prefixSeq, prefixNoSeq = txidSlot[0].TransactionIDPrefixes()
	}
	iter := common.MakeTraversableReaderPartition(r.trie, PartitionCommittedTransactionID).Iterator(prefixNoSeq)
	var slot ledger.Slot
	exit := false

	iter.Iterate(func(k, v []byte) bool {
		util.Assertf(len(k) == ledger.TransactionIDLength, "wrong length %d", len(k))
		txid, err := ledger.TransactionIDFromBytes(k)
		util.AssertNoError(err)
		slot, err = ledger.SlotFromBytes(v)
		util.AssertNoError(err)

		exit = !fun(&txid, slot)
		return !exit
	})
	if exit || len(txidSlot) == 0 {
		return
	}

	iter = common.MakeTraversableReaderPartition(r.trie, PartitionCommittedTransactionID).Iterator(prefixSeq)
	iter.Iterate(func(k, v []byte) bool {
		txid, err := ledger.TransactionIDFromBytes(k)
		util.AssertNoError(err)
		slot, err = ledger.SlotFromBytes(v)
		util.AssertNoError(err)

		return fun(&txid, slot)
	})
}

func (r *Readable) AccountsByLocks() map[string]LockedAccountInfo {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	var oid ledger.OutputID
	var err error

	ret := make(map[string]LockedAccountInfo)

	r.trie.Iterator([]byte{PartitionAccounts}).IterateKeys(func(k []byte) bool {
		oid, err = ledger.OutputIDFromBytes(k[2+k[1]:])
		util.AssertNoError(err)

		oData, found := r._getUTXO(&oid)
		util.Assertf(found, "can't get output")

		_, amount, lock, err := ledger.OutputFromBytesMain(oData)
		util.AssertNoError(err)

		lockStr := lock.String()
		lockInfo := ret[lockStr]
		lockInfo.Balance += uint64(amount)
		lockInfo.NumOutputs++
		ret[lockStr] = lockInfo

		return true
	})
	return ret
}

func (r *Readable) ChainInfo() map[ledger.ChainID]ChainRecordInfo {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	ret := make(map[ledger.ChainID]ChainRecordInfo)
	var chainID ledger.ChainID
	var err error
	var oData *ledger.OutputDataWithID
	var amount ledger.Amount

	r.trie.Iterator([]byte{PartitionChainID}).Iterate(func(k, v []byte) bool {
		chainID, err = ledger.ChainIDFromBytes(k[1:])
		util.AssertNoError(err)
		oData, err = r._getUTXOForChainID(&chainID)
		util.AssertNoError(err)

		_, already := ret[chainID]
		util.Assertf(!already, "repeating chain record")
		_, amount, _, err = ledger.OutputFromBytesMain(oData.OutputData)
		util.AssertNoError(err)

		ret[chainID] = ChainRecordInfo{
			Balance:     uint64(amount),
			IsSequencer: oData.ID.IsSequencerTransaction(),
			IsBranch:    oData.ID.IsBranchTransaction(),
		}
		return true
	})
	return ret
}

func (r *Readable) Root() common.VCommitment {
	// non need to lock
	return r.trie.Root()
}

func (u *Updatable) Readable() *Readable {
	return &Readable{
		mutex: &sync.Mutex{},
		trie:  u.trie.TrieReader,
	}
}

func (u *Updatable) Root() common.VCommitment {
	return u.trie.Root()
}

type RootRecordParams struct {
	StemOutputID    ledger.OutputID
	SeqID           ledger.ChainID
	Coverage        uint64
	SlotInflation   uint64
	Supply          uint64
	NumTransactions uint32
}

// Update updates trie with mutations
// If par.GenesisStemOutputID != nil, also writes root partition record
func (u *Updatable) Update(muts *Mutations, rootRecordParams *RootRecordParams) error {
	return u.updateUTXOLedgerDB(func(trie *immutable.TrieUpdatable) error {
		return UpdateTrie(u.trie, muts)
	}, rootRecordParams)
}

func (u *Updatable) MustUpdate(muts *Mutations, par *RootRecordParams) {
	err := u.Update(muts, par)
	util.AssertNoError(err)
}

func (u *Updatable) updateUTXOLedgerDB(updateFun func(updatable *immutable.TrieUpdatable) error, rootRecordsParams *RootRecordParams) error {
	if err := updateFun(u.trie); err != nil {
		return err
	}
	batch := u.store.BatchedWriter()
	newRoot := u.trie.Commit(batch)
	if rootRecordsParams != nil {
		latestSlot := FetchLatestCommittedSlot(u.store)
		if latestSlot < rootRecordsParams.StemOutputID.Slot() {
			writeLatestSlot(batch, rootRecordsParams.StemOutputID.Slot())
		}
		branchID := rootRecordsParams.StemOutputID.TransactionID()
		writeRootRecord(batch, branchID, RootRecord{
			Root:            newRoot,
			SequencerID:     rootRecordsParams.SeqID,
			LedgerCoverage:  rootRecordsParams.Coverage,
			SlotInflation:   rootRecordsParams.SlotInflation,
			Supply:          rootRecordsParams.Supply,
			NumTransactions: rootRecordsParams.NumTransactions,
		})
	}
	var err error
	if err = batch.Commit(); err != nil {
		return err
	}
	if u.trie, err = immutable.NewTrieUpdatable(ledger.CommitmentModel, u.store, newRoot); err != nil {
		return err
	}
	return nil
}

func RootHasTransaction(store common.KVReader, root common.VCommitment, txid *ledger.TransactionID) bool {
	return MustNewSugaredReadableState(store, root, 0).KnowsCommittedTransaction(txid)
}
