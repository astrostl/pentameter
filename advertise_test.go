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
	resp := adv.buildPTRResponse()

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
	if ptr.PTR.String() != instanceName {
		t.Errorf("expected PTR target %s, got %s", instanceName, ptr.PTR.String())
	}
}

func TestBuildSRVResponse(t *testing.T) {
	adv := newTestAdvertiser()
	resp := adv.buildSRVResponse()

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
	resp := adv.buildTXTResponse()

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

func TestBuildResponseMatching(t *testing.T) {
	adv := newTestAdvertiser()

	tests := []struct {
		name     string
		qName    string
		qType    dnsmessage.Type
		wantNil  bool
		wantType dnsmessage.Type
	}{
		{
			name:     "A query for pentameter.local",
			qName:    pentameterHostname,
			qType:    dnsmessage.TypeA,
			wantNil:  false,
			wantType: dnsmessage.TypeA,
		},
		{
			name:     "PTR query for service",
			qName:    serviceName,
			qType:    dnsmessage.TypePTR,
			wantNil:  false,
			wantType: dnsmessage.TypePTR,
		},
		{
			name:     "SRV query for instance",
			qName:    instanceName,
			qType:    dnsmessage.TypeSRV,
			wantNil:  false,
			wantType: dnsmessage.TypeSRV,
		},
		{
			name:     "TXT query for instance",
			qName:    instanceName,
			qType:    dnsmessage.TypeTXT,
			wantNil:  false,
			wantType: dnsmessage.TypeTXT,
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
			resp := adv.buildResponse(q)
			if tt.wantNil && resp != nil {
				t.Error("expected nil response")
			}
			if !tt.wantNil && resp == nil {
				t.Error("expected non-nil response")
			}
			if !tt.wantNil && resp != nil && resp.Answers[0].Header.Type != tt.wantType {
				t.Errorf("expected answer type %v, got %v", tt.wantType, resp.Answers[0].Header.Type)
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
		adv.buildPTRResponse(),
		adv.buildSRVResponse(),
		adv.buildTXTResponse(),
	}

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
