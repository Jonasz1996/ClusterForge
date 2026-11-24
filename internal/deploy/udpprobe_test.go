package deploy

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestUDPProbe: een antwoord van het gevraagde adres telt; zonder antwoord
// faalt de controle.
func TestUDPProbe(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], from)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := udpProbe(ctx, pc.LocalAddr().String(), []byte("x")); err != nil {
		t.Fatalf("antwoord niet gezien: %v", err)
	}
	// Een poort waar niets antwoordt.
	quiet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = quiet.Close() }()
	if err := udpProbe(ctx, quiet.LocalAddr().String(), []byte("x")); err == nil {
		t.Fatal("geen antwoord moet een fout geven")
	}
}
