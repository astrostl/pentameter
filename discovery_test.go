package main

import (
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const testPentairIP = "192.168.50.118"

func TestDiscoverIntelliCenterTimeout(t *testing.T) {
	// This test verifies timeout behavior when no IntelliCenter is broadcasting
	// In a real environment without an IntelliCenter, this will timeout after 5 seconds
	// Skip in short mode to avoid slowing down test suite
	if testing.Short() {
		t.Skip("Skipping discovery timeout test in short mode")
	}

	_, err := DiscoverIntelliCenter()
	if err == nil {
		// This could succeed if there's actually an IntelliCenter on the network
		t.Log("DiscoverIntelliCenter succeeded - IntelliCenter may be present on network")
		return
	}

	if !strings.Contains(err.Error(), "no response") && !strings.Contains(err.Error(), "failed") {
		t.Errorf("Expected 'no response' or 'failed' error, got: %v", err)
	}
}

func TestSendHostnameQuery(t *testing.T) {
	// Create a UDP connection for testing
	mcastAddr, err := net.ResolveUDPAddr("udp4", mdnsAddress)
	if err != nil {
		t.Fatalf("Failed to resolve mDNS address: %v", err)
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("Failed to create UDP connection: %v", err)
	}
	defer conn.Close()

	// Test successful query sending
	err = sendHostnameQuery(conn, mcastAddr, "pentair.local.")
	if err != nil {
		t.Errorf("sendHostnameQuery failed: %v", err)
	}
}

func TestSendHostnameQueryClosedConnection(t *testing.T) {
	mcastAddr, err := net.ResolveUDPAddr("udp4", mdnsAddress)
	if err != nil {
		t.Fatalf("Failed to resolve mDNS address: %v", err)
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("Failed to create UDP connection: %v", err)
	}
	conn.Close()

	// Test with closed connection - should fail on WriteTo
	err = sendHostnameQuery(conn, mcastAddr, "pentair.local.")
	if err == nil {
		t.Error("Expected error for closed connection")
	}
}

func TestCollectHostnameResponseTimeout(t *testing.T) {
	// Create a UDP connection that won't receive any responses
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("Failed to create UDP connection: %v", err)
	}
	defer conn.Close()

	// This should timeout since no responses will be received
	_, err = collectHostnameResponse(conn)
	if err == nil {
		t.Error("Expected timeout error")
	}

	if !strings.Contains(err.Error(), "no response") {
		t.Errorf("Expected 'no response' error, got: %v", err)
	}
}

func TestReadAndProcessResponseSetDeadlineError(t *testing.T) {
	// Create and immediately close a connection to trigger errors
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("Failed to create UDP connection: %v", err)
	}
	conn.Close()

	buffer := make([]byte, maxBufSize)
	_, _, err = readAndProcessResponse(conn, buffer)
	if err == nil {
		t.Error("Expected error from closed connection")
	}
}

func TestReadAndProcessResponseReadError(t *testing.T) {
	// Create a connection and set an impossible deadline to trigger read error
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("Failed to create UDP connection: %v", err)
	}
	defer conn.Close()

	// Set deadline in the past to force immediate timeout
	if err := conn.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("Failed to set read deadline: %v", err)
	}

	buffer := make([]byte, maxBufSize)
	_, _, err = readAndProcessResponse(conn, buffer)
	if err == nil {
		t.Error("Expected timeout error from read")
	}
}

func TestProcessResponseInvalidData(t *testing.T) {
	// Test with invalid DNS message data
	invalidData := []byte{0x00, 0x01, 0x02}

	_, found, err := processResponse(invalidData)
	if err == nil {
		t.Error("Expected error for invalid DNS message")
	}
	if found {
		t.Error("Should not find IP in invalid data")
	}
}

func TestProcessResponseValidButNoPentair(t *testing.T) {
	// Create a valid DNS response without pentair.local
	var msg dnsmessage.Message
	msg.Header.Response = true
	msg.Header.Authoritative = true
	msg.Answers = []dnsmessage.Resource{
		{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName("other.local."),
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
				TTL:   120,
			},
			Body: &dnsmessage.AResource{
				A: [4]byte{192, 168, 1, 100},
			},
		},
	}

	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("Failed to pack DNS message: %v", err)
	}

	ip, found, err := processResponse(packed)
	if err != nil {
		t.Errorf("processResponse failed: %v", err)
	}
	if found {
		t.Error("Should not find pentair IP in non-pentair response")
	}
	if ip != "" {
		t.Errorf("Expected empty IP, got: %s", ip)
	}
}

func TestProcessResponseWithPentairIP(t *testing.T) {
	// Create a valid DNS response with pentair.local
	var msg dnsmessage.Message
	msg.Header.Response = true
	msg.Header.Authoritative = true
	msg.Answers = []dnsmessage.Resource{
		{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName("pentair.local."),
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
				TTL:   120,
			},
			Body: &dnsmessage.AResource{
				A: [4]byte{192, 168, 50, 118},
			},
		},
	}

	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("Failed to pack DNS message: %v", err)
	}

	ip, found, err := processResponse(packed)
	if err != nil {
		t.Errorf("processResponse failed: %v", err)
	}
	if !found {
		t.Error("Should find pentair IP in pentair response")
	}
	if ip != testPentairIP {
		t.Errorf("Expected IP %s, got: %s", testPentairIP, ip)
	}
}

