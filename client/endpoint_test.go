package client

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveEndpoint(t *testing.T) {
	mainnet, err := resolveEndpoint("https://fullnode.mainnet.sui.io")
	require.NoError(t, err)
	require.Equal(t, "fullnode.mainnet.sui.io:443", mainnet.grpcTarget)
	require.Equal(t, "https://graphql.mainnet.sui.io/graphql", mainnet.graphql)
	require.False(t, mainnet.insecure)

	testnet, err := resolveEndpoint("fullnode.testnet.sui.io:443")
	require.NoError(t, err)
	require.Equal(t, "fullnode.testnet.sui.io:443", testnet.grpcTarget)
	require.Equal(t, "https://graphql.testnet.sui.io/graphql", testnet.graphql)

	local, err := resolveEndpoint("http://127.0.0.1:9000")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:9000", local.grpcTarget)
	require.True(t, local.insecure)
	require.Empty(t, local.graphql)

	provider, err := resolveEndpoint("https://sui-mainnet.core.chainstack.com")
	require.NoError(t, err)
	require.Equal(t, "sui-mainnet.core.chainstack.com:443", provider.grpcTarget)
	require.Equal(t, "https://sui-mainnet.core.chainstack.com/graphql", provider.graphql)
}
