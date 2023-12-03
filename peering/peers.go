package peering

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"github.com/multiformats/go-multiaddr"
	"github.com/spf13/viper"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	Config struct {
		LedgerIDHash     [32]byte
		HostIDPrivateKey ed25519.PrivateKey
		HostID           peer.ID
		HostPort         int
		KnownPeers       map[string]multiaddr.Multiaddr // name -> PeerAddr
		LogLevel         zapcore.Level
		LogOutputs       []string
	}

	Peers struct {
		mutex             sync.RWMutex
		cfg               *Config
		log               *zap.SugaredLogger
		ctx               context.Context
		stopHeartbeatChan chan struct{}
		stopOnce          sync.Once
		host              host.Host
		peers             map[peer.ID]*Peer // except self
		onReceiveGossip   func(from peer.ID, txBytes []byte)
		onReceivePull     func(from peer.ID, txids []core.TransactionID)
		traceFlag         atomic.Bool
	}

	Peer struct {
		mutex                  sync.RWMutex
		name                   string
		id                     peer.ID
		lastActivity           time.Time
		postponeActivityUntil  time.Time
		hasTxStore             bool
		needsLogLostConnection bool
	}
)

const (
	lppProtocolGossip    = "/proxima/gossip/1.0.0"
	lppProtocolPull      = "/proxima/pull/1.0.0"
	lppProtocolHeartbeat = "/proxima/heartbeat/1.0.0"

	// blocking comms with the peer which violates the protocol
	commBlockDuration = time.Minute

	// clockTolerance is how big the difference between local and remote clocks is tolerated
	clockTolerance = 5 * time.Second // for testing only
)

func NewPeersDummy() *Peers {
	return &Peers{
		peers:           make(map[peer.ID]*Peer),
		onReceiveGossip: func(_ peer.ID, _ []byte) {},
		onReceivePull:   func(_ peer.ID, _ []core.TransactionID) {},
	}
}

