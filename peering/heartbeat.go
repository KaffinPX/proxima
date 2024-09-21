package peering

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"golang.org/x/exp/maps"
)

type heartbeatInfo struct {
	clock                                  time.Time
	ignoresAllPullRequests                 bool
	acceptsPullRequestsFromStaticPeersOnly bool
}

// flags of the heartbeat message. Information for the peer about the node

const (
	// flagIgnoresAllPullRequests node ignores all pull requests
	flagIgnoresAllPullRequests = byte(0b00000001)
	// flagAcceptsPullRequestsFromStaticPeersOnly node ignores pull requests from automatic peers
	// only effective if flagIgnoresAllPullRequests is false
	flagAcceptsPullRequestsFromStaticPeersOnly = byte(0x00000010)
)

func heartbeatInfoFromBytes(data []byte) (heartbeatInfo, error) {
	if len(data) != 8+1 {
		return heartbeatInfo{}, fmt.Errorf("heartbeatInfoFromBytes: wrong data len")
	}
	ret := heartbeatInfo{
		clock: time.Unix(0, int64(binary.BigEndian.Uint64(data[:8]))),
	}
	ret.setFromFlags(data[8])
	return ret, nil
}

func (ps *Peers) NumAlive() (aliveStatic, aliveDynamic int) {
	ps.forEachPeerRLock(func(p *Peer) bool {
		if p._isAlive() {
			if p.isStatic {
				aliveStatic++
			} else {
				aliveDynamic++
			}
		}
		return true
	})
	return
}

func (ps *Peers) logConnectionStatusIfNeeded(id peer.ID) {
	ps.withPeer(id, func(p *Peer) {
		if p == nil {
			return
		}
		if p._isDead() && p.lastLoggedConnected {
			ps.Log().Infof("[peering] LOST CONNECTION with %s peer %s ('%s'). Host (self): %s",
				p.staticOrDynamic(), ShortPeerIDString(id), p.name, ShortPeerIDString(ps.host.ID()))
			p.lastLoggedConnected = false
			return
		}

		if p._isAlive() && !p.lastLoggedConnected {
			ps.Log().Infof("[peering] CONNECTED to %s peer %s ('%s'), msg src '%s'. Host (self): %s",
				p.staticOrDynamic(), ShortPeerIDString(id), p.name, p.lastMsgReceivedFrom, ShortPeerIDString(ps.host.ID()))
			p.lastLoggedConnected = true
		}

	})
}

// heartbeat protocol is used to monitor
// - if peer is alive and
// - to ensure clocks difference is within tolerance interval. Clock difference is
// sum of difference between local clocks plus communication delay
// Clock difference is perceived differently by two connected peers. If one of them
// goes out of tolerance interval, connection is dropped from one, then from the other side

func (ps *Peers) heartbeatStreamHandler(stream network.Stream) {
	ps.inMsgCounter.Inc()

	id := stream.Conn().RemotePeer()

	if ps.isInBlacklist(id) {
		// ignore
		//_ = stream.Reset()
		_ = stream.Close()
		return
	}

	exit := false

	ps.withPeer(id, func(p *Peer) {
		if p != nil {
			// known peer, static or dynamic
			return
		}
		// incoming heartbeat from new peer
		if !ps.isAutopeeringEnabled() {
			// node does not take any incoming dynamic peers
			ps.Tracef(TraceTag, "autopeering disabled: unknown peer %s", id.String)

			//  do not be harsh, just ignore
			//_ = stream.Reset()
			_ = stream.Close()
			exit = true
			return
		}
		// Add new incoming dynamic peer and then let the autopeering handle if too many

		// does not work -> addrInfo, err := peer.AddrInfoFromP2pAddr(remote)
		// for some reason peer.AddrInfoFromP2pAddr does not work -> compose AddrInfo from parts

		remote := stream.Conn().RemoteMultiaddr()
		addrInfo := &peer.AddrInfo{
			ID:    id,
			Addrs: []multiaddr.Multiaddr{remote},
		}
		ps.Log().Infof("[peering] incoming peer request from %s. Add new dynamic peer", ShortPeerIDString(id))
		ps._addPeer(addrInfo, "", false)
	})
	if exit {
		return
	}

	var hbInfo heartbeatInfo
	var err error
	var msgData []byte

	if msgData, err = readFrame(stream); err != nil {
		ps.Log().Errorf("[peering] hb: error while reading message from peer %s: err='%v'. Ignore", ShortPeerIDString(id), err)
		// ignore
		ps.withPeer(id, func(p *Peer) {
			if p != nil {
				p.errorCounter++
			}
		})
		_ = stream.Close()
		return
	}

	if hbInfo, err = heartbeatInfoFromBytes(msgData); err != nil {
		// protocol violation
		err = fmt.Errorf("[peering] hb: error while serializing message from peer %s: %v. Reset stream", ShortPeerIDString(id), err)
		ps.Log().Error(err)
		ps.dropPeer(id, err.Error())
		_ = stream.Reset()
		return
	}

	_ = stream.Close()

	ps.withPeer(id, func(p *Peer) {
		if p == nil {
			return
		}
		p._evidenceActivity("hb")
		p.ignoresAllPullRequests = hbInfo.ignoresAllPullRequests
		p.acceptsPullRequestsFromStaticPeersOnly = hbInfo.acceptsPullRequestsFromStaticPeersOnly

		p._evidenceClockDifference(time.Now().Sub(hbInfo.clock))
	})
}

