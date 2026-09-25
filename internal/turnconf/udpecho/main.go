// Command udpecho answers every UDP packet with the same bytes. The relay's
// Docker test (internal/turnconf) runs it as the phone system's stand-in on
// linx-media, and as a stranger the relay must refuse. Never shipped.
package main

import (
	"flag"
	"log"
	"net"
)

func main() {
	addr := flag.String("listen", ":10000", "UDP address")
	flag.Parse()
	c, err := net.ListenPacket("udp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	buf := make([]byte, 2048)
	for {
		n, from, err := c.ReadFrom(buf)
		if err != nil {
			log.Fatal(err)
		}
		c.WriteTo(buf[:n], from)
	}
}
