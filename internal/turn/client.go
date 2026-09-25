package turn

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"
)

// A minimal TURN client (RFC 5766/8656): allocate with long-term
// credentials, create a permission, send and receive data. Just enough for
// linx doctor to prove a relay works through the front door, and for the
// coturn Docker test to check what coturn allows; browsers use their own.

// STUN (RFC 5389) message types and magic cookie.
const (
	stunBindingRequest = 0x0001
	stunBindingSuccess = 0x0101
	stunMagicCookie    = 0x2112A442
)

const (
	methodAllocate         = 0x003
	methodCreatePermission = 0x008
	methodSend             = 0x006
	methodData             = 0x007

	attrUsername          = 0x0006
	attrMessageIntegrity  = 0x0008
	attrErrorCode         = 0x0009
	attrXorPeerAddress    = 0x0012
	attrData              = 0x0013
	attrRealm             = 0x0014
	attrNonce             = 0x0015
	attrXorRelayedAddress = 0x0016
	attrRequestedTranspt  = 0x0019
)

type stunAttr struct {
	typ uint16
	val []byte
}

type stunMsg struct {
	typ   uint16
	tid   [12]byte
	attrs []stunAttr
}

func (m stunMsg) get(typ uint16) []byte {
	for _, a := range m.attrs {
		if a.typ == typ {
			return a.val
		}
	}
	return nil
}

// class bits: request 00, indication 01, success 10, error 11.
func msgType(method uint16, class uint16) uint16 {
	return method&0x000F | (method&0x0070)<<1 | (method&0x0F80)<<2 | (class&1)<<4 | (class&2)<<7
}

func (m stunMsg) encode(integrityKey []byte) []byte {
	body := []byte{}
	put := func(typ uint16, val []byte) {
		h := make([]byte, 4)
		binary.BigEndian.PutUint16(h, typ)
		binary.BigEndian.PutUint16(h[2:], uint16(len(val)))
		body = append(body, h...)
		body = append(body, val...)
		for len(body)%4 != 0 {
			body = append(body, 0)
		}
	}
	for _, a := range m.attrs {
		put(a.typ, a.val)
	}
	header := func(length int) []byte {
		h := make([]byte, 20)
		binary.BigEndian.PutUint16(h, m.typ)
		binary.BigEndian.PutUint16(h[2:], uint16(length))
		binary.BigEndian.PutUint32(h[4:], stunMagicCookie)
		copy(h[8:], m.tid[:])
		return h
	}
	if integrityKey != nil {
		// The length covers MESSAGE-INTEGRITY itself (RFC 5389 §15.4).
		mac := hmac.New(sha1.New, integrityKey)
		mac.Write(header(len(body) + 24))
		mac.Write(body)
		put(attrMessageIntegrity, mac.Sum(nil))
	}
	return append(header(len(body)), body...)
}

func decodeStun(b []byte) (stunMsg, error) {
	if len(b) < 20 || binary.BigEndian.Uint32(b[4:]) != stunMagicCookie {
		return stunMsg{}, errors.New("not STUN")
	}
	m := stunMsg{typ: binary.BigEndian.Uint16(b)}
	copy(m.tid[:], b[8:20])
	n := int(binary.BigEndian.Uint16(b[2:]))
	body := b[20:min(len(b), 20+n)]
	for len(body) >= 4 {
		typ, l := binary.BigEndian.Uint16(body), int(binary.BigEndian.Uint16(body[2:]))
		if 4+l > len(body) {
			break
		}
		m.attrs = append(m.attrs, stunAttr{typ, body[4 : 4+l]})
		body = body[min(len(body), 4+(l+3)/4*4):]
	}
	return m, nil
}

func xorAddr(a netip.AddrPort) []byte {
	b := make([]byte, 8)
	b[1] = 0x01
	binary.BigEndian.PutUint16(b[2:], a.Port()^uint16(stunMagicCookie>>16))
	ip := a.Addr().As4()
	binary.BigEndian.PutUint32(b[4:], binary.BigEndian.Uint32(ip[:])^stunMagicCookie)
	return b
}

func parseXorAddr(b []byte) netip.AddrPort {
	if len(b) < 8 {
		return netip.AddrPort{}
	}
	port := binary.BigEndian.Uint16(b[2:]) ^ uint16(stunMagicCookie>>16)
	var ip [4]byte
	binary.BigEndian.PutUint32(ip[:], binary.BigEndian.Uint32(b[4:])^stunMagicCookie)
	return netip.AddrPortFrom(netip.AddrFrom4(ip), port)
}

// Error is a STUN error response's code (401, 403, 486, ...).
type Error int

