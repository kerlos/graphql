package graphql

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/matryer/is"
)

var upgrader = websocket.Upgrader{} // use default options

func TestSub(t *testing.T) {
	is := is.New(t)

	flag.Parse()
	log.SetFlags(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		is.NoErr(err)
		var pl subscriptionMessage
		defer c.Close()

		is.NoErr(c.ReadJSON(&pl))
		is.Equal(string(pl.Type), string(gqlConnectionInit))

		pl.Type = gqlConnectionAck
		is.NoErr(c.WriteJSON(pl))

		is.NoErr(c.ReadJSON(&pl))
		is.Equal(string(pl.Type), gqlStart)
		var tmp1 struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		is.NoErr(json.Unmarshal(*pl.Payload, &tmp1))
		is.Equal(tmp1.Query, `subscription ($q: String) { cnt }`)
		is.Equal(len(tmp1.Variables), 1)
		is.Equal(tmp1.Variables["q"], "foo")
		is.Equal(*pl.ID, "0")

		id := *pl.ID

		pl.Payload = nil
		pl.ID = nil
		pl.Type = gqlConnectionAck
		is.NoErr(c.WriteJSON(pl))

		pl.ID = &id
		pl_pl := json.RawMessage(`{"data": "bar"}`)
		pl.Payload = &pl_pl
		pl.Type = gqlData
		is.NoErr(c.WriteJSON(pl))

		is.NoErr(c.ReadJSON(&pl))
		is.Equal(string(pl.Type), gqlStop)
		is.Equal(*pl.ID, id)
		pl.Payload = nil
		pl.Type = gqlComplete
		is.NoErr(c.WriteJSON(pl))

		is.NoErr(c.ReadJSON(&pl))
		is.Equal(string(pl.Type), gqlConnectionTerminate)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient(srv.URL)
	header := http.Header{}

	cl, err := client.SubscriptionClient(ctx, header)
	is.NoErr(err)

	vars := make(map[string]interface{})
	vars["q"] = "foo"
	sub, err := cl.Subscribe(&Request{Header: header, q: `subscription ($q: String) { cnt }`, vars: vars})
	is.NoErr(err)
	res := <-sub
	is.Equal(string(*res.Data), `{"data":"bar"}`)

	cl.Unsubscribe(sub)

	defer cancel()
	defer is.NoErr(cl.Close())
}
