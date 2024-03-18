# Example Defi App-Chain Rollup
This is a MVP defi ERC20 style rollup. Currently two fuctions are implemented: `initErc20()` and `transfer()`. This rollup accepts [EIP-1559](https://docs.infura.io/api/networks/ethereum/concepts/transaction-types#eip-1559-transactions) ethereum rlp encoded transactions and provides the http json rpc `postTransfer()` to create and sign the `transfer()` transactions. The `initErc20()` function is used as a constructor to ERC20 logic and as the 'genesis' transaction for the rollup.

The genesis transaction mints 10_000 balance to the owner's address which then can be sent to other uses via `transfer()`. The `transfer()`'s full signature is `tranfer(address to, uint64 amount)`, which is different from the normal `uint256` used for `amount`.


Example http interaction calls:
```bash
curl -kv localhost:8080/transfer \
   -H "Accept: application/json" -H "Content-Type: application/json" \
   --data '{"signerPub":"0xb4752b5bcd27605d33eae232ee3fda722f275568","signerPriv":"27f83a5f3a424724f1300f2b165cc5308a7ba5651ad51349e7c1b8ba6fef3753", "to":"0x5dB057b7AC171ac90dEd3616F3CDC606751E2090", "amount":10}'
   
curl -kv localhost:8080/balances
```

## Setup steps

Set the genesis transaction owner's pub/priv key in `docker-compose/local.yaml` to the desired first-funded address.

Set up Execution API dependencies (protobufs and gRPC):

```bash
go get buf.build/gen/go/astria/execution-apis/grpc/go
go get buf.build/gen/go/astria/execution-apis/protocolbuffers/go
```

Set up Sequencer Client and Tendermint RPC types dependencies:
```bash
go get "github.com/astriaorg/go-sequencer-client"
go get "github.com/cometbft/cometbft"
```

## Running the rollup w/ docker-compose

```bash
just docker-run
```

This will launch a local sequencer, conductor, the rollup, and the chat frontend.

### Reset rollup data

```bash
just docker-reset
```

### Rebuild rollup images

You might need to rebuild the rollup docker images

```bash
just docker-build
```

# Warning: incomplete design

This rollup is missing the ability to deterministcally re-build its state from the sequencer. The plan is to implement this via calls to the sequencer's service [`SequencerService::GetFilteredSequencerBlock`](https://github.com/astriaorg/astria/blob/8a23685831ee76ff53c284734a5948f142424867/proto/sequencerapis/astria/sequencer/v1/service.proto#L29) but has not been implemented yet. 