func New(cfg *Config, ctx context.Context) (*Peers, error) {
	hostIDPrivateKey, err := crypto.UnmarshalEd25519PrivateKey(cfg.HostIDPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("wrong private key: %w", err)
	}
	lppHost, err := libp2p.New(
		libp2p.Identity(hostIDPrivateKey),
		libp2p.ListenAddrStrings(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", cfg.HostPort)),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.NoSecurity,
	)
	if err != nil {
		return nil, fmt.Errorf("unable create libp2p host: %w", err)
	}

	ret := &Peers{
		cfg:               cfg,
		log:               global.NewLogger("[peering]", cfg.LogLevel, cfg.LogOutputs, ""),
		ctx:               ctx,
		stopHeartbeatChan: make(chan struct{}),
		host:              lppHost,
		peers:             make(map[peer.ID]*Peer),
		onReceiveGossip:   func(_ peer.ID, _ []byte) {},
		onReceivePull:     func(_ peer.ID, _ []core.TransactionID) {},
	}

	for name, maddr := range cfg.KnownPeers {
		if err = ret.AddPeer(maddr, name); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func readPeeringConfig() (*Config, error) {
	cfg := &Config{
		KnownPeers: make(map[string]multiaddr.Multiaddr),
	}
	cfg.HostPort = viper.GetInt("peering.host.port")
	if cfg.HostPort == 0 {
		return nil, fmt.Errorf("peering.host.port: wrong port")
	}
	pkStr := viper.GetString("peering.host.id_private_key")
	pkBin, err := hex.DecodeString(pkStr)
	if err != nil {
		return nil, fmt.Errorf("host.id_private_key: wrong id private key: %v", err)
	}
	if len(pkBin) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("host.private_key: wrong host id private key size")
	}
	cfg.HostIDPrivateKey = pkBin

	encodedHostID := viper.GetString("peering.host.id")
	cfg.HostID, err = peer.Decode(encodedHostID)
	if err != nil {
		return nil, fmt.Errorf("can't decode host ID: %v", err)
	}
	privKey, err := crypto.UnmarshalEd25519PrivateKey(cfg.HostIDPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("UnmarshalEd25519PrivateKey: %v", err)
	}

	if !cfg.HostID.MatchesPrivateKey(privKey) {
		return nil, fmt.Errorf("config: host private key does not match hostID")
	}

	peerNames := util.KeysSorted(viper.GetStringMap("peering.peers"), func(k1, k2 string) bool {
		return k1 < k2
	})

	for _, peerName := range peerNames {
		addrString := viper.GetString("peering.peers." + peerName)
		if cfg.KnownPeers[peerName], err = multiaddr.NewMultiaddr(addrString); err != nil {
			return nil, fmt.Errorf("can't parse multiaddress: %w", err)
		}
	}
	return cfg, nil
}

func NewPeersFromConfig(ctx context.Context, logLevel zapcore.Level, logOutputs []string) (*Peers, error) {
	cfg, err := readPeeringConfig()
	if err != nil {
		return nil, err
	}

	cfg.LogLevel = logLevel
	cfg.LogOutputs = logOutputs
	cfg.LedgerIDHash = genesis.GetGlobalLedgerIdentity().Hash()

	return New(cfg, ctx)
}

func (ps *Peers) Run() {
	ps.host.SetStreamHandler(lppProtocolGossip, ps.gossipStreamHandler)
	ps.host.SetStreamHandler(lppProtocolPull, ps.pullStreamHandler)
	ps.host.SetStreamHandler(lppProtocolHeartbeat, ps.heartbeatStreamHandler)

	go ps.heartbeatLoop()
	go func() {
		<-ps.ctx.Done()
		ps.Stop()
	}()

	ps.log.Infof("libp2p host %s (self) started on %v with %d configured known peers", ShortPeerIDString(ps.host.ID()), ps.host.Addrs(), len(ps.cfg.KnownPeers))
	_ = ps.log.Sync()
}

func (ps *Peers) Stop() {
	ps.stopOnce.Do(func() {
		ps.log.Infof("stopping libp2p host %s (self)..", ShortPeerIDString(ps.host.ID()))
		_ = ps.log.Sync()
		close(ps.stopHeartbeatChan)
		_ = ps.host.Close()
	})
}

func (ps *Peers) AddPeer(maddr multiaddr.Multiaddr, name string) error {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return fmt.Errorf("can't get multiaddress info: %v", err)
	}
	ps.host.Peerstore().AddAddr(info.ID, maddr, peerstore.PermanentAddrTTL)
	if _, already := ps.peers[info.ID]; !already {
		ps.peers[info.ID] = &Peer{
			name: name,
			id:   info.ID,
		}
	}
	return nil
}

func (ps *Peers) SetTrace(b bool) {
	ps.traceFlag.Store(b)
}

func (ps *Peers) trace(format string, args ...any) {
	if ps.traceFlag.Load() {
		ps.log.Infof("TRACE "+format, util.EvalLazyArgs(args...)...)
		_ = ps.log.Sync()
	}
}

func (ps *Peers) OnReceiveTxBytes(fun func(from peer.ID, txBytes []byte)) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	ps.onReceiveGossip = fun
}

func (ps *Peers) OnReceivePullRequest(fun func(from peer.ID, txids []core.TransactionID)) {
	ps.mutex.Lock()
	defer ps.mutex.Unlock()

	ps.onReceivePull = fun
}

func (ps *Peers) getPeer(id peer.ID) *Peer {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	if ret, ok := ps.peers[id]; ok {
		return ret
	}
	return nil
}

func (ps *Peers) getPeerIDs() []peer.ID {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	return util.Keys(ps.peers)
}

func (ps *Peers) getPeerIDsWithOpenComms() []peer.ID {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	ret := make([]peer.ID, 0)
	for id, p := range ps.peers {
		if p.isCommunicationOpen() {
			ret = append(ret, id)
		}
	}
	return ret
}

func (ps *Peers) gossipStreamHandler(stream network.Stream) {
	id := stream.Conn().RemotePeer()
	p := ps.getPeer(id)
	if p == nil {
		// peer not found
		ps.log.Warnf("unknown peer %s", id.String())
		_ = stream.Reset()
		return
	}

	if !p.isCommunicationOpen() {
		_ = stream.Reset()
		return
	}

	txBytes, err := readFrame(stream)
	if err != nil {
		ps.log.Errorf("error while reading message from peer %s: %v", id.String(), err)
		_ = stream.Reset()
		return
	}
	defer stream.Close()

	p.evidenceActivity(ps, "gossip")
	ps.onReceiveGossip(id, txBytes)
}

