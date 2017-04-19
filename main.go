package main

import (
	"fmt"
	"log"
	"sync"
)

func main() {
	var clients []Client

	// Create admin clients
	for i := 0; i < AdminClients; i++ {
		client := NewAdminClient(fmt.Sprintf("admin%d", i))
		clients = append(clients, client)
	}

	// Create user clients
	for i := 0; i < NormalClients; i++ {
		client := NewUserClient(fmt.Sprintf("user%d", i))
		clients = append(clients, client)
	}

	fmt.Printf("Use %d clients\n", len(clients))

	// Login all clients
	var wg sync.WaitGroup
	wg.Add(len(clients))
	for _, client := range clients {
		go func(client AuthClient) {
			defer wg.Done()
			err := client.Login()
			if err != nil {
				log.Fatalf("Can not login client `%s`: %s", client, err)
			}
		}(client.(AuthClient))
	}
	// Wait until all clients are logged in
	wg.Wait()
	log.Println("All Clients have logged in.")

	// Run all tests and print the results
	for _, result := range RunTests(clients, Tests) {
		fmt.Println(result.String())
	}
}
