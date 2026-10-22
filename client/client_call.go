package client

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Sudo-Finance/go-sui-sdk/v2/lib"
	"github.com/Sudo-Finance/go-sui-sdk/v2/sui_types"
	"github.com/Sudo-Finance/go-sui-sdk/v2/suirpc"
	"github.com/Sudo-Finance/go-sui-sdk/v2/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NOTE: This copys the query limit from our Rust JSON RPC backend, this needs to be kept in sync!
const QUERY_MAX_RESULT_LIMIT = 1000

const stakedSuiType = "0x3::staking_pool::StakedSui"

type suiAddress = sui_types.SuiAddress
type suiObjectID = sui_types.ObjectID
type suiDigest = sui_types.TransactionDigest
type suiBase64Data = lib.Base64Data

// GetBalance uses 0x2::sui::SUI when coinType is empty.
func (c *Client) GetBalance(ctx context.Context, owner suiAddress, coinType string) (*types.Balance, error) {
	if coinType == "" {
		coinType = types.SuiCoinType
	}
	resp, err := c.state.GetBalance(ctx, &suirpc.GetBalanceRequest{
		Owner:    strp(owner.String()),
		CoinType: strp(coinType),
	})
	if err != nil {
		return nil, err
	}
	balance := balanceFromProto(resp.GetBalance())
	return &balance, nil
}

