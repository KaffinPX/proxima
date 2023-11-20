package peering

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

func encodePeerMessageQueryTransactions(txids ...core.TransactionID) []byte {
	util.Assertf(len(txids) < math.MaxUint16, "too many transaction IDs")

	var buf bytes.Buffer
	buf.WriteByte(PeerMessageTypeQueryTransactions)
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(txids)))
	buf.Write(size[:])
	for i := range txids {
		buf.Write(txids[i][:])
	}
	return buf.Bytes()
}

func decodePeerMessageQueryTransactions(data []byte) ([]core.TransactionID, error) {
	if len(data) < 3 || data[0] != PeerMessageTypeQueryTransactions {
		return nil, fmt.Errorf("not a QueryTransactions message")
	}
	ret := make([]core.TransactionID, binary.BigEndian.Uint16(data[1:3]))
	rdr := bytes.NewReader(data[3:])
	var txid [core.TransactionIDLength]byte
	for i := range ret {
		n, err := rdr.Read(txid[:])
		if err != nil || n != core.TransactionIDLength {
			return nil, fmt.Errorf("DecodePeerMessageQueryTransactions: wrong msg data")
		}
		ret[i], err = core.TransactionIDFromBytes(txid[:])
		common.AssertNoError(err)
	}
	return ret, nil
}

func encodePeerMessageTxBytes(txBytes []byte) []byte {
	util.Assertf(len(txBytes) < math.MaxUint16, "too long transaction bytes")

	var buf bytes.Buffer
	buf.WriteByte(PeerMessageTypeTxBytes)
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(txBytes)))
	buf.Write(size[:])
	buf.Write(txBytes)
	return buf.Bytes()
}

func decodePeerMessageTxBytes(data []byte) ([]byte, error) {
	if len(data) < 3 || data[0] != PeerMessageTypeTxBytes {
		return nil, fmt.Errorf("not a TxBytes message")
	}
	size := binary.BigEndian.Uint16(data[1:3])
	if len(data) != int(size)+3 {
		return nil, fmt.Errorf("DecodePeerMessageTxBytes: wrong data length")
	}
	return data[3:], nil
}