func (e Error) Error() string { return fmt.Sprintf("TURN error %d", int(e)) }

// Client talks TURN over one connection: UDP, or TCP/TLS (Stream, where
// STUN messages are self-delimiting).
type Client struct {
	Conn         net.Conn
	Stream       bool
	User, Pass   string
	realm, nonce []byte
	// Relayed is the relay address Allocate got.
	Relayed netip.AddrPort
}

func (c *Client) key() []byte {
	s := md5.Sum([]byte(c.User + ":" + string(c.realm) + ":" + c.Pass))
	return s[:]
}

func (c *Client) read() (stunMsg, error) {
	c.Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if !c.Stream {
		b := make([]byte, 2048)
		n, err := c.Conn.Read(b)
		if err != nil {
			return stunMsg{}, err
		}
		return decodeStun(b[:n])
	}
	h := make([]byte, 20)
	if _, err := io.ReadFull(c.Conn, h); err != nil {
		return stunMsg{}, err
	}
	body := make([]byte, binary.BigEndian.Uint16(h[2:]))
	if _, err := io.ReadFull(c.Conn, body); err != nil {
		return stunMsg{}, err
	}
	return decodeStun(append(h, body...))
}

// request sends method with attrs, authenticated once the server has sent
// its realm and nonce (retrying once on the first 401), and returns the
// success response or a turnError.
func (c *Client) request(method uint16, attrs ...stunAttr) (stunMsg, error) {
	for attempt := 0; attempt < 2; attempt++ {
		m := stunMsg{typ: msgType(method, 0), attrs: attrs}
		rand.Read(m.tid[:])
		var key []byte
		if c.realm != nil {
			m.attrs = append(append([]stunAttr{}, attrs...), stunAttr{attrUsername, []byte(c.User)},
				stunAttr{attrRealm, c.realm}, stunAttr{attrNonce, c.nonce})
			key = c.key()
		}
		if _, err := c.Conn.Write(m.encode(key)); err != nil {
			return stunMsg{}, err
		}
		for {
			resp, err := c.read()
			if err != nil {
				return stunMsg{}, err
			}
			if resp.tid != m.tid {
				continue // e.g. a data indication
			}
			if resp.typ == msgType(method, 2) {
				return resp, nil
			}
			ec := resp.get(attrErrorCode)
			code := 0
			if len(ec) >= 4 {
				code = int(ec[2])*100 + int(ec[3])
			}
			if (code == 401 || code == 438) && attempt == 0 {
				c.realm, c.nonce = resp.get(attrRealm), resp.get(attrNonce)
				break
			}
			return resp, Error(code)
		}
	}
	return stunMsg{}, Error(401)
}

// Allocate asks for a relay address (UDP).
func (c *Client) Allocate() error {
	resp, err := c.request(methodAllocate, stunAttr{attrRequestedTranspt, []byte{17, 0, 0, 0}})
	if err != nil {
		return err
	}
	c.Relayed = parseXorAddr(resp.get(attrXorRelayedAddress))
	return nil
}

// Permit lets peer send through the relay.
func (c *Client) Permit(peer netip.AddrPort) error {
	_, err := c.request(methodCreatePermission, stunAttr{attrXorPeerAddress, xorAddr(peer)})
	return err
}

// Echo sends data to peer through the relay and waits for it to come back.
func (c *Client) Echo(peer netip.AddrPort, data []byte) ([]byte, error) {
	m := stunMsg{typ: msgType(methodSend, 1), attrs: []stunAttr{{attrXorPeerAddress, xorAddr(peer)}, {attrData, data}}}
	rand.Read(m.tid[:])
	if _, err := c.Conn.Write(m.encode(nil)); err != nil {
		return nil, err
	}
	for {
		resp, err := c.read()
		if err != nil {
			return nil, err
		}
		if resp.typ == msgType(methodData, 1) {
			return resp.get(attrData), nil
		}
	}
}

// Ping sends a STUN binding request to addr (UDP) and waits for the
// answer: proof a STUN/TURN server is listening there.
func Ping(ctx context.Context, addr string) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:], stunBindingRequest)
	binary.BigEndian.PutUint32(req[4:], stunMagicCookie)
	if _, err := rand.Read(req[8:20]); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(3 * time.Second)
	}
	c.SetDeadline(deadline)
	if _, err := c.Write(req); err != nil {
		return err
	}
	resp := make([]byte, 1500)
	n, err := c.Read(resp)
	if err != nil {
		return err
	}
	if n < 20 || binary.BigEndian.Uint16(resp[0:]) != stunBindingSuccess || !bytes.Equal(resp[8:20], req[8:20]) {
		return errors.New("no STUN answer")
	}
	return nil
}