func (c *Client) GetAllBalances(ctx context.Context, owner suiAddress) ([]types.Balance, error) {
	var (
		out   []types.Balance
		token []byte
	)
	for page := 0; page < 50; page++ {
		resp, err := c.state.ListBalances(ctx, &suirpc.ListBalancesRequest{
			Owner:     strp(owner.String()),
			PageSize:  u32p(1000),
			PageToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, balance := range resp.GetBalances() {
			out = append(out, balanceFromProto(balance))
		}
		token = resp.GetNextPageToken()
		if len(token) == 0 {
			break
		}
	}
	return out, nil
}

// GetSuiCoinsOwnedByAddress retrieves at most 200 SUI coins.
func (c *Client) GetSuiCoinsOwnedByAddress(ctx context.Context, address suiAddress) (types.Coins, error) {
	coinType := types.SuiCoinType
	page, err := c.GetCoins(ctx, address, &coinType, nil, 200)
	if err != nil {
		return nil, err
	}
	return page.Data, nil
}

// GetCoins uses 0x2::sui::SUI when coinType is nil.
// cursor is the base64 NextPageToken from a previous page, not a JSON-RPC object id.
func (c *Client) GetCoins(
	ctx context.Context,
	owner suiAddress,
	coinType *string,
	cursor *string,
	limit uint,
) (*types.CoinPage, error) {
	coin := types.SuiCoinType
	if coinType != nil && *coinType != "" {
		coin = *coinType
	}
	return c.listCoins(ctx, owner, "0x2::coin::Coin<"+coin+">", cursor, limit)
}

// GetAllCoins lists every Coin<T> owned by the address.
// cursor is the base64 NextPageToken from a previous page.
func (c *Client) GetAllCoins(
	ctx context.Context,
	owner suiAddress,
	cursor *string,
	limit uint,
) (*types.CoinPage, error) {
	return c.listCoins(ctx, owner, "0x2::coin::Coin", cursor, limit)
}

func (c *Client) listCoins(ctx context.Context, owner suiAddress, objectType string, cursor *string, limit uint) (*types.CoinPage, error) {
	token, err := decodePageToken(cursor)
	if err != nil {
		return nil, err
	}
	resp, err := c.state.ListOwnedObjects(ctx, &suirpc.ListOwnedObjectsRequest{
		Owner:      strp(owner.String()),
		PageSize:   pageLimit(&limit, 50, 1000),
		PageToken:  token,
		ObjectType: strp(objectType),
		ReadMask:   mask("object_id", "version", "digest", "object_type", "balance", "previous_transaction"),
	})
	if err != nil {
		return nil, err
	}
	page := &types.CoinPage{}
	for _, obj := range resp.GetObjects() {
		coin, err := coinFromObject(obj)
		if err != nil {
			return nil, err
		}
		page.Data = append(page.Data, coin)
	}
	if encoded := encodePageToken(resp.GetNextPageToken()); encoded != "" {
		page.HasNextPage = true
		page.NextCursor = &encoded
		page.NextPageToken = encoded
	}
	return page, nil
}

func (c *Client) GetCoinMetadata(ctx context.Context, coinType string) (*types.SuiCoinMetadata, error) {
	resp, err := c.state.GetCoinInfo(ctx, &suirpc.GetCoinInfoRequest{CoinType: strp(coinType)})
	if err != nil {
		return nil, err
	}
	meta := resp.GetMetadata()
	if meta == nil {
		return nil, fmt.Errorf("coin metadata not found for %s", coinType)
	}
	var id suiObjectID
	if meta.GetId() != "" {
		id, err = mustAddress(meta.GetId())
		if err != nil {
			return nil, err
		}
	}
	return &types.SuiCoinMetadata{
		Decimals:    uint8(meta.GetDecimals()),
		Description: meta.GetDescription(),
		IconUrl:     meta.GetIconUrl(),
		Id:          id,
		Name:        meta.GetName(),
		Symbol:      meta.GetSymbol(),
	}, nil
}

func (c *Client) GetObject(
	ctx context.Context,
	objID suiObjectID,
	options *types.SuiObjectDataOptions,
) (*types.SuiObjectResponse, error) {
	resp, err := c.ledger.GetObject(ctx, &suirpc.GetObjectRequest{
		ObjectId: strp(objID.String()),
		ReadMask: objectReadMask(options),
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return notExistsResponse(objID), nil
		}
		return nil, err
	}
	return objectToResponse(resp.GetObject())
}

func (c *Client) MultiGetObjects(
	ctx context.Context,
	objIDs []suiObjectID,
	options *types.SuiObjectDataOptions,
) ([]types.SuiObjectResponse, error) {
	reqs := make([]*suirpc.GetObjectRequest, 0, len(objIDs))
	for _, id := range objIDs {
		reqs = append(reqs, &suirpc.GetObjectRequest{ObjectId: strp(id.String())})
	}
	resp, err := c.ledger.BatchGetObjects(ctx, &suirpc.BatchGetObjectsRequest{
		Requests: reqs,
		ReadMask: objectReadMask(options),
	})
	if err != nil {
		return nil, err
	}
	out := make([]types.SuiObjectResponse, 0, len(resp.GetObjects()))
	for i, result := range resp.GetObjects() {
		if result.GetError() != nil {
			id := suiObjectID{}
			if i < len(objIDs) {
				id = objIDs[i]
			}
			out = append(out, *notExistsResponse(id))
			continue
		}
		mapped, err := objectToResponse(result.GetObject())
		if err != nil {
			return nil, err
		}
		out = append(out, *mapped)
	}
	return out, nil
}

// GetOwnedObjects lists objects owned by address.
// cursor is the base64 NextPageToken from a previous page.
func (c *Client) GetOwnedObjects(
	ctx context.Context,
	address suiAddress,
	query *types.SuiObjectResponseQuery,
	cursor *string,
	limit *uint,
) (*types.ObjectsPage, error) {
	token, err := decodePageToken(cursor)
	if err != nil {
		return nil, err
	}
	var options *types.SuiObjectDataOptions
	var filter *types.SuiObjectDataFilter
	if query != nil {
		options = query.Options
		filter = query.Filter
	}
	objectType := ownedTypeFilter(query)
	want := 50
	if limit != nil && *limit > 0 {
		want = int(*limit)
	}
	pageSize := limit
	if filter != nil && filter.StructType == "" && (filter.Package != nil || filter.MoveModule != nil) {
		scan := uint(50)
		pageSize = &scan
	}
	page := &types.ObjectsPage{}
	// Package and module filters are applied after the gRPC page, because
	// ListOwnedObjects only accepts a full object type.
	for n := 0; n < 20 && len(page.Data) < want; n++ {
		req := &suirpc.ListOwnedObjectsRequest{
			Owner:     strp(address.String()),
			PageSize:  pageLimit(pageSize, 50, 1000),
			PageToken: token,
			ReadMask:  objectReadMask(options, "object_type"),
		}
		if objectType != "" {
			req.ObjectType = strp(objectType)
		}
		resp, err := c.state.ListOwnedObjects(ctx, req)
		if err != nil {
			return nil, err
		}
		for _, obj := range resp.GetObjects() {
			if !objectMatchesFilter(obj, filter) {
				continue
			}
			mapped, err := objectToResponse(obj)
			if err != nil {
				return nil, err
			}
			page.Data = append(page.Data, *mapped)
			if len(page.Data) >= want {
				break
			}
		}
		token = resp.GetNextPageToken()
		if len(token) == 0 {
			break
		}
		if encoded := encodePageToken(token); encoded != "" {
			page.HasNextPage = true
			page.NextPageToken = encoded
		}
	}
	if len(page.Data) < want {
		page.HasNextPage = false
		page.NextPageToken = ""
	}
	return page, nil
}

func (c *Client) GetTotalSupply(ctx context.Context, coinType string) (*types.Supply, error) {
	resp, err := c.state.GetCoinInfo(ctx, &suirpc.GetCoinInfoRequest{CoinType: strp(coinType)})
	if err != nil {
		return nil, err
	}
	if resp.GetTreasury() == nil {
		return nil, fmt.Errorf("treasury not found for %s", coinType)
	}
	return &types.Supply{Value: types.NewSafeSuiBigInt(resp.GetTreasury().GetTotalSupply())}, nil
}

func (c *Client) GetTotalTransactionBlocks(ctx context.Context) (string, error) {
	resp, err := c.ledger.GetCheckpoint(ctx, &suirpc.GetCheckpointRequest{
		ReadMask: mask("summary.total_network_transactions"),
	})
	if err != nil {
		return "", err
	}
	summary := resp.GetCheckpoint().GetSummary()
	if summary == nil {
		return "", fmt.Errorf("checkpoint summary missing")
	}
	return strconv.FormatUint(summary.GetTotalNetworkTransactions(), 10), nil
}

func (c *Client) GetLatestCheckpointSequenceNumber(ctx context.Context) (string, error) {
	resp, err := c.ledger.GetServiceInfo(ctx, &suirpc.GetServiceInfoRequest{})
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(resp.GetCheckpointHeight(), 10), nil
}

func (c *Client) BatchGetObjectsOwnedByAddress(
	ctx context.Context,
	address suiAddress,
	options types.SuiObjectDataOptions,
	filterType string,
) ([]types.SuiObjectResponse, error) {
	return c.BatchGetFilteredObjectsOwnedByAddress(ctx, address, options, func(sod *types.SuiObjectData) bool {
		return filterType == "" || (sod.Type != nil && filterType == *sod.Type)
	})
}

func (c *Client) BatchGetFilteredObjectsOwnedByAddress(
	ctx context.Context,
	address suiAddress,
	options types.SuiObjectDataOptions,
	filter func(*types.SuiObjectData) bool,
) ([]types.SuiObjectResponse, error) {
	queryLimit := uint(QUERY_MAX_RESULT_LIMIT)
	query := types.SuiObjectResponseQuery{Options: &types.SuiObjectDataOptions{ShowType: true}}
	page, err := c.GetOwnedObjects(ctx, address, &query, nil, &queryLimit)
	if err != nil {
		return nil, err
	}
	var ids []suiObjectID
	for _, obj := range page.Data {
		if obj.Data == nil {
			continue
		}
		if filter != nil && !filter(obj.Data) {
			continue
		}
		ids = append(ids, obj.Data.ObjectId)
	}
	if len(ids) == 0 {
		return []types.SuiObjectResponse{}, nil
	}
	return c.MultiGetObjects(ctx, ids, &options)
}

func (c *Client) GetTransactionBlock(
	ctx context.Context,
	digest suiDigest,
	options types.SuiTransactionBlockResponseOptions,
) (*types.SuiTransactionBlockResponse, error) {
	resp, err := c.ledger.GetTransaction(ctx, &suirpc.GetTransactionRequest{
		Digest:   strp(digest.String()),
		ReadMask: transactionReadMask(&options),
	})
	if err != nil {
		return nil, err
	}
	return transactionFromProto(resp.GetTransaction(), &options)
}

func (c *Client) GetReferenceGasPrice(ctx context.Context) (*types.SafeSuiBigInt[uint64], error) {
	resp, err := c.ledger.GetEpoch(ctx, &suirpc.GetEpochRequest{
		ReadMask: mask("reference_gas_price"),
	})
	if err != nil {
		return nil, err
	}
	price := types.NewSafeSuiBigInt(resp.GetEpoch().GetReferenceGasPrice())
	return &price, nil
}

func (c *Client) GetEvents(ctx context.Context, digest suiDigest) ([]types.SuiEvent, error) {
	resp, err := c.ledger.GetTransaction(ctx, &suirpc.GetTransactionRequest{
		Digest:   strp(digest.String()),
		ReadMask: mask("digest", "events", "timestamp"),
	})
	if err != nil {
		return nil, err
	}
	tx := resp.GetTransaction()
	if tx.GetEvents() == nil {
		return []types.SuiEvent{}, nil
	}
	return eventsFromProto(tx.GetEvents().GetEvents(), tx)
}

func (c *Client) TryGetPastObject(
	ctx context.Context,
	objectId suiObjectID,
	version uint64,
	options *types.SuiObjectDataOptions,
) (*types.SuiPastObjectResponse, error) {
	resp, err := c.ledger.GetObject(ctx, &suirpc.GetObjectRequest{
		ObjectId: strp(objectId.String()),
		Version:  u64p(version),
		ReadMask: objectReadMask(options),
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			past := types.SuiPastObject{VersionNotFound: &struct{ ObjectId sui_types.SequenceNumber }{ObjectId: version}}
			wrapped := types.SuiPastObjectResponse{Data: past}
			return &wrapped, nil
		}
		return nil, err
	}
	data, err := objectToData(resp.GetObject())
	if err != nil {
		return nil, err
	}
	wrapped := types.SuiPastObjectResponse{Data: types.SuiPastObject{VersionFound: data}}
	return &wrapped, nil
}

func (c *Client) DevInspectTransactionBlock(
	ctx context.Context,
	senderAddress suiAddress,
	txByte suiBase64Data,
	gasPrice *types.SafeSuiBigInt[uint64],
	epoch *uint64,
) (*types.DevInspectResults, error) {
	_ = senderAddress
	_ = gasPrice
	_ = epoch
	checks := suirpc.SimulateTransactionRequest_DISABLED
	resp, err := c.exec.SimulateTransaction(ctx, &suirpc.SimulateTransactionRequest{
		Transaction: &suirpc.Transaction{Bcs: &suirpc.Bcs{Value: txByte}},
		ReadMask:    simulateReadMask(),
		Checks:      &checks,
	})
	if err != nil {
		return nil, err
	}
	return simulationToDevInspect(resp)
}

func (c *Client) DryRunTransaction(
	ctx context.Context,
	txBytes suiBase64Data,
) (*types.DryRunTransactionBlockResponse, error) {
	resp, err := c.exec.SimulateTransaction(ctx, &suirpc.SimulateTransactionRequest{
		Transaction: &suirpc.Transaction{Bcs: &suirpc.Bcs{Value: txBytes}},
		ReadMask:    simulateReadMask(),
	})
	if err != nil {
		return nil, err
	}
	return simulationToDryRun(resp)
}

func (c *Client) ExecuteTransactionBlock(
	ctx context.Context, txBytes suiBase64Data, signatures []any,
	options *types.SuiTransactionBlockResponseOptions, requestType types.ExecuteTransactionRequestType,
) (*types.SuiTransactionBlockResponse, error) {
	_ = requestType
	sigs := make([]*suirpc.UserSignature, 0, len(signatures))
	for _, signature := range signatures {
		raw, err := signatureBytes(signature)
		if err != nil {
			return nil, err
		}
		sigs = append(sigs, &suirpc.UserSignature{Bcs: &suirpc.Bcs{Value: raw}})
	}
	resp, err := c.exec.ExecuteTransaction(ctx, &suirpc.ExecuteTransactionRequest{
		Transaction: &suirpc.Transaction{Bcs: &suirpc.Bcs{Value: txBytes}},
		Signatures:  sigs,
		ReadMask:    transactionReadMask(options),
	})
	if err != nil {
		return nil, err
	}
	return transactionFromProto(resp.GetTransaction(), options)
}

func removedBuilder(name string) error {
	return fmt.Errorf("%w: %s was a JSON-RPC transaction builder; construct the transaction with sui_types.ProgrammableTransactionBuilder and submit the BCS bytes with ExecuteTransactionBlock", ErrJSONRPCRemoved, name)
}

// TransferObject is no longer built by the full node.
func (c *Client) TransferObject(ctx context.Context, signer, recipient suiAddress, objID suiObjectID, gas *suiObjectID, gasBudget types.SafeSuiBigInt[uint64]) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_transferObject")
}

// TransferSui is no longer built by the full node.
func (c *Client) TransferSui(ctx context.Context, signer, recipient suiAddress, suiObjID suiObjectID, amount, gasBudget types.SafeSuiBigInt[uint64]) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_transferSui")
}

