package main

import (
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func newTestAdvertiser() *MDNSAdvertiser {
	return &MDNSAdvertiser{
		ip:       net.IPv4(192, 168, 1, 100),
		httpPort: 8080,
	}
}

func TestBuildAResponse(t *testing.T) {
	adv := newTestAdvertiser()
	name := dnsmessage.MustNewName(pentameterHostname)
	resp := adv.buildAResponse(name)

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !resp.Header.Response {
		t.Error("expected response flag to be set")
	}
	if !resp.Header.Authoritative {
		t.Error("expected authoritative flag to be set")
	}
	if len(resp.Answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(resp.Answers))
	}

	a, ok := resp.Answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatal("expected AResource body")
	}
	expected := [4]byte{192, 168, 1, 100}
	if a.A != expected {
		t.Errorf("expected IP %v, got %v", expected, a.A)
	}
}

func TestBuildPTRResponse(t *testing.T) {
	adv := newTestAdvertiser()
	resp := adv.buildPTRResponse(pentameterServiceName, pentameterInstanceName)

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(resp.Answers))
	}

	ptr, ok := resp.Answers[0].Body.(*dnsmessage.PTRResource)
	if !ok {
		t.Fatal("expected PTRResource body")
	}
	if ptr.PTR.String() != pentameterInstanceName {
		t.Errorf("expected PTR target %s, got %s", pentameterInstanceName, ptr.PTR.String())
	}
}

func TestBuildSRVResponse(t *testing.T) {
	adv := newTestAdvertiser()
	resp := adv.buildSRVResponse(pentameterInstanceName)

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(resp.Answers))
	}

	srv, ok := resp.Answers[0].Body.(*dnsmessage.SRVResource)
	if !ok {
		t.Fatal("expected SRVResource body")
	}
	if srv.Port != 8080 {
		t.Errorf("expected port 8080, got %d", srv.Port)
	}
	if srv.Target.String() != pentameterHostname {
		t.Errorf("expected target %s, got %s", pentameterHostname, srv.Target.String())
	}

	// Should include A record in additionals
	if len(resp.Additionals) != 1 {
		t.Fatalf("expected 1 additional record, got %d", len(resp.Additionals))
	}
}

func TestBuildTXTResponse(t *testing.T) {
	adv := newTestAdvertiser()
	resp := adv.buildTXTResponse(pentameterInstanceName)

	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Answers) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(resp.Answers))
	}

	txt, ok := resp.Answers[0].Body.(*dnsmessage.TXTResource)
	if !ok {
		t.Fatal("expected TXTResource body")
	}
	if len(txt.TXT) < 2 {
		t.Fatalf("expected at least 2 TXT entries, got %d", len(txt.TXT))
	}
	if txt.TXT[0] != "path=/metrics" {
		t.Errorf("expected first TXT entry 'path=/metrics', got %q", txt.TXT[0])
	}
}

func TestBuildServiceEnumResponses(t *testing.T) {
	adv := newTestAdvertiser()
	responses := adv.buildServiceEnumResponses()

	services := allServiceTypes()
	if len(responses) != len(services) {
		t.Fatalf("expected %d responses, got %d", len(services), len(responses))
	}

	for i, resp := range responses {
		if !resp.Header.Response {
			t.Errorf("response %d: expected response flag", i)
		}
		if !resp.Header.Authoritative {
			t.Errorf("response %d: expected authoritative flag", i)
		}
		if len(resp.Answers) != 1 {
			t.Errorf("response %d: expected 1 answer, got %d", i, len(resp.Answers))
			continue
		}
		ptr, ok := resp.Answers[0].Body.(*dnsmessage.PTRResource)
		if !ok {
			t.Errorf("response %d: expected PTRResource body", i)
			continue
		}
		if ptr.PTR.String() != services[i].service {
			t.Errorf("response %d: expected PTR target %s, got %s", i, services[i].service, ptr.PTR.String())
		}
	}
}

