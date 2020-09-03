package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

type SubscriptionClient struct {
	subWebsocket *websocket.Conn
	subBuffer    chan subscriptionMessage
	subWait      sync.WaitGroup
	subs         sync.Map
	subIDGen     int
}

type subscriptionMessageType string

const (
	gqlConnectionInit      subscriptionMessageType = "connection_init"
	gqlStart                                       = "start"
	gqlStop                                        = "stop"
	gqlConnectionAck                               = "connection_ack"
	gqlConnectionTerminate                         = "connection_terminate"
	gqlConnectionError                             = "connection_error"
	gqlData                                        = "data"
	gqlError                                       = "error"
	gqlComplete                                    = "GQL_COMPLETE"
	gqlConnectionKeepAlive                         = "ka"
)

type subscriptionMessage struct {
	Payload *json.RawMessage        `json:"payload,omitempty"`
	ID      *string                 `json:"id,omitempty"`
	Type    subscriptionMessageType `json:"type"`
}

func (c *Client) SubscriptionClient(ctx context.Context, header http.Header) (*SubscriptionClient, error) {
	dialer := websocket.DefaultDialer
	// header.Set("Sec-WebSocket-Protocol", "graphql-ws")
	header.Set("Content-Type", "application/json")

	for k, v := range c.globalHeaders {
		header.Set(k, v)
	}

	conn, _, err := dialer.DialContext(ctx, strings.Replace(c.endpoint, "http", "ws", 1), header)

	if err != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, err
	}
	subClient := &SubscriptionClient{
		subWebsocket: conn,
		subBuffer:    make(chan subscriptionMessage),
	}

	var msg subscriptionMessage

	msg.Type = gqlConnectionInit
	emptyPayload := json.RawMessage("{}")
	msg.Payload = &emptyPayload
	err = conn.WriteJSON(msg)
	if err != nil {
		return nil, err
	}

	for i := 0; i <= 5; i++ {
		err = conn.ReadJSON(&msg)
		if err != nil {
			return nil, err
		}

		switch msg.Type {
		case gqlConnectionKeepAlive:
			continue

		case gqlConnectionAck:
			goto DONE

		default:
			conn.Close()

			if msg.Type == gqlConnectionError {
				errJ, _ := json.Marshal(*msg.Payload)
				return nil, errors.New(string(errJ))
			}

			return nil, errors.New("server-did-not-acknowledge")
		}
	}

	return nil, errors.New("timeout")

DONE:
	go subClient.subWork()
	return subClient, nil
}

func (c *SubscriptionClient) Close() error {
	if c.subWebsocket == nil {
		return nil
	}
	err := c.subWebsocket.WriteJSON(subscriptionMessage{Type: gqlConnectionTerminate})
	if err != nil {
		return err
	}

	c.subWait.Wait()
	err = c.subWebsocket.Close()
	if err != nil {
		return err
	}
	return nil
}

type SubscriptionPayload struct {
	Data  *json.RawMessage
	Error *json.RawMessage
}

type Subscription chan SubscriptionPayload

func (c *SubscriptionClient) subWork() {
	c.subWait.Add(1)

	defer c.subWait.Done()
	defer c.subs.Range(func(_, sub interface{}) bool {
		close(sub.(Subscription))
		return true
	})

	for {
		var msg subscriptionMessage
		err := c.subWebsocket.ReadJSON(&msg)

		if err != nil {
			if err == io.ErrUnexpectedEOF || err == io.EOF {
				//close every subscription
				return
			}
			if strings.HasSuffix(err.Error(), io.ErrUnexpectedEOF.Error()) {
				return
			}

			errMsg := json.RawMessage([]byte(fmt.Sprintf("Error reading from subscription websocket : %s", err)))
			c.subs.Range(func(_, ch interface{}) bool {
				ch.(Subscription) <- SubscriptionPayload{Error: &errMsg}
				return true
			})

			return
		}

		switch msg.Type {
		case gqlError:
			id := *msg.ID
			ch, _ := c.subs.Load(id)
			ch.(Subscription) <- SubscriptionPayload{Error: msg.Payload}

		case gqlData:
			id := *msg.ID
			ch, _ := c.subs.Load(id)
			ch.(Subscription) <- SubscriptionPayload{Data: msg.Payload}

		case gqlComplete:
			id := *msg.ID
			ch, _ := c.subs.Load(id)
			close(ch.(Subscription))
			c.subs.Delete(id)

		case gqlConnectionKeepAlive: //ignore...
		}
	}
}

func (c *SubscriptionClient) Subscribe(req *Request) (Subscription, error) {
	var requestBody bytes.Buffer
	requestBodyObj := struct {
		Query     string                 `json:"query"`
		Variables map[string]interface{} `json:"variables"`
	}{
		Query:     req.q,
		Variables: req.vars,
	}
	if err := json.NewEncoder(&requestBody).Encode(requestBodyObj); err != nil {
		return nil, errors.Wrap(err, "encode body")
	}
	id := strconv.Itoa(c.subIDGen)
	c.subIDGen++

	payload := json.RawMessage(requestBody.Bytes())
	sReq := subscriptionMessage{
		Payload: &payload,
		ID:      &id,
		Type:    gqlStart,
	}

	subChan := make(Subscription)
	c.subs.Store(id, subChan)
	err := c.subWebsocket.WriteJSON(sReq)
	if err != nil {
		return nil, err
	}

	return subChan, nil
}

func (c *SubscriptionClient) Unsubscribe(sub Subscription) {
	c.subs.Range(func(key interface{}, value interface{}) bool {
		if value == sub {
			id := key.(string)
			_ = c.subWebsocket.WriteJSON(subscriptionMessage{ID: &id, Type: gqlStop})
			return false
		}
		return true
	})
}