// PayAllSui is no longer built by the full node.
func (c *Client) PayAllSui(ctx context.Context, signer, recipient suiAddress, inputCoins []suiObjectID, gasBudget types.SafeSuiBigInt[uint64]) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_payAllSui")
}

func (c *Client) Pay(ctx context.Context, signer suiAddress, inputCoins []suiObjectID, recipients []suiAddress, amount []types.SafeSuiBigInt[uint64], gas *suiObjectID, gasBudget types.SafeSuiBigInt[uint64]) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_pay")
}

func (c *Client) PaySui(ctx context.Context, signer suiAddress, inputCoins []suiObjectID, recipients []suiAddress, amount []types.SafeSuiBigInt[uint64], gasBudget types.SafeSuiBigInt[uint64]) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_paySui")
}

func (c *Client) SplitCoin(ctx context.Context, signer suiAddress, Coin suiObjectID, splitAmounts []types.SafeSuiBigInt[uint64], gas *suiObjectID, gasBudget types.SafeSuiBigInt[uint64]) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_splitCoin")
}

func (c *Client) SplitCoinEqual(ctx context.Context, signer suiAddress, Coin suiObjectID, splitCount types.SafeSuiBigInt[uint64], gas *suiObjectID, gasBudget types.SafeSuiBigInt[uint64]) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_splitCoinEqual")
}

