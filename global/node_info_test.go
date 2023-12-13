package global

import (
	"encoding/json"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

func randomPeerID() peer.ID {
	privateKey := testutil.GetTestingPrivateKey(101)

	pklpp, err := crypto.UnmarshalEd25519PrivateKey(privateKey)
	util.AssertNoError(err)

	ret, err := peer.IDFromPrivateKey(pklpp)
	util.AssertNoError(err)
	return ret
}

func TestPeerInfo(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		pi := &NodeInfo{
			Name:           "peerName",
			ID:             randomPeerID(),
			NumStaticPeers: 5,
			NumActivePeers: 3,
		}
		jsonData, err := json.MarshalIndent(pi, "", "  ")
		require.NoError(t, err)
		t.Logf("json string:\n%s", string(jsonData))

		var piBack NodeInfo
		err = json.Unmarshal(jsonData, &piBack)
		require.NoError(t, err)
		require.EqualValues(t, pi.Name, piBack.Name)
		require.EqualValues(t, pi.ID, piBack.ID)
		require.EqualValues(t, pi.NumStaticPeers, piBack.NumStaticPeers)
		require.EqualValues(t, pi.NumActivePeers, piBack.NumActivePeers)

		require.True(t, util.EqualSlices(pi.Sequencers, piBack.Sequencers))
		require.True(t, util.EqualSlices(pi.Branches, piBack.Branches))
	})
	t.Run("2", func(t *testing.T) {
		branches := []core.TransactionID{core.RandomTransactionID(true, true), core.RandomTransactionID(true, true), core.RandomTransactionID(true, true)}
		sequencers := []core.ChainID{core.RandomChainID()}
		pi := &NodeInfo{
			Name:           "peerName",
			ID:             randomPeerID(),
			NumStaticPeers: 5,
			NumActivePeers: 3,
			Sequencers:     sequencers,
			Branches:       branches,
		}
		jsonData, err := json.MarshalIndent(pi, "", "  ")
		require.NoError(t, err)
		t.Logf("json string:\n%s", string(jsonData))

		var piBack NodeInfo
		err = json.Unmarshal(jsonData, &piBack)
		require.NoError(t, err)
		require.EqualValues(t, pi.Name, piBack.Name)
		require.EqualValues(t, pi.ID, piBack.ID)
		require.EqualValues(t, pi.NumStaticPeers, piBack.NumStaticPeers)
		require.EqualValues(t, pi.NumActivePeers, piBack.NumActivePeers)

		require.True(t, util.EqualSlices(pi.Sequencers, piBack.Sequencers))
		require.True(t, util.EqualSlices(pi.Branches, piBack.Branches))
	})
}
