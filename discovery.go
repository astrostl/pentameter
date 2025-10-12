package main

import (
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	discoveryTimeout = 60 * time.Second
	retryInterval    = 2 * time.Second
	mdnsAddress      = "224.0.0.251:5353"
	readTimeout      = 100 * time.Millisecond
	maxBufSize       = 1500
)

// DiscoverIntelliCenter discovers IntelliCenter via mDNS by looking for Pentair services on _http._tcp.
// Returns the IP address if found, or an error if discovery fails.
// If verbose is true, logs each retry attempt.
func DiscoverIntelliCenter(verbose bool) (string, error) {
	// Setup multicast connection
	mcastAddr, err := net.ResolveUDPAddr("udp4", mdnsAddress)
	if err != nil {
		return "", fmt.Errorf("failed to resolve mDNS address: %w", err)
	}

	// Get the appropriate interface for multicast listening
	iface, err := getBestMulticastInterface(verbose)
	if err != nil && verbose {
		log.Printf("Warning: Could not find best interface, using default: %v", err)
	}

	conn, err := net.ListenMulticastUDP("udp4", iface, mcastAddr)
	if err != nil {
		return "", fmt.Errorf("failed to create multicast UDP listener: %w", err)
	}
	defer conn.Close()

	// Collect responses and find Pentair IntelliCenter IP with retries
	ip, err := collectHostnameResponseWithRetry(conn, mcastAddr, verbose)
	if err != nil {
		return "", err
	}

	return ip, nil
}

// getBestMulticastInterface finds the best network interface for multicast mDNS.
// Prefers non-loopback, up interfaces with multicast support.
func getBestMulticastInterface(verbose bool) (*net.Interface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get network interfaces: %w", err)
	}

	// First pass: look for ideal interface (up, multicast, not loopback, has addresses)
	for _, iface := range interfaces {
		if isIdealMulticastInterface(&iface, verbose) {
			if verbose {
				log.Printf("Using interface for mDNS: %s (%s)", iface.Name, iface.HardwareAddr)
			}
			return &iface, nil
		}
	}

	// Second pass: accept any up interface with multicast
	for _, iface := range interfaces {
		if isUsableMulticastInterface(&iface) {
			if verbose {
				log.Printf("Using fallback interface for mDNS: %s", iface.Name)
			}
			return &iface, nil
		}
	}

	// No suitable interface found - return nil to use default behavior
	return nil, fmt.Errorf("no suitable multicast interface found")
}

// isIdealMulticastInterface checks if interface is ideal for multicast (up, multicast, not loopback, has IPs).
func isIdealMulticastInterface(iface *net.Interface, verbose bool) bool {
	// Must be up and support multicast
	if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
		return false
	}

	// Skip loopback
	if iface.Flags&net.FlagLoopback != 0 {
		return false
	}

	// Check if it has IPv4 addresses
	addrs, err := iface.Addrs()
	if err != nil || len(addrs) == 0 {
		return false
	}

	// Verify at least one IPv4 address exists
	hasIPv4 := false
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil {
			hasIPv4 = true
			if verbose {
				log.Printf("Found interface %s with IPv4: %s", iface.Name, ipNet.IP)
			}
			break
		}
	}

	return hasIPv4
}

// isUsableMulticastInterface checks if interface can be used for multicast (up and multicast-capable).
func isUsableMulticastInterface(iface *net.Interface) bool {
	return iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagMulticast != 0
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

// collectHostnameResponseWithRetry collects mDNS responses for pentair.local hostname with periodic query retries.
func collectHostnameResponseWithRetry(conn *net.UDPConn, mcastAddr *net.UDPAddr, verbose bool) (string, error) {
	deadline := time.Now().Add(discoveryTimeout)
	lastQueryTime := time.Time{} // Force immediate first query
	buffer := make([]byte, maxBufSize)
	queryCount := 0

	for time.Now().Before(deadline) {
		// Send query every retryInterval
		if time.Since(lastQueryTime) >= retryInterval {
			queryCount++
			if verbose {
				log.Printf("Sending mDNS query #%d for pentair.local...", queryCount)
			}
			if err := sendHostnameQuery(conn, mcastAddr, "pentair.local."); err != nil {
				return "", err
			}
			lastQueryTime = time.Now()
		}

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