func (c *Client) MergeCoins(ctx context.Context, signer suiAddress, primaryCoin, coinToMerge suiObjectID, gas *suiObjectID, gasBudget types.SafeSuiBigInt[uint64]) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_mergeCoins")
}

func (c *Client) Publish(ctx context.Context, sender suiAddress, compiledModules []*suiBase64Data, dependencies []suiObjectID, gas suiObjectID, gasBudget uint) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_publish")
}

// MoveCall is no longer built by the full node.
func (c *Client) MoveCall(ctx context.Context, signer suiAddress, packageId suiObjectID, module, function string, typeArgs []string, arguments []any, gas *suiObjectID, gasBudget types.SafeSuiBigInt[uint64]) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_moveCall")
}

func (c *Client) BatchTransaction(ctx context.Context, signer suiAddress, txnParams []map[string]interface{}, gas *suiObjectID, gasBudget uint64) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_batchTransaction")
}

// QueryTransactionBlocks lists transactions through LedgerService.ListTransactions.
// cursor is the base64 NextPageToken from a previous page.
func (c *Client) QueryTransactionBlocks(
	ctx context.Context, query types.SuiTransactionBlockResponseQuery,
	cursor *string, limit *uint, descendingOrder bool,
) (*types.TransactionBlocksPage, error) {
	token, err := decodePageToken(cursor)
	if err != nil {
		return nil, err
	}
	filter, start, end, err := transactionFilter(query.Filter)
	if err != nil {
		return nil, err
	}
	ordering := suirpc.Ordering_ORDERING_ASCENDING
	if descendingOrder {
		ordering = suirpc.Ordering_ORDERING_DESCENDING
	}
	options := &suirpc.QueryOptions{Limit: pageLimit(limit, 50, 50), Ordering: &ordering}
	if len(token) > 0 {
		if descendingOrder {
			options.Before = token
		} else {
			options.After = token
		}
	}
	stream, err := c.ledger.ListTransactions(ctx, &suirpc.ListTransactionsRequest{
		ReadMask:        transactionReadMask(query.Options),
		StartCheckpoint: start,
		EndCheckpoint:   end,
		Filter:          filter,
		Options:         options,
	})
	if err != nil {
		return nil, err
	}
	page := &types.TransactionBlocksPage{}
	var resume []byte
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if msg.GetWatermark() != nil && len(msg.GetWatermark().GetCursor()) > 0 {
			resume = msg.GetWatermark().GetCursor()
		}
		if tx := msg.GetTransaction(); tx != nil {
			mapped, err := transactionFromProto(tx, query.Options)
			if err != nil {
				return nil, err
			}
			page.Data = append(page.Data, *mapped)
		}
		if end := msg.GetEnd(); end != nil {
			switch end.GetReason() {
			case suirpc.QueryEndReason_QUERY_END_REASON_ITEM_LIMIT, suirpc.QueryEndReason_QUERY_END_REASON_SCAN_LIMIT:
				if encoded := encodePageToken(resume); encoded != "" {
					page.HasNextPage = true
					page.NextPageToken = encoded
				}
			}
		}
	}
	return page, nil
}

