package rooms

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestInvitationRoundTripContainsOnlyMembershipAndAlwaysCreatesGuest(t *testing.T) {
	store, _ := newTestStore(t)
	host, err := store.Save(Config{Name: "Мой сервер с друзьями", ServerID: "private-local-server", AllowRelay: true, GamePort: 25570, LocalPort: 25580, BootstrapPeers: []string{"tcp://localhost:11010", "wss://peer.example:443/"}})
	if err != nil {
		t.Fatal(err)
	}
	code, err := Invitation(host)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, invitationPrefix))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(decoded, &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"v": true, "name": true, "networkName": true, "secret": true, "hostVirtualIp": true, "gamePort": true, "bootstrapPeers": true}
	if len(fields) != len(allowed) {
		t.Fatal("invitation field count differs from the membership whitelist")
	}
	for field := range fields {
		if !allowed[field] {
			t.Fatalf("invitation exposed local field %q", field)
		}
	}
	guest, err := ParseInvitation(code)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseInvitation(code)
	if err != nil {
		t.Fatal(err)
	}
	if guest.Role != "guest" || guest.ServerID != "" || guest.AllowRelay || guest.LocalPort != 25566 || guest.ID == host.ID || guest.ID == again.ID {
		t.Fatal("invitation trusted host-local settings or reused an identity")
	}
	if guest.Secret != host.Secret || guest.NetworkName != host.NetworkName || guest.GamePort != host.GamePort || guest.Name != host.Name || guest.HostVirtualIP != host.HostVirtualIP {
		t.Fatal("invitation changed room membership settings")
	}
	if _, err := store.Save(guest); err != nil {
		t.Fatal("parsed invitation could not be saved")
	}
}

func TestInvitationRejectsMalformedTamperedUnknownAndOversizeInput(t *testing.T) {
	store, _ := newTestStore(t)
	host, err := store.Save(Config{Name: "Комната"})
	if err != nil {
		t.Fatal(err)
	}
	code, _ := Invitation(host)
	data, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, invitationPrefix))
	var fields map[string]any
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	mutated := func(key string, value any) string {
		copy := make(map[string]any)
		for k, v := range fields {
			copy[k] = v
		}
		copy[key] = value
		encoded, _ := json.Marshal(copy)
		return invitationPrefix + base64.RawURLEncoding.EncodeToString(encoded)
	}
	cases := map[string]string{
		"unknown version": mutated("v", 2), "host role injection": mutated("role", "host"), "local path injection": mutated("executable", `C:\evil.exe`),
		"missing secret": mutated("secret", ""), "public IP": mutated("hostVirtualIp", "8.8.8.8"), "network address": mutated("hostVirtualIp", "10.144.144.0"),
		"broadcast": mutated("hostVirtualIp", "10.144.144.255"), "port": mutated("gamePort", 65536), "file bootstrap": mutated("bootstrapPeers", []string{"file:///tmp/data"}),
		"credentials bootstrap": mutated("bootstrapPeers", []string{"tcp://user:password@localhost:11010"}), "query bootstrap": mutated("bootstrapPeers", []string{"tcp://localhost:11010?secret=hidden"}),
		"invalid base64": invitationPrefix + "???", "wrong scheme": "https://example.com/room", "oversize": strings.Repeat("x", MaxInvitationLength+1),
		"duplicate JSON": invitationPrefix + base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"V":1}`)),
		"trailing JSON":  invitationPrefix + base64.RawURLEncoding.EncodeToString(append(data, []byte(` {}`)...)),
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseInvitation(candidate); err == nil {
				t.Fatal("accepted an invalid invitation")
			}
		})
	}
}

func TestRoomValidationBootstrapBoundariesAndSecretErrors(t *testing.T) {
	store, _ := newTestStore(t)
	valid, err := store.Save(Config{Name: "Комната"})
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range []string{"udp://127.0.0.1:11010", "tcp://localhost:11010", "quic://peer.example:11010", "ws://[::1]:11012/", "wss://peer.example:443"} {
		candidate := valid
		candidate.BootstrapPeers = []string{peer}
		if err := Validate(candidate); err != nil {
			t.Fatalf("rejected valid bootstrap protocol: %v", err)
		}
	}
	for _, peer := range []string{"tcp://localhost", "tcp://localhost:0", "tcp://localhost:65536", "tcp://localhost:001", "tcp://0.0.0.0:1", "tcp://224.0.0.1:1", "tcp://localhost:1/" + valid.Secret, "tcp://user:" + valid.Secret + "@localhost:1", "cmd://localhost:1"} {
		candidate := valid
		candidate.BootstrapPeers = []string{peer}
		if err := Validate(candidate); err == nil {
			t.Fatal("accepted unsafe bootstrap")
		} else if strings.Contains(err.Error(), valid.Secret) {
			t.Fatal("validation error leaked the room secret")
		}
	}
	badName := valid
	badName.Name = strings.Repeat("я", 81)
	if err := Validate(badName); err == nil {
		t.Fatal("accepted overlong Unicode room name")
	}
	tooMany := valid
	tooMany.BootstrapPeers = make([]string, 9)
	if err := Validate(tooMany); err == nil {
		t.Fatal("accepted more than eight bootstrap peers")
	}
	guest := Config{Name: "Гость", Role: "guest"}
	if _, err := store.Save(guest); err == nil {
		t.Fatal("invented missing guest membership credentials")
	}
}
