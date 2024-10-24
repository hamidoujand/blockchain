// Package worker implements mining, peer updates, and transaction sharing for
// the blockchain.
package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hamidoujand/blockchain/foundation/blockchain/database"
	"github.com/hamidoujand/blockchain/foundation/blockchain/peer"
	"github.com/hamidoujand/blockchain/foundation/blockchain/state"
)

// CORE NOTE: Sharing new transactions received directly by a wallet is
// performed by this goroutine. When a wallet transaction is received,
// the request goroutine shares it with this goroutine to send it over the
// p2p network. Up to 100 transactions can be pending to be sent before new
// transactions are dropped and not sent.

// maxTxShareRequests represents the max number of pending tx network share
// requests that can be outstanding before share requests are dropped. To keep
// this simple, a buffered channel of this arbitrary number is being used. If
// the channel does become full, requests for new transactions to be shared
// will not be accepted.
const maxTxShareRequests = 100

// Worker manages the POW workflows for the blockchain.
type Worker struct {
	state        *state.State
	wg           sync.WaitGroup
	shut         chan struct{}
	startMining  chan bool
	cancelMining chan bool
	evHandler    state.EventHandler
	txSharing    chan database.BlockTx
}

// Run creates a worker, registers the worker with the state package, and
// starts up all the background processes.
func Run(st *state.State, evHandler state.EventHandler) {
	w := Worker{
		state:        st,
		shut:         make(chan struct{}),
		startMining:  make(chan bool, 1),
		cancelMining: make(chan bool, 1),
		txSharing:    make(chan database.BlockTx, maxTxShareRequests),
		evHandler:    evHandler,
	}

	//state has only access to the interface and can not assign itself to it
	//but the worker has access to the pointer to the state it can easily add
	//itself into it.
	// Register this worker with the state package.
	st.Worker = &w

	// Update this node before starting any support G's.
	w.Sync()

	// Load the set of operations we need to run.
	operations := []func(){
		w.shareTxOperations,
		w.powOperations,
	}

	// Set waitgroup to match the number of G's we need for the set
	// of operations we have.
	g := len(operations)
	w.wg.Add(g)

	// We don't want to return until we know all the G's are up and running.
	hasStarted := make(chan bool)

	// Start all the operational G's.
	for _, op := range operations {
		go func(op func()) {
			defer w.wg.Done()
			hasStarted <- true
			op()
		}(op)
	}

	// Wait for the G's to report they are running.
	for i := 0; i < g; i++ {
		<-hasStarted
	}
}

// Shutdown terminates the goroutine performing work.
func (w *Worker) Shutdown() {
	w.evHandler("worker: shutdown: started")
	defer w.evHandler("worker: shutdown: completed")

	w.evHandler("worker: shutdown: signal cancel mining")
	w.SignalCancelMining()

	w.evHandler("worker: shutdown: terminate goroutines")
	close(w.shut)
	w.wg.Wait()
}

// SignalStartMining starts a mining operation. If there is already a signal
// pending in the channel, just return since a mining operation will start.
func (w *Worker) SignalStartMining() {
	select {
	case w.startMining <- true:
	default:
	}
	w.evHandler("worker: SignalStartMining: mining signaled")
}

// CORE NOTE: The POW mining operation is managed by this function which runs on
// it's own goroutine. When a startMining signal is received (mainly because a
// wallet transaction was received) a block is created and then the POW operation
// starts. This operation can be cancelled if a proposed block is received and
// is validated.

// powOperations handles mining.
func (w *Worker) powOperations() {
	w.evHandler("worker: powOperations: G started")
	defer w.evHandler("worker: powOperations: G completed")

	for {
		select {
		case <-w.startMining:
			if !w.isShutdown() {
				w.runPowOperation()
			}
		case <-w.shut:
			w.evHandler("worker: powOperations: received shut signal")
			return
		}
	}
}

// isShutdown is used to test if a shutdown has been signaled.
func (w *Worker) isShutdown() bool {
	select {
	case <-w.shut:
		return true
	default:
		return false
	}
}

// SignalCancelMining signals the G executing the runMiningOperation function
// to stop immediately.
func (w *Worker) SignalCancelMining() {

	select {
	case w.cancelMining <- true:
	default:
	}
	w.evHandler("worker: SignalCancelMining: MINING: CANCEL: signaled")
}