func (c *Client) ResolveNameServiceAddress(ctx context.Context, suiName string) (*suiAddress, error) {
	resp, err := c.names.LookupName(ctx, &suirpc.LookupNameRequest{Name: strp(suiName)})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, fmt.Errorf("sui name not found")
		}
		return nil, err
	}
	record := resp.GetRecord()
	if record == nil {
		return nil, fmt.Errorf("sui name not found")
	}
	if record.GetTargetAddress() == "" {
		return nil, fmt.Errorf("sui name has no target address")
	}
	addr, err := mustAddress(record.GetTargetAddress())
	if err != nil {
		return nil, err
	}
	return &addr, nil
}

// ResolveNameServiceNames returns the single name linked to owner.
// gRPC reverse lookup is not paginated; a non-nil cursor yields an empty page.
func (c *Client) ResolveNameServiceNames(ctx context.Context, owner suiAddress, cursor *suiObjectID, limit *uint) (*types.SuiNamePage, error) {
	_ = limit
	page := &types.SuiNamePage{}
	if cursor != nil {
		return page, nil
	}
	resp, err := c.names.ReverseLookupName(ctx, &suirpc.ReverseLookupNameRequest{Address: strp(owner.String())})
	if err != nil {
		if status.Code(err) == codes.NotFound || strings.Contains(status.Convert(err).Message(), "expired") {
			return page, nil
		}
		return nil, fmt.Errorf("reverse name lookup: %w", err)
	}
	if resp.GetRecord() != nil && resp.GetRecord().GetName() != "" {
		page.Data = []string{resp.GetRecord().GetName()}
	}
	return page, nil
}