func (ps *Peers) sendHeartbeatToPeer(id peer.ID) {
	ps.sendMsgOutQueued(&heartbeatInfo{
		// time now will be set in the queue consumer
		ignoresAllPullRequests:                 ps.cfg.IgnoreAllPullRequests,
		acceptsPullRequestsFromStaticPeersOnly: ps.cfg.AcceptPullRequestsFromStaticPeersOnly,
	}, id, ps.lppProtocolHeartbeat)
}

func (ps *Peers) peerIDsAlive() []peer.ID {
	ret := make([]peer.ID, 0)
	ps.forEachPeerRLock(func(p *Peer) bool {
		if p._isAlive() {
			ret = append(ret, p.id)
		}
		return true
	})
	return ret
}

func (ps *Peers) peerIDs() []peer.ID {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	return maps.Keys(ps.peers)
}

// startHeartbeat periodically sends HB message to each known peer
func (ps *Peers) startHeartbeat() {
	var logNumPeersDeadline time.Time

	ps.RepeatInBackground("peering_heartbeat_loop", heartbeatRate, func() bool {
		nowis := time.Now()
		peerIDs := ps.peerIDs()

		for _, id := range peerIDs {
			ps.logConnectionStatusIfNeeded(id)
			ps.sendHeartbeatToPeer(id)
		}

		if nowis.After(logNumPeersDeadline) {
			aliveStatic, aliveDynamic := ps.NumAlive()

			ps.Log().Infof("[peering] node is connected to %d peer(s). Static: %d/%d, dynamic %d/%d) (took %v)",
				aliveStatic+aliveDynamic, aliveStatic, len(ps.cfg.PreConfiguredPeers),
				aliveDynamic, ps.cfg.MaxDynamicPeers, time.Since(nowis))

			logNumPeersDeadline = nowis.Add(logPeersEvery)
		}

		return true
	}, true)
}

// dropPeersWithTooBigClockDiffs drops all peers which average clock diff exceeds tolerance threshold
func (ps *Peers) dropPeersWithTooBigClockDiffs() {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	for _, p := range ps.peers {
		d := p.avgClockDifference()
		if d > clockTolerance/2 {
			ps.Log().Warnf("clock difference with peer %s is %v (average over %d instances). Tolerance is %v",
				ShortPeerIDString(p.id), d, len(p.clockDifferences), clockTolerance)
		}
		if p.avgClockDifference() > clockTolerance {
			ps._dropPeer(p, "clock tolerance")
		}
	}
}

func (hi *heartbeatInfo) flags() (ret byte) {
	if hi.ignoresAllPullRequests {
		ret |= flagIgnoresAllPullRequests
	}
	if hi.acceptsPullRequestsFromStaticPeersOnly {
		ret |= flagAcceptsPullRequestsFromStaticPeersOnly
	}
	return
}

func (hi *heartbeatInfo) setFromFlags(fl byte) {
	hi.ignoresAllPullRequests = (fl & flagIgnoresAllPullRequests) != 0
	hi.acceptsPullRequestsFromStaticPeersOnly = (fl & flagAcceptsPullRequestsFromStaticPeersOnly) != 0
}

func (hi *heartbeatInfo) Bytes() []byte {
	var buf bytes.Buffer
	var timeNanoBin [8]byte

	binary.BigEndian.PutUint64(timeNanoBin[:], uint64(hi.clock.UnixNano()))
	buf.Write(timeNanoBin[:])
	buf.WriteByte(hi.flags())
	return buf.Bytes()
}

func (hi *heartbeatInfo) SetNow() {
	hi.clock = time.Now()
}
