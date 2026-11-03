package rooms

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	DefaultGamePort      = 25565
	DefaultLocalPort     = 25566
	DefaultHostVirtualIP = "10.144.144.1"
	VirtualNetworkCIDR   = "10.144.144.0/24"
	MaxInvitationLength  = 16 * 1024
)

// Config is local room configuration. Secret must never be returned to the UI
// except as part of an explicitly requested invitation.
type Config struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	NetworkName    string    `json:"networkName"`
	Secret         string    `json:"secret"`
	Role           string    `json:"role"`
	ServerID       string    `json:"serverId"`
	GamePort       int       `json:"gamePort"`
	LocalPort      int       `json:"localPort"`
	HostVirtualIP  string    `json:"hostVirtualIp"`
	BootstrapPeers []string  `json:"bootstrapPeers"`
	AllowRelay     bool      `json:"allowRelay"`
	CreatedAt      time.Time `json:"createdAt"`
}

var (
	idPattern      = regexp.MustCompile(`^[a-f0-9]{32}$`)
	networkPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	hostPattern    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	virtualNetwork = netip.MustParsePrefix(VirtualNetworkCIDR)
)

func randomHex() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("не удалось создать случайный идентификатор комнаты")
	}
	return hex.EncodeToString(value[:]), nil
}

func randomSecret() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errors.New("не удалось создать ключ комнаты")
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func cloneConfig(c Config) Config {
	c.BootstrapPeers = append([]string{}, c.BootstrapPeers...)
	return c
}

func prepareConfig(c Config, previous *Config) (Config, error) {
	var err error
	c.Name = strings.TrimSpace(c.Name)
	c.NetworkName = strings.TrimSpace(c.NetworkName)
	if previous != nil {
		c.CreatedAt = previous.CreatedAt
		if c.Role == "" {
			c.Role = previous.Role
		}
		if c.NetworkName == "" {
			c.NetworkName = previous.NetworkName
		}
		if c.Secret == "" {
			c.Secret = previous.Secret
		}
	} else {
		if c.ID == "" {
			if c.ID, err = randomHex(); err != nil {
				return c, err
			}
		}
		c.CreatedAt = time.Now().UTC()
		if c.Role == "" {
			c.Role = "host"
		}
	}
	if c.Role == "host" {
		if c.NetworkName == "" {
			random, err := randomHex()
			if err != nil {
				return c, err
			}
			c.NetworkName = "gamedock-" + random
		}
		if c.Secret == "" {
			if c.Secret, err = randomSecret(); err != nil {
				return c, err
			}
		}
	}
	if c.GamePort == 0 {
		c.GamePort = DefaultGamePort
	}
	if c.LocalPort == 0 {
		c.LocalPort = DefaultLocalPort
	}
	if c.HostVirtualIP == "" {
		c.HostVirtualIP = DefaultHostVirtualIP
	}
	c = cloneConfig(c)
	if err = Validate(c); err != nil {
		return c, err
	}
	return c, nil
}

// Validate does not generate defaults and never includes credentials in errors.
func Validate(c Config) error {
	if !idPattern.MatchString(c.ID) {
		return errors.New("неверный идентификатор комнаты")
	}
	if c.Role != "host" && c.Role != "guest" {
		return errors.New("роль комнаты должна быть host или guest")
	}
	if c.CreatedAt.IsZero() {
		return errors.New("не указано время создания комнаты")
	}
	if len(c.ServerID) > 128 || hasControl(c.ServerID) {
		return errors.New("неверный идентификатор сервера комнаты")
	}
	if c.LocalPort < 1 || c.LocalPort > 65535 {
		return errors.New("локальный порт должен быть от 1 до 65535")
	}
	return validateMembership(c)
}

func validateMembership(c Config) error {
	if strings.TrimSpace(c.Name) == "" || len([]rune(c.Name)) > 80 || hasControl(c.Name) {
		return errors.New("название комнаты должно содержать от 1 до 80 символов без переводов строки")
	}
	if !networkPattern.MatchString(c.NetworkName) {
		return errors.New("имя сети: от 1 до 64 латинских букв, цифр, знаков _ или -")
	}
	if len(c.Secret) < 16 || len(c.Secret) > 256 || hasControl(c.Secret) || strings.TrimSpace(c.Secret) != c.Secret {
		return errors.New("ключ комнаты должен содержать от 16 до 256 байт без управляющих символов и пробелов по краям")
	}
	if c.GamePort < 1 || c.GamePort > 65535 {
		return errors.New("порт игрового сервера должен быть от 1 до 65535")
	}
	ip, err := netip.ParseAddr(c.HostVirtualIP)
	if err != nil || !ip.Is4() || !ip.IsPrivate() || !virtualNetwork.Contains(ip) {
		return errors.New("виртуальный адрес хоста должен принадлежать сети " + VirtualNetworkCIDR)
	}
	bytes := ip.As4()
	if bytes[3] == 0 || bytes[3] == 255 {
		return errors.New("виртуальный адрес хоста не может быть адресом сети или широковещательным адресом")
	}
	if len(c.BootstrapPeers) > 8 {
		return errors.New("можно указать не больше 8 начальных узлов")
	}
	seen := make(map[string]bool)
	for i, peer := range c.BootstrapPeers {
		if err := validateBootstrapPeer(peer); err != nil {
			return fmt.Errorf("начальный узел №%d: %w", i+1, err)
		}
		if strings.Contains(peer, c.Secret) {
			return errors.New("ключ комнаты нельзя включать в адрес начального узла")
		}
		key := strings.ToLower(strings.TrimSuffix(peer, "/"))
		if seen[key] {
			return errors.New("адреса начальных узлов не должны повторяться")
		}
		seen[key] = true
	}
	return nil
}

func validateBootstrapPeer(peer string) error {
	if len(peer) == 0 || len(peer) > 512 || strings.ContainsAny(peer, " \t\r\n\\") || hasControl(peer) {
		return errors.New("неверный адрес узла")
	}
	u, err := url.Parse(peer)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("адрес должен содержать только протокол, хост и порт, без паролей и параметров")
	}
	switch u.Scheme {
	case "tcp", "udp", "ws", "wss", "quic":
	default:
		return errors.New("разрешены только протоколы tcp, udp, ws, wss и quic")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" {
		return errors.New("укажите хост и порт узла")
	}
	if strings.Contains(host, "%") {
		return errors.New("неверный хост узла")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.IsUnspecified() || ip.IsMulticast() {
			return errors.New("нельзя подключаться к неопределённому или многоадресному хосту")
		}
	} else if !hostPattern.MatchString(host) || strings.Contains(host, "..") {
		return errors.New("неверное имя хоста узла")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
		return errors.New("порт узла должен быть от 1 до 65535")
	}
	return nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
