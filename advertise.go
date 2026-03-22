package main

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

const (
	pentameterHostname = "pentameter.local."
	mdnsTTL            = 120 // seconds

	// DNS-SD service type enumeration (RFC 6763 §9)
	dnsSDServiceName = "_services._dns-sd._udp.local."

	// Service types we advertise under
	pentameterServiceName = "_pentameter._tcp.local."
	httpServiceName       = "_http._tcp.local."
	promHTTPServiceName   = "_prometheus-http._tcp.local."

	// Instance names for each service type
	pentameterInstanceName = "pentameter._pentameter._tcp.local."
	httpInstanceName       = "pentameter._http._tcp.local."
	promHTTPInstanceName   = "pentameter._prometheus-http._tcp.local."
)

// MDNSAdvertiser responds to mDNS queries for pentameter's service.
type MDNSAdvertiser struct {
	ip       net.IP
	httpPort uint16
	conn     *net.UDPConn
	pconn    *ipv4.PacketConn
	iface    *net.Interface
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

	conn, pconn, err := listenMDNS(iface)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on mDNS port: %w", err)
	}

	adv := &MDNSAdvertiser{
		ip:       ip,
		httpPort: uint16(port),
		conn:     conn,
		pconn:    pconn,
		iface:    iface,
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

// listenMDNS creates a UDP connection listening on the mDNS multicast group
// and returns both the raw conn (for reading) and an ipv4.PacketConn (for
// multicast-aware sending with explicit interface control).
func listenMDNS(iface *net.Interface) (*net.UDPConn, *ipv4.PacketConn, error) {
	mcastAddr, err := net.ResolveUDPAddr("udp4", mdnsAddress)
	if err != nil {
		return nil, nil, err
	}

	conn, err := net.ListenMulticastUDP("udp4", iface, mcastAddr)
	if err != nil {
		return nil, nil, err
	}

	pconn := ipv4.NewPacketConn(conn)

	// Set the outgoing multicast interface so responses go out on the right NIC.
	if iface != nil {
		if err := pconn.SetMulticastInterface(iface); err != nil {
			conn.Close()
			return nil, nil, fmt.Errorf("failed to set multicast interface: %w", err)
		}
	}

	// Enable multicast loopback so local clients (e.g. dns-sd, avahi-browse
	// running on the same host) can see our responses.
	if err := pconn.SetMulticastLoopback(true); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("failed to enable multicast loopback: %w", err)
	}

	return conn, pconn, nil
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

	if verbose {
		for i := range msg.Questions {
			log.Printf("mDNS advertise: RECV query from %s: %s %s (class=0x%04x)",
				remoteAddr, msg.Questions[i].Name.String(), msg.Questions[i].Type, msg.Questions[i].Class)
		}
	}

	mcastDst := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

	for i := range msg.Questions {
		responses := a.buildResponses(&msg.Questions[i])

		// RFC 6762 §5.4: If the QU (unicast-response) bit is set (top bit of
		// question class), respond unicast to the querier. Otherwise, respond
		// to the multicast group so all listeners on the network see the reply.
		unicast := msg.Questions[i].Class&(1<<15) != 0

		for _, response := range responses {
			packed, err := response.Pack()
			if err != nil {
				if verbose {
					log.Printf("mDNS advertise: failed to pack response: %v", err)
				}
				continue
			}

			if unicast {
				if verbose {
					log.Printf("mDNS advertise: SEND unicast response to %s (%d bytes)", remoteAddr, len(packed))
				}
				if _, err := a.conn.WriteTo(packed, remoteAddr); err != nil && verbose {
					log.Printf("mDNS advertise: failed to send unicast response: %v", err)
				}
			} else {
				// Use ipv4.PacketConn for multicast send — this ensures the
				// packet goes out on the correct interface via SetMulticastInterface.
				var cm *ipv4.ControlMessage
				if a.iface != nil {
					cm = &ipv4.ControlMessage{IfIndex: a.iface.Index}
				}
				if verbose {
					log.Printf("mDNS advertise: SEND multicast response to %s (%d bytes, iface=%v)", mcastDst, len(packed), a.iface)
				}
				if _, err := a.pconn.WriteTo(packed, cm, mcastDst); err != nil {
					if verbose {
						log.Printf("mDNS advertise: FAILED to send multicast response: %v", err)
					}
				}
			}
		}
	}
}

