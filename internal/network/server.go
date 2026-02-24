package network

import (
	//\"encoding/hex\"
	"fmt"
	"log"
	"net"
	"time"
)

// StartDNSServer initializes and starts a UDP-based DNS server
func StartDNSServer(port string, node *Node) {
	addr := fmt.Sprintf(":%s", port)
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		log.Fatalf("Failed to resolve UDP address: %v", err)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Printf("Failed to start UDP server on :%s: %v", port, err)
		return
	}
	defer conn.Close()

	// Store the conn so NodesCleanup can close it
	node.dnsConn = conn

	//log.Printf("BDNS Server started on port %s...\n", port)

	buffer := make([]byte, 512)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			// conn was closed by NodesCleanup — exit cleanly.
			return
		}

		query := string(buffer[:n])
		log.Printf("Received query: %s", query)

		currentSlot := (time.Now().Unix() - node.Config.InitialTimestamp) / node.Config.SlotInterval
		slotsPerDay := int64(86400) / node.Config.SlotInterval

		records, err := ResolveDomain(query, "A", node, currentSlot, slotsPerDay)
		var responseStr string
		if err != nil || len(records) == 0 {
			log.Printf("Resolution error: %v", err)
			responseStr = "ERROR: Domain not found"
		} else {
			responseStr = records[0].Value
		}

		_, err = conn.WriteToUDP([]byte(responseStr), clientAddr)
		if err != nil {
			log.Printf("Failed to send response: %v", err)
		}
	}
}
