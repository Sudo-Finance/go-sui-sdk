package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Sudo-Finance/go-sui-sdk/v2/lib"
	"github.com/Sudo-Finance/go-sui-sdk/v2/sui_types"
	"github.com/Sudo-Finance/go-sui-sdk/v2/suirpc"
	"github.com/Sudo-Finance/go-sui-sdk/v2/types"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
)

func strp(s string) *string { return &s }

func u32p(v uint32) *uint32 { return &v }

func u64p(v uint64) *uint64 { return &v }

func mask(paths ...string) *fieldmaskpb.FieldMask {
	return &fieldmaskpb.FieldMask{Paths: paths}
}

func pageLimit(limit *uint, fallback, max uint32) *uint32 {
	if limit == nil || *limit == 0 {
		return u32p(fallback)
	}
	if *limit > uint(max) {
		return u32p(max)
	}
	return u32p(uint32(*limit))
}

func encodePageToken(token []byte) string {
	if len(token) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(token)
}

func decodePageToken(cursor *string) ([]byte, error) {
	if cursor == nil || *cursor == "" {
		return nil, nil
	}
	token, err := base64.StdEncoding.DecodeString(*cursor)
	if err != nil {
		return nil, fmt.Errorf("page token must be NextPageToken from a previous response: %w", err)
	}
	return token, nil
}

func mustAddress(hex string) (sui_types.SuiAddress, error) {
	addr, err := sui_types.NewAddressFromHex(hex)
	if err != nil {
		return sui_types.SuiAddress{}, fmt.Errorf("address %q: %w", hex, err)
	}
	return *addr, nil
}

func mustDigest(b58 string) (sui_types.Digest, error) {
	d, err := sui_types.NewDigest(b58)
	if err != nil {
		return nil, fmt.Errorf("digest %q: %w", b58, err)
	}
	return *d, nil
}

func decimalU64(v uint64) decimal.Decimal {
	d, err := decimal.NewFromString(strconv.FormatUint(v, 10))
	if err != nil {
		return decimal.Zero
	}
	return d
}

func protoValue(v *structpb.Value) (any, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := protojson.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil && err != io.EOF {
		return nil, err
	}
	return out, nil
}

func asUint64(v any) (uint64, bool) {
	switch n := v.(type) {
	case json.Number:
		u, err := strconv.ParseUint(n.String(), 10, 64)
		return u, err == nil
	case string:
		u, err := strconv.ParseUint(n, 10, 64)
		return u, err == nil
	case float64:
		if n < 0 {
			return 0, false
		}
		return uint64(n), true
	default:
		return 0, false
	}
}

