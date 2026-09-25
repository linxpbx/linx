// Command tcpfwd forwards TCP connections to one address. The call suite
// (internal/calltest) runs it on linx-sipws and a published port, so the
// test process itself can reach Asterisk's browser websocket the way the
// control plane's /sip relay does (TLS passes through untouched). Never
// shipped.
package main

import (
	"flag"
	"io"
	"log"
	"net"
)

func main() {
	listen := flag.String("listen", ":8089", "address to listen on")
	to := flag.String("to", "linx-sipws:8089", "address to forward to")
	flag.Parse()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			defer c.Close()
			up, err := net.Dial("tcp", *to)
			if err != nil {
				log.Print(err)
				return
			}
			defer up.Close()
			go func() {
				io.Copy(up, c)
				up.(*net.TCPConn).CloseWrite()
			}()
			io.Copy(c, up)
		}()
	}
}
