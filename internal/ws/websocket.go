package ws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Conn ist eine minimale WebSocket-Verbindung (nur Write, Server-Side)
type Conn struct {
	conn net.Conn
	rw   *bufio.ReadWriter
}

// Upgrade führt den WebSocket Handshake durch (HTTP → WS)
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, fmt.Errorf("not a websocket request")
	}

	key := r.Header.Get("Sec-Websocket-Key")
	if key == "" {
		return nil, fmt.Errorf("missing Sec-WebSocket-Key")
	}

	// Accept-Key berechnen (RFC 6455 §4.2.2)
	h := sha1.New()
	h.Write([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	// HTTP-Connection hijacken
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("ResponseWriter does not support hijacking")
	}

	netConn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack: %w", err)
	}

	// 101 Switching Protocols senden
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"

	if _, err := rw.WriteString(response); err != nil {
		netConn.Close()
		return nil, fmt.Errorf("write handshake: %w", err)
	}
	if err := rw.Flush(); err != nil {
		netConn.Close()
		return nil, fmt.Errorf("flush handshake: %w", err)
	}

	return &Conn{conn: netConn, rw: rw}, nil
}

// WriteJSON serialisiert v als JSON und sendet es als WS Text Frame
func (c *Conn) WriteJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.writeFrame(data)
}

// Close sendet einen Close-Frame und schließt die Verbindung
func (c *Conn) Close() error {
	// Opcode 0x08 = Connection Close
	_ = c.writeRawFrame(0x88, nil)
	return c.conn.Close()
}

// writeFrame sendet einen Text-Frame (opcode 0x81)
func (c *Conn) writeFrame(payload []byte) error {
	return c.writeRawFrame(0x81, payload)
}

// writeRawFrame schreibt einen WebSocket Frame direkt auf den Socket.
// firstByte: kombiniertes FIN+Opcode Byte (z.B. 0x81 = FIN+Text)
func (c *Conn) writeRawFrame(firstByte byte, payload []byte) error {
	l := len(payload)

	// Byte 1: FIN + Opcode
	frame := []byte{firstByte}

	// Byte 2+: Payload Length (keine Maskierung vom Server)
	switch {
	case l <= 125:
		frame = append(frame, byte(l))
	case l <= 65535:
		frame = append(frame, 126)
		lenBytes := make([]byte, 2)
		binary.BigEndian.PutUint16(lenBytes, uint16(l))
		frame = append(frame, lenBytes...)
	default:
		frame = append(frame, 127)
		lenBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(lenBytes, uint64(l))
		frame = append(frame, lenBytes...)
	}

	frame = append(frame, payload...)

	_, err := c.rw.Write(frame)
	if err != nil {
		return err
	}
	return c.rw.Flush()
}

// ReadFrame liest einen eingehenden Client-Frame (für Ping/Close Handling)
// Gibt opcode und payload zurück.
func (c *Conn) ReadFrame() (opcode byte, payload []byte, err error) {
	// Byte 1: FIN + Opcode
	b0, err := c.rw.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	opcode = b0 & 0x0F

	// Byte 2: Mask-Bit + Payload Length
	b1, err := c.rw.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	masked := (b1 & 0x80) != 0
	payloadLen := uint64(b1 & 0x7F)

	switch payloadLen {
	case 126:
		var ext uint16
		if err := binary.Read(c.rw, binary.BigEndian, &ext); err != nil {
			return 0, nil, err
		}
		payloadLen = uint64(ext)
	case 127:
		if err := binary.Read(c.rw, binary.BigEndian, &payloadLen); err != nil {
			return 0, nil, err
		}
	}

	// Masking Key (Client → Server immer maskiert per RFC)
	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(c.rw, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}

	// Payload lesen
	payload = make([]byte, payloadLen)
	if _, err := io.ReadFull(c.rw, payload); err != nil {
		return 0, nil, err
	}

	// Demaskieren
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}

	return opcode, payload, nil
}
