// Package mcstatus checks whether the local Minecraft server answers status pings.
package mcstatus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
	"unicode/utf8"
)

const (
	host             = "127.0.0.1"
	protocolVersion  = 774
	minecraftVersion = "1.21.11"
	maxJSONBytes     = 32767 * 3
	maxPacketBytes   = 1 + 3 + maxJSONBytes // Packet ID, string length, string.
)

// Check verifies that the loopback port answers a Minecraft 1.21.11 status ping.
func Check(ctx context.Context, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid Minecraft port %d", port)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	err = ping(conn, port)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func ping(conn net.Conn, port int) error {
	handshake := binary.AppendUvarint([]byte{0}, protocolVersion)
	handshake = binary.AppendUvarint(handshake, uint64(len(host)))
	handshake = append(handshake, host...)
	handshake = binary.BigEndian.AppendUint16(handshake, uint16(port))
	handshake = append(handshake, 1) // Next state: status.
	if err := writePacket(conn, handshake); err != nil {
		return fmt.Errorf("send status handshake: %w", err)
	}
	if err := writePacket(conn, []byte{0}); err != nil {
		return fmt.Errorf("request server status: %w", err)
	}

	reader := bufio.NewReader(conn)
	status, err := readPacket(reader, maxPacketBytes)
	if err != nil {
		return fmt.Errorf("read server status: %w", err)
	}
	if err := checkStatus(status); err != nil {
		return err
	}

	request := binary.BigEndian.AppendUint64([]byte{1}, uint64(time.Now().UnixNano()))
	if err := writePacket(conn, request); err != nil {
		return fmt.Errorf("send server ping: %w", err)
	}
	reply, err := readPacket(reader, len(request))
	if err != nil {
		return fmt.Errorf("read server pong: %w", err)
	}
	if !bytes.Equal(request, reply) {
		return errors.New("invalid Minecraft pong")
	}
	return nil
}

func checkStatus(packet []byte) error {
	if packet[0] != 0 {
		return errors.New("invalid Minecraft status packet ID")
	}
	body := bytes.NewReader(packet[1:])
	size, err := readSize(body)
	if err != nil || size > maxJSONBytes || size != body.Len() {
		return errors.New("invalid Minecraft status string length")
	}
	payload := packet[len(packet)-size:]
	if !utf8.Valid(payload) {
		return errors.New("invalid UTF-8 in Minecraft status")
	}
	var status struct {
		Version struct {
			Name     string
			Protocol int
		}
	}
	if err := json.Unmarshal(payload, &status); err != nil {
		return fmt.Errorf("decode Minecraft status: %w", err)
	}
	if status.Version.Name != minecraftVersion || status.Version.Protocol != protocolVersion {
		return fmt.Errorf("unexpected Minecraft version %q (protocol %d), want %s (protocol %d)",
			status.Version.Name, status.Version.Protocol, minecraftVersion, protocolVersion)
	}
	return nil
}

func writePacket(w io.Writer, payload []byte) error {
	frame := binary.AppendUvarint(nil, uint64(len(payload)))
	frame = append(frame, payload...)
	_, err := w.Write(frame)
	return err
}

func readPacket(r *bufio.Reader, limit int) ([]byte, error) {
	size, err := readSize(r)
	if err != nil {
		return nil, err
	}
	if size < 1 || size > limit {
		return nil, fmt.Errorf("invalid Minecraft packet length %d", size)
	}
	packet := make([]byte, size)
	_, err = io.ReadFull(r, packet)
	return packet, err
}

// Packet and string lengths are nonnegative Minecraft VarInts.
func readSize(r io.ByteReader) (int, error) {
	var size int
	for shift := 0; shift <= 28; shift += 7 {
		part, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if shift == 28 && part > 7 {
			break
		}
		size |= int(part&0x7f) << shift
		if part < 0x80 {
			if shift != 0 && part == 0 {
				break
			}
			return size, nil
		}
	}
	return 0, errors.New("invalid Minecraft length VarInt")
}
