// Package state is the core API for the blockchain and implements all the
// business rules and processing.
package state

import (
	"sync"

	"github.com/hamidoujand/blockchain/foundation/blockchain/database"
	"github.com/hamidoujand/blockchain/foundation/blockchain/genesis"
)

// EventHandler defines a function that is called when events
// occur in the processing of persisting blocks.
type EventHandler func(v string, args ...any)

// Config represents the configuration required to start
// the blockchain node.
type Config struct {
	//account that receives mining rewards
	BeneficiaryID database.AccountID
	Genesis       genesis.Genesis
	EvHandler     EventHandler
}

// State manages the blockchain database.
type State struct {
	mu sync.RWMutex

	beneficiaryID database.AccountID
	evHandler     EventHandler

	genesis genesis.Genesis
	db      *database.Database
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
	db, err := database.New(conf.Genesis, ev)
	if err != nil {
		return nil, err
	}

	// Create the State to provide support for managing the blockchain.
	state := State{
		beneficiaryID: conf.BeneficiaryID,
		evHandler:     ev,
		genesis:       conf.Genesis,
		db:            db,
	}

	return &state, nil
}

// Shutdown cleanly brings the node down.
func (s *State) Shutdown() error {
	s.evHandler("state: shutdown: started")
	defer s.evHandler("state: shutdown: completed")
	return nil
}
