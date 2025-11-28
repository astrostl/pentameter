//go:build ignore

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// Test utility to check if IntelliCenter sends unsolicited push messages.
// This connects to the WebSocket and listens WITHOUT sending any requests.

func main() {
	icIP := flag.String("ic-ip", os.Getenv("PENTAMETER_IC_IP"), "IntelliCenter IP address")
	icPort := flag.String("ic-port", "6680", "IntelliCenter WebSocket port")
	duration := flag.Duration("duration", 120*time.Second, "How long to listen for push messages")
	flag.Parse()

	if *icIP == "" {
		log.Fatal("IntelliCenter IP required: use --ic-ip flag or set PENTAMETER_IC_IP environment variable")
	}

	log.Printf("=== IntelliCenter Push Notification Test ===")
	log.Printf("Target: %s:%s", *icIP, *icPort)
	log.Printf("Duration: %v", *duration)
	log.Printf("Connecting to IntelliCenter...")

	// Connect to WebSocket
	wsURL := fmt.Sprintf("ws://%s", net.JoinHostPort(*icIP, *icPort))
	u, err := url.Parse(wsURL)
	if err != nil {
		log.Fatalf("Failed to parse URL: %v", err)
	}

	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	ctx := context.Background()
	conn, resp, err := dialer.DialContext(ctx, u.String(), nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	log.Printf("✅ Connected successfully!")
	log.Printf("")
	log.Printf("Now listening for %v WITHOUT sending any requests...", *duration)
	log.Printf("If IntelliCenter supports push notifications, we should see messages below.")
	log.Printf("Press Ctrl-C to exit early.")
	log.Printf("")

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Channel for receiving messages
	msgChan := make(chan []byte, 10)
	errChan := make(chan error, 1)

	// Goroutine to read messages
	go func() {
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}
			msgChan <- message
		}
	}()

	// Listen for messages
	messageCount := 0
	startTime := time.Now()
	timeout := time.After(*duration)

	for {
		select {
		case <-sigChan:
			log.Printf("")
			log.Printf("Interrupted by user")
			printSummary(messageCount, time.Since(startTime))
			return

		case <-timeout:
			log.Printf("")
			log.Printf("Test duration completed")
			printSummary(messageCount, *duration)
			return

		case msg := <-msgChan:
			messageCount++
			elapsed := time.Since(startTime)
			log.Printf("📨 [%v] Unsolicited message #%d received (%d bytes):",
				elapsed.Round(time.Millisecond), messageCount, len(msg))
			log.Printf("   %s", string(msg))
			log.Printf("")

		case err := <-errChan:
			log.Printf("")
			log.Printf("Connection error: %v", err)
			printSummary(messageCount, time.Since(startTime))
			return
		}
	}
}

func printSummary(messageCount int, duration time.Duration) {
	log.Printf("")
	log.Printf("=== Test Summary ===")
	log.Printf("Duration: %v", duration.Round(time.Millisecond))
	log.Printf("Unsolicited messages received: %d", messageCount)
	log.Printf("")

	if messageCount == 0 {
		log.Printf("✅ RESULT: IntelliCenter does NOT support push notifications")
		log.Printf("   The API is request/response only - polling is required")
	} else {
		log.Printf("🎉 RESULT: IntelliCenter DOES support push notifications!")
		log.Printf("   Listen mode could be enhanced to use event-driven updates")
	}
}
