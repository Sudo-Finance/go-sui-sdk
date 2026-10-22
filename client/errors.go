package client

import (
	"errors"
	"fmt"
)

// ErrJSONRPCRemoved is returned for JSON-RPC methods Sui full nodes no longer serve.
// Transaction construction that used to happen in unsafe_* RPCs is done with
// sui_types.ProgrammableTransactionBuilder, then submitted with ExecuteTransactionBlock.
var ErrJSONRPCRemoved = errors.New("JSON-RPC is no longer served by Sui full nodes")

// ErrValidatorsApyUnavailable is returned because neither gRPC nor GraphQL
// exposes suix_getValidatorsApy. APY is derived from staking-pool exchange rates.
var ErrValidatorsApyUnavailable = errors.New("validator APY is not served by gRPC or GraphQL; derive it from staking pool exchange rates")

type HTTPError struct {
	StatusCode int
	Status     string
	Body       []byte
}

func (err HTTPError) Error() string {
	if len(err.Body) == 0 {
		return err.Status
	}
	return fmt.Sprintf("%v: %s", err.Status, err.Body)
}