func objectReadMask(options *types.SuiObjectDataOptions, extra ...string) *fieldmaskpb.FieldMask {
	paths := []string{"object_id", "version", "digest"}
	paths = append(paths, extra...)
	if options == nil {
		return mask(paths...)
	}
	if options.ShowType {
		paths = append(paths, "object_type", "has_public_transfer")
	}
	if options.ShowContent {
		paths = append(paths, "json", "object_type")
	}
	if options.ShowBcs {
		paths = append(paths, "bcs", "contents", "object_type")
	}
	if options.ShowOwner {
		paths = append(paths, "owner")
	}
	if options.ShowPreviousTransaction {
		paths = append(paths, "previous_transaction")
	}
	if options.ShowStorageRebate {
		paths = append(paths, "storage_rebate")
	}
	if options.ShowDisplay {
		paths = append(paths, "display")
	}
	return mask(uniqueStrings(paths)...)
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func simulateReadMask() *fieldmaskpb.FieldMask {
	// SimulateTransaction's read mask is rooted at SimulateTransactionResponse,
	// so executed-transaction fields are under `transaction`.
	return mask(
		"transaction.effects.status",
		"transaction.effects.epoch",
		"transaction.effects.transaction_digest",
		"transaction.effects.gas_used.computation_cost",
		"transaction.effects.gas_used.storage_cost",
		"transaction.effects.gas_used.storage_rebate",
		"transaction.effects.gas_used.non_refundable_storage_fee",
		"transaction.events",
		"transaction.balance_changes",
		"command_outputs",
	)
}

func transactionReadMask(options *types.SuiTransactionBlockResponseOptions) *fieldmaskpb.FieldMask {
	paths := []string{"digest", "checkpoint", "timestamp"}
	if options == nil || options.ShowEffects || options.ShowObjectChanges {
		paths = append(paths,
			"effects.status",
			"effects.epoch",
			"effects.transaction_digest",
			"effects.gas_used.computation_cost",
			"effects.gas_used.storage_cost",
			"effects.gas_used.storage_rebate",
			"effects.gas_used.non_refundable_storage_fee",
		)
	}
	if options == nil {
		return mask(paths...)
	}
	if options.ShowInput {
		paths = append(paths, "transaction")
	}
	if options.ShowEvents {
		paths = append(paths, "events")
	}
	if options.ShowBalanceChanges {
		paths = append(paths, "balance_changes")
	}
	return mask(uniqueStrings(paths)...)
}

func objectToResponse(obj *suirpc.Object) (*types.SuiObjectResponse, error) {
	if obj == nil || obj.GetObjectId() == "" {
		return &types.SuiObjectResponse{}, nil
	}
	data, err := objectToData(obj)
	if err != nil {
		return nil, err
	}
	return &types.SuiObjectResponse{Data: data}, nil
}

func objectToData(obj *suirpc.Object) (*types.SuiObjectData, error) {
	id, err := mustAddress(obj.GetObjectId())
	if err != nil {
		return nil, err
	}
	data := &types.SuiObjectData{
		ObjectId: id,
		Version:  types.NewSafeSuiBigInt(obj.GetVersion()),
	}
	if obj.GetDigest() != "" {
		digest, err := mustDigest(obj.GetDigest())
		if err != nil {
			return nil, err
		}
		data.Digest = digest
	}
	if obj.ObjectType != nil {
		objectType := obj.GetObjectType()
		data.Type = &objectType
	}
	if obj.PreviousTransaction != nil && obj.GetPreviousTransaction() != "" {
		prev, err := mustDigest(obj.GetPreviousTransaction())
		if err != nil {
			return nil, err
		}
		data.PreviousTransaction = &prev
	}
	if obj.StorageRebate != nil {
		rebate := types.NewSafeSuiBigInt(obj.GetStorageRebate())
		data.StorageRebate = &rebate
	}
	if obj.GetOwner() != nil {
		owner, err := ownerFromProto(obj.GetOwner())
		if err != nil {
			return nil, err
		}
		data.Owner = &owner
	}
	if obj.GetJson() != nil || obj.GetContents() != nil || obj.GetBcs() != nil {
		if err := fillObjectContents(data, obj); err != nil {
			return nil, err
		}
	}
	if obj.GetDisplay() != nil && obj.GetDisplay().GetOutput() != nil {
		display, err := protoValue(obj.GetDisplay().GetOutput())
		if err != nil {
			return nil, err
		}
		data.Display = display
	}
	return data, nil
}

func fillObjectContents(data *types.SuiObjectData, obj *suirpc.Object) error {
	objectType := obj.GetObjectType()
	if obj.GetJson() != nil && objectType != "package" {
		fields, err := protoValue(obj.GetJson())
		if err != nil {
			return err
		}
		parsed := types.SuiParsedData{MoveObject: &types.SuiParsedMoveObject{
			Type:              objectType,
			HasPublicTransfer: obj.GetHasPublicTransfer(),
			Fields:            fields,
		}}
		data.Content = &lib.TagJson[types.SuiParsedData]{Data: parsed}
	}
	if obj.GetContents() != nil || obj.GetBcs() != nil {
		raw := types.SuiRawData{}
		if objectType == "package" {
			raw.Package = &types.SuiRawMovePackage{Id: data.ObjectId, Version: obj.GetVersion()}
		} else if len(obj.GetContents().GetValue()) > 0 || len(obj.GetBcs().GetValue()) > 0 {
			bcsBytes := obj.GetContents().GetValue()
			if len(bcsBytes) == 0 {
				bcsBytes = obj.GetBcs().GetValue()
			}
			raw.MoveObject = &types.SuiRawMoveObject{
				Type:              objectType,
				HasPublicTransfer: obj.GetHasPublicTransfer(),
				Version:           obj.GetVersion(),
				BcsBytes:          lib.Base64Data(bcsBytes),
			}
		}
		if raw.MoveObject != nil || raw.Package != nil {
			data.Bcs = &lib.TagJson[types.SuiRawData]{Data: raw}
		}
	}
	return nil
}

func ownerFromProto(owner *suirpc.Owner) (types.ObjectOwner, error) {
	switch owner.GetKind() {
	case suirpc.Owner_ADDRESS, suirpc.Owner_CONSENSUS_ADDRESS:
		addr, err := mustAddress(owner.GetAddress())
		if err != nil {
			return types.ObjectOwner{}, err
		}
		return types.ObjectOwner{ObjectOwnerInternal: &types.ObjectOwnerInternal{AddressOwner: &addr}}, nil
	case suirpc.Owner_OBJECT:
		addr, err := mustAddress(owner.GetAddress())
		if err != nil {
			return types.ObjectOwner{}, err
		}
		return types.ObjectOwner{ObjectOwnerInternal: &types.ObjectOwnerInternal{ObjectOwner: &addr}}, nil
	case suirpc.Owner_SHARED:
		version := owner.GetVersion()
		return types.ObjectOwner{ObjectOwnerInternal: &types.ObjectOwnerInternal{
			Shared: &struct {
				InitialSharedVersion *sui_types.SequenceNumber `json:"initial_shared_version"`
			}{InitialSharedVersion: &version},
		}}, nil
	case suirpc.Owner_IMMUTABLE:
		var owner types.ObjectOwner
		if err := json.Unmarshal([]byte(`"Immutable"`), &owner); err != nil {
			return types.ObjectOwner{}, err
		}
		return owner, nil
	default:
		return types.ObjectOwner{}, nil
	}
}

func coinFromObject(obj *suirpc.Object) (types.Coin, error) {
	id, err := mustAddress(obj.GetObjectId())
	if err != nil {
		return types.Coin{}, err
	}
	var digest sui_types.ObjectDigest
	if obj.GetDigest() != "" {
		digest, err = mustDigest(obj.GetDigest())
		if err != nil {
			return types.Coin{}, err
		}
	}
	coin := types.Coin{
		CoinType:     coinTypeOf(obj.GetObjectType()),
		CoinObjectId: id,
		Version:      types.NewSafeSuiBigInt(obj.GetVersion()),
		Digest:       digest,
		Balance:      types.NewSafeSuiBigInt(obj.GetBalance()),
	}
	if obj.GetPreviousTransaction() != "" {
		prev, err := mustDigest(obj.GetPreviousTransaction())
		if err != nil {
			return types.Coin{}, err
		}
		coin.PreviousTransaction = prev
	}
	return coin, nil
}

func coinTypeOf(objectType string) string {
	const prefix = "0x2::coin::Coin<"
	if strings.HasPrefix(objectType, prefix) && strings.HasSuffix(objectType, ">") {
		return objectType[len(prefix) : len(objectType)-1]
	}
	return objectType
}

func effectsFromProto(fx *suirpc.TransactionEffects, digest string) (types.SuiTransactionBlockEffects, error) {
	v1 := &types.SuiTransactionBlockEffectsV1{
		Status: types.ExecutionStatus{Status: types.ExecutionStatusFailure},
	}
	if digest != "" {
		d, err := mustDigest(digest)
		if err != nil {
			return types.SuiTransactionBlockEffects{}, err
		}
		v1.TransactionDigest = d
	}
	if fx == nil {
		return types.SuiTransactionBlockEffects{V1: v1}, nil
	}
	if fx.GetTransactionDigest() != "" {
		d, err := mustDigest(fx.GetTransactionDigest())
		if err != nil {
			return types.SuiTransactionBlockEffects{}, err
		}
		v1.TransactionDigest = d
	}
	v1.ExecutedEpoch = types.NewSafeSuiBigInt(fx.GetEpoch())
	if st := fx.GetStatus(); st != nil {
		if st.Success != nil && *st.Success {
			v1.Status.Status = types.ExecutionStatusSuccess
		}
		if st.GetError() != nil {
			v1.Status.Error = st.GetError().GetDescription()
			v1.Status.Status = types.ExecutionStatusFailure
		}
	}
	if gas := fx.GetGasUsed(); gas != nil {
		v1.GasUsed = types.GasCostSummary{
			ComputationCost:         types.NewSafeSuiBigInt(gas.GetComputationCost()),
			StorageCost:             types.NewSafeSuiBigInt(gas.GetStorageCost()),
			StorageRebate:           types.NewSafeSuiBigInt(gas.GetStorageRebate()),
			NonRefundableStorageFee: types.NewSafeSuiBigInt(gas.GetNonRefundableStorageFee()),
		}
	}
	return types.SuiTransactionBlockEffects{V1: v1}, nil
}

func transactionFromProto(tx *suirpc.ExecutedTransaction, options *types.SuiTransactionBlockResponseOptions) (*types.SuiTransactionBlockResponse, error) {
	if tx == nil {
		return nil, fmt.Errorf("empty transaction")
	}
	digest, err := mustDigest(tx.GetDigest())
	if err != nil {
		return nil, err
	}
	resp := &types.SuiTransactionBlockResponse{Digest: digest}
	if tx.Checkpoint != nil {
		cp := types.NewSafeSuiBigInt(tx.GetCheckpoint())
		resp.Checkpoint = &cp
	}
	if tx.GetTimestamp() != nil {
		ms := types.NewSafeSuiBigInt(uint64(tx.GetTimestamp().AsTime().UnixMilli()))
		resp.TimestampMs = &ms
	}
	showEffects := options == nil || options.ShowEffects || options.ShowObjectChanges
	if showEffects {
		fx, err := effectsFromProto(tx.GetEffects(), tx.GetDigest())
		if err != nil {
			return nil, err
		}
		resp.Effects = &lib.TagJson[types.SuiTransactionBlockEffects]{Data: fx}
	}
	if options != nil && options.ShowEvents && tx.GetEvents() != nil {
		events, err := eventsFromProto(tx.GetEvents().GetEvents(), tx)
		if err != nil {
			return nil, err
		}
		resp.Events = events
	}
	if options != nil && options.ShowBalanceChanges {
		for _, change := range tx.GetBalanceChanges() {
			mapped, err := balanceChangeFromProto(change)
			if err != nil {
				return nil, err
			}
			resp.BalanceChanges = append(resp.BalanceChanges, mapped)
		}
	}
	return resp, nil
}

func eventsFromProto(events []*suirpc.Event, tx *suirpc.ExecutedTransaction) ([]types.SuiEvent, error) {
	out := make([]types.SuiEvent, 0, len(events))
	for i, event := range events {
		mapped, err := eventFromProto(event, tx, uint64(i))
		if err != nil {
			return nil, err
		}
		out = append(out, mapped)
	}
	return out, nil
}

func eventFromProto(event *suirpc.Event, tx *suirpc.ExecutedTransaction, index uint64) (types.SuiEvent, error) {
	mapped := types.SuiEvent{
		TransactionModule: event.GetModule(),
		Type:              event.GetEventType(),
	}
	if event.GetPackageId() != "" {
		pkg, err := mustAddress(event.GetPackageId())
		if err != nil {
			return types.SuiEvent{}, err
		}
		mapped.PackageId = pkg
	}
	if event.GetSender() != "" {
		sender, err := mustAddress(event.GetSender())
		if err != nil {
			return types.SuiEvent{}, err
		}
		mapped.Sender = sender
	}
	digest := event.GetTransactionDigest()
	if digest == "" && tx != nil {
		digest = tx.GetDigest()
	}
	if digest != "" {
		d, err := mustDigest(digest)
		if err != nil {
			return types.SuiEvent{}, err
		}
		mapped.Id.TxDigest = d
	}
	seq := index
	if event.EventIndex != nil {
		seq = uint64(event.GetEventIndex())
	}
	mapped.Id.EventSeq = types.NewSafeSuiBigInt(seq)
	if len(event.GetContents().GetValue()) > 0 {
		mapped.Bcs = base64.StdEncoding.EncodeToString(event.GetContents().GetValue())
	}
	if event.GetJson() != nil {
		parsed, err := protoValue(event.GetJson())
		if err != nil {
			return types.SuiEvent{}, err
		}
		mapped.ParsedJson = parsed
	}
	if tx != nil && tx.GetTimestamp() != nil {
		ms := types.NewSafeSuiBigInt(uint64(tx.GetTimestamp().AsTime().UnixMilli()))
		mapped.TimestampMs = &ms
	}
	return mapped, nil
}

func balanceChangeFromProto(change *suirpc.BalanceChange) (types.BalanceChange, error) {
	addr, err := mustAddress(change.GetAddress())
	if err != nil {
		return types.BalanceChange{}, err
	}
	return types.BalanceChange{
		Owner:    types.ObjectOwner{ObjectOwnerInternal: &types.ObjectOwnerInternal{AddressOwner: &addr}},
		CoinType: change.GetCoinType(),
		Amount:   change.GetAmount(),
	}, nil
}

func balanceFromProto(b *suirpc.Balance) types.Balance {
	if b == nil {
		return types.Balance{}
	}
	return types.Balance{
		CoinType:      b.GetCoinType(),
		TotalBalance:  decimalU64(b.GetBalance()),
		LockedBalance: map[types.SafeSuiBigInt[uint64]]types.SuiBigInt{},
	}
}

func transactionFilter(filter *types.TransactionFilter) (*suirpc.TransactionFilter, *uint64, *uint64, error) {
	if filter == nil {
		return nil, nil, nil, nil
	}
	var literals []*suirpc.TransactionLiteral
	if filter.FromAddress != nil {
		literals = append(literals, &suirpc.TransactionLiteral{
			Predicate: &suirpc.TransactionLiteral_Sender{Sender: &suirpc.SenderFilter{Address: strp(filter.FromAddress.String())}},
		})
	}
	if filter.ToAddress != nil {
		literals = append(literals, &suirpc.TransactionLiteral{
			Predicate: &suirpc.TransactionLiteral_AffectedAddress{AffectedAddress: &suirpc.AffectedAddressFilter{Address: strp(filter.ToAddress.String())}},
		})
	}
	if filter.FromAndToAddress != nil {
		if filter.FromAndToAddress.From != nil {
			literals = append(literals, &suirpc.TransactionLiteral{
				Predicate: &suirpc.TransactionLiteral_Sender{Sender: &suirpc.SenderFilter{Address: strp(filter.FromAndToAddress.From.String())}},
			})
		}
		if filter.FromAndToAddress.To != nil {
			literals = append(literals, &suirpc.TransactionLiteral{
				Predicate: &suirpc.TransactionLiteral_AffectedAddress{AffectedAddress: &suirpc.AffectedAddressFilter{Address: strp(filter.FromAndToAddress.To.String())}},
			})
		}
	}
	if filter.InputObject != nil {
		literals = append(literals, affectedObjectLiteral(filter.InputObject.String()))
	}
	if filter.ChangedObject != nil {
		literals = append(literals, affectedObjectLiteral(filter.ChangedObject.String()))
	}
	if filter.MoveFunction != nil {
		path := filter.MoveFunction.Package.String()
		if filter.MoveFunction.Module != "" {
			path += "::" + filter.MoveFunction.Module
		}
		if filter.MoveFunction.Function != "" {
			path += "::" + filter.MoveFunction.Function
		}
		literals = append(literals, &suirpc.TransactionLiteral{
			Predicate: &suirpc.TransactionLiteral_MoveCall{MoveCall: &suirpc.MoveCallFilter{Function: strp(path)}},
		})
	}
	if filter.TransactionKind != nil {
		return nil, nil, nil, fmt.Errorf("transaction kind filter %q has no gRPC equivalent", *filter.TransactionKind)
	}
	var start, end *uint64
	if filter.Checkpoint != nil {
		cp := *filter.Checkpoint
		start = u64p(cp)
		end = u64p(cp + 1)
	}
	if len(literals) == 0 {
		return nil, start, end, nil
	}
	return &suirpc.TransactionFilter{Terms: []*suirpc.TransactionTerm{{Literals: literals}}}, start, end, nil
}

func affectedObjectLiteral(id string) *suirpc.TransactionLiteral {
	return &suirpc.TransactionLiteral{
		Predicate: &suirpc.TransactionLiteral_AffectedObject{AffectedObject: &suirpc.AffectedObjectFilter{ObjectId: strp(id)}},
	}
}

func ownedTypeFilter(query *types.SuiObjectResponseQuery) string {
	if query == nil || query.Filter == nil {
		return ""
	}
	return query.Filter.StructType
}

func objectMatchesFilter(obj *suirpc.Object, filter *types.SuiObjectDataFilter) bool {
	if filter == nil {
		return true
	}
	objectType := obj.GetObjectType()
	if filter.StructType != "" && objectType != filter.StructType && !strings.HasPrefix(objectType, filter.StructType) {
		return false
	}
	if filter.Package != nil && !samePackage(objectType, filter.Package.String()) {
		return false
	}
	if filter.MoveModule != nil {
		if !samePackage(objectType, filter.MoveModule.Package.String()) {
			return false
		}
		parts := strings.Split(objectType, "::")
		if len(parts) < 2 || parts[1] != filter.MoveModule.Module {
			return false
		}
	}
	return true
}

func samePackage(objectType, pkg string) bool {
	typePkg := objectType
	if i := strings.Index(objectType, "::"); i >= 0 {
		typePkg = objectType[:i]
	}
	left, err1 := mustAddress(typePkg)
	right, err2 := mustAddress(pkg)
	if err1 != nil || err2 != nil {
		return strings.EqualFold(typePkg, pkg)
	}
	return left == right
}

func dynamicFieldInfo(df *suirpc.DynamicField) (types.DynamicFieldInfo, error) {
	info := types.DynamicFieldInfo{ObjectType: df.GetValueType()}
	objectID := df.GetChildId()
	if objectID == "" {
		objectID = df.GetFieldId()
	}
	if objectID != "" {
		id, err := mustAddress(objectID)
		if err != nil {
			return types.DynamicFieldInfo{}, err
		}
		info.ObjectId = id
	}
	if df.GetKind() == suirpc.DynamicField_OBJECT {
		info.Type = lib.TagJson[sui_types.DynamicFieldType]{Data: sui_types.DynamicFieldType{DynamicObject: &lib.EmptyEnum{}}}
	} else {
		info.Type = lib.TagJson[sui_types.DynamicFieldType]{Data: sui_types.DynamicFieldType{DynamicField: &lib.EmptyEnum{}}}
	}
	if name := df.GetName(); name != nil {
		info.BcsName = lib.Base58(name.GetValue())
	}
	if field := df.GetFieldObject(); field != nil {
		if field.GetVersion() != 0 {
			info.Version = field.GetVersion()
		}
		if field.GetDigest() != "" {
			digest, err := mustDigest(field.GetDigest())
			if err != nil {
				return types.DynamicFieldInfo{}, err
			}
			info.Digest = digest
		}
		if info.ObjectType == "" {
			info.ObjectType = field.GetObjectType()
		}
		nameType, nameValue := dynamicFieldName(field)
		info.Name = sui_types.DynamicFieldName{Type: nameType, Value: nameValue}
	}
	return info, nil
}

func dynamicFieldName(field *suirpc.Object) (string, any) {
	nameType := firstTypeParam(field.GetObjectType())
	parsed, err := protoValue(field.GetJson())
	if err != nil {
		return nameType, nil
	}
	obj, ok := parsed.(map[string]any)
	if !ok {
		return nameType, nil
	}
	return nameType, obj["name"]
}

func firstTypeParam(objectType string) string {
	start := strings.Index(objectType, "<")
	end := strings.LastIndex(objectType, ">")
	if start < 0 || end <= start {
		return ""
	}
	inner := objectType[start+1 : end]
	depth := 0
	for i, r := range inner {
		switch r {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(inner[:i])
			}
		}
	}
	return strings.TrimSpace(inner)
}