// GetDynamicFields lists dynamic fields on parentObjectId.
// cursor is the base64 NextPageToken from a previous page.
func (c *Client) GetDynamicFields(ctx context.Context, parentObjectId suiObjectID, cursor *string, limit *uint) (*types.DynamicFieldPage, error) {
	token, err := decodePageToken(cursor)
	if err != nil {
		return nil, err
	}
	resp, err := c.state.ListDynamicFields(ctx, &suirpc.ListDynamicFieldsRequest{
		Parent:    strp(parentObjectId.String()),
		PageSize:  pageLimit(limit, 50, 1000),
		PageToken: token,
		ReadMask: mask(
			"kind", "parent", "field_id", "name", "value_type", "child_id",
			"field_object.object_id", "field_object.version", "field_object.digest",
			"field_object.object_type", "field_object.json",
		),
	})
	if err != nil {
		return nil, err
	}
	page := &types.DynamicFieldPage{}
	for _, field := range resp.GetDynamicFields() {
		info, err := dynamicFieldInfo(field)
		if err != nil {
			return nil, err
		}
		page.Data = append(page.Data, info)
	}
	if encoded := encodePageToken(resp.GetNextPageToken()); encoded != "" {
		page.HasNextPage = true
		page.NextPageToken = encoded
	}
	return page, nil
}