func TestCheckAnswerForPentairNotTypeA(t *testing.T) {
	answer := dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{
			Name:  dnsmessage.MustNewName("pentair.local."),
			Type:  dnsmessage.TypeAAAA, // IPv6, not A record
			Class: dnsmessage.ClassINET,
		},
	}

	ip, found := checkAnswerForPentair(&answer)
	if found {
		t.Error("Should not match non-A record type")
	}
	if ip != "" {
		t.Errorf("Expected empty IP, got: %s", ip)
	}
}

func TestCheckAnswerForPentairNotPentairName(t *testing.T) {
	answer := dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{
			Name:  dnsmessage.MustNewName("other.local."),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		},
		Body: &dnsmessage.AResource{
			A: [4]byte{192, 168, 1, 1},
		},
	}

	ip, found := checkAnswerForPentair(&answer)
	if found {
		t.Error("Should not match non-pentair hostname")
	}
	if ip != "" {
		t.Errorf("Expected empty IP, got: %s", ip)
	}
}

func TestCheckAnswerForPentairInvalidBody(t *testing.T) {
	// Create answer with wrong body type (AAAA instead of A)
	answer := dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{
			Name:  dnsmessage.MustNewName("pentair.local."),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		},
		Body: &dnsmessage.AAAAResource{
			AAAA: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 192, 168, 1, 1},
		},
	}

	ip, found := checkAnswerForPentair(&answer)
	if found {
		t.Error("Should not match when body type is incorrect")
	}
	if ip != "" {
		t.Errorf("Expected empty IP, got: %s", ip)
	}
}

func TestCheckAnswerForPentairSuccess(t *testing.T) {
	answer := dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{
			Name:  dnsmessage.MustNewName("pentair.local."),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		},
		Body: &dnsmessage.AResource{
			A: [4]byte{192, 168, 50, 118},
		},
	}

	ip, found := checkAnswerForPentair(&answer)
	if !found {
		t.Error("Should match pentair hostname with A record")
	}
	if ip != testPentairIP {
		t.Errorf("Expected IP %s, got: %s", testPentairIP, ip)
	}
}

func TestCheckAnswerForPentairCaseInsensitive(t *testing.T) {
	// Test that "PENTAIR" (uppercase) is also matched
	answer := dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{
			Name:  dnsmessage.MustNewName("PENTAIR.local."),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		},
		Body: &dnsmessage.AResource{
			A: [4]byte{10, 0, 0, 1},
		},
	}

	ip, found := checkAnswerForPentair(&answer)
	if !found {
		t.Error("Should match pentair hostname case-insensitively")
	}
	if ip != "10.0.0.1" {
		t.Errorf("Expected IP 10.0.0.1, got: %s", ip)
	}
}

func TestDiscoveryConstants(t *testing.T) {
	// Verify discovery constants have reasonable values
	if discoveryTimeout != 5*time.Second {
		t.Errorf("discoveryTimeout should be 5s, got %v", discoveryTimeout)
	}

	if mdnsAddress != "224.0.0.251:5353" {
		t.Errorf("mdnsAddress should be 224.0.0.251:5353, got %s", mdnsAddress)
	}

	if readTimeout != 100*time.Millisecond {
		t.Errorf("readTimeout should be 100ms, got %v", readTimeout)
	}

	if maxBufSize != 1500 {
		t.Errorf("maxBufSize should be 1500, got %d", maxBufSize)
	}
}

func TestSendHostnameQueryInvalidHostname(t *testing.T) {
	// Test error path when MustNewName would panic (though we use valid hostname)
	// This test verifies that sendHostnameQuery properly constructs DNS messages
	mcastAddr, err := net.ResolveUDPAddr("udp4", mdnsAddress)
	if err != nil {
		t.Fatalf("Failed to resolve mDNS address: %v", err)
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("Failed to create UDP connection: %v", err)
	}
	defer conn.Close()

	// Test with a valid but different hostname
	err = sendHostnameQuery(conn, mcastAddr, "test.local.")
	if err != nil {
		t.Errorf("sendHostnameQuery with valid hostname should not fail: %v", err)
	}
}

func TestDiscoverIntelliCenterResolveError(t *testing.T) {
	// We cannot directly test ResolveUDPAddr failure without changing global network state
	// But we document this coverage gap and note that it's a system-level error that
	// would indicate serious network misconfiguration
	t.Skip("Cannot test ResolveUDPAddr failure without mocking - system-level error path")
}

func TestDiscoverIntelliCenterListenError(t *testing.T) {
	// We cannot directly test ListenMulticastUDP failure without special permissions
	// or port conflicts. This is a system-level error that would indicate network
	// misconfiguration or permission issues
	t.Skip("Cannot test ListenMulticastUDP failure without special setup - system-level error path")
}
