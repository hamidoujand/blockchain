package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

// Tx is the transactional information between two parties.
type Tx struct {
	FromID string `json:"from"`
	ToID   string `json:"to"`
	Value  uint64 `json:"value"`
}

func main() {
	if err := run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func run() error {
	private, err := crypto.LoadECDSA("zblock/accounts/kennedy.ecdsa")
	if err != nil {
		return fmt.Errorf("load ECDSA key: %w", err)
	}

	tx := Tx{
		FromID: "Hamid",
		ToID:   "Wolf",
		Value:  10,
	}

	data, err := json.Marshal(tx)
	if err != nil {
		return fmt.Errorf("json: %w", err)
	}

	stamp := []byte(fmt.Sprintf("\x19Ardan Signed Message:\n%d", len(data)))

	digest := crypto.Keccak256(stamp, data) //hash it to 32 bytes

	sig, err := crypto.Sign(digest, private)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	hex := hexutil.Encode(sig)
	fmt.Printf("SIGNATURE:\n%s\n", hex)

	//==========================================================================
	// get the public key from the signature.

	publicKey, err := crypto.SigToPub(digest, sig)
	if err != nil {
		return fmt.Errorf("sig to pub: %w", err)
	}

	//get the public address
	address := crypto.PubkeyToAddress(*publicKey)
	fmt.Printf("PUBLIC ADDR:\n%s\n", address.String())
	return nil
}
