package erc20

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"

	log "github.com/sirupsen/logrus"

	astriaGrpc "buf.build/gen/go/astria/execution-apis/grpc/go/astria/execution/v1alpha2/executionv1alpha2grpc"
	"github.com/gorilla/mux"
	"github.com/rs/cors"
	"google.golang.org/grpc"
)

type Config struct {
	SequencerRPC string `env:"SEQUENCER_RPC, required"`
	ConductorRPC string `env:"CONDUCTOR_RPC, required"`
	RESTApiPort  string `env:"RESTAPI_PORT, required"`
	RollupName   string `env:"ROLLUP_NAME, required"`
	SeqPrivate   string `env:"SEQUENCER_PRIVATE, required"`
}

// App is the main application struct, containing all the necessary components.
type App struct {
	executionRPC       string
	sequencerRPC       string
	sequencerClient    SequencerClient
	restRouter         *mux.Router
	restAddr           string
	rollupBlocks       *RollupBlocks
	rollupName         string
	rollupID           []byte
	newBlockChan       chan Block
	erc20              *ERC20
	lastBlockProcessed uint32
	sync.RWMutex
}

func NewApp(cfg Config) *App {
	log.Debugf("Creating new messenger app with config: %v", cfg)

	rollupID := sha256.Sum256([]byte(cfg.RollupName))
	newBlockChan := make(chan Block, 20)
	chainId := new(big.Int).SetBytes(rollupID[:32])
	rollupBlocks := NewRollupBlocks(newBlockChan, *chainId)
	router := mux.NewRouter()

	// sequencer private key
	privateKeyBytes, err := hex.DecodeString(cfg.SeqPrivate)
	if err != nil {
		panic(err)
	}
	private := ed25519.NewKeyFromSeed(privateKeyBytes)

	erc20 := NewERC20()

	return &App{
		executionRPC:       cfg.ConductorRPC,
		sequencerRPC:       cfg.SequencerRPC,
		sequencerClient:    *NewSequencerClient(cfg.SequencerRPC, rollupID[:], private),
		restRouter:         router,
		restAddr:           cfg.RESTApiPort,
		rollupBlocks:       rollupBlocks,
		rollupName:         cfg.RollupName,
		rollupID:           rollupID[:],
		newBlockChan:       newBlockChan,
		erc20:              erc20,
		lastBlockProcessed: 0,
	}
}

// makeExecutionServer creates a new ExecutionServiceServer.
func (a *App) makeExecutionServer() *ExecutionServiceServerV1Alpha2 {
	return NewExecutionServiceServerV1Alpha2(a.rollupBlocks, a.rollupID)
}

// setupRestRoutes sets up the routes for the REST API.
func (a *App) setupRestRoutes() {
	a.restRouter.HandleFunc("/block/{height}", a.getBlock).Methods("GET")
	registerHandlers(a)
}

// makeRestServer creates a new HTTP server for the REST API.
func (a *App) makeRestServer() *http.Server {
	return &http.Server{
		Addr:    a.restAddr,
		Handler: cors.Default().Handler(a.restRouter),
	}
}

func (a *App) getBlock(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	heightStr, ok := vars["height"]
	if !ok {
		log.Errorf("error getting height from request\n")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	height, err := strconv.Atoi(heightStr)
	if err != nil {
		log.Errorf("error converting height to int: %s\n", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	log.Debugf("getting block %d\n", height)
	block, err := a.rollupBlocks.GetSingleBlock(uint32(height))
	if err != nil {
		log.Errorf("error getting block: %s\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	blockJson, err := json.Marshal(block)
	if err != nil {
		log.Errorf("error marshalling block: %s\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Write(blockJson)
}

func (a *App) Run() {
	// run execution api
	go func() {
		server := a.makeExecutionServer()
		lis, err := net.Listen("tcp", a.executionRPC)
		if err != nil {
			log.Fatalf("failed to listen: %v", err)
		}
		grpcServer := grpc.NewServer()
		astriaGrpc.RegisterExecutionServiceServer(grpcServer, server)
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()

	// run rest api server
	a.setupRestRoutes()
	server := a.makeRestServer()

	log.Infof("API server listening on %s\n", a.restAddr)
	go func() {
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			log.Warnf("rest api server closed\n")
		} else if err != nil {
			log.Errorf("error listening for rest api server: %s\n", err)
		}
	}()

	// have App sync with older blocks
	// start with genesis tx
	log.Infof("pre genesis process")
	chainId := new(big.Int).SetBytes(a.rollupID[:32])
	processTransaction(a, GenesisTransaction(*chainId))
	log.Infof("post genesis process")

	// process txs as we get them
	go func() {
		for block := range a.newBlockChan {
			log.Infof("xxx start processing block height of %d", block.Height)

			if block.Height != a.lastBlockProcessed+1 {
				log.Fatalf("block received skipped a block, wanted %d, got %d", a.lastBlockProcessed+1, block.Height)
				panic("block height err")
			}
			// only write blocks with transactions
			if len(block.Txs) == 0 {
				a.lastBlockProcessed = block.Height
				continue
			}

			for _, tx := range block.Txs {
				processTransaction(a, tx)
			}

			log.Infof("finished processing block height of %d", block.Height)

			a.lastBlockProcessed = block.Height

		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c

	log.Info("Shutting down server...")
	if err := server.Shutdown(context.Background()); err != nil {
		log.Fatalf("Could not gracefully shutdown the server: %v\n", err)
	}
	log.Info("Server gracefully stopped")
}
