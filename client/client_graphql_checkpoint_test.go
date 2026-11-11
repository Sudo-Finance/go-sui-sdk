package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Sudo-Finance/go-sui-sdk/v2/types"
)

func TestQueryEventsPreservesCheckpointAndCursor(t *testing.T) {
	checkpoint := uint64(237661232)
	cursor := "resume"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Variables map[string]json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		var filter map[string]json.RawMessage
		if err := json.Unmarshal(body.Variables["filter"], &filter); err != nil {
			t.Error(err)
			return
		}
		if string(filter["afterCheckpoint"]) != "237661232" || string(filter["type"]) != `"0x2::example::Event"` {
			t.Errorf("checkpoint or event filter missing: %s", body.Variables["filter"])
		}
		if string(body.Variables["after"]) != `"resume"` {
			t.Error("resume cursor missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"events":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"next"}}}}`))
	}))
	defer server.Close()
	c := &Client{graphql: server.URL, http: server.Client()}
	eventType := "0x2::example::Event"
	page, err := c.QueryEvents(context.Background(), types.EventFilter{
		AfterCheckpoint: &checkpoint,
		MoveEventType:   &eventType,
	}, &cursor, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasNextPage || page.NextPageToken != "next" {
		t.Fatalf("empty filtered page lost its forward cursor: %#v", page)
	}
}
