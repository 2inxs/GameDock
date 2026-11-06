package mcstatus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	const valid = `{"version":{"name":"1.21.11","protocol":774}}`
	for _, tt := range []struct {
		name        string
		payload     []byte
		corruptPong bool
		wantErr     string
	}{
		{name: "ready", payload: statusFrame(valid)},
		{name: "wrong protocol", payload: statusFrame(`{"version":{"name":"1.21.11","protocol":773}}`), wantErr: "unexpected Minecraft version"},
		{name: "wrong version", payload: statusFrame(`{"version":{"name":"1.21.10","protocol":774}}`), wantErr: "unexpected Minecraft version"},
		{name: "invalid JSON", payload: statusFrame("{"), wantErr: "decode Minecraft status"},
		{name: "invalid string size", payload: []byte{3, 0, 2, '{'}, wantErr: "invalid Minecraft status string length"},
		{name: "oversized frame", payload: binary.AppendUvarint(nil, maxPacketBytes+1), wantErr: "invalid Minecraft packet length"},
		{name: "overlong length", payload: []byte{0x81, 0}, wantErr: "invalid Minecraft length VarInt"},
		{name: "wrong pong", payload: statusFrame(valid), corruptPong: true, wantErr: "invalid Minecraft pong"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			port, done := listen(t, func(conn net.Conn, port int) error {
				reader := bufio.NewReader(conn)
				if err := expectRequests(reader, port); err != nil {
					return err
				}
				if _, err := conn.Write(tt.payload); err != nil {
					return err
				}
				if tt.wantErr != "" && !tt.corruptPong {
					return nil
				}
				request := make([]byte, 10)
				if _, err := io.ReadFull(reader, request); err != nil {
					return err
				}
				if request[0] != 9 || request[1] != 1 {
					return fmt.Errorf("invalid ping: %x", request)
				}
				if tt.corruptPong {
					request[9] ^= 1
				}
				_, err := conn.Write(request)
				return err
			})
			err := Check(context.Background(), port)
			if tt.wantErr == "" && err != nil {
				t.Errorf("Check: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("Check = %v, want error containing %q", err, tt.wantErr)
			}
			if err := <-done; err != nil {
				t.Errorf("fake server: %v", err)
			}
		})
	}
}

func TestCheckDeadline(t *testing.T) {
	port, done := listen(t, func(conn net.Conn, _ int) error {
		_, err := io.Copy(io.Discard, conn)
		return err
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := Check(ctx, port)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("Check = %v, want deadline exceeded", err)
	}
	if err := <-done; err != nil {
		t.Errorf("fake server: %v", err)
	}
}

func listen(t *testing.T, serve func(net.Conn, int) error) (int, <-chan error) {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			done <- err
			return
		}
		done <- serve(conn, port)
	}()
	return port, done
}

func expectRequests(reader io.Reader, port int) error {
	// Handshake frame, protocol 774, length-prefixed host, port, status state,
	// then the one-byte status request packet.
	expected := append([]byte{16, 0, 0x86, 0x06, 9}, "127.0.0.1"...)
	expected = binary.BigEndian.AppendUint16(expected, uint16(port))
	expected = append(expected, 1, 1, 0)
	actual := make([]byte, len(expected))
	if _, err := io.ReadFull(reader, actual); err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("unexpected handshake and request: got %x, want %x", actual, expected)
	}
	return nil
}

func statusFrame(payload string) []byte {
	packet := binary.AppendUvarint([]byte{0}, uint64(len(payload)))
	packet = append(packet, payload...)
	frame := binary.AppendUvarint(nil, uint64(len(packet)))
	return append(frame, packet...)
}
