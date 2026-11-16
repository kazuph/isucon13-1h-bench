package main

import (
	"golang.org/x/net/dns/dnsmessage"
	"strings"
	"testing"
)

func TestDNSAnswers(t *testing.T) {
	zone, _ := dnsmessage.NewName("u.isucon.dev.")
	host, _ := dnsmessage.NewName("test.u.isucon.dev.")
	ns, _ := dnsmessage.NewName("ns1.u.isucon.dev.")
	longName := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + ".u.isucon.dev."
	long, _ := dnsmessage.NewName(longName)
	a := func(name dnsmessage.Name) dnsmessage.Resource {
		return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: [4]byte{10, 99, 0, 11}}}
	}
	dnsRecords.names = map[string][]dnsmessage.Resource{
		host.String(): {a(host)}, longName: {a(long)},
		zone.String(): {{Header: dnsmessage.ResourceHeader{Name: zone, Type: dnsmessage.TypeSOA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.SOAResource{NS: ns, MBox: ns, MinTTL: 60}}},
	}
	for _, tc := range []struct {
		name    string
		typ     dnsmessage.Type
		code    dnsmessage.RCode
		answers int
	}{
		{host.String(), dnsmessage.TypeA, dnsmessage.RCodeSuccess, 1},
		{"TEST.u.isucon.dev.", dnsmessage.TypeA, dnsmessage.RCodeSuccess, 1},
		{longName, dnsmessage.TypeA, dnsmessage.RCodeSuccess, 1},
		{host.String(), dnsmessage.TypeAAAA, dnsmessage.RCodeSuccess, 0},
		{"missing.u.isucon.dev.", dnsmessage.TypeA, dnsmessage.RCodeNameError, 0},
		{zone.String(), dnsmessage.TypeSOA, dnsmessage.RCodeSuccess, 1},
		{"example.com.", dnsmessage.TypeA, dnsmessage.RCodeRefused, 0},
	} {
		t.Run(tc.name+tc.typ.String(), func(t *testing.T) {
			name, _ := dnsmessage.NewName(tc.name)
			query, err := (&dnsmessage.Message{Header: dnsmessage.Header{ID: 123, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: tc.typ, Class: dnsmessage.ClassINET}}}).Pack()
			if err != nil {
				t.Fatal(err)
			}
			packet, err := answerDNS(query, make([]byte, 0, 4096))
			if err != nil {
				t.Fatal(err)
			}
			var response dnsmessage.Message
			if err := response.Unpack(packet); err != nil {
				t.Fatal(err)
			}
			if response.ID != 123 || !response.Response || response.RCode != tc.code || len(response.Answers) != tc.answers {
				t.Fatalf("unexpected response: %+v", response)
			}
			if len(packet) > 512 {
				t.Fatalf("UDP response too large: %d", len(packet))
			}
			if tc.code == dnsmessage.RCodeNameError && len(response.Authorities) != 1 {
				t.Fatal("missing negative SOA")
			}
			if tc.typ == dnsmessage.TypeA && tc.answers == 1 {
				if got := response.Answers[0].Body.(*dnsmessage.AResource).A; got != [4]byte{10, 99, 0, 11} {
					t.Fatalf("wrong address: %v", got)
				}
			}
		})
	}
	for n := 0; n < 12; n++ {
		if _, err := answerDNS(make([]byte, n), nil); err == nil {
			t.Fatalf("accepted short packet %d", n)
		}
	}
}