func TestBuildResponsesMatching(t *testing.T) {
	adv := newTestAdvertiser()

	tests := []struct {
		name      string
		qName     string
		qType     dnsmessage.Type
		wantNil   bool
		wantCount int
		wantType  dnsmessage.Type
	}{
		{
			name:      "A query for pentameter.local",
			qName:     pentameterHostname,
			qType:     dnsmessage.TypeA,
			wantCount: 1,
			wantType:  dnsmessage.TypeA,
		},
		{
			name:      "DNS-SD service enumeration",
			qName:     dnsSDServiceName,
			qType:     dnsmessage.TypePTR,
			wantCount: 3, // pentameter, http, prometheus-http
			wantType:  dnsmessage.TypePTR,
		},
		{
			name:      "PTR query for pentameter service",
			qName:     pentameterServiceName,
			qType:     dnsmessage.TypePTR,
			wantCount: 1,
			wantType:  dnsmessage.TypePTR,
		},
		{
			name:      "PTR query for http service",
			qName:     httpServiceName,
			qType:     dnsmessage.TypePTR,
			wantCount: 1,
			wantType:  dnsmessage.TypePTR,
		},
		{
			name:      "PTR query for prometheus-http service",
			qName:     promHTTPServiceName,
			qType:     dnsmessage.TypePTR,
			wantCount: 1,
			wantType:  dnsmessage.TypePTR,
		},
		{
			name:      "SRV query for pentameter instance",
			qName:     pentameterInstanceName,
			qType:     dnsmessage.TypeSRV,
			wantCount: 1,
			wantType:  dnsmessage.TypeSRV,
		},
		{
			name:      "SRV query for http instance",
			qName:     httpInstanceName,
			qType:     dnsmessage.TypeSRV,
			wantCount: 1,
			wantType:  dnsmessage.TypeSRV,
		},
		{
			name:      "SRV query for prometheus-http instance",
			qName:     promHTTPInstanceName,
			qType:     dnsmessage.TypeSRV,
			wantCount: 1,
			wantType:  dnsmessage.TypeSRV,
		},
		{
			name:      "TXT query for pentameter instance",
			qName:     pentameterInstanceName,
			qType:     dnsmessage.TypeTXT,
			wantCount: 1,
			wantType:  dnsmessage.TypeTXT,
		},
		{
			name:      "TXT query for http instance",
			qName:     httpInstanceName,
			qType:     dnsmessage.TypeTXT,
			wantCount: 1,
			wantType:  dnsmessage.TypeTXT,
		},
		{
			name:    "unrelated A query",
			qName:   "other.local.",
			qType:   dnsmessage.TypeA,
			wantNil: true,
		},
		{
			name:    "wrong type for pentameter.local",
			qName:   pentameterHostname,
			qType:   dnsmessage.TypeAAAA,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &dnsmessage.Question{
				Name:  dnsmessage.MustNewName(tt.qName),
				Type:  tt.qType,
				Class: dnsmessage.ClassINET,
			}
			responses := adv.buildResponses(q)
			if tt.wantNil && responses != nil {
				t.Error("expected nil responses")
			}
			if !tt.wantNil {
				if responses == nil {
					t.Fatal("expected non-nil responses")
				}
				if len(responses) != tt.wantCount {
					t.Fatalf("expected %d responses, got %d", tt.wantCount, len(responses))
				}
				if responses[0].Answers[0].Header.Type != tt.wantType {
					t.Errorf("expected answer type %v, got %v", tt.wantType, responses[0].Answers[0].Header.Type)
				}
			}
		})
	}
}

func TestHandleQueryIgnoresResponses(t *testing.T) {
	adv := newTestAdvertiser()

	// Build a message that is a response (QR=1), not a query
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{Response: true},
		Questions: []dnsmessage.Question{{
			Name:  dnsmessage.MustNewName(pentameterHostname),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("failed to pack message: %v", err)
	}

	// Should not panic or send anything (no conn set)
	adv.handleQuery(packed, nil, false)
}

func TestGetInterfaceIPv4NilInterface(t *testing.T) {
	_, err := getInterfaceIPv4(nil)
	if err == nil {
		t.Error("expected error for nil interface")
	}
}

func TestResponsesPackSuccessfully(t *testing.T) {
	adv := newTestAdvertiser()

	name := dnsmessage.MustNewName(pentameterHostname)
	responses := []*dnsmessage.Message{
		adv.buildAResponse(name),
		adv.buildPTRResponse(pentameterServiceName, pentameterInstanceName),
		adv.buildPTRResponse(httpServiceName, httpInstanceName),
		adv.buildPTRResponse(promHTTPServiceName, promHTTPInstanceName),
		adv.buildSRVResponse(pentameterInstanceName),
		adv.buildSRVResponse(httpInstanceName),
		adv.buildSRVResponse(promHTTPInstanceName),
		adv.buildTXTResponse(pentameterInstanceName),
		adv.buildTXTResponse(httpInstanceName),
		adv.buildTXTResponse(promHTTPInstanceName),
	}

	// Also include service enum responses
	responses = append(responses, adv.buildServiceEnumResponses()...)

	for i, resp := range responses {
		if resp == nil {
			t.Errorf("response %d is nil", i)
			continue
		}
		if _, err := resp.Pack(); err != nil {
			t.Errorf("response %d failed to pack: %v", i, err)
		}
	}
}

func TestAllServiceTypes(t *testing.T) {
	services := allServiceTypes()
	if len(services) != 3 {
		t.Fatalf("expected 3 service types, got %d", len(services))
	}

	// Verify each service type has both fields set
	for i, svc := range services {
		if svc.service == "" {
			t.Errorf("service type %d has empty service name", i)
		}
		if svc.instance == "" {
			t.Errorf("service type %d has empty instance name", i)
		}
	}
}
