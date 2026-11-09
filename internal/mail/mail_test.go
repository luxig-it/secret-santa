package mail

import (
	"bufio"
	"context"
	"io"
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
					say("250 fake")
				}
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
				got <- b.String()
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
