package client

import (
	"context"

	"github.com/Sudo-Finance/go-sui-sdk/v2/lib"
	"github.com/Sudo-Finance/go-sui-sdk/v2/sui_types"
	"github.com/Sudo-Finance/go-sui-sdk/v2/sui_types/sui_system_state"
	"github.com/Sudo-Finance/go-sui-sdk/v2/suirpc"
	"github.com/Sudo-Finance/go-sui-sdk/v2/types"
	"github.com/fardream/go-bcs/bcs"
)

func (c *Client) GetLatestSuiSystemState(ctx context.Context) (*types.SuiSystemStateSummary, error) {
	resp, err := c.ledger.GetEpoch(ctx, &suirpc.GetEpochRequest{
		ReadMask: mask("epoch", "reference_gas_price", "system_state"),
	})
	if err != nil {
		return nil, err
	}
	return systemStateFromEpoch(resp.GetEpoch())
}

func (c *Client) GetValidatorsApy(ctx context.Context) (*types.ValidatorsApy, error) {
	return nil, ErrValidatorsApyUnavailable
}

func (c *Client) GetStakes(ctx context.Context, owner suiAddress) ([]types.DelegatedStake, error) {
	epoch, pools, err := c.stakingContext(ctx)
	if err != nil {
		return nil, err
	}
	var (
		cursor *string
		objs   []*suirpc.Object
	)
	for page := 0; page < 20; page++ {
		token, err := decodePageToken(cursor)
		if err != nil {
			return nil, err
		}
		resp, err := c.state.ListOwnedObjects(ctx, &suirpc.ListOwnedObjectsRequest{
			Owner:      strp(owner.String()),
			PageSize:   u32p(200),
			PageToken:  token,
			ObjectType: strp(stakedSuiType),
			ReadMask:   mask("object_id", "object_type", "json"),
		})
		if err != nil {
			return nil, err
		}
		objs = append(objs, resp.GetObjects()...)
		if len(resp.GetNextPageToken()) == 0 {
			break
		}
		encoded := encodePageToken(resp.GetNextPageToken())
		cursor = &encoded
	}
	return groupStakes(objs, epoch, pools)
}

func (c *Client) GetStakesByIds(ctx context.Context, stakedSuiIds []suiObjectID) ([]types.DelegatedStake, error) {
	epoch, pools, err := c.stakingContext(ctx)
	if err != nil {
		return nil, err
	}
	reqs := make([]*suirpc.GetObjectRequest, 0, len(stakedSuiIds))
	for _, id := range stakedSuiIds {
		reqs = append(reqs, &suirpc.GetObjectRequest{ObjectId: strp(id.String())})
	}
	resp, err := c.ledger.BatchGetObjects(ctx, &suirpc.BatchGetObjectsRequest{
		Requests: reqs,
		ReadMask: mask("object_id", "object_type", "json"),
	})
	if err != nil {
		return nil, err
	}
	objs := make([]*suirpc.Object, 0, len(resp.GetObjects()))
	for _, result := range resp.GetObjects() {
		if result.GetError() != nil || result.GetObject() == nil {
			continue
		}
		objs = append(objs, result.GetObject())
	}
	return groupStakes(objs, epoch, pools)
}

func (c *Client) stakingContext(ctx context.Context) (uint64, map[string]suiAddress, error) {
	resp, err := c.ledger.GetEpoch(ctx, &suirpc.GetEpochRequest{
		ReadMask: mask("epoch", "system_state.epoch", "system_state.validators.active_validators.address", "system_state.validators.active_validators.staking_pool.id"),
	})
	if err != nil {
		return 0, nil, err
	}
	epoch := resp.GetEpoch().GetEpoch()
	if resp.GetEpoch().GetSystemState() != nil && resp.GetEpoch().GetSystemState().Epoch != nil {
		epoch = resp.GetEpoch().GetSystemState().GetEpoch()
	}
	pools := map[string]suiAddress{}
	validators := resp.GetEpoch().GetSystemState().GetValidators().GetActiveValidators()
	for _, validator := range validators {
		if validator.GetStakingPool().GetId() == "" || validator.GetAddress() == "" {
			continue
		}
		addr, err := mustAddress(validator.GetAddress())
		if err != nil {
			return 0, nil, err
		}
		pools[validator.GetStakingPool().GetId()] = addr
	}
	return epoch, pools, nil
}

func groupStakes(objs []*suirpc.Object, epoch uint64, pools map[string]suiAddress) ([]types.DelegatedStake, error) {
	grouped := map[string]*types.DelegatedStake{}
	var order []string
	for _, obj := range objs {
		pool, stake, err := stakeFromObject(obj, epoch)
		if err != nil {
			return nil, err
		}
		entry, ok := grouped[pool]
		if !ok {
			poolID, err := mustAddress(pool)
			if err != nil {
				return nil, err
			}
			entry = &types.DelegatedStake{
				ValidatorAddress: pools[pool],
				StakingPool:      poolID,
			}
			grouped[pool] = entry
			order = append(order, pool)
		}
		entry.Stakes = append(entry.Stakes, types.JsonFlatten[types.Stake]{Data: stake})
	}
	out := make([]types.DelegatedStake, 0, len(order))
	for _, pool := range order {
		out = append(out, *grouped[pool])
	}
	return out, nil
}

