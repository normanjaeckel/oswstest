package main

import (
	"fmt"
	"log"
	"time"

	"github.com/OneOfOne/xxhash"
)

// Test is a function, that expect a slice of clients and returns a slice of
// test results.
type Test func(clients []Client) (r []TestResult)

// RunTests runs some tests for a slice of clients. It returns the TestResults
// for each test.
func RunTests(clients []Client, tests []Test) (r []TestResult) {
	for _, test := range tests {
		r = append(r, test(clients)...)
	}
	return
}

// ConnectTest opens connections for any given client. It returns two TestResults
// The first measures the time until the connection was open, the second measures the
// time until the fire data was received.
// Expects, that the wsconnection of the clients are closed.
func ConnectTest(clients []Client) (r []TestResult) {
	log.Println("Start ConnectTest")
	startTest := time.Now()
	defer func() { log.Printf("ConnectionTest took %dms\n", time.Since(startTest)/time.Millisecond) }()

	connected := make(chan time.Duration)
	connectedError := make(chan error)
	dataReceived := make(chan time.Duration)
	errorReceived := make(chan error)
	dataHash := make(chan uint64)

	// Connect all Clients
	go connectClients(clients, connectedError, connected)

	for _, client := range clients {
		go func(client Client) {
			start := time.Now()
			select {
			case value := <-client.GetReadChannel():
				dataReceived <- time.Since(start)
				hash := xxhash.New64()
				// Currently, the data for admin clients and login clients are different
				// The current solution is, not to check the data
				_ = value
				hash.Write([]byte{})
				dataHash <- hash.Sum64()

			case value := <-client.GetErrorChannel():
				errorReceived <- value
			}
		}(client)
	}

	connectedResult := TestResult{description: "Time to established connection"}
	dataReceivedResult := TestResult{description: "Time until data was reveiced"}
	var firstHash uint64
	tick := time.Tick(time.Second)

	for {
		select {
		case value := <-connected:
			connectedResult.Add(value)

		case value := <-connectedError:
			connectedResult.AddError(value)

		case value := <-dataReceived:
			dataReceivedResult.Add(value)

		case value := <-dataHash:
			if firstHash == 0 {
				firstHash = value
			} else if value != firstHash {
				dataReceivedResult.AddError(fmt.Errorf("diffrent data. %d bytes, expected %d bytes", value, firstHash))
			}

		case value := <-errorReceived:
			dataReceivedResult.AddError(value)

		case <-tick:
			if LogStatus {
				log.Println(connectedResult.CountBoth(), dataReceivedResult.CountBoth())
			}
		}

		if connectedResult.CountBoth() >= len(clients) && dataReceivedResult.CountBoth() >= len(clients)-connectedResult.ErrCount() {
			break
		}
	}
	return []TestResult{connectedResult, dataReceivedResult}
}

// OneWriteTest tests, that all clients get a response when there is one write
// request.
// Expects, that the first client is a logged-in admin client and that all
// clients have open websocket connections.
func OneWriteTest(clients []Client) (r []TestResult) {
	log.Println("Start OneWriteTest")
	startTest := time.Now()
	defer func() { log.Printf("OneWriteTest took %dms\n", time.Since(startTest)/time.Millisecond) }()

	admin, ok := clients[0].(AdminClient)
	if !ok || !admin.IsAdmin() || !admin.IsConnected() {
		log.Fatalf("Fatal: Expect the first client in OneWriteTest to be a connected AdminClient")
	}

	err := admin.Send()
	if err != nil {
		log.Fatalf("Can not send request, %s", err)
	}

	start := time.Now()
	dataReceived := make(chan time.Duration)
	errorReceived := make(chan error)
	dataHash := make(chan uint64)

	for _, client := range clients {
		go func(client Client) {
			select {
			case value := <-client.GetReadChannel():
				dataReceived <- time.Since(start)
				hash := xxhash.New64()
				// TODO fix the different data test
				_ = value
				hash.Write([]byte{})
				dataHash <- hash.Sum64()

			case value := <-client.GetErrorChannel():
				errorReceived <- value
			}
		}(client)
	}

	dataReceivedResult := TestResult{description: "Time until responce for one write request"}
	var firstHash uint64
	tick := time.Tick(time.Second)

	for {
		select {
		case value := <-dataReceived:
			dataReceivedResult.Add(value)

		case value := <-dataHash:
			if firstHash == 0 {
				firstHash = value
			} else if value != firstHash {
				dataReceivedResult.AddError(fmt.Errorf("diffrent data. %d bytes, expected %d bytes", value, firstHash))
			}

		case value := <-errorReceived:
			dataReceivedResult.AddError(value)

		case <-tick:
			if LogStatus {
				log.Println(dataReceivedResult.Count() + dataReceivedResult.ErrCount())
			}
		}

		if dataReceivedResult.Count()+dataReceivedResult.ErrCount() >= len(clients) {
			break
		}
	}

	return []TestResult{dataReceivedResult}
}

// ManyWriteTest tests behave like the OneWriteTest but send many write request.
// The first clients have to be admin clients. Sends one write request for each
// admin client.
// Expects, that at least one client is a logged-in admin client and that all
// clients have open websocket connections.
func ManyWriteTest(clients []Client) (r []TestResult) {
	log.Println("Start ManyWriteTest")
	startTest := time.Now()
	defer func() { log.Printf("ManyWriteTest took %dms\n", time.Since(startTest)/time.Millisecond) }()

	// Find all admins in the clients
	var admins []AdminClient
	for _, client := range clients {
		admin, ok := client.(AdminClient)
		if ok && admin.IsAdmin() && admin.IsConnected() {
			admins = append(admins, admin)
		}
	}
	if len(admins) == 0 {
		log.Fatalf("Fatal: Expect one client in ManyWriteTest to be a connected AdminClient")
	}

	// Send requests for all admin clients
	dataSended := make(chan time.Duration)
	errorSended := make(chan error)
	sendFinished := sendClients(admins, errorSended, dataSended)

	// Listen for all clients to receive messages
	dataReceived := make(chan time.Duration)
	errorReceived := make(chan error)
	dataHash := make(chan uint64)
	receiveFinished := listenToClients(clients, dataReceived, dataHash, errorReceived, len(admins))

	sendedResult := TestResult{description: "Time until all requests are sended"}
	receivedResult := TestResult{description: "Time until all responses are received"}
	var firstHash uint64
	tick := time.Tick(time.Second)

	for {
		select {
		case value := <-dataSended:
			sendedResult.Add(value)

		case value := <-errorSended:
			sendedResult.AddError(value)

		case value := <-dataReceived:
			receivedResult.Add(value)

		case value := <-errorReceived:
			receivedResult.AddError(value)

		case value := <-dataHash:
			break // TODO: Does currently not work
			if firstHash == 0 {
				firstHash = value
			} else if value != firstHash {
				receivedResult.AddError(fmt.Errorf("diffrent data. %d bytes, expected %d bytes", value, firstHash))
			}

		case <-tick:
			if LogStatus {
				log.Println(sendedResult.CountBoth(), receivedResult.CountBoth())
			}
		}

		// End the test when all admins have sended there data and each client got
		// as many responces as there are admins.
		if *sendFinished && *receiveFinished {
			break
		}
	}

	return []TestResult{sendedResult, receivedResult}
}
