package erc20

import (
	"encoding/binary"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	log "github.com/sirupsen/logrus"
)

// only encoding functions which mutate state, also only doing MVP of erc20 and skipping approve()/tranferFrom()
var functionABIs = map[string]string{
	"initErc20": `[{ "type" : "function", "name" : "initErc20", "inputs" : [ { "name" : "owner", "type" : "address" } ] }]`,
	"transfer":  `[{ "type" : "function", "name" : "transfer", "inputs" : [ { "name" : "_to", "type" : "address" },  { "name" : "_value", "type" : "uint64" }] }]`,
}

var functionSigs = map[string][4]byte{
	"initErc20": {0x33, 0xb4, 0x2f, 0x4f}, // abi.encodeWithSignature("initErc20(address)")
	"transfer":  {0x5d, 0x35, 0x9f, 0xbd}, // abi.encodeWithSignature("transfer(address,uint64)")
}

type ERC20 struct {
	Owner     *common.Address
	Balances  *map[common.Address]uint64
	Approvals *map[common.Address]uint64
	Nonces    *map[common.Address]uint64
}

func NewERC20() *ERC20 {
	balances := make(map[common.Address]uint64)
	approvals := make(map[common.Address]uint64)
	nonces := make(map[common.Address]uint64)

	return &ERC20{
		Owner:     nil,
		Balances:  &balances,
		Approvals: &approvals,
		Nonces:    &nonces,
	}
}

// this should be called as the genesis tx for the rollup
func initErc20(erc20 *ERC20, owner common.Address) bool {
	// ensure not already init'd
	if erc20.Owner != nil {
		log.Errorf("owner already set %v, new owner %v not set\n", &erc20.Owner, owner)
		return false
	}

	// set owner
	erc20.Owner = &owner

	// give owner starting balance
	(*erc20.Balances)[owner] = 10000

	log.Info("init ran successfully")

	return true
}

func transfer(erc20 *ERC20, from common.Address, to common.Address, amount uint64) bool {
	// check that from has enough balance
	if (*erc20.Balances)[from] < amount {
		log.Errorf("from %s's balance of %d is not enough to transfer %d", from, (*erc20.Balances)[from], amount)
		return false
	}

	// perform transfer
	(*erc20.Balances)[from] -= amount
	(*erc20.Balances)[to] += amount

	log.Infof("transferred %d from %s to %s", amount, from, to)

	return true
}

func processTransaction(a *App, txEncoded []byte) {
	// note: we ignore all tx elements execpt the nonce, signer, chainId, and data
	log.Info("in processTransaction")

	// decode transaction
	signedTx, err := decodeTx(txEncoded)
	if err != nil {
		log.Errorf("error decoding tx in processTransaction %v: %s\n", txEncoded, err)
		return
	}

	// check chain id (replay protection)
	chainId := new(big.Int).SetBytes(a.rollupID[:32])
	if chainId.Cmp(signedTx.ChainId()) != 0 {
		log.Errorf("incorrect chain id, expected %v, got %v", chainId, signedTx.ChainId())
		return
	}

	// grab signer
	signer := types.LatestSignerForChainID(chainId)
	from, err := types.Sender(signer, signedTx)
	if err != nil {
		log.Errorf("err: %s", err)
		log.Errorf("could not extract signer from tx: %v", signedTx)
		return
	}

	// check nonce (replay protection)
	wantedNonce := (*a.erc20.Nonces)[from]
	signedNonce := signedTx.Nonce()

	if signedNonce != wantedNonce {
		log.Errorf("wrong nonce, expected %d but got %d: %v", wantedNonce, signedNonce, signedTx)
		return
	}

	// route tx to function if it exists
	var funcSig [4]byte
	copy(funcSig[:], signedTx.Data()[:4])

	switch funcSig {
	case (functionSigs["initErc20"]):
		{
			// extract owner
			var ownerBytes [20]byte
			copy(ownerBytes[:], signedTx.Data()[16:36])
			owner := common.BytesToAddress(ownerBytes[:])

			// perform init logic
			success := initErc20(a.erc20, owner)
			if !success {
				// escape nonce update
				return
			}
		}
	case (functionSigs["transfer"]):
		{
			// extract to
			var toBytes [20]byte
			copy(toBytes[:], signedTx.Data()[16:36])
			to := common.BytesToAddress(toBytes[:])

			// extract amount
			var amountBytes [32]byte
			copy(amountBytes[:], signedTx.Data()[36:68])
			amount := binary.BigEndian.Uint64(amountBytes[24:])

			success := transfer(a.erc20, from, to, amount)
			if !success {
				// escape nonce update
				return
			}
		}
	default:
		log.Errorf("received invalid function sig: %s", funcSig)
		return
	}

	// if successful increment nonce
	log.Infof("incrementing nonce for %s to %d", from, (*a.erc20.Nonces)[from])
	(*a.erc20.Nonces)[from] += 1
}

// register rollup specific handler
func registerHandlers(a *App) {
	a.restRouter.HandleFunc("/transfer", a.postTransfer).Methods("POST")
	a.restRouter.HandleFunc("/balances", a.getBalances).Methods("GET")
}

