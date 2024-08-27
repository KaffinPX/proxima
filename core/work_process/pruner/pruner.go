package pruner

import (
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/prometheus/client_golang/prometheus"
)

type (
	environment interface {
		global.NodeGlobal
		WithGlobalWriteLock(func())
		Vertices(filterByID ...func(txid *ledger.TransactionID) bool) []*vertex.WrappedTx
		PurgeDeletedVertices(deleted []*vertex.WrappedTx)
		PurgeCachedStateReaders() (int, int)
		NumVertices() int
		NumStateReaders() int
	}
	Pruner struct {
		environment

		// metrics
		metricsEnabled       bool
		numVerticesGauge     prometheus.Gauge
		numStateReadersGauge prometheus.Gauge
	}
)

const (
	Name     = "pruner"
	TraceTag = Name
)

func New(env environment) *Pruner {
	ret := &Pruner{
		environment: env,
	}
	ret.registerMetrics()
	return ret
}

// pruneVertices returns how many marked for deletion and how many past cones unreferenced
func (p *Pruner) pruneVertices() (markedForDeletionCount, unreferencedPastConeCount int, refStats [6]uint32) {
	toDelete := make([]*vertex.WrappedTx, 0)
	nowis := time.Now()
	for _, vid := range p.Vertices() {
		markedForDeletion, unreferencedPastCone, nReferences := vid.DoPruningIfRelevant(nowis)
		if markedForDeletion {
			toDelete = append(toDelete, vid)
			markedForDeletionCount++
			p.Tracef(TraceTag, "marked for deletion %s", vid.IDShortString)
		}
		if unreferencedPastCone {
			unreferencedPastConeCount++
			p.Tracef(TraceTag, "past cone of %s has been unreferenced", vid.IDShortString)
		}
		if int(nReferences) < len(refStats)-1 {
			refStats[nReferences]++
		} else {
			refStats[len(refStats)-1]++
		}
	}
	p.PurgeDeletedVertices(toDelete)
	for _, deleted := range toDelete {
		p.StopTracingTx(deleted.ID)
	}
	return
}

func (p *Pruner) Start() {
	p.Infof0("STARTING MemDAG pruner..")
	go func() {
		p.mainLoop()
		p.Log().Debugf("MemDAG pruner STOPPED")
	}()
}

func (p *Pruner) doPrune() {
	nDeleted, nUnReferenced, refStats := p.pruneVertices()
	nReadersPurged, readersLeft := p.PurgeCachedStateReaders()

	p.Infof0("[memDAG pruner] vertices: %d, deleted: %d, detached past cones: %d. state readers purged: %d, left: %d. Ref stats: %v",
		p.NumVertices(), nDeleted, nUnReferenced, nReadersPurged, readersLeft, refStats)
}

func (p *Pruner) mainLoop() {
	p.MarkWorkProcessStarted(Name)
	defer p.MarkWorkProcessStopped(Name)

	prunerLoopPeriod := ledger.SlotDuration() / 2

	for {
		select {
		case <-p.Ctx().Done():
			return
		case <-time.After(prunerLoopPeriod):
		}
		p.doPrune()
		p.updateMetrics()
	}
}

func (p *Pruner) registerMetrics() {
	p.numVerticesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "memDAG_numVerticesGauge",
		Help: "number of vertices in the memDAG",
	})
	p.MetricsRegistry().MustRegister(p.numVerticesGauge)

	p.numStateReadersGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "memDAG_numStateReadersGauge",
		Help: "number of cached state readers in the memDAG",
	})
	p.MetricsRegistry().MustRegister(p.numStateReadersGauge)
}

func (p *Pruner) updateMetrics() {
	p.numVerticesGauge.Set(float64(p.NumVertices()))
	p.numStateReadersGauge.Set(float64(p.NumStateReaders()))
}
