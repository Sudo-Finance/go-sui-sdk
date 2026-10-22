package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Sudo-Finance/go-sui-sdk/v2/suirpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const maxMessageBytes = 64 << 20

// BatchElem is retained so existing JSON-RPC batch call sites still compile.
// BatchCall returns ErrJSONRPCRemoved.
type BatchElem struct {
	Method string
	Args   []interface{}
	Result interface{}
	Error  error
}

// Client talks to a Sui full node over gRPC, and to GraphQL for event queries.
type Client struct {
	conn    *grpc.ClientConn
	ledger  suirpc.LedgerServiceClient
	state   suirpc.StateServiceClient
	exec    suirpc.TransactionExecutionServiceClient
	names   suirpc.NameServiceClient
	graphql string
	http    *http.Client
	token   string
}

type dialConfig struct {
	graphql  string
	token    string
	http     *http.Client
	insecure bool
}

// DialOption configures Dial.
type DialOption func(*dialConfig)

// WithGraphQLEndpoint sets the GraphQL URL used by QueryEvents.
// When omitted, Dial derives it from a public fullnode URL
// (https://fullnode.<network>.sui.io -> https://graphql.<network>.sui.io/graphql)
// or appends /graphql to any other https endpoint.
func WithGraphQLEndpoint(endpoint string) DialOption {
	return func(cfg *dialConfig) {
		cfg.graphql = strings.TrimRight(endpoint, "/")
	}
}

// WithToken sends the Chainstack (and compatible) x-token credential on
// gRPC metadata and GraphQL requests.
func WithToken(token string) DialOption {
	return func(cfg *dialConfig) {
		cfg.token = token
	}
}

// WithHTTPClient sets the HTTP client used for GraphQL.
func WithHTTPClient(c *http.Client) DialOption {
	return func(cfg *dialConfig) {
		cfg.http = c
	}
}

// WithInsecure dials gRPC without TLS. Use this for a local node.
func WithInsecure() DialOption {
	return func(cfg *dialConfig) {
		cfg.insecure = true
	}
}

// Dial connects to a Sui gRPC endpoint.
//
// target accepts the historical JSON-RPC URLs (https://fullnode.mainnet.sui.io),
// a gRPC host:port (fullnode.mainnet.sui.io:443), or a provider https URL.
func Dial(target string, opts ...DialOption) (*Client, error) {
	cfg := dialConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	resolved, err := resolveEndpoint(target)
	if err != nil {
		return nil, err
	}
	if cfg.graphql != "" {
		resolved.graphql = cfg.graphql
	}
	insecureDial := cfg.insecure || resolved.insecure

	var dialOpts []grpc.DialOption
	if insecureDial {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	}
	dialOpts = append(dialOpts, grpc.WithDefaultCallOptions(
		grpc.MaxCallRecvMsgSize(maxMessageBytes),
		grpc.MaxCallSendMsgSize(maxMessageBytes),
	))
	if cfg.token != "" {
		dialOpts = append(dialOpts,
			grpc.WithUnaryInterceptor(tokenUnaryInterceptor(cfg.token)),
			grpc.WithStreamInterceptor(tokenStreamInterceptor(cfg.token)),
		)
	}

	conn, err := grpc.NewClient(resolved.grpcTarget, dialOpts...)
	if err != nil {
		return nil, err
	}
	hc := cfg.http
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		conn:    conn,
		ledger:  suirpc.NewLedgerServiceClient(conn),
		state:   suirpc.NewStateServiceClient(conn),
		exec:    suirpc.NewTransactionExecutionServiceClient(conn),
		names:   suirpc.NewNameServiceClient(conn),
		graphql: resolved.graphql,
		http:    hc,
		token:   cfg.token,
	}, nil
}

// DialWithClient matches the old helper. The HTTP client is used for GraphQL.
func DialWithClient(target string, c *http.Client) (*Client, error) {
	return Dial(target, WithHTTPClient(c))
}

// Close releases the gRPC connection.
func (c *Client) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Call is the old JSON-RPC escape hatch. Sui full nodes no longer serve it.
func (c *Client) Call(result interface{}, method Method, args ...interface{}) error {
	return c.CallContext(context.Background(), result, method, args...)
}

// CallContext reports that the named JSON-RPC method is no longer served.
func (c *Client) CallContext(ctx context.Context, result interface{}, method Method, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrJSONRPCRemoved, method.String())
}

// BatchCall reports that JSON-RPC batch calls are no longer served.
func (c *Client) BatchCall(b []BatchElem) error {
	return c.BatchCallContext(context.Background(), b)
}

// BatchCallContext reports that JSON-RPC batch calls are no longer served.
func (c *Client) BatchCallContext(ctx context.Context, b []BatchElem) error {
	return fmt.Errorf("%w: batch JSON-RPC", ErrJSONRPCRemoved)
}

type resolvedEndpoint struct {
	grpcTarget string
	graphql    string
	insecure   bool
}

func resolveEndpoint(target string) (resolvedEndpoint, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return resolvedEndpoint{}, errors.New("empty Sui endpoint")
	}
	if strings.Contains(target, "://") {
		return resolveURL(target)
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return resolvedEndpoint{}, fmt.Errorf("gRPC target %q must be host:port or an https URL", target)
	}
	return resolvedEndpoint{
		grpcTarget: target,
		graphql:    graphqlForHost(host),
		insecure:   port != "443" && isLocalHost(host),
	}, nil
}

func resolveURL(raw string) (resolvedEndpoint, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return resolvedEndpoint{}, err
	}
	if u.Hostname() == "" {
		return resolvedEndpoint{}, fmt.Errorf("endpoint %q has no host", raw)
	}
	port := u.Port()
	insecure := u.Scheme == "http"
	if port == "" {
		if insecure {
			port = "80"
		} else {
			port = "443"
		}
	}
	graphql := ""
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/graphql") {
		graphql = strings.TrimRight(raw, "/")
	} else if derived := graphqlForHost(u.Hostname()); derived != "" {
		graphql = derived
	} else if !insecure {
		origin := u.Scheme + "://" + u.Host
		graphql = strings.TrimRight(origin, "/") + "/graphql"
	}
	return resolvedEndpoint{
		grpcTarget: net.JoinHostPort(u.Hostname(), port),
		graphql:    graphql,
		insecure:   insecure,
	}, nil
}

func graphqlForHost(host string) string {
	const prefix = "fullnode."
	if strings.HasPrefix(host, prefix) {
		return "https://graphql." + strings.TrimPrefix(host, prefix) + "/graphql"
	}
	return ""
}

func isLocalHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func tokenUnaryInterceptor(token string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(metadata.AppendToOutgoingContext(ctx, "x-token", token), method, req, reply, cc, opts...)
	}
}

func tokenStreamInterceptor(token string) grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(metadata.AppendToOutgoingContext(ctx, "x-token", token), desc, cc, method, opts...)
	}
}
