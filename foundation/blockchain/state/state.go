// Package state is the core API for the blockchain and implements all the
// business rules and processing.
package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/hamidoujand/blockchain/foundation/blockchain/database"
	"github.com/hamidoujand/blockchain/foundation/blockchain/genesis"
	"github.com/hamidoujand/blockchain/foundation/blockchain/mempool"
	"github.com/hamidoujand/blockchain/foundation/blockchain/peer"
)

// The set of different consensus protocols that can be used.
const (
	ConsensusPOW = "POW"
	ConsensusPOA = "POA"
)

const baseURL = "http://%s/v1/node"

// QueryLastest represents to query the latest block in the chain.
const QueryLastest = ^uint64(0) >> 1

// ErrNoTransactions is returned when a block is requested to be created
// and there are not enough transactions.
var ErrNoTransactions = errors.New("no transactions in mempool")

// EventHandler defines a function that is called when events
// occur in the processing of persisting blocks.
type EventHandler func(v string, args ...any)

// Worker interface represents the behavior required to be implemented by any
// package providing support for mining, peer updates, and transaction sharing.
type Worker interface {
	Shutdown()
	Sync()
	SignalStartMining()
	SignalCancelMining()
	SignalShareTx(blockTx database.BlockTx)
}

// Config represents the configuration required to start
// the blockchain node.
type Config struct {
	//account that receives mining rewards
	BeneficiaryID database.AccountID
	Genesis       genesis.Genesis
	EvHandler     EventHandler
	Storage       database.Storage
	KnownPeers    *peer.PeerSet
	Host          string
	Consensus     string
}

// State manages the blockchain database.
type State struct {
	mu sync.RWMutex

	beneficiaryID database.AccountID
	evHandler     EventHandler

	knownPeers *peer.PeerSet
	genesis    genesis.Genesis
	mempool    *mempool.Mempool
	db         *database.Database
	storage    database.Storage

	host      string
	consensus string

	Worker Worker
}

// New constructs a new blockchain for data management.
func New(conf Config) (*State, error) {
	// Build a safe event handler function for use.
	ev := func(v string, args ...any) {
		if conf.EvHandler != nil {
			conf.EvHandler(v, args...)
		}
	}

	// Access the storage for the blockchain.
	db, err := database.New(conf.Genesis, conf.Storage, ev)
	if err != nil {
		return nil, err
	}

	// Construct a mempool with the specified sort strategy.
	mempool, err := mempool.New()
	if err != nil {
		return nil, err
	}

	// Create the State to provide support for managing the blockchain.
	state := State{
		beneficiaryID: conf.BeneficiaryID,
		evHandler:     ev,
		genesis:       conf.Genesis,
		mempool:       mempool,
		db:            db,
		storage:       conf.Storage,
		knownPeers:    conf.KnownPeers,
		host:          conf.Host,
		consensus:     conf.Consensus,
	}
	// The Worker is not set here. The call to worker.Run will assign itself
	// and start everything up and running for the node.
	return &state, nil
}

// Shutdown cleanly brings the node down.
func (s *State) Shutdown() error {
	s.evHandler("state: shutdown: started")
	defer s.evHandler("state: shutdown: completed")

	// Make sure the database file is properly closed.
	// defer func() {
	// 	s.db.Close()
	// }()

	// Stop all blockchain writing activity.
	s.Worker.Shutdown()

	return nil
}

// MempoolLength returns the current length of the mempool.
func (s *State) MempoolLength() int {
	return s.mempool.Count()
}

// Mempool returns a copy of the mempool.
func (s *State) Mempool() []database.BlockTx {
	return s.mempool.PickBest()
}

// UpsertMempool adds a new transaction to the mempool.
func (s *State) UpsertMempool(tx database.BlockTx) error {
	return s.mempool.Upsert(tx)
}

// Accounts returns a copy of the database accounts.
func (s *State) Accounts() map[database.AccountID]database.Account {
	return s.db.Copy()
}

// Genesis returns a copy of the genesis information.
func (s *State) Genesis() genesis.Genesis {
	return s.genesis
}

// QueryAccount returns a copy of the account from the database.
func (s *State) QueryAccount(account database.AccountID) (database.Account, error) {
	return s.db.Query(account)
}

