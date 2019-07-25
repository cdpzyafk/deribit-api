package deribit

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/chuckpreslar/emission"
	"github.com/sourcegraph/jsonrpc2"
	"github.com/sumorf/deribit-api/models"
	"log"
	"net/http"
	"nhooyr.io/websocket"
	"strings"
	"time"
)

const (
	RealBaseURL = "wss://www.deribit.com/ws/api/v2/"
	TestBaseURL = "wss://test.deribit.com/ws/api/v2/"
)

const (
	MaxTryTimes = 10
)

var (
	ErrAuthenticationIsRequired = errors.New("authentication is required")
)

// Event is wrapper of received event
type Event struct {
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

type Configuration struct {
	Ctx           context.Context
	Addr          string `json:"addr"`
	ApiKey        string `json:"api_key"`
	SecretKey     string `json:"secret_key"`
	AutoReconnect bool   `json:"auto_reconnect"`
	DebugMode     bool   `json:"debug_mode"`
}

type Client struct {
	ctx       context.Context
	addr      string
	apiKey    string
	secretKey string

	conn    *websocket.Conn
	rpcConn *jsonrpc2.Conn

	auth struct {
		token   string
		refresh string
	}

	subscriptions    []string
	subscriptionsMap map[string]struct{}

	emitter *emission.Emitter
}

func New(cfg *Configuration) *Client {
	ctx := cfg.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return &Client{
		ctx:              ctx,
		addr:             cfg.Addr,
		apiKey:           cfg.ApiKey,
		secretKey:        cfg.SecretKey,
		subscriptionsMap: make(map[string]struct{}),
		emitter:          emission.NewEmitter(),
	}
}

func (c *Client) Subscribe(channels []string) {
	c.subscriptions = append(c.subscriptions, channels...)
	c.subscribe(channels)
}

func (c *Client) subscribe(channels []string) {
	var publicChannels []string
	var privateChannels []string

	for _, v := range c.subscriptions {
		if _, ok := c.subscriptionsMap[v]; ok {
			continue
		}
		if strings.HasPrefix(v, "user.") {
			privateChannels = append(privateChannels, v)
		} else {
			publicChannels = append(publicChannels, v)
		}
	}

	if len(publicChannels) > 0 {
		c.PublicSubscribe(&models.SubscribeParams{
			Channels: publicChannels,
		})
	}
	if len(privateChannels) > 0 {
		c.PrivateSubscribe(&models.SubscribeParams{
			Channels: privateChannels,
		})
	}

	allChannels := append(publicChannels, privateChannels...)
	for _, v := range allChannels {
		c.subscriptionsMap[v] = struct{}{}
	}
}

func (c *Client) Start() error {
	c.conn = nil
	for i := 0; i < MaxTryTimes; i++ {
		conn, _, err := c.connect()
		if err != nil {
			log.Println(err)
			time.Sleep(1 * time.Second)
			continue
		}
		c.conn = conn
		break
	}
	if c.conn == nil {
		return errors.New("connect fail")
	}

	c.rpcConn = jsonrpc2.NewConn(context.Background(), NewObjectStream(c.conn), c)

	return nil
}

// Call issues JSONRPC v2 calls
func (c *Client) Call(method string, params interface{}, result interface{}) error {
	if params == nil {
		params = emptyParams
	}

	if token, ok := params.(privateParams); ok {
		if c.auth.token == "" {
			return ErrAuthenticationIsRequired
		}
		token.setToken(c.auth.token)
	}

	return c.rpcConn.Call(c.ctx, method, params, result)
}

// Handle implements jsonrpc2.Handler
func (c *Client) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	//log.Printf("Handle %v", req.Method)
	if req.Method == "subscription" {
		// update events
		if req.Params != nil && len(*req.Params) > 0 {
			var event Event
			if err := json.Unmarshal(*req.Params, &event); err != nil {
				//c.setError(err)
				return
			}
			c.subscriptionsProcess(&event)
		}
	}
}

// DisconnectNotify returns a channel that is closed when the
// underlying connection is disconnected.
func (c *Client) DisconnectNotify() <-chan struct{} {
	return c.rpcConn.DisconnectNotify()
}

func (c *Client) connect() (*websocket.Conn, *http.Response, error) {
	ctx, _ := context.WithTimeout(context.Background(), 10*time.Second)
	//defer cancel()
	conn, resp, err := websocket.Dial(ctx, c.addr, websocket.DialOptions{})
	return conn, resp, err
}