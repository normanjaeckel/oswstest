package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Client interface {
	Connect() error
	String() string
	IsAdmin() bool
	IsConnected() bool
	ExpectData(sinceTime chan time.Duration, err chan error, count int, finish chan bool, expect uint64, since *time.Time, sinceSet chan bool)
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
	wsError chan error

	wsConnection *websocket.Conn
	cookies      *cookiejar.Jar

	connected       time.Time
	connectionError chan bool
	waitForConnect  chan bool
}

// NewAnonymousClient creates an anonymous client.
func NewAnonymousClient() *client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Fatalln("Can not create cookie jar, %s", err)
	}
	return &client{
		waitForConnect:  make(chan bool),
		connectionError: make(chan bool),
		cookies:         jar,
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
	return !c.connected.IsZero()
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
		break
	}
	if err != nil {
		log.Printf("Could not connect client %s, %s\n", c, err)
		close(c.connectionError)
		return err
	}

	// Set the connected time to now and close the waitForConnect channel to signal
	// that the client is now connected.
	c.connected = time.Now()
	close(c.waitForConnect)

	go func() {
		// Write all incomming messages into c.wsRead.
		// Before SetChannel() wist called, this channel is nil, so all messages
		// will be dropped.
		defer c.wsConnection.Close()
		for {
			_, m, err := c.wsConnection.ReadMessage()
			if err != nil {
				c.wsError <- err
				// TODO: What can happen after we break?
				break
			}
			// Send the message to the channel. If no channel is set, then the message
			// is send to /dev/null
			// TODO: Maybe buffer the messages, so there is no problem, if a message
			// is received before a test starts to listen?
			c.wsRead <- m
		}
	}()
	return nil
}

// Set the channels to receive data.
func (c *client) SetChannels(read chan []byte, err chan error) {
	if c.wsRead != nil || c.wsError != nil {
		log.Fatalf("Second call to SetChannels on client %s. Please call ClearChannels before.\n", c)
	}
	c.wsRead = read
	c.wsError = err
}

func (c *client) ClearChannels() {
	c.wsRead = nil
	c.wsError = nil
}

// ExpectData runs, until there are count websocket messages or one websocket error.
// It sends the time since the the start of this function, but not before the websocket
// connection was established. If since is nil, then the function waits until it
// chances to something else. Therefore the sinceSet channel has to be closed.
// If sinceChan != nil, then the time in since is used. In this case the function
// blocks until sinceChan is closed. Make sure to set since before.
// When count messages or one error was received, then it sends a signal
// to the finish channel.
// If expect it different then 0, then it checks, that the received message has the
// same hash as expect and sends an error if not.
func (c *client) ExpectData(sinceTime chan time.Duration, err chan error, count int, finish chan bool, expect uint64, since *time.Time, sinceSet chan bool) {
	var start time.Time
	defer func() { finish <- true }()

	// Wait until the client is connected or the connection has failed
	select {
	case <-c.waitForConnect:
		start = time.Now()

	case <-c.connectionError:
		// If the connection faild, then there is nothing to do here.
		return
	}

	// Sets the channels to receive the data
	readChan := make(chan []byte)
	errChan := make(chan error)
	c.SetChannels(readChan, errChan)
	defer c.ClearChannels()

	for i := 0; i < count; i++ {
		select {
		case data := <-readChan:
			if expect != 0 && expect != hashData(data) {
				err <- fmt.Errorf("Received data has a different hash. Expected: %d, Received: %d", expect, hashData(data))
				return
			}

		case data := <-errChan:
			err <- data
			return
		}
	}
	if sinceSet != nil {
		// The since channel is set. Wait until the channel is closed and then
		// (re-) set the start value
		<-sinceSet
		start = *since
	}
	sinceTime <- time.Since(start)
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

// Login a slice of clients. Uses X connectWorker to work X clients in parallel.
// Expects the clients to be AuthClients.
// Blocks until all clients are logged in.
func loginClients(clients []Client) {
	// Block the function until all clients are logged in
	var wg sync.WaitGroup
	wg.Add(len(clients))
	defer wg.Wait()

	// Start workers
	toWorker := make(chan Client)
	defer close(toWorker)
	for i := 0; i < ParallelLogins; i++ {
		go func() {
			for client := range toWorker {
				err := client.(AuthClient).Login()
				if err != nil {
					log.Fatalf("Can not login client %s", client)
				}
				wg.Done()
			}
		}()
	}

	// Send clients to workers
	for _, client := range clients {
		toWorker <- client
	}
}

// Connects a slice of clients. Uses X connectWorker to work X clients in parallel.
// The return value is set to true, when all clients are connected.
func connectClients(clients []Client, errChan chan error, connected chan time.Duration) *bool {
	var done bool

	go func() {
		// First close the channel (to signal the workers to finish)
		// Then wait for all workers to finish
		// Then set the done variable to true
		defer func() { done = true }()
		var wg sync.WaitGroup
		wg.Add(len(clients))
		defer wg.Wait()
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
					wg.Done()
				}
			}()
		}
		// Send clients to workers
		for _, client := range clients {
			toWorker <- client
		}
	}()
	return &done
}

// Send the write request for a slice of AdminClients.
// The return value is set to true, when all messages where send.
func sendClients(clients []AdminClient, errChan chan error, sended chan time.Duration) *bool {
	var done bool

	go func() {
		// First close the channel (to signal the workers to finish)
		// Then wait for all workers to finish
		// Then set the done variable to true
		defer func() { done = true }()
		var wg sync.WaitGroup
		wg.Add(len(clients))
		defer wg.Wait()
		toWorker := make(chan AdminClient)
		defer close(toWorker)

		// Start workers
		for i := 0; i < ParallelSends; i++ {
			go func() {
				for client := range toWorker {
					start := time.Now()
					err := client.Send()
					if err != nil {
						errChan <- err
					} else {
						sended <- time.Since(start)
					}
				}
				wg.Done()
			}()
		}
		// Send clients to workers
		for _, client := range clients {
			toWorker <- client
		}
	}()
	return &done
}

// Listens to a list of clients. Sends the results
// via the given channels. One for the data (duration since connected) and one for errors.
// Ends the process, when each client got count messages or one errors. When this happens,
// then the returned value is set to true.
// This function does not block.
func listenToClients(clients []Client, data chan time.Duration, err chan error, count int, since *time.Time, sinceSet chan bool) *bool {
	var done bool

	go func() {
		finish := make(chan bool)

		for _, client := range clients {
			// TODO: Expected data
			go client.ExpectData(data, err, count, finish, 0, since, sinceSet)
		}

		// Wait for all clients to send the finish signal
		for i := 0; i < len(clients); i++ {
			<-finish
		}
		done = true
	}()
	return &done
}