// encode transaction into bytes to be sent to the sequencer
func encodeTx(signedTx types.Transaction) ([]byte, error) {
	encodedTx, err := signedTx.MarshalBinary()
	if err != nil {
		log.Errorf("error encoding transaction %v: %s\n", signedTx, err)
		return nil, err
	}
	return encodedTx, nil
}

// decode transaction from bytes back into rollup format
func decodeTx(txEncoded []byte) (*types.Transaction, error) {
	tx := &types.Transaction{}
	if err := tx.UnmarshalBinary(txEncoded); err != nil {
		log.Errorf("error decoding transaction: %s\n", err)
		return nil, err
	}
	return tx, nil
}

func (a *App) getBalances(w http.ResponseWriter, r *http.Request) {
	log.Infof("processing getBalances")

	// print to console also for debugging
	for account, balance := range *a.erc20.Balances {
		log.Infof("account: %s, balance: %d", account, balance)
	}

	balancesJson, err := json.Marshal(*a.erc20.Balances)
	if err != nil {
		log.Errorf("error marshalling balances: %s\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Write(balancesJson)
}

// create starting block for this rollup
func GenesisTransaction(chainId big.Int, owner string, ownerPk string) []byte {
	// encode transaction for 'init' function which creates start of new ERC20 contract, similar to a constructor
	abiObject, err := abi.JSON(strings.NewReader(functionABIs["initErc20"]))
	if err != nil {
		log.Errorf("Error: %s", err)
		panic(err)
	}

	// encode arguments
	data, err := abiObject.Pack("initErc20", common.HexToAddress(owner))
	if err != nil {
		log.Errorf("Error: %s", err)
		panic(err)
	}

	// create and sign transaction
	signedTx, err := SignTxn(ownerPk, chainId, 0, data)
	if err != nil {
		log.Errorf("Error: %s", err)
		panic(err)
	}

	// rlp encode into byte string
	encodedTx, err := encodeTx(*signedTx)
	if err != nil {
		log.Errorf("Error: %s", err)
		panic(err)
	}

	return encodedTx
}

// send rollup message transfer
type Transfer struct {
	SignerPub  string `json:"signerPub"`
	SignerPriv string `json:"signerPriv"`
	To         string `json:"to"`
	Amount     uint64 `json:"amount"`
}

func (a *App) postTransfer(w http.ResponseWriter, r *http.Request) {
	log.Info("in postTransfer")
	var transfer Transfer
	// decode transfer request
	err := json.NewDecoder(r.Body).Decode(&transfer)
	if err != nil {
		log.Errorf("error decoding transfer request: %s\n", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// turn into evm transaction request
	abiObject, err := abi.JSON(strings.NewReader(functionABIs["transfer"]))
	if err != nil {
		log.Errorf("error crafting abi object: %s", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// encode arguments
	data, err := abiObject.Pack("transfer", common.HexToAddress(transfer.To), &transfer.Amount)
	if err != nil {
		log.Errorf("error encoding arguments: %s", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// get next unprocessed nonce (repeat nonces can happen if multiple clients are used)
	publicKey := common.HexToAddress(transfer.SignerPub)
	nonce := (*a.erc20.Nonces)[publicKey]

	// create and sign transaction
	chainId := new(big.Int).SetBytes(a.rollupID)
	signedTx, err := SignTxn(transfer.SignerPriv, *chainId, nonce, data)
	if err != nil {
		log.Errorf("error signing transaction: %s", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// rlp encode into byte string
	encodedTx, err := encodeTx(*signedTx)
	if err != nil {
		log.Errorf("error encoding transaction: %s", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// send transaction to the sequencer
	resp, err := a.sequencerClient.SendMessage(encodedTx)
	if err != nil {
		log.Errorf("error sending message: %s\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	log.WithField("responseCode", resp.Code).Debug("transaction submission result")
}

func SignTxn(signersKey string, chainId big.Int, nonce uint64, data []byte) (*types.Transaction, error) {
	privateKey, err := crypto.HexToECDSA(signersKey)

	if err != nil {
		log.Errorf("error decoding private key: %s\n", err)
		return nil, err
	}

	contractAddress := common.BigToAddress(big.NewInt(0))

	log.Info("making tx")
	txData := types.DynamicFeeTx{
		Nonce:      nonce,
		To:         &contractAddress, // whole rollup is acting as contract so can be zero
		Value:      big.NewInt(0),    // we don't support native transfers
		Gas:        0,                // we don't process gas so values don't matter
		GasTipCap:  big.NewInt(0),
		GasFeeCap:  big.NewInt(0),
		ChainID:    &chainId, // should be the rollup's chain id
		AccessList: types.AccessList([]types.AccessTuple{}),
		Data:       data, // is function call with arguments
		V:          big.NewInt(0),
		R:          big.NewInt(0),
		S:          big.NewInt(0),
	}

	tx := types.NewTx(&txData)
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(&chainId), privateKey)
	if err != nil {
		log.Errorf("error signing transaction: %s\n", err)
		return nil, err
	}

	return signedTx, nil
}