// serviceType groups the DNS names for a single mDNS service registration.
type serviceType struct {
	service  string // e.g. "_http._tcp.local."
	instance string // e.g. "pentameter._http._tcp.local."
}

// allServiceTypes returns all service types we advertise.
func allServiceTypes() []serviceType {
	return []serviceType{
		{pentameterServiceName, pentameterInstanceName},
		{httpServiceName, httpInstanceName},
		{promHTTPServiceName, promHTTPInstanceName},
	}
}

// buildResponses creates mDNS responses for a matching query. Returns nil if no match.
func (a *MDNSAdvertiser) buildResponses(question *dnsmessage.Question) []*dnsmessage.Message {
	qName := strings.ToLower(question.Name.String())

	// A record for pentameter.local
	if qName == strings.ToLower(pentameterHostname) && question.Type == dnsmessage.TypeA {
		return []*dnsmessage.Message{a.buildAResponse(question.Name)}
	}

	// DNS-SD service type enumeration (RFC 6763 §9)
	if qName == strings.ToLower(dnsSDServiceName) && question.Type == dnsmessage.TypePTR {
		return a.buildServiceEnumResponses()
	}

	// Check each service type for PTR/SRV/TXT queries
	for _, svc := range allServiceTypes() {
		if qName == strings.ToLower(svc.service) && question.Type == dnsmessage.TypePTR {
			return []*dnsmessage.Message{a.buildPTRResponse(svc.service, svc.instance)}
		}
		if qName == strings.ToLower(svc.instance) && question.Type == dnsmessage.TypeSRV {
			return []*dnsmessage.Message{a.buildSRVResponse(svc.instance)}
		}
		if qName == strings.ToLower(svc.instance) && question.Type == dnsmessage.TypeTXT {
			return []*dnsmessage.Message{a.buildTXTResponse(svc.instance)}
		}
	}

	return nil
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

// buildServiceEnumResponses creates PTR responses for DNS-SD service type enumeration (RFC 6763 §9).
// Returns one response per service type so browsers can discover all advertised services.
func (a *MDNSAdvertiser) buildServiceEnumResponses() []*dnsmessage.Message {
	var responses []*dnsmessage.Message
	for _, svc := range allServiceTypes() {
		responses = append(responses, &dnsmessage.Message{
			Header: dnsmessage.Header{
				Response:      true,
				Authoritative: true,
			},
			Answers: []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{
					Name:  dnsmessage.MustNewName(dnsSDServiceName),
					Type:  dnsmessage.TypePTR,
					Class: dnsmessage.ClassINET,
					TTL:   mdnsTTL,
				},
				Body: &dnsmessage.PTRResource{
					PTR: dnsmessage.MustNewName(svc.service),
				},
			}},
		})
	}
	return responses
}

// buildPTRResponse creates a PTR response pointing to the service instance.
func (a *MDNSAdvertiser) buildPTRResponse(service, instance string) *dnsmessage.Message {
	return &dnsmessage.Message{
		Header: dnsmessage.Header{
			Response:      true,
			Authoritative: true,
		},
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName(service),
				Type:  dnsmessage.TypePTR,
				Class: dnsmessage.ClassINET,
				TTL:   mdnsTTL,
			},
			Body: &dnsmessage.PTRResource{
				PTR: dnsmessage.MustNewName(instance),
			},
		}},
	}
}

// buildSRVResponse creates an SRV response with host and port.
func (a *MDNSAdvertiser) buildSRVResponse(instance string) *dnsmessage.Message {
	var aRecord [4]byte
	copy(aRecord[:], a.ip.To4())

	return &dnsmessage.Message{
		Header: dnsmessage.Header{
			Response:      true,
			Authoritative: true,
		},
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName(instance),
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
func (a *MDNSAdvertiser) buildTXTResponse(instance string) *dnsmessage.Message {
	return &dnsmessage.Message{
		Header: dnsmessage.Header{
			Response:      true,
			Authoritative: true,
		},
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName(instance),
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