func (c *Client) GetDynamicFieldObject(ctx context.Context, parentObjectId suiObjectID, name sui_types.DynamicFieldName) (*types.SuiObjectResponse, error) {
	var cursor *string
	for page := 0; page < 40; page++ {
		fields, err := c.GetDynamicFields(ctx, parentObjectId, cursor, u32AsUint(200))
		if err != nil {
			return nil, err
		}
		for _, field := range fields.Data {
			if name.Type != "" && field.Name.Type != "" && field.Name.Type != name.Type {
				continue
			}
			if looseEqual(field.Name.Value, name.Value) {
				return c.GetObject(ctx, field.ObjectId, &types.SuiObjectDataOptions{
					ShowType: true, ShowContent: true, ShowOwner: true, ShowBcs: true,
				})
			}
		}
		if !fields.HasNextPage || fields.NextPageToken == "" {
			break
		}
		cursor = &fields.NextPageToken
	}
	return notExistsResponse(parentObjectId), nil
}

func u32AsUint(v uint) *uint { return &v }

func simulationToDryRun(resp *suirpc.SimulateTransactionResponse) (*types.DryRunTransactionBlockResponse, error) {
	tx := resp.GetTransaction()
	fx, err := effectsFromProto(tx.GetEffects(), tx.GetDigest())
	if err != nil {
		return nil, err
	}
	out := &types.DryRunTransactionBlockResponse{
		Effects: lib.TagJson[types.SuiTransactionBlockEffects]{Data: fx},
	}
	if tx.GetEvents() != nil {
		events, err := eventsFromProto(tx.GetEvents().GetEvents(), tx)
		if err != nil {
			return nil, err
		}
		out.Events = events
	}
	for _, change := range tx.GetBalanceChanges() {
		mapped, err := balanceChangeFromProto(change)
		if err != nil {
			return nil, err
		}
		out.BalanceChanges = append(out.BalanceChanges, mapped)
	}
	return out, nil
}

func simulationToDevInspect(resp *suirpc.SimulateTransactionResponse) (*types.DevInspectResults, error) {
	dry, err := simulationToDryRun(resp)
	if err != nil {
		return nil, err
	}
	out := &types.DevInspectResults{
		Effects: dry.Effects,
		Events:  dry.Events,
	}
	if dry.Effects.Data.V1 != nil && dry.Effects.Data.V1.Status.Error != "" {
		msg := dry.Effects.Data.V1.Status.Error
		out.Error = &msg
	}
	for _, command := range resp.GetCommandOutputs() {
		result := types.ExecutionResultType{}
		for _, value := range command.GetReturnValues() {
			parsed, err := protoValue(value.GetJson())
			if err != nil {
				return nil, err
			}
			result.ReturnValues = append(result.ReturnValues, parsed)
		}
		out.Results = append(out.Results, result)
	}
	return out, nil
}
