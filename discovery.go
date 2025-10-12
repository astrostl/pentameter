package main

import (
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	discoveryTimeout = 60 * time.Second
	mdnsAddress      = "224.0.0.251:5353"
	readTimeout      = 100 * time.Millisecond
	maxBufSize       = 1500
)

// DiscoverIntelliCenter discovers IntelliCenter via mDNS by looking for Pentair services on _http._tcp.
// Returns the IP address if found, or an error if discovery fails.
func DiscoverIntelliCenter() (string, error) {
	// Setup multicast connection
	mcastAddr, err := net.ResolveUDPAddr("udp4", mdnsAddress)
	if err != nil {
		return "", fmt.Errorf("failed to resolve mDNS address: %w", err)
	}

	conn, err := net.ListenMulticastUDP("udp4", nil, mcastAddr)
	if err != nil {
		return "", fmt.Errorf("failed to create multicast UDP listener: %w", err)
	}
	defer conn.Close()

	// Send mDNS query for pentair.local hostname
	if err := sendHostnameQuery(conn, mcastAddr, "pentair.local."); err != nil {
		return "", err
	}

	// Collect responses and find Pentair IntelliCenter IP
	ip, err := collectHostnameResponse(conn)
	if err != nil {
		return "", err
	}

	return ip, nil
}

// sendHostnameQuery sends an mDNS query for a specific hostname.
func sendHostnameQuery(conn *net.UDPConn, mcastAddr *net.UDPAddr, hostname string) error {
	var msg dnsmessage.Message
	msg.Header.ID = 0
	msg.Header.RecursionDesired = false
	msg.Questions = []dnsmessage.Question{
		{
			Name:  dnsmessage.MustNewName(hostname),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		},
	}

	packed, err := msg.Pack()
	if err != nil {
		return fmt.Errorf("failed to pack DNS message: %w", err)
	}

	_, err = conn.WriteTo(packed, mcastAddr)
	if err != nil {
		return fmt.Errorf("failed to send mDNS query: %w", err)
	}

	return nil
}

// collectHostnameResponse collects mDNS responses for pentair.local hostname.
func collectHostnameResponse(conn *net.UDPConn) (string, error) {
	deadline := time.Now().Add(discoveryTimeout)
	buffer := make([]byte, maxBufSize)

	for time.Now().Before(deadline) {
		ip, found, err := readAndProcessResponse(conn, buffer)
		if err != nil {
			continue // Continue trying on errors
		}
		if found {
			return ip, nil
		}
	}

	return "", fmt.Errorf("IntelliCenter not found on network after %v. Ensure IntelliCenter is powered on and connected to the same network", discoveryTimeout)
}

// readAndProcessResponse reads one mDNS response and checks for pentair IP.
//
//nolint:nonamedreturns // Multiple return values benefit from named returns for clarity
func readAndProcessResponse(conn *net.UDPConn, buffer []byte) (ip string, found bool, err error) {
	if err = conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return "", false, fmt.Errorf("failed to set read deadline: %w", err)
	}

	bytesRead, _, err := conn.ReadFrom(buffer)
	if err != nil {
		return "", false, fmt.Errorf("failed to read from connection: %w", err)
	}

	return processResponse(buffer[:bytesRead])
}

// processResponse unpacks and processes a DNS message looking for pentair IP.
//
//nolint:nonamedreturns // Multiple return values benefit from named returns for clarity
func processResponse(data []byte) (ip string, found bool, err error) {
	var response dnsmessage.Message
	if err = response.Unpack(data); err != nil {
		return "", false, fmt.Errorf("failed to unpack DNS message: %w", err)
	}

	// Check A records in answers for pentair.local
	for i := range response.Answers {
		if foundIP, foundAnswer := checkAnswerForPentair(&response.Answers[i]); foundAnswer {
			return foundIP, true, nil
		}
	}

	return "", false, nil
}

// checkAnswerForPentair checks if a DNS answer contains pentair IP address.
func checkAnswerForPentair(answer *dnsmessage.Resource) (string, bool) {
	if answer.Header.Type != dnsmessage.TypeA {
		return "", false
	}

	if !strings.Contains(strings.ToLower(answer.Header.Name.String()), "pentair") {
		return "", false
	}

	a, ok := answer.Body.(*dnsmessage.AResource)
	if !ok {
		return "", false
	}

	ip := net.IP(a.A[:])
	return ip.String(), true
}
