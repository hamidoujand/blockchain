package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/hamidoujand/blockchain/foundation/blockchain/database"
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
	//parse the raw private key of the account that sending the TX into a ecdsa.PrivateKey struct.
	private, err := crypto.LoadECDSA("zblock/accounts/kennedy.ecdsa")
	if err != nil {
		return fmt.Errorf("load ECDSA key: %w", err)
	}

	//tx to sign
	tx := Tx{
		FromID: "Hamid",
		ToID:   "Wolf",
		Value:  10,
	}

	//marshal it to sign it
	data, err := json.Marshal(tx)
	if err != nil {
		return fmt.Errorf("json: %w", err)
	}

	//add our unique stamp to it
	stamp := []byte(fmt.Sprintf("\x19Ardan Signed Message:\n%d", len(data)))

	digest := crypto.Keccak256(stamp, data) //hash of the data + salt as 32 bytes.

	sig, err := crypto.Sign(digest, private) //signature that we use to sign the data
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	hex := hexutil.Encode(sig)
	fmt.Printf("DIGITAL SIGNATURE:\n%s\n", hex)

	//==========================================================================
	// get the public key from the signature.

	publicKey, err := crypto.SigToPub(digest, sig) // "hash + salt"  + "signature"
	if err != nil {
		return fmt.Errorf("sig to pub: %w", err)
	}

	//get the public address
	address := crypto.PubkeyToAddress(*publicKey)
	fmt.Printf("PUBLIC ADDR:\n%s\n", address.String())

	//==========================================================================
	// let send another tx
	tx = Tx{
		FromID: "Hamid",
		ToID:   "Bill",
		Value:  250,
	}
	// marshal it to sign it
	data, err = json.Marshal(tx)
	if err != nil {
		return fmt.Errorf("json: %w", err)
	}

	//add our unique stamp to it
	stamp = []byte(fmt.Sprintf("\x19Ardan Signed Message:\n%d", len(data)))

	digest = crypto.Keccak256(stamp, data)  //hash of the data + salt as 32 bytes.
	sig, err = crypto.Sign(digest, private) //signature that we use to sign the data
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	hex = hexutil.Encode(sig)
	fmt.Printf("DIGITAL SIGNATURE 2:\n%s\n", hex)

	// get the public key from the signature.

	publicKey, err = crypto.SigToPub(digest, sig) // "hash + salt"  + "signature"
	if err != nil {
		return fmt.Errorf("sig to pub: %w", err)
	}

	// get the public address
	address = crypto.PubkeyToAddress(*publicKey)
	fmt.Printf("PUBLIC ADDR 2:\n%s\n", address.String())

	//to different signature but the same public address of sender, since the signature
	//was generated off of that tx and tx are different.

	//==========================================================================
	// get the R S V out of the sig
	v, s, r, err := ToVRSFromHexSignature(hexutil.Encode(sig))
	if err != nil {
		return fmt.Errorf("to VRS: %w", err)
	}

	fmt.Printf("\nR:[%d]\nS:[%d]\nV:[%d]\n", r, s, v)

	fmt.Println("====================== TX ========================")
	transaction, err := database.NewTx(1, 1, "0xF01813E4B85e178A83e29B8E7bF26BD830a25f32", "0xbEE6ACE826eC3DE1B6349888B9151B92522F7F76", 1000, 0, nil)
	if err != nil {
		return fmt.Errorf("newTx: %w", err)
	}

	//now sign it
	signedTX, err := transaction.Sign(private)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}

	fmt.Printf("%+v\n", signedTX)
	return nil
}

func ToVRSFromHexSignature(sigStr string) (v, r, s *big.Int, err error) {
	sig, err := hex.DecodeString(sigStr[2:]) // 0x<hex>
	if err != nil {
		return nil, nil, nil, err
	}

	r = big.NewInt(0).SetBytes(sig[:32]) //first 32 bytes are R
	s = big.NewInt(0).SetBytes(sig[32:64])
	v = big.NewInt(0).SetBytes([]byte{sig[64]})

	return v, r, s, nil
}