func (ps *Peers) pullStreamHandler(stream network.Stream) {
	id := stream.Conn().RemotePeer()
	p := ps.getPeer(id)
	if p == nil {
		// peer not found
		ps.log.Warnf("unknown peer %s", id.String())
		_ = stream.Reset()
		return
	}

	if !p.isCommunicationOpen() {
		_ = stream.Reset()
		return
	}

	msgData, err := readFrame(stream)
	if err != nil {
		ps.log.Errorf("error while reading message from peer %s: %v", id.String(), err)
		_ = stream.Reset()
		return
	}
	defer stream.Close()

	txLst, err := decodePeerMsgPull(msgData)
	if err != nil {
		ps.log.Errorf("error while decoding pull message from peer %s: %v", id.String(), err)
		return
	}

	p.evidenceActivity(ps, "pull")
	ps.onReceivePull(id, txLst)
}

func (ps *Peers) PeerIsAlive(id peer.ID) bool {
	p := ps.getPeer(id)
	if p == nil {
		return false
	}
	return p.isAlive()
}

func (ps *Peers) PeerName(id peer.ID) string {
	p := ps.getPeer(id)
	if p == nil {
		return "(unknown peer)"
	}
	return p.name
}

func (ps *Peers) sendPullToPeer(id peer.ID, txLst ...core.TransactionID) {
	stream, err := ps.host.NewStream(ps.ctx, id, lppProtocolPull)
	if err != nil {
		return
	}
	defer stream.Close()

	_ = writeFrame(stream, encodePeerMsgPull(txLst...))
}

// PullTransactionsFromRandomPeer sends pull request to the random peer which has txStore
func (ps *Peers) PullTransactionsFromRandomPeer(txids ...core.TransactionID) bool {
	if len(txids) == 0 {
		return false
	}
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	all := util.Keys(ps.peers)
	for _, idx := range rand.Perm(len(all)) {
		rndID := all[idx]
		p := ps.peers[rndID]
		if p.isCommunicationOpen() && p.isAlive() && p.HasTxStore() {
			global.TracePull(ps.log, "pull to random peer %s: %s",
				func() any { return ShortPeerIDString(rndID) },
				func() any { return _txidLst(txids...) },
			)

			ps.sendPullToPeer(rndID, txids...)
			return true
		}
	}
	return false
}

func _txidLst(txids ...core.TransactionID) string {
	ret := make([]string, len(txids))
	for i := range ret {
		ret[i] = txids[i].StringShort()
	}
	return strings.Join(ret, ",")
}

func (ps *Peers) GossipTxBytesToPeers(txBytes []byte, except ...peer.ID) int {
	ps.mutex.RLock()
	defer ps.mutex.RUnlock()

	countSent := 0
	for id, p := range ps.peers {
		if !p.isCommunicationOpen() {
			continue
		}
		if len(except) > 0 && id == except[0] {
			continue
		}
		if !p.isAlive() {
			continue
		}
		if ps.SendTxBytesToPeer(id, txBytes) {
			countSent++
		}
	}
	return countSent
}

func (ps *Peers) SendTxBytesToPeer(id peer.ID, txBytes []byte) bool {
	ps.trace("SendTxBytesToPeer to %s, length: %d (host %s)",
		func() any { return ShortPeerIDString(id) },
		len(txBytes),
		func() any { return ShortPeerIDString(ps.host.ID()) },
	)

	if p := ps.getPeer(id); p == nil || !p.isCommunicationOpen() {
		return false
	}

	stream, err := ps.host.NewStream(ps.ctx, id, lppProtocolGossip)
	if err != nil {
		ps.trace("SendTxBytesToPeer to %s: %v (host %s)",
			func() any { return ShortPeerIDString(id) }, err,
			func() any { return ShortPeerIDString(ps.host.ID()) },
		)
		return false
	}
	defer stream.Close()

	if err = writeFrame(stream, txBytes); err != nil {
		ps.trace("SendTxBytesToPeer.writeFrame to %s: %v (host %s)", ShortPeerIDString(id), err, ShortPeerIDString(ps.host.ID()))
	}
	return err == nil
}