// runPowOperation takes all the transactions from the mempool and writes a
// new block to the database.
func (w *Worker) runPowOperation() {
	w.evHandler("worker: runMiningOperation: MINING: started")
	defer w.evHandler("worker: runMiningOperation: MINING: completed")

	// Make sure there are transactions in the mempool.
	length := w.state.MempoolLength()
	if length == 0 {
		w.evHandler("worker: runMiningOperation: MINING: no transactions to mine: Txs[%d]", length)
		return
	}

	// After running a mining operation, check if a new operation should
	// be signaled again.
	defer func() {
		length := w.state.MempoolLength()
		if length > 0 {
			w.evHandler("worker: runMiningOperation: MINING: signal new mining operation: Txs[%d]", length)
			w.SignalStartMining()
		}
	}()

	// Drain the cancel mining channel before starting.
	select {
	case <-w.cancelMining:
		w.evHandler("worker: runMiningOperation: MINING: drained cancel channel")
	default:
	}

	// Create a context so mining can be cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Can't return from this function until these G's are complete.
	var wg sync.WaitGroup
	wg.Add(2)

	// This G exists to cancel the mining operation.
	go func() {
		defer func() {
			cancel()
			wg.Done()
		}()

		select {
		case <-w.cancelMining:
			w.evHandler("worker: runMiningOperation: MINING: CANCEL: requested")
		case <-ctx.Done():
		}
	}()

	// This G is performing the mining.
	go func() {
		defer func() {
			cancel() //it is ok to call this cancel() multiple times.
			wg.Done()
		}()

		t := time.Now()
		_, err := w.state.MineNewBlock(ctx)
		duration := time.Since(t)

		w.evHandler("worker: runMiningOperation: MINING: mining duration[%v]", duration)

		if err != nil {
			switch {
			case errors.Is(err, state.ErrNoTransactions):
				w.evHandler("worker: runMiningOperation: MINING: WARNING: no transactions in mempool")
			case ctx.Err() != nil:
				w.evHandler("worker: runMiningOperation: MINING: CANCEL: complete")
			default:
				w.evHandler("worker: runMiningOperation: MINING: ERROR: %s", err)
			}
			return
		}

		// WOW, we mined a block.
	}()
	// Wait for both Gs to finish
	wg.Wait()
}

// CORE NOTE: On startup or when reorganizing the chain, the node needs to be
// in sync with the rest of the network. This includes the mempool and
// blockchain database. This operation needs to finish before the node can
// participate in the network.

// Sync updates the peer list, mempool and blocks.
func (w *Worker) Sync() {
	w.evHandler("worker: sync: started")
	defer w.evHandler("worker: sync: completed")

	for _, peer := range w.state.KnownExternalPeers() {

		// Retrieve the status of this peer.
		peerStatus, err := w.state.NetRequestPeerStatus(peer)
		if err != nil {
			w.evHandler("worker: sync: queryPeerStatus: %s: ERROR: %s", peer.Host, err)
		}

		// Add new peers to this nodes list.
		w.addNewPeers(peerStatus.KnownPeers)

		// Retrieve the mempool from the peer.
		pool, err := w.state.NetRequestPeerMempool(peer)
		if err != nil {
			w.evHandler("worker: sync: retrievePeerMempool: %s: ERROR: %s", peer.Host, err)
		}
		for _, tx := range pool {
			w.evHandler("worker: sync: retrievePeerMempool: %s: Add Tx: %s", peer.Host, tx.SignatureString()[:16])
			w.state.UpsertMempool(tx)
		}

		// If this peer has blocks we don't have, we need to add them.
		if peerStatus.LatestBlockNumber > w.state.LatestBlock().Header.Number {
			w.evHandler("worker: sync: retrievePeerBlocks: %s: latestBlockNumber[%d]", peer.Host, peerStatus.LatestBlockNumber)

			if err := w.state.NetRequestPeerBlocks(peer); err != nil {
				w.evHandler("worker: sync: retrievePeerBlocks: %s: ERROR %s", peer.Host, err)
			}
		}
	}

	// Share with peers this node is available to participate in the network.
	w.state.NetSendNodeAvailableToPeers()
}

// CORE NOTE: The p2p network is managed by this goroutine. There is
// a single node that is considered the origin node. The defaults in
// main.go represent the origin node. That node must be running first.
// All new peer nodes connect to the origin node to identify all other
// peers on the network. The topology is all nodes having a connection
// to all other nodes. If a node does not respond to a network call,
// they are removed from the peer list until the next peer operation.

// addNewPeers takes the list of known peers and makes sure they are included
// in the nodes list of know peers.
func (w *Worker) addNewPeers(knownPeers []peer.Peer) error {
	w.evHandler("worker: runPeerUpdatesOperation: addNewPeers: started")
	defer w.evHandler("worker: runPeerUpdatesOperation: addNewPeers: completed")

	for _, peer := range knownPeers {

		// Don't add this running node to the known peer list.
		if peer.Match(w.state.Host()) {
			continue
		}

		// Only log when the peer is new.
		if w.state.AddKnownPeer(peer) {
			w.evHandler("worker: runPeerUpdatesOperation: addNewPeers: add peer nodes: adding peer-node %s", peer.Host)
		}
	}

	return nil
}

// shareTxOperations handles sharing new block transactions.
func (w *Worker) shareTxOperations() {
	w.evHandler("worker: shareTxOperations: G started")
	defer w.evHandler("worker: shareTxOperations: G completed")

	for {
		select {
		case tx := <-w.txSharing:
			if !w.isShutdown() {
				w.state.NetSendTxToPeers(tx)
			}
		case <-w.shut:
			w.evHandler("worker: shareTxOperations: received shut signal")
			return
		}
	}
}

// SignalShareTx signals a share transaction operation. If
// maxTxShareRequests signals exist in the channel, we won't send these.
func (w *Worker) SignalShareTx(blockTx database.BlockTx) {
	select {
	case w.txSharing <- blockTx:
		w.evHandler("worker: SignalShareTx: share Tx signaled")
	default:
		w.evHandler("worker: SignalShareTx: queue full, transactions won't be shared.")
	}
}