// UpsertWalletTransaction accepts a transaction from a wallet for inclusion.
func (s *State) UpsertWalletTransaction(signedTx database.SignedTx) error {
	// CORE NOTE: It's up to the wallet to make sure the account has a proper
	// balance and this transaction has a proper nonce. Fees will be taken if
	// this transaction is mined into a block it doesn't have enough money to
	// pay or the nonce isn't the next expected nonce for the account.

	// Check the signed transaction has a proper signature, the from matches the
	// signature, and the from and to fields are properly formatted.
	if err := signedTx.Validate(s.genesis.ChainID); err != nil {
		return err
	}

	const oneUnitOfGas = 1
	tx := database.NewBlockTx(signedTx, s.genesis.GasPrice, oneUnitOfGas)
	if err := s.mempool.Upsert(tx); err != nil {
		return err
	}

	s.Worker.SignalStartMining()
	s.Worker.SignalShareTx(tx)

	return nil
}

// MineNewBlock attempts to create a new block with a proper hash that can become
// the next block in the chain.
func (s *State) MineNewBlock(ctx context.Context) (database.Block, error) {
	defer s.evHandler("viewer: MineNewBlock: MINING: completed")

	s.evHandler("state: MineNewBlock: MINING: check mempool count")

	// Are there enough transactions in the pool.
	if s.mempool.Count() == 0 {
		return database.Block{}, ErrNoTransactions
	}

	// Pick the best transactions from the mempool.
	trans := s.mempool.PickBest(s.genesis.TransPerBlock)

	// If PoA is being used, drop the difficulty down to 1 to speed up
	// the mining operation.
	difficulty := s.genesis.Difficulty
	if s.Consensus() == ConsensusPOA {
		difficulty = 1
	}

	// Attempt to create a new block by solving the POW puzzle. This can be cancelled.
	block, err := database.POW(ctx, database.POWArgs{
		BeneficiaryID: s.beneficiaryID,
		Difficulty:    difficulty,
		MiningReward:  s.genesis.MiningReward,
		PrevBlock:     s.db.LatestBlock(),
		StateRoot:     s.db.HashState(),
		Trans:         trans,
		EvHandler:     s.evHandler,
	})
	if err != nil {
		return database.Block{}, err
	}

	s.evHandler("state: MineNewBlock: MINING: validate and update database")

	// Validate the block and then update the blockchain database.
	if err := s.validateUpdateDatabase(block); err != nil {
		return database.Block{}, err
	}

	return block, nil
}

// validateUpdateDatabase takes the block and validates the block against the
// consensus rules. If the block passes, then the state of the node is updated
// including adding the block to disk.
func (s *State) validateUpdateDatabase(block database.Block) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.evHandler("state: validateUpdateDatabase: validate block")

	// CORE NOTE: I could add logic to determine if this block was mined by this
	// node or a peer. If the block is mined by this node, even if a peer beat
	// me to this function for the same block number, I could replace the peer
	// block with my own and attempt to have other peers accept my block instead.

	if err := block.ValidateBlock(s.db.LatestBlock(), s.db.HashState(), s.evHandler); err != nil {
		return err
	}

	s.evHandler("state: validateUpdateDatabase: write to disk")

	// Write the new block to the chain on disk.
	if err := s.db.Write(block); err != nil {
		return err
	}
	s.db.UpdateLatestBlock(block)

	s.evHandler("state: validateUpdateDatabase: update accounts and remove from mempool")

	// Process the transactions and update the accounts.
	for _, tx := range block.MerkleTree.Values() {
		s.evHandler("state: validateUpdateDatabase: tx[%s] update and remove", tx)

		// Remove this transaction from the mempool.
		s.mempool.Delete(tx)

		// Apply the balance changes based on this transaction.
		if err := s.db.ApplyTransaction(block, tx); err != nil {
			s.evHandler("state: validateUpdateDatabase: WARNING : %s", err)
			continue
		}
	}

	// Send an event about this new block.
	// s.blockEvent(block)

	return nil
}

// LatestBlock returns a copy the current latest block.
func (s *State) LatestBlock() database.Block {
	return s.db.LatestBlock()
}

// KnownExternalPeers retrieves a copy of the known peer list without
// including this node.
func (s *State) KnownExternalPeers() []peer.Peer {
	return s.knownPeers.Copy(s.host)
}

// Host returns a copy of host information.
func (s *State) Host() string {
	return s.host
}

// AddKnownPeer provides the ability to add a new peer to
// the known peer list.
func (s *State) AddKnownPeer(peer peer.Peer) bool {
	return s.knownPeers.Add(peer)
}

