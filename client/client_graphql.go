package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Sudo-Finance/go-sui-sdk/v2/sui_types"
	"github.com/Sudo-Finance/go-sui-sdk/v2/types"
)

const eventsQuery = `
query Events($filter: EventFilter, $first: Int, $last: Int, $after: String, $before: String) {
  events(filter: $filter, first: $first, last: $last, after: $after, before: $before) {
    pageInfo { hasNextPage hasPreviousPage startCursor endCursor }
    nodes {
      timestamp
      sender { address }
      transaction { digest }
      transactionModule { name package { address } }
      contents { type { repr } json }
      eventBcs
    }
  }
}`

// QueryEvents reads events from the GraphQL endpoint.
// cursor is the GraphQL page cursor from a previous response's NextPageToken.
// JSON-RPC EventId cursors are not accepted.
//
// GraphQL filters are flat. Sender, MoveEventType, Package, and MoveModule are
// sent as sender, type, and module. All, Any, TimeRange, MoveEventField, and
// Transaction filters are rejected because GraphQL has no equivalent combinator.
func (c *Client) QueryEvents(
	ctx context.Context, query types.EventFilter, cursor *string, limit *uint, descendingOrder bool,
) (*types.EventPage, error) {
	if c.graphql == "" {
		return nil, fmt.Errorf("GraphQL endpoint is not configured; pass WithGraphQLEndpoint or dial a fullnode URL")
	}
	filter, err := graphQLEventFilter(query)
	if err != nil {
		return nil, err
	}
	variables := map[string]any{}
	if len(filter) > 0 {
		variables["filter"] = filter
	}
	pageSize := 50
	if limit != nil && *limit > 0 && *limit < 50 {
		pageSize = int(*limit)
	}
	if descendingOrder {
		variables["last"] = pageSize
		if cursor != nil && *cursor != "" {
			variables["before"] = *cursor
		}
	} else {
		variables["first"] = pageSize
		if cursor != nil && *cursor != "" {
			variables["after"] = *cursor
		}
	}
	var payload struct {
		Data struct {
			Events struct {
				PageInfo struct {
					HasNextPage     bool    `json:"hasNextPage"`
					HasPreviousPage bool    `json:"hasPreviousPage"`
					StartCursor     *string `json:"startCursor"`
					EndCursor       *string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []graphQLEvent `json:"nodes"`
			} `json:"events"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.graphQL(ctx, eventsQuery, variables, &payload); err != nil {
		return nil, err
	}
	if len(payload.Errors) > 0 {
		return nil, fmt.Errorf("graphql: %s", payload.Errors[0].Message)
	}
	page := &types.EventPage{}
	for i, node := range payload.Data.Events.Nodes {
		event, err := node.toSuiEvent(uint64(i))
		if err != nil {
			return nil, err
		}
		page.Data = append(page.Data, event)
	}
	info := payload.Data.Events.PageInfo
	if descendingOrder {
		page.HasNextPage = info.HasPreviousPage
		if info.StartCursor != nil {
			page.NextPageToken = *info.StartCursor
		}
	} else {
		page.HasNextPage = info.HasNextPage
		if info.EndCursor != nil {
			page.NextPageToken = *info.EndCursor
		}
	}
	return page, nil
}

func graphQLEventFilter(query types.EventFilter) (map[string]any, error) {
	switch {
	case query.All != nil:
		return nil, fmt.Errorf("GraphQL event filters have no All combinator")
	case query.Any != nil:
		return nil, fmt.Errorf("GraphQL event filters have no Any combinator")
	case query.TimeRange != nil:
		return nil, fmt.Errorf("GraphQL event filters have no time range; bound the query with a checkpoint instead")
	case query.MoveEventField != nil:
		return nil, fmt.Errorf("GraphQL event filters cannot match a JSON field; filter by type and sender")
	case query.Transaction != nil:
		return nil, fmt.Errorf("GraphQL event filters cannot match a transaction digest; use GetEvents")
	}
	filter := map[string]any{}
	if query.AfterCheckpoint != nil {
		filter["afterCheckpoint"] = *query.AfterCheckpoint
	}
	if query.Sender != nil {
		filter["sender"] = query.Sender.String()
	}
	if query.MoveEventType != nil {
		filter["type"] = *query.MoveEventType
	}
	switch {
	case query.MoveModule != nil:
		filter["module"] = query.MoveModule.Package.String() + "::" + query.MoveModule.Module
	case query.Package != nil:
		filter["module"] = query.Package.String()
	}
	return filter, nil
}

type graphQLEvent struct {
	Timestamp *time.Time `json:"timestamp"`
	Sender    *struct {
		Address string `json:"address"`
	} `json:"sender"`
	Transaction *struct {
		Digest string `json:"digest"`
	} `json:"transaction"`
	TransactionModule *struct {
		Name    string `json:"name"`
		Package *struct {
			Address string `json:"address"`
		} `json:"package"`
	} `json:"transactionModule"`
	Contents *struct {
		Type *struct {
			Repr string `json:"repr"`
		} `json:"type"`
		JSON any `json:"json"`
	} `json:"contents"`
	EventBcs string `json:"eventBcs"`
}

func (e graphQLEvent) toSuiEvent(index uint64) (types.SuiEvent, error) {
	event := types.SuiEvent{
		Bcs:        e.EventBcs,
		Id:         types.EventId{EventSeq: types.NewSafeSuiBigInt(index)},
		ParsedJson: nil,
	}
	if e.TransactionModule != nil {
		event.TransactionModule = e.TransactionModule.Name
		if e.TransactionModule.Package != nil && e.TransactionModule.Package.Address != "" {
			pkg, err := mustAddress(e.TransactionModule.Package.Address)
			if err != nil {
				return types.SuiEvent{}, err
			}
			event.PackageId = pkg
		}
	}
	if e.Sender != nil && e.Sender.Address != "" {
		sender, err := mustAddress(e.Sender.Address)
		if err != nil {
			return types.SuiEvent{}, err
		}
		event.Sender = sender
	}
	if e.Contents != nil {
		if e.Contents.Type != nil {
			event.Type = e.Contents.Type.Repr
		}
		event.ParsedJson = e.Contents.JSON
	}
	if e.Transaction != nil && e.Transaction.Digest != "" {
		digest, err := sui_types.NewDigest(e.Transaction.Digest)
		if err != nil {
			return types.SuiEvent{}, err
		}
		event.Id.TxDigest = *digest
	}
	if e.Timestamp != nil {
		ms := types.NewSafeSuiBigInt(uint64(e.Timestamp.UnixMilli()))
		event.TimestampMs = &ms
	}
	return event, nil
}

func (c *Client) graphQL(ctx context.Context, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphql, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("x-token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return HTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Body: payload}
	}
	return json.Unmarshal(payload, out)
}
