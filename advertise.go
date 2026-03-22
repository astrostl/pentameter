package main

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	pentameterHostname = "pentameter.local."
	serviceName        = "_pentameter._tcp.local."
	instanceName       = "pentameter._pentameter._tcp.local."
	mdnsTTL            = 120 // seconds
)

// MDNSAdvertiser responds to mDNS queries for pentameter's service.
type MDNSAdvertiser struct {
	ip       net.IP
	httpPort uint16
	conn     *net.UDPConn
}

// StartMDNSAdvertiser starts an mDNS responder that advertises pentameter on the network.
// It runs in the background and returns the advertiser for cleanup.
func StartMDNSAdvertiser(httpPort string, verbose bool) (*MDNSAdvertiser, error) {
	port, err := strconv.ParseUint(httpPort, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid HTTP port: %w", err)
	}

	iface, err := getBestMulticastInterface(verbose)
	if err != nil && verbose {
		log.Printf("mDNS advertise: could not find best interface, using default: %v", err)
	}

	ip, err := getInterfaceIPv4(iface)
	if err != nil {
		return nil, fmt.Errorf("could not determine local IP for mDNS advertisement: %w", err)
	}

	conn, err := listenMDNS(iface)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on mDNS port: %w", err)
	}

	adv := &MDNSAdvertiser{
		ip:       ip,
		httpPort: uint16(port),
		conn:     conn,
	}

	go adv.listen(verbose)

	if verbose {
		log.Printf("mDNS advertiser started: %s → %s:%d", pentameterHostname, ip, port)
	}

	return adv, nil
}

// Close stops the mDNS advertiser.
func (a *MDNSAdvertiser) Close() error {
	if a.conn != nil {
		return a.conn.Close()
	}
	return nil
}

// listenMDNS creates a UDP connection listening on the mDNS multicast group.
func listenMDNS(iface *net.Interface) (*net.UDPConn, error) {
	mcastAddr, err := net.ResolveUDPAddr("udp4", mdnsAddress)
	if err != nil {
		return nil, err
	}

	conn, err := net.ListenMulticastUDP("udp4", iface, mcastAddr)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// getInterfaceIPv4 returns the first IPv4 address of the given interface.
// If iface is nil, it finds one from any suitable interface.
func getInterfaceIPv4(iface *net.Interface) (net.IP, error) {
	if iface == nil {
		return nil, fmt.Errorf("no interface provided")
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}

	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			if ipv4 := ipNet.IP.To4(); ipv4 != nil {
				return ipv4, nil
			}
		}
	}

	return nil, fmt.Errorf("no IPv4 address found on interface %s", iface.Name)
}

// listen processes incoming mDNS queries and responds to matching ones.
func (a *MDNSAdvertiser) listen(verbose bool) {
	buf := make([]byte, maxBufSize)
	for {
		n, remoteAddr, err := a.conn.ReadFrom(buf)
		if err != nil {
			// Connection closed
			return
		}

		a.handleQuery(buf[:n], remoteAddr, verbose)
	}
}

// handleQuery processes a single mDNS query and sends a response if it matches.
func (a *MDNSAdvertiser) handleQuery(data []byte, remoteAddr net.Addr, verbose bool) {
	var msg dnsmessage.Message
	if err := msg.Unpack(data); err != nil {
		return
	}

	// Only respond to queries (QR=0)
	if msg.Header.Response {
		return
	}

	for i := range msg.Questions {
		response := a.buildResponse(&msg.Questions[i])
		if response == nil {
			continue
		}

		packed, err := response.Pack()
		if err != nil {
			if verbose {
				log.Printf("mDNS advertise: failed to pack response: %v", err)
			}
			continue
		}

		if _, err := a.conn.WriteTo(packed, remoteAddr); err != nil {
			if verbose {
				log.Printf("mDNS advertise: failed to send response: %v", err)
			}
		}
	}
}

// buildResponse creates an mDNS response for a matching query, or nil if no match.
func (a *MDNSAdvertiser) buildResponse(question *dnsmessage.Question) *dnsmessage.Message {
	qName := strings.ToLower(question.Name.String())

	switch {
	case qName == strings.ToLower(pentameterHostname) && question.Type == dnsmessage.TypeA:
		return a.buildAResponse(question.Name)

	case qName == strings.ToLower(serviceName) && question.Type == dnsmessage.TypePTR:
		return a.buildPTRResponse()

	case qName == strings.ToLower(instanceName) && question.Type == dnsmessage.TypeSRV:
		return a.buildSRVResponse()

	case qName == strings.ToLower(instanceName) && question.Type == dnsmessage.TypeTXT:
		return a.buildTXTResponse()

	default:
		return nil
	}
}

// buildAResponse creates a response with the A record for pentameter.local.
func (a *MDNSAdvertiser) buildAResponse(name dnsmessage.Name) *dnsmessage.Message {
	var aRecord [4]byte
	copy(aRecord[:], a.ip.To4())

	return &dnsmessage.Message{
		Header: dnsmessage.Header{
			Response:      true,
			Authoritative: true,
		},
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  name,
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
				TTL:   mdnsTTL,
			},
			Body: &dnsmessage.AResource{A: aRecord},
		}},
	}
}

// buildPTRResponse creates a PTR response pointing to the service instance.
func (a *MDNSAdvertiser) buildPTRResponse() *dnsmessage.Message {
	return &dnsmessage.Message{
		Header: dnsmessage.Header{
			Response:      true,
			Authoritative: true,
		},
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName(serviceName),
				Type:  dnsmessage.TypePTR,
				Class: dnsmessage.ClassINET,
				TTL:   mdnsTTL,
			},
			Body: &dnsmessage.PTRResource{
				PTR: dnsmessage.MustNewName(instanceName),
			},
		}},
	}
}

// buildSRVResponse creates an SRV response with host and port.
func (a *MDNSAdvertiser) buildSRVResponse() *dnsmessage.Message {
	var aRecord [4]byte
	copy(aRecord[:], a.ip.To4())

	return &dnsmessage.Message{
		Header: dnsmessage.Header{
			Response:      true,
			Authoritative: true,
		},
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName(instanceName),
				Type:  dnsmessage.TypeSRV,
				Class: dnsmessage.ClassINET,
				TTL:   mdnsTTL,
			},
			Body: &dnsmessage.SRVResource{
				Priority: 0,
				Weight:   0,
				Port:     a.httpPort,
				Target:   dnsmessage.MustNewName(pentameterHostname),
			},
		}},
		Additionals: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName(pentameterHostname),
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
				TTL:   mdnsTTL,
			},
			Body: &dnsmessage.AResource{A: aRecord},
		}},
	}
}

// buildTXTResponse creates a TXT response with service metadata.
func (a *MDNSAdvertiser) buildTXTResponse() *dnsmessage.Message {
	return &dnsmessage.Message{
		Header: dnsmessage.Header{
			Response:      true,
			Authoritative: true,
		},
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName(instanceName),
				Type:  dnsmessage.TypeTXT,
				Class: dnsmessage.ClassINET,
				TTL:   mdnsTTL,
			},
			Body: &dnsmessage.TXTResource{
				TXT: []string{
					"path=/metrics",
					"version=" + version,
				},
			},
		}},
	}
}