func looseEqual(a, b any) bool {
	ab, aErr := json.Marshal(a)
	bb, bErr := json.Marshal(b)
	if aErr != nil || bErr != nil {
		return fmt.Sprint(a) == fmt.Sprint(b)
	}
	if bytes.Equal(ab, bb) {
		return true
	}
	return strings.Trim(string(ab), `"`) == strings.Trim(string(bb), `"`)
}

func signatureBytes(v any) ([]byte, error) {
	switch s := v.(type) {
	case []byte:
		return s, nil
	case string:
		decoded, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("signature string is not base64: %w", err)
		}
		return decoded, nil
	case lib.Base64Data:
		return []byte(s), nil
	case *lib.Base64Data:
		if s == nil {
			return nil, fmt.Errorf("nil signature")
		}
		return []byte(*s), nil
	default:
		return nil, fmt.Errorf("unsupported signature type %T", v)
	}
}

func systemStateFromEpoch(epoch *suirpc.Epoch) (*types.SuiSystemStateSummary, error) {
	if epoch == nil || epoch.GetSystemState() == nil {
		return nil, fmt.Errorf("system state is not in the epoch response")
	}
	ss := epoch.GetSystemState()
	summary := &types.SuiSystemStateSummary{
		Epoch:                           types.NewSafeSuiBigInt(ss.GetEpoch()),
		ProtocolVersion:                 types.NewSafeSuiBigInt(ss.GetProtocolVersion()),
		SystemStateVersion:              types.NewSafeSuiBigInt(ss.GetVersion()),
		ReferenceGasPrice:               types.NewSafeSuiBigInt(firstNonZero(ss.GetReferenceGasPrice(), epoch.GetReferenceGasPrice())),
		SafeMode:                        ss.GetSafeMode(),
		SafeModeStorageRewards:          types.NewSafeSuiBigInt(ss.GetSafeModeStorageRewards()),
		SafeModeComputationRewards:      types.NewSafeSuiBigInt(ss.GetSafeModeComputationRewards()),
		SafeModeStorageRebates:          types.NewSafeSuiBigInt(ss.GetSafeModeStorageRebates()),
		SafeModeNonRefundableStorageFee: types.NewSafeSuiBigInt(ss.GetSafeModeNonRefundableStorageFee()),
		EpochStartTimestampMs:           types.NewSafeSuiBigInt(ss.GetEpochStartTimestampMs()),
	}
	if fund := ss.GetStorageFund(); fund != nil {
		summary.StorageFundTotalObjectStorageRebates = types.NewSafeSuiBigInt(fund.GetTotalObjectStorageRebates())
		summary.StorageFundNonRefundableBalance = types.NewSafeSuiBigInt(fund.GetNonRefundableBalance())
	}
	if params := ss.GetParameters(); params != nil {
		summary.EpochDurationMs = types.NewSafeSuiBigInt(params.GetEpochDurationMs())
		summary.StakeSubsidyStartEpoch = types.NewSafeSuiBigInt(params.GetStakeSubsidyStartEpoch())
		summary.MaxValidatorCount = types.NewSafeSuiBigInt(params.GetMaxValidatorCount())
		summary.MinValidatorJoiningStake = types.NewSafeSuiBigInt(params.GetMinValidatorJoiningStake())
		summary.ValidatorLowStakeThreshold = types.NewSafeSuiBigInt(params.GetValidatorLowStakeThreshold())
		summary.ValidatorVeryLowStakeThreshold = types.NewSafeSuiBigInt(params.GetValidatorVeryLowStakeThreshold())
		summary.ValidatorLowStakeGracePeriod = types.NewSafeSuiBigInt(params.GetValidatorLowStakeGracePeriod())
	}
	if subsidy := ss.GetStakeSubsidy(); subsidy != nil {
		summary.StakeSubsidyBalance = types.NewSafeSuiBigInt(subsidy.GetBalance())
		summary.StakeSubsidyDistributionCounter = types.NewSafeSuiBigInt(subsidy.GetDistributionCounter())
		summary.StakeSubsidyCurrentDistributionAmount = types.NewSafeSuiBigInt(subsidy.GetCurrentDistributionAmount())
		summary.StakeSubsidyPeriodLength = types.NewSafeSuiBigInt(subsidy.GetStakeSubsidyPeriodLength())
		summary.StakeSubsidyDecreaseRate = uint16(subsidy.GetStakeSubsidyDecreaseRate())
	}
	if validators := ss.GetValidators(); validators != nil {
		summary.TotalStake = types.NewSafeSuiBigInt(validators.GetTotalStake())
		summary.ActiveValidators = make([]types.SuiValidatorSummary, 0, len(validators.GetActiveValidators()))
		for _, validator := range validators.GetActiveValidators() {
			mapped, err := validatorFromProto(validator)
			if err != nil {
				return nil, err
			}
			summary.ActiveValidators = append(summary.ActiveValidators, mapped)
		}
		summary.PendingActiveValidatorsId, summary.PendingActiveValidatorsSize = tableID(validators.GetPendingActiveValidators())
		summary.StakingPoolMappingsId, summary.StakingPoolMappingsSize = tableID(validators.GetStakingPoolMappings())
		summary.InactivePoolsId, summary.InactivePoolsSize = tableID(validators.GetInactiveValidators())
		summary.ValidatorCandidatesId, summary.ValidatorCandidatesSize = tableID(validators.GetValidatorCandidates())
		for _, removal := range validators.GetPendingRemovals() {
			summary.PendingRemovals = append(summary.PendingRemovals, types.NewSafeSuiBigInt(removal))
		}
		summary.AtRiskValidators = validators.GetAtRiskValidators()
	}
	if len(ss.GetValidatorReportRecords()) > 0 {
		records := make([]map[string]any, 0, len(ss.GetValidatorReportRecords()))
		for _, record := range ss.GetValidatorReportRecords() {
			records = append(records, map[string]any{
				"reported":  record.GetReported(),
				"reporters": record.GetReporters(),
			})
		}
		summary.ValidatorReportRecords = records
	}
	return summary, nil
}

