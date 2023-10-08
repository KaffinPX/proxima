package workflow

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
)

func (w *Workflow) TransactionInAPI(txBytes []byte) error {
	_, err := w.transactionInWithOptions(txBytes, false, func(inData *PrimaryInputConsumerData) error {
		w.primaryInputConsumer.Push(inData)
		return nil
	})
	return err
}

func (w *Workflow) transactionInWithOptions(txBytes []byte, insider bool, doFun func(*PrimaryInputConsumerData) error) (*transaction.Transaction, error) {
	util.Assertf(w.IsRunning(), "workflow has not been started yet")
	// base validation
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		return nil, err
	}

	out := newPrimaryInputConsumerData(tx)
	if insider {
		out.Source = TransactionSourceSequencer
	}

	// if raw transaction data passes the basic check, it means it is identifiable as a transaction and main properties
	// are correct: ID, timestamp, sequencer and branch transaction flags. The signature and semantic has not been checked yet
	return out.Tx, doFun(out)
}

// TransactionInWaitAppendSyncTx for testing
func (w *Workflow) TransactionInWaitAppendSyncTx(txBytes []byte, insider ...bool) (*transaction.Transaction, error) {
	special := false
	if len(insider) > 0 {
		special = insider[0]
	}
	var wg sync.WaitGroup
	wg.Add(1)

	var err error
	tx, err1 := w.transactionInWithOptions(txBytes, special, func(inData *PrimaryInputConsumerData) error {
		err2 := w.defaultTxStatusWaitingList.OnTransactionStatus(inData.Tx.ID(), func(statusMsg *TxStatusMsg) {
			defer wg.Done()

			if statusMsg == nil {
				err = fmt.Errorf("timeout on appending %s", inData.Tx.IDShort())
				return
			}
			if !statusMsg.Appended {
				err = fmt.Errorf("transaction %s has been rejected: '%s'", inData.Tx.IDShort(), statusMsg.Msg)
				return
			}
		})
		if err2 == nil {
			w.primaryInputConsumer.Push(inData)
		}
		return err2
	})
	if err1 != nil {
		return nil, err1
	}
	wg.Wait()
	if err != nil {
		return tx, err
	}
	return tx, nil
}

func (w *Workflow) TransactionInWaitAppendSync(txBytes []byte, insider ...bool) (*utangle.WrappedTx, error) {
	tx, err := w.TransactionInWaitAppendSyncTx(txBytes, insider...)
	if err != nil {
		return nil, err
	}
	return w.utxoTangle.MustGetVertex(tx.ID()), nil
}

func (w *Workflow) TransactionInWithOptions(txBytes []byte, opts ...TransactionInOption) (*transaction.Transaction, error) {
	util.Assertf(w.IsRunning(), "workflow has not been started yet")
	// base validation
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		return nil, err
	}
	// if raw transaction data passes the basic check, it means it is identifiable as a transaction and main properties
	// are correct: ID, timestamp, sequencer and branch transaction flags. The signature and semantic has not been checked yet

	inData := newPrimaryInputConsumerData(tx)
	for _, opt := range opts {
		opt(inData)
	}

	w.primaryInputConsumer.Push(inData)
	return tx, nil
}

func newPrimaryInputConsumerData(tx *transaction.Transaction) *PrimaryInputConsumerData {
	return &PrimaryInputConsumerData{
		Tx:            tx,
		Source:        TransactionSourceAPI,
		eventCallback: func(_ string, _ any) {},
	}
}

func WithTransactionSource(src TransactionSource) TransactionInOption {
	return func(data *PrimaryInputConsumerData) {
		data.Source = src
	}
}

func WithWorkflowEventCallback(fun func(event string, data any)) TransactionInOption {
	return func(data *PrimaryInputConsumerData) {
		prev := data.eventCallback
		data.eventCallback = func(event string, data any) {
			prev(event, data)
			fun(event, data)
		}
	}
}

func WithOnWorkflowEventPrefix(eventPrefix string, fun func(event string, data any)) TransactionInOption {
	return WithWorkflowEventCallback(func(event string, data any) {
		if strings.HasPrefix(event, eventPrefix) {
			fun(event, data)
		}
	})
}

func (w *Workflow) TransactionInWaitAppend(txBytes []byte, timeout time.Duration, opts ...TransactionInOption) (*transaction.Transaction, error) {
	errCh := make(chan error)
	var closed bool
	var closeMutex sync.Mutex

	waitFailOpt := WithOnWorkflowEventPrefix("finish", func(event string, data any) {
		errStr, ok := data.(string)
		util.Assertf(ok, "wrong data type, string expected")
		var err error
		if errStr != "" {
			err = errors.New(errStr)
		}

		closeMutex.Lock()
		defer closeMutex.Unlock()

		if !closed {
			errCh <- err
		}
	})

	defer func() {
		closeMutex.Lock()
		defer closeMutex.Unlock()

		close(errCh)
		closed = true
	}()

	opts = append(opts, waitFailOpt)
	tx, err := w.TransactionInWithOptions(txBytes, opts...)
	if err != nil {
		return nil, err
	}
	select {
	case err = <-errCh:
		return nil, err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout of %v exceed", timeout)
	}
	return tx, nil
}
