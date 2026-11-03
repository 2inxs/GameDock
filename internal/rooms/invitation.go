package rooms

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

const invitationPrefix = "gamedock://join/"

type invitationPayload struct {
	Version        int      `json:"v"`
	Name           string   `json:"name"`
	NetworkName    string   `json:"networkName"`
	Secret         string   `json:"secret"`
	HostVirtualIP  string   `json:"hostVirtualIp"`
	GamePort       int      `json:"gamePort"`
	BootstrapPeers []string `json:"bootstrapPeers"`
}

// Invitation is a shared network password. Anyone holding it may join or share
// it; removing one saved guest does not revoke that guest's room membership.
func Invitation(c Config) (string, error) {
	if err := validateMembership(c); err != nil {
		return "", err
	}
	payload := invitationPayload{Version: 1, Name: c.Name, NetworkName: c.NetworkName, Secret: c.Secret, HostVirtualIP: c.HostVirtualIP, GamePort: c.GamePort, BootstrapPeers: append([]string{}, c.BootstrapPeers...)}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", errors.New("не удалось создать приглашение")
	}
	code := invitationPrefix + base64.RawURLEncoding.EncodeToString(data)
	if len(code) > MaxInvitationLength {
		return "", errors.New("приглашение превышает допустимый размер 16 КиБ")
	}
	return code, nil
}

func ParseInvitation(code string) (Config, error) {
	if len(code) > MaxInvitationLength {
		return Config{}, errors.New("приглашение превышает допустимый размер 16 КиБ")
	}
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, invitationPrefix) {
		return Config{}, errors.New("ожидается приглашение gamedock://join/…")
	}
	encoded := strings.TrimPrefix(code, invitationPrefix)
	if encoded == "" || strings.ContainsAny(encoded, "\r\n\t =?#/%") {
		return Config{}, errors.New("приглашение повреждено")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return Config{}, errors.New("приглашение повреждено")
	}
	var payload invitationPayload
	if err = decodeRoomJSON(data, &payload); err != nil {
		return Config{}, errors.New("неверные данные приглашения")
	}
	if payload.Version != 1 {
		return Config{}, errors.New("эта версия приглашения не поддерживается")
	}
	c := Config{Name: payload.Name, NetworkName: payload.NetworkName, Secret: payload.Secret, HostVirtualIP: payload.HostVirtualIP, GamePort: payload.GamePort, BootstrapPeers: append([]string{}, payload.BootstrapPeers...), Role: "guest", LocalPort: DefaultLocalPort, AllowRelay: false, CreatedAt: time.Now().UTC()}
	if c.ID, err = randomHex(); err != nil {
		return Config{}, err
	}
	if err = Validate(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Reject duplicate/unknown fields and trailing JSON instead of accepting an
// ambiguous invitation or silently resetting an unreadable configuration.
func decodeRoomJSON(data []byte, target any) error {
	check := json.NewDecoder(bytes.NewReader(data))
	if err := checkJSONValue(check, 0); err != nil {
		return err
	}
	if _, err := check.Token(); err != io.EOF {
		return errors.New("лишние данные JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func checkJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("слишком глубокая структура JSON")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[strings.ToLower(name)] {
				return errors.New("повторяющееся поле JSON")
			}
			seen[strings.ToLower(name)] = true
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("неверный объект JSON")
		}
	case '[':
		for decoder.More() {
			if err := checkJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("неверный массив JSON")
		}
	default:
		return errors.New("неверная структура JSON")
	}
	return nil
}