func tableID(table *suirpc.MoveTable) (sui_types.ObjectID, types.SafeSuiBigInt[uint64]) {
	if table == nil || table.GetId() == "" {
		return sui_types.ObjectID{}, types.NewSafeSuiBigInt(uint64(0))
	}
	id, err := mustAddress(table.GetId())
	if err != nil {
		return sui_types.ObjectID{}, types.NewSafeSuiBigInt(table.GetSize())
	}
	return id, types.NewSafeSuiBigInt(table.GetSize())
}

func validatorFromProto(v *suirpc.Validator) (types.SuiValidatorSummary, error) {
	addr, err := mustAddress(v.GetAddress())
	if err != nil {
		return types.SuiValidatorSummary{}, err
	}
	summary := types.SuiValidatorSummary{
		SuiAddress:                   addr,
		ProtocolPubkeyBytes:          lib.Base64Data(v.GetProtocolPublicKey()),
		NetworkPubkeyBytes:           lib.Base64Data(v.GetNetworkPublicKey()),
		WorkerPubkeyBytes:            lib.Base64Data(v.GetWorkerPublicKey()),
		ProofOfPossessionBytes:       lib.Base64Data(v.GetProofOfPossession()),
		Name:                         v.GetName(),
		Description:                  v.GetDescription(),
		ImageUrl:                     v.GetImageUrl(),
		ProjectUrl:                   v.GetProjectUrl(),
		P2pAddress:                   v.GetP2PAddress(),
		NetAddress:                   v.GetNetworkAddress(),
		PrimaryAddress:               v.GetPrimaryAddress(),
		WorkerAddress:                v.GetWorkerAddress(),
		NextEpochProtocolPubkeyBytes: lib.Base64Data(v.GetNextEpochProtocolPublicKey()),
		NextEpochProofOfPossession:   lib.Base64Data(v.GetNextEpochProofOfPossession()),
		NextEpochNetworkPubkeyBytes:  lib.Base64Data(v.GetNextEpochNetworkPublicKey()),
		NextEpochWorkerPubkeyBytes:   lib.Base64Data(v.GetNextEpochWorkerPublicKey()),
		NextEpochNetAddress:          v.GetNextEpochNetworkAddress(),
		NextEpochP2pAddress:          v.GetNextEpochP2PAddress(),
		NextEpochPrimaryAddress:      v.GetNextEpochPrimaryAddress(),
		NextEpochWorkerAddress:       v.GetNextEpochWorkerAddress(),
		VotingPower:                  types.NewSafeSuiBigInt(v.GetVotingPower()),
		GasPrice:                     types.NewSafeSuiBigInt(v.GetGasPrice()),
		CommissionRate:               types.NewSafeSuiBigInt(v.GetCommissionRate()),
		NextEpochStake:               types.NewSafeSuiBigInt(v.GetNextEpochStake()),
		NextEpochGasPrice:            types.NewSafeSuiBigInt(v.GetNextEpochGasPrice()),
		NextEpochCommissionRate:      types.NewSafeSuiBigInt(v.GetNextEpochCommissionRate()),
	}
	if v.GetOperationCapId() != "" {
		op, err := mustAddress(v.GetOperationCapId())
		if err != nil {
			return types.SuiValidatorSummary{}, err
		}
		summary.OperationCapId = op
	}
	if pool := v.GetStakingPool(); pool != nil {
		if pool.GetId() != "" {
			id, err := mustAddress(pool.GetId())
			if err != nil {
				return types.SuiValidatorSummary{}, err
			}
			summary.StakingPoolId = id
		}
		summary.StakingPoolActivationEpoch = types.NewSafeSuiBigInt(pool.GetActivationEpoch())
		summary.StakingPoolDeactivationEpoch = types.NewSafeSuiBigInt(pool.GetDeactivationEpoch())
		summary.StakingPoolSuiBalance = types.NewSafeSuiBigInt(pool.GetSuiBalance())
		summary.RewardsPool = types.NewSafeSuiBigInt(pool.GetRewardsPool())
		summary.PoolTokenBalance = types.NewSafeSuiBigInt(pool.GetPoolTokenBalance())
		summary.PendingStake = types.NewSafeSuiBigInt(pool.GetPendingStake())
		summary.PendingPoolTokenWithdraw = types.NewSafeSuiBigInt(pool.GetPendingPoolTokenWithdraw())
		summary.PendingTotalSuiWithdraw = types.NewSafeSuiBigInt(pool.GetPendingTotalSuiWithdraw())
		if rates := pool.GetExchangeRates(); rates != nil {
			if rates.GetId() != "" {
				id, err := mustAddress(rates.GetId())
				if err != nil {
					return types.SuiValidatorSummary{}, err
				}
				summary.ExchangeRatesId = id
			}
			summary.ExchangeRatesSize = types.NewSafeSuiBigInt(rates.GetSize())
		}
	}
	return summary, nil
}

func firstNonZero(values ...uint64) uint64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func notExistsResponse(id suiObjectID) *types.SuiObjectResponse {
	return &types.SuiObjectResponse{
		Error: &lib.TagJson[types.SuiObjectResponseError]{Data: types.SuiObjectResponseError{
			NotExists: &struct {
				ObjectId sui_types.ObjectID `json:"object_id"`
			}{ObjectId: id},
		}},
	}
}