// NetRequestPeerStatus looks for new nodes on the blockchain by asking
// known nodes for their peer list. New nodes are added to the list.
func (s *State) NetRequestPeerStatus(pr peer.Peer) (peer.PeerStatus, error) {
	s.evHandler("state: NetRequestPeerStatus: started: %s", pr)
	defer s.evHandler("state: NetRequestPeerStatus: completed: %s", pr)

	url := fmt.Sprintf("%s/status", fmt.Sprintf(baseURL, pr.Host))

	var ps peer.PeerStatus
	if err := send(http.MethodGet, url, nil, &ps); err != nil {
		return peer.PeerStatus{}, err
	}

	s.evHandler("state: NetRequestPeerStatus: peer-node[%s]: latest-blknum[%d]: peer-list[%s]", pr, ps.LatestBlockNumber, ps.KnownPeers)

	return ps, nil
}

// send is a helper function to send an HTTP request to a node.
func send(method string, url string, dataSend any, dataRecv any) error {
	var req *http.Request

	switch {
	case dataSend != nil:
		data, err := json.Marshal(dataSend)
		if err != nil {
			return err
		}
		req, err = http.NewRequest(method, url, bytes.NewReader(data))
		if err != nil {
			return err
		}

	default:
		var err error
		req, err = http.NewRequest(method, url, nil)
		if err != nil {
			return err
		}
	}

	var client http.Client
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		msg, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return errors.New(string(msg))
	}

	if dataRecv != nil {
		if err := json.NewDecoder(resp.Body).Decode(dataRecv); err != nil {
			return err
		}
	}

	return nil
}

// NetRequestPeerMempool asks the peer for the transactions in their mempool.
func (s *State) NetRequestPeerMempool(pr peer.Peer) ([]database.BlockTx, error) {
	s.evHandler("state: NetRequestPeerMempool: started: %s", pr)
	defer s.evHandler("state: NetRequestPeerMempool: completed: %s", pr)

	url := fmt.Sprintf("%s/tx/list", fmt.Sprintf(baseURL, pr.Host))

	var mempool []database.BlockTx
	if err := send(http.MethodGet, url, nil, &mempool); err != nil {
		return nil, err
	}

	s.evHandler("state: NetRequestPeerMempool: len[%d]", len(mempool))

	return mempool, nil
}

// NetRequestPeerBlocks queries the specified node asking for blocks this node does
// not have, then writes them to disk.
func (s *State) NetRequestPeerBlocks(pr peer.Peer) error {
	s.evHandler("state: NetRequestPeerBlocks: started: %s", pr)
	defer s.evHandler("state: NetRequestPeerBlocks: completed: %s", pr)

	// CORE NOTE: Ideally you want to start by pulling just block headers and
	// performing the cryptographic audit so you know your're not being attacked.
	// After that you can start pulling the full block data for each block header
	// if you are a full node and maybe only the last 1000 full blocks if you
	// are a pruned node. That can be done in the background. Remember, you
	// only need block headers to validate new blocks.

	// Currently the Ardan blockchain is a full node only system and needs the
	// transactions to have a complete account database. The cryptographic audit
	// does take place as each full block is downloaded from peers.

	from := s.LatestBlock().Header.Number + 1
	url := fmt.Sprintf("%s/block/list/%d/latest", fmt.Sprintf(baseURL, pr.Host), from)

	var blocksData []database.BlockData
	if err := send(http.MethodGet, url, nil, &blocksData); err != nil {
		return err
	}

	s.evHandler("state: NetRequestPeerBlocks: found blocks[%d]", len(blocksData))

	for _, blockData := range blocksData {
		block, err := database.ToBlock(blockData)
		if err != nil {
			return err
		}

		if err := s.ProcessProposedBlock(block); err != nil {
			return err
		}
	}

	return nil
}

// QueryBlocksByNumber returns the set of blocks based on block numbers. This
// function reads the blockchain from disk first.
func (s *State) QueryBlocksByNumber(from uint64, to uint64) []database.Block {
	if from == QueryLastest {
		from = s.db.LatestBlock().Header.Number
		to = from
	}
	if to == QueryLastest {
		to = s.db.LatestBlock().Header.Number
	}

	var out []database.Block
	for i := from; i <= to; i++ {
		block, err := s.db.GetBlock(i)
		if err != nil {
			s.evHandler("state: getblock: ERROR: %s", err)
			return nil
		}
		out = append(out, block)
	}

	return out
}

