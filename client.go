package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type Client interface {
	Connect() error
	GetReadChannel() chan []byte
	GetErrorChannel() chan error
	String() string
	IsAdmin() bool
	IsConnected() bool
}

type AuthClient interface {
	Client
	Login() error
}

type AdminClient interface {
	AuthClient
	Send() error
}

func getLoginURL() string {
	return fmt.Sprintf(BaseURL, "http", LoginURLPath)
}

func getWebsocketURL() string {
	return fmt.Sprintf(BaseURL, "ws", WSURLPath)
}

// getSendRequest returns the request that is send by the admin clients
func getSendRequest() (r *http.Request) {
	r, err := http.NewRequest(
		"PUT",
		fmt.Sprintf(BaseURL, "http", "rest/agenda/item/1/"),
		strings.NewReader(`
			{"id":1,"item_number":"","title":"foo1","list_view_title":"foo1",
			"comment":"test","closed":false,"type":1,"is_hidden":false,"duration":null,
			"speaker_list_closed":false,"content_object":{"collection":"topics/topic",
			"id":1},"weight":10000,"parent_id":null,"parentCount":0,"hover":true}`),
	)
	if err != nil {
		log.Fatalf("Coud not build the request, %s", err)
	}
	return r
}

// Client represents one of many openslides users
type client struct {
	username string
	isAuth   bool
	isAdmin  bool

	wsRead  chan []byte
	wsClose chan bool
	wsError chan error

	wsConnection *websocket.Conn
	cookies      *cookiejar.Jar

	connected bool
}

// NewAnonymousClient creates an anonymous client.
func NewAnonymousClient() *client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Fatalln("Can not create cookie jar, %s", err)
	}
	return &client{
		wsRead:  make(chan []byte),
		wsClose: make(chan bool),
		wsError: make(chan error),
		cookies: jar,
	}
}

// NewUserClient creates an user client.
func NewUserClient(username string) *client {
	client := NewAnonymousClient()
	client.username = username
	client.isAuth = true
	return client
}

// NewAdminClient creates an admin client.
func NewAdminClient(username string) *client {
	client := NewUserClient(username)
	client.isAdmin = true
	return client
}

func (c *client) IsAdmin() bool {
	return c.isAdmin
}

func (c *client) IsConnected() bool {
	return c.connected
}

func (c *client) String() string {
	if !c.isAuth {
		return "anonymous"
	}
	return c.username
}

// Connect creates a websocket connection. It blocks until the connection is
// established.
func (c *client) Connect() (err error) {
	loginErrorCount := 0
	for loginErrorCount < MaxConnectionAttemts {
		dialer := websocket.Dialer{
			Jar: c.cookies,
		}
		var r *http.Response
		c.wsConnection, r, err = dialer.Dial(getWebsocketURL(), nil)
		if err != nil {
			if err == websocket.ErrBadHandshake && r.StatusCode == 503 {
				// The channel was full. Try again later. This does not count as error.
				time.Sleep(100 * time.Millisecond)
				continue
			}
			loginErrorCount++
			continue
		}
		// if no error happend, then we can break the loop
		c.connected = true
		break
	}
	if err != nil {
		log.Printf("Count not connect client %s\n", c)
		return err
	}

	go func() {
		defer c.wsConnection.Close()
		for {
			_, m, err := c.wsConnection.ReadMessage()
			if err != nil {
				c.wsError <- err
			}
			c.GetReadChannel() <- m
		}
	}()
	return nil
}

func (c *client) GetReadChannel() chan []byte {
	return c.wsRead
}

func (c *client) GetErrorChannel() chan error {
	return c.wsError
}

func (c *client) getLoginData() string {
	return fmt.Sprintf("{\"username\": \"%s\", \"password\": \"%s\"}", c.username, LoginPassword)
}

func (c *client) Login() (err error) {
	httpClient := &http.Client{
		Jar: c.cookies,
	}
	var resp *http.Response
	loginErrorCount := 0
	for loginErrorCount < MaxLoginAttemts {
		resp, err = httpClient.Post(
			getLoginURL(),
			"application/json",
			strings.NewReader(c.getLoginData()),
		)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode < 500 || resp.StatusCode >= 600 {
			break
		}
		// If the error is on the server side, then retry
		loginErrorCount++
		time.Sleep(100 * time.Millisecond)
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("login for client %s failed: StatusCode: %d", c, resp.StatusCode)
	}
	return nil
}

func (c *client) Send() (err error) {
	httpClient := &http.Client{
		Jar: c.cookies,
	}
	req := getSendRequest()

	// Write csrf token from cookie into the http header
	var CSRFToken string
	for _, cookie := range c.cookies.Cookies(req.URL) {
		if cookie.Name == CSRFCookieName {
			CSRFToken = cookie.Value
			break
		}
	}
	if CSRFToken == "" {
		log.Fatalln("No CSRFToken in cookies")
	}

	req.Header.Set("X-CSRFToken", CSRFToken)
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBuffer, _ := ioutil.ReadAll(resp.Body)
		fmt.Printf("%s\n", bodyBuffer)
		return fmt.Errorf("Got an error by sending the request, status: %s", resp.Status)
	}
	return nil
}

// Connects a slice of clients. Uses X connectWorker to work X clients in parallel.
func connectClients(clients []Client, errChan chan error, connected chan time.Duration) {
	toWorker := make(chan Client)
	defer close(toWorker)
	// Start workers
	for i := 0; i < ParallelConnections; i++ {
		go func() {
			for client := range toWorker {
				start := time.Now()
				err := client.Connect()
				if err != nil {
					errChan <- err
				} else {
					connected <- time.Since(start)
				}
			}
		}()
	}
	// Send clients to workers
	for _, client := range clients {
		toWorker <- client
	}
}

// Login a slice of clients. Uses X connectWorker to work X clients in parallel.
// Expects the clients to be AuthClients
func loginClients(clients []Client) {
	toWorker := make(chan Client)
	defer close(toWorker)
	// Start workers
	for i := 0; i < ParallelLogins; i++ {
		go func() {
			for client := range toWorker {
				err := client.(AuthClient).Login()
				if err != nil {
					log.Fatalf("Can not login client %s", client)
				}
			}
		}()
	}
	// Send clients to workers
	for _, client := range clients {
		toWorker <- client
	}
}
