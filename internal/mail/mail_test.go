package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime/quotedprintable"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeServer speaks just enough SMTP to accept one message, without TLS.
// It returns the address to dial and a channel with the raw DATA.
func fakeServer(t *testing.T, starttls bool) (string, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return serveFake(t, ln, starttls)
}

// serveFake runs the fake SMTP conversation on ln, which may be a TLS
// listener. It accepts AUTH PLAIN and reports it as an "AUTH ..." line in
// front of the message data.
func serveFake(t *testing.T, ln net.Listener, starttls bool) (string, <-chan string) {
	t.Helper()
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		say := func(s string) { io.WriteString(conn, s+"\r\n") }
		say("220 fake ESMTP")
		var auth string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				if starttls {
					say("250-fake")
					say("250 STARTTLS")
				} else {
					say("250-fake")
					say("250 AUTH PLAIN")
				}
			case strings.HasPrefix(cmd, "AUTH PLAIN"):
				auth = strings.TrimSpace(line[len("AUTH PLAIN"):])
				say("235 ok")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				say("250 ok")
			case cmd == "DATA":
				say("354 go ahead")
				var b strings.Builder
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				if auth != "" {
					got <- "AUTH " + auth + "\n" + b.String()
				} else {
					got <- b.String()
				}
				say("250 queued")
			case cmd == "QUIT":
				say("221 bye")
				return
			default:
				say("502 not implemented")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestSend(t *testing.T) {
	addr, got := fakeServer(t, false)
	host, port, _ := net.SplitHostPort(addr)
	s := SMTP{Host: host, Port: port, From: "Wichteln <wichteln@example.com>", Timeout: 5 * time.Second, Insecure: true}
	err := s.Send(context.Background(), Message{
		To:      "ben@example.com",
		Subject: "Dein Link für „Büro“",
		Body:    "Hallo Ben,\nhier ist dein Link: https://example.com/p/abc\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := <-got
	head, body, _ := strings.Cut(raw, "\r\n\r\n")
	for _, want := range []string{"To: ben@example.com", "From: \"Wichteln\" <wichteln@example.com>", "Subject: =?utf-8?q?", "Content-Type: text/plain; charset=utf-8"} {
		if !strings.Contains(head, want) {
			t.Errorf("headers lack %q:\n%s", want, head)
		}
	}
	decoded, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
	if !strings.Contains(string(decoded), "https://example.com/p/abc") || !strings.Contains(string(decoded), "Hallo Ben") {
		t.Errorf("body = %q", decoded)
	}
}

func TestRefusesPlainTextByDefault(t *testing.T) {
	addr, _ := fakeServer(t, false)
	host, port, _ := net.SplitHostPort(addr)
	s := SMTP{Host: host, Port: port, From: "x@example.com", Timeout: 5 * time.Second}
	err := s.Send(context.Background(), Message{To: "ben@example.com", Subject: "s", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "unencrypted") {
		t.Fatalf("got %v, want a refusal to send the link without STARTTLS", err)
	}
}

func TestRefusesClearTextLogin(t *testing.T) {
	addr, _ := fakeServer(t, false)
	host, port, _ := net.SplitHostPort(addr)
	s := SMTP{Host: host, Port: port, From: "x@example.com", User: "u", Pass: "secret", Timeout: 5 * time.Second, Insecure: true}
	err := s.Send(context.Background(), Message{To: "ben@example.com", Subject: "s", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("got %v, want a refusal to log in without STARTTLS", err)
	}
}

func TestTimeout(t *testing.T) {
	// A server that accepts but never greets.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			time.Sleep(3 * time.Second)
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	s := SMTP{Host: host, Port: port, From: "x@example.com", Timeout: 300 * time.Millisecond}
	start := time.Now()
	if err := s.Send(context.Background(), Message{To: "ben@example.com"}); err == nil {
		t.Fatal("a silent server did not cause an error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the timeout did not apply")
	}
}

func TestAddress(t *testing.T) {
	good := map[string]string{"ben@example.com": "ben@example.com", "  Ben.X+wichteln@example.de ": "Ben.X+wichteln@example.de"}
	for in, want := range good {
		if got, err := Address(in); err != nil || got != want {
			t.Errorf("Address(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ben", "Ben <ben@example.com>", "a@example.com, b@example.com", "ben@example.com\r\nBcc: x@evil.test", strings.Repeat("a", 250) + "@example.com"} {
		if _, err := Address(bad); err == nil {
			t.Errorf("Address(%q) accepted", bad)
		}
	}
}

// selfSigned makes a certificate for 127.0.0.1 and a pool that trusts it.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fake smtp"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func tlsFake(t *testing.T) (host, port string, got <-chan string, roots *x509.CertPool) {
	t.Helper()
	cert, pool := selfSigned(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	addr, got := serveFake(t, ln, false)
	host, port, _ = net.SplitHostPort(addr)
	return host, port, got, pool
}

func TestImplicitTLSWithLogin(t *testing.T) {
	host, port, got, roots := tlsFake(t)
	s := SMTP{
		Host: host, Port: port, User: "wichteln@example.com", Pass: "geheim",
		From: "Wichteln <wichteln@example.com>", ImplicitTLS: true, Timeout: 5 * time.Second,
		TLSConfig: &tls.Config{RootCAs: roots},
	}
	if err := s.Send(context.Background(), Message{To: "ben@example.com", Subject: "s", Body: "https://example.com/p/abc"}); err != nil {
		t.Fatal(err)
	}
	raw := <-got
	first, rest, _ := strings.Cut(raw, "\n")
	creds, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(first, "AUTH "))
	if string(creds) != "\x00wichteln@example.com\x00geheim" {
		t.Fatalf("login = %q", creds)
	}
	if !strings.Contains(rest, "To: ben@example.com") {
		t.Fatalf("message = %q", rest)
	}
}

func TestImplicitTLSRejectsUntrustedCertificate(t *testing.T) {
	host, port, _, _ := tlsFake(t)
	// No RootCAs: the fake server's self-signed certificate is not trusted.
	s := SMTP{Host: host, Port: port, User: "u", Pass: "p", From: "x@example.com", ImplicitTLS: true, Timeout: 5 * time.Second}
	err := s.Send(context.Background(), Message{To: "ben@example.com", Subject: "s", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("got %v, want a certificate error", err)
	}
}

func TestImplicitTLSChecksTheHostEvenWithACustomConfig(t *testing.T) {
	host, port, got, roots := tlsFake(t)
	// RootCAs but no ServerName: the host must still be checked, and since
	// the certificate is for 127.0.0.1 the configured host passes.
	s := SMTP{Host: host, Port: port, From: "x@example.com", ImplicitTLS: true, Timeout: 5 * time.Second,
		TLSConfig: &tls.Config{RootCAs: roots}}
	if err := s.Send(context.Background(), Message{To: "ben@example.com", Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	<-got
}

func TestImplicitTLSRejectsACertificateForAnotherName(t *testing.T) {
	_, port, _, roots := tlsFake(t)
	// Trusted CA, but the certificate covers 127.0.0.1, not this name.
	s := SMTP{Host: "127.0.0.1", Port: port, From: "x@example.com", ImplicitTLS: true, Timeout: 5 * time.Second,
		TLSConfig: &tls.Config{RootCAs: roots, ServerName: "mail.example.com"}}
	err := s.Send(context.Background(), Message{To: "ben@example.com", Subject: "s", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("got %v, want a certificate name error", err)
	}
}