// NetSendNodeAvailableToPeers shares this node is available to
// participate in the network with the known peers.
func (s *State) NetSendNodeAvailableToPeers() {
	s.evHandler("state: NetSendNodeAvailableToPeers: started")
	defer s.evHandler("state: NetSendNodeAvailableToPeers: completed")

	host := peer.Peer{Host: s.Host()}

	for _, peer := range s.KnownExternalPeers() {
		s.evHandler("state: NetSendNodeAvailableToPeers: send: host[%s] to peer[%s]", host, peer)

		url := fmt.Sprintf("%s/peers", fmt.Sprintf(baseURL, peer.Host))

		if err := send(http.MethodPost, url, host, nil); err != nil {
			s.evHandler("state: NetSendNodeAvailableToPeers: WARNING: %s", err)
		}
	}
}

// ProcessProposedBlock takes a block received from a peer, validates it and
// if that passes, adds the block to the local blockchain.
func (s *State) ProcessProposedBlock(block database.Block) error {
	s.evHandler("state: ValidateProposedBlock: started: prevBlk[%s]: newBlk[%s]: numTrans[%d]", block.Header.PrevBlockHash, block.Hash(), len(block.MerkleTree.Values()))
	defer s.evHandler("state: ValidateProposedBlock: completed: newBlk[%s]", block.Hash())

	// Validate the block and then update the blockchain database.
	if err := s.validateUpdateDatabase(block); err != nil {
		return err
	}

	// If the runMiningOperation function is being executed it needs to stop
	// immediately.
	s.Worker.SignalCancelMining()

	return nil
}

// NetSendTxToPeers shares a new block transaction with the known peers.
func (s *State) NetSendTxToPeers(tx database.BlockTx) {
	s.evHandler("state: NetSendTxToPeers: started")
	defer s.evHandler("state: NetSendTxToPeers: completed")

	// CORE NOTE: Bitcoin does not send the full transaction immediately to save
	// on bandwidth. A node will send the transaction's mempool key first so the
	// receiving node can check if they already have the transaction or not. If
	// the receiving node doesn't have it, then it will request the transaction
	// based on the mempool key it received.

	// For now, the Ardan blockchain just sends the full transaction.
	for _, peer := range s.KnownExternalPeers() {
		s.evHandler("state: NetSendTxToPeers: send: tx[%s] to peer[%s]", tx, peer)
		fmt.Println("**************************************************************")
		url := fmt.Sprintf("%s/tx/submit", fmt.Sprintf(baseURL, peer.Host))

		if err := send(http.MethodPost, url, tx, nil); err != nil {
			s.evHandler("state: NetSendTxToPeers: WARNING: %s", err)
		}
	}
}

// UpsertNodeTransaction accepts a transaction from a node for inclusion.
func (s *State) UpsertNodeTransaction(tx database.BlockTx) error {

	// Check the signed transaction has a proper signature, the from matches the
	// signature, and the from and to fields are properly formatted.
	if err := tx.Validate(s.genesis.ChainID); err != nil {
		return err
	}

	if err := s.mempool.Upsert(tx); err != nil {
		return err
	}
	s.Worker.SignalStartMining()
	return nil
}

// NetSendBlockToPeers takes the new mined block and sends it to all know peers.
func (s *State) NetSendBlockToPeers(block database.Block) error {
	s.evHandler("state: NetSendBlockToPeers: started")
	defer s.evHandler("state: NetSendBlockToPeers: completed")

	for _, peer := range s.KnownExternalPeers() {
		s.evHandler("state: NetSendBlockToPeers: send: block[%s] to peer[%s]", block.Hash(), peer)

		url := fmt.Sprintf("%s/block/propose", fmt.Sprintf(baseURL, peer.Host))

		var status struct {
			Status string `json:"status"`
		}
		if err := send(http.MethodPost, url, database.NewBlockData(block), &status); err != nil {
			return fmt.Errorf("%s: %s", peer.Host, err)
		}
	}

	return nil
}

// RemoveKnownPeer provides the ability to remove a peer from
// the known peer list.
func (s *State) RemoveKnownPeer(peer peer.Peer) {
	s.knownPeers.Remove(peer)
}

// Consensus returns a copy of consensus algorithm being used.
func (s *State) Consensus() string {
	return s.consensus
}

// KnownPeers retrieves a copy of the full known peer list which includes
// this node as well. Used by the PoA selection algorithm.
func (s *State) KnownPeers() []peer.Peer {
	return s.knownPeers.Copy("")
}