func stakeFromObject(obj *suirpc.Object, epoch uint64) (string, types.Stake, error) {
	id, err := mustAddress(obj.GetObjectId())
	if err != nil {
		return "", types.Stake{}, err
	}
	parsed, err := protoValue(obj.GetJson())
	if err != nil {
		return "", types.Stake{}, err
	}
	fields, _ := parsed.(map[string]any)
	pool, _ := fields["pool_id"].(string)
	if pool == "" {
		if nested, ok := fields["pool_id"].(map[string]any); ok {
			pool, _ = nested["id"].(string)
		}
	}
	principal, _ := asUint64(fields["principal"])
	activation, _ := asUint64(fields["stake_activation_epoch"])
	status := types.Status{}
	if activation > 0 && epoch >= activation {
		status.Active = &struct {
			EstimatedReward types.SafeSuiBigInt[uint64] `json:"estimatedReward"`
		}{}
	} else {
		status.Pending = &struct{}{}
	}
	stakeStatus := types.StakeStatus{Data: status}
	return pool, types.Stake{
		StakedSuiId:       id,
		StakeRequestEpoch: types.NewSafeSuiBigInt(activation),
		StakeActiveEpoch:  types.NewSafeSuiBigInt(activation),
		Principal:         types.NewSafeSuiBigInt(principal),
		StakeStatus:       &stakeStatus,
	}, nil
}

func (c *Client) RequestAddStake(
	ctx context.Context,
	signer suiAddress,
	coins []suiObjectID,
	amount types.SuiBigInt,
	validator suiAddress,
	gas *suiObjectID,
	gasBudget types.SuiBigInt,
) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_requestAddStake")
}

func (c *Client) RequestWithdrawStake(
	ctx context.Context,
	signer suiAddress,
	stakedSuiId suiObjectID,
	gas *suiObjectID,
	gasBudget types.SuiBigInt,
) (*types.TransactionBytes, error) {
	return nil, removedBuilder("unsafe_requestWithdrawStake")
}

func BCS_RequestAddStake(
	signer suiAddress,
	coins []*sui_types.ObjectRef,
	amount types.SafeSuiBigInt[uint64],
	validator suiAddress,
	gasBudget, gasPrice uint64,
) ([]byte, error) {
	// build with BCS
	ptb := sui_types.NewProgrammableTransactionBuilder()
	amtArg, err := ptb.Pure(amount.Uint64())
	if err != nil {
		return nil, err
	}
	arg0, err := ptb.Obj(sui_types.SuiSystemMutObj)
	if err != nil {
		return nil, err
	}
	arg1 := ptb.Command(
		sui_types.Command{
			SplitCoins: &struct {
				Argument  sui_types.Argument
				Arguments []sui_types.Argument
			}{
				Argument:  sui_types.Argument{GasCoin: &lib.EmptyEnum{}},
				Arguments: []sui_types.Argument{amtArg},
			},
		},
	) // the coin is split result argument
	arg2, err := ptb.Pure(validator)
	if err != nil {
		return nil, err
	}

	ptb.Command(
		sui_types.Command{
			MoveCall: &sui_types.ProgrammableMoveCall{
				Package:  *sui_types.SuiSystemAddress,
				Module:   sui_system_state.SuiSystemModuleName,
				Function: sui_types.AddStakeFunName,
				Arguments: []sui_types.Argument{
					arg0, arg1, arg2,
				},
			},
		},
	)
	pt := ptb.Finish()
	tx := sui_types.NewProgrammable(
		signer, coins, pt, gasBudget, gasPrice,
	)
	return bcs.Marshal(tx)
}

func BCS_RequestWithdrawStake(signer suiAddress, stakedSuiRef sui_types.ObjectRef, gas []*sui_types.ObjectRef, gasBudget, gasPrice uint64) ([]byte, error) {
	// build with BCS
	ptb := sui_types.NewProgrammableTransactionBuilder()
	arg0, err := ptb.Obj(sui_types.SuiSystemMutObj)
	if err != nil {
		return nil, err
	}
	arg1, err := ptb.Obj(sui_types.ObjectArg{
		ImmOrOwnedObject: &stakedSuiRef,
	})
	if err != nil {
		return nil, err
	}
	ptb.Command(sui_types.Command{
		MoveCall: &sui_types.ProgrammableMoveCall{
			Package:  *sui_types.SuiSystemAddress,
			Module:   sui_system_state.SuiSystemModuleName,
			Function: sui_types.WithdrawStakeFunName,
			Arguments: []sui_types.Argument{
				arg0, arg1,
			},
		},
	})
	pt := ptb.Finish()
	tx := sui_types.NewProgrammable(
		signer, gas, pt, gasBudget, gasPrice,
	)
	return bcs.Marshal(tx)
}
