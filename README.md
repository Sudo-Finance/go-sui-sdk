# go-sui-sdk

Sui Golang SDK

[![Documentation (master)](https://img.shields.io/badge/docs-master-59f)](https://github.com/Sudo-Finance/go-sui-sdk-sdk)
[![License](https://img.shields.io/badge/license-Apache-green.svg)](https://github.com/Sudo-Finance/go-sui-sdk-sdk/blob/main/LICENSE)

The Sui Golang SDK for ComingChat.
We welcome other developers to participate in the development and testing of sui-sdk.

## Install

```sh
go get github.com/Sudo-Finance/go-sui-sdk/v2
```

## Usage

### Account

```go
import "github.com/Sudo-Finance/go-sui-sdk/v2/account"

// Import account with mnemonic
acc, err := account.NewAccountWithMnemonic(mnemonic)

// Import account with private key
privateKey, err := hex.DecodeString("4ec5a9eefc0bb86027a6f3ba718793c813505acc25ed09447caf6a069accdd4b")
acc, err := account.NewAccount(privateKey)

// Get private key, public key, address
fmt.Printf("privateKey = %x\n", acc.PrivateKey[:32])
fmt.Printf(" publicKey = %x\n", acc.PublicKey)
fmt.Printf("   address = %v\n", acc.Address)

// Sign data
signedData := acc.Sign(data)
```

### gRPC and GraphQL client

Sui full nodes no longer serve JSON-RPC. Reads, transaction simulation, and
transaction submission use gRPC. `QueryEvents` uses GraphQL, which is the
supported replacement for `suix_queryEvents`.

`Dial` accepts the old fullnode URLs. A public fullnode URL also selects the
matching public GraphQL endpoint. Pass `WithToken` and `WithGraphQLEndpoint`
for a provider such as Chainstack.

```go
import "github.com/Sudo-Finance/go-sui-sdk/v2/client"
import "github.com/Sudo-Finance/go-sui-sdk/v2/types"

cli, err := client.Dial(types.MainnetRpcUrl)
defer cli.Close()

checkpoint, err := cli.GetLatestCheckpointSequenceNumber(ctx)

// Events are queried over GraphQL. Resume with page.NextPageToken.
page, err := cli.QueryEvents(ctx, types.EventFilter{Sender: address}, nil, nil, true)

// Provider endpoint: gRPC on :443, GraphQL on the same host, x-token on both.
cli, err = client.Dial(
    "sui-mainnet.core.chainstack.com:443",
    client.WithToken(token),
    client.WithGraphQLEndpoint("https://sui-mainnet.core.chainstack.com/graphql"),
)
```

Pagination cursors from JSON-RPC are not reusable. Pass `NextPageToken` from
the previous page. The old `unsafe_*` transaction builders are not part of
gRPC; build those transactions with `sui_types.ProgrammableTransactionBuilder`
and submit the BCS bytes with `ExecuteTransactionBlock`. `GetValidatorsApy`
has no replacement on either API.

The gRPC stubs in `suirpc` are generated from [MystenLabs/sui-apis](https://github.com/MystenLabs/sui-apis).

### Build, sign, and send

The full node no longer builds transactions. Construct one locally, sign the BCS bytes, and submit them.

```go
import "github.com/Sudo-Finance/go-sui-sdk/v2/account"
import "github.com/Sudo-Finance/go-sui-sdk/v2/client"
import "github.com/Sudo-Finance/go-sui-sdk/v2/sui_types"
import "github.com/Sudo-Finance/go-sui-sdk/v2/types"
import "github.com/fardream/go-bcs/bcs"

acc, err := account.NewAccountWithMnemonic(mnemonic)
signer, err := sui_types.NewAddressFromHex(acc.Address)
recipient, err := sui_types.NewAddressFromHex("0x12345678.......")

cli, err := client.Dial(types.TestnetRpcUrl)
defer cli.Close()

coins, err := cli.GetCoins(ctx, *signer, nil, nil, 1)
gasPrice, err := cli.GetReferenceGasPrice(ctx)

amount := uint64(10000)
gasBudget := uint64(2000000)
ptb := sui_types.NewProgrammableTransactionBuilder()
err = ptb.TransferSui(*recipient, &amount)
tx := sui_types.NewProgrammable(
    *signer,
    []*sui_types.ObjectRef{coins.Data[0].Reference()},
    ptb.Finish(),
    gasBudget,
    gasPrice.Uint64(),
)
txBytes, err := bcs.Marshal(tx)

signature, err := acc.SignSecureWithoutEncode(txBytes, sui_types.DefaultIntent())
resp, err := cli.ExecuteTransactionBlock(
    ctx,
    txBytes,
    []any{signature},
    &types.SuiTransactionBlockResponseOptions{ShowEffects: true},
    types.TxnRequestTypeWaitForLocalExecution,
)
```
