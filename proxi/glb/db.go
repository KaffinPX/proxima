package glb

import (
	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
)

var (
	stateDB    *badger.DB
	stateStore global.StateStore
)

func InitLedger() {
	dbName := global.MultiStateDBName
	Infof("Multi-state store database: %s", dbName)
	FileMustExist(dbName)
	stateDB = badger_adaptor.MustCreateOrOpenBadgerDB(dbName)
	stateStore = badger_adaptor.New(stateDB)
	multistate.InitLedgerFromStore(stateStore)
}

func StateStore() global.StateStore {
	return stateStore
}

func CloseStateStore() {
	_ = stateDB.Close()
}

func InitTxStoreDB() {
	txDBName := global.TxStoreDBName
	Infof("Transaction store database: %s", txDBName)

}
