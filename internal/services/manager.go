package services

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/place1/wireguard-access-server/internal/storage"
	"github.com/place1/wireguard-access-server/internal/wg"
	"github.com/sirupsen/logrus"
)

var vpnip, vpnsubnet = MustParseCIDR("10.0.0.1/24")

var peerid = 1

// we need to give each device (i.e. wg peer)
// an ip within the VPN's subnet
// idk how i'm going to maintain this infomation just yet
func nextPeerID() int {
	peerid = peerid + 1
	return peerid
}

type DeviceManager struct {
	wgserver *wg.Server
	storage  storage.Storage
}

func NewDeviceManager(w *wg.Server, s storage.Storage) *DeviceManager {
	return &DeviceManager{w, s}
}

func (d *DeviceManager) Sync() error {
	devices, err := d.ListDevices()
	if err != nil {
		return errors.Wrap(err, "failed to list devices")
	}
	for _, device := range devices {
		if err := d.wgserver.AddPeer(device.PublicKey, device.Address); err != nil {
			logrus.Warn(errors.Wrapf(err, "failed to sync device '%s' (ignoring)", device.Name))
		}
	}
	return nil
}

func (d *DeviceManager) AddDevice(name string, publicKey string) (*storage.Device, error) {

	if name == "" {
		return nil, errors.New("device name must not be empty")
	}

	clientAddr, err := d.nextClientAddress()
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate an ip address for device")
	}

	device := &storage.Device{
		Name:            name,
		PublicKey:       publicKey,
		Endpoint:        d.wgserver.Endpoint(),
		Address:         clientAddr,
		DNS:             d.wgserver.DNS(),
		CreatedAt:       time.Now(),
		ServerPublicKey: d.wgserver.PublicKey(),
	}

	if err := d.storage.Save(device); err != nil {
		// TODO: might need to clean up the wg config?
		// might need to save before adding to wg?
		// idk lol
		return nil, errors.Wrap(err, "failed to save the new device")
	}

	if err := d.wgserver.AddPeer(publicKey, clientAddr); err != nil {
		return nil, errors.Wrap(err, "unable to provision peer")
	}

	return device, nil
}

func (d *DeviceManager) ListDevices() ([]*storage.Device, error) {
	return d.storage.List()
}

func (d *DeviceManager) DeleteDevice(name string) error {
	device, err := d.storage.Get(name)
	if err != nil {
		return errors.Wrap(err, "failed to retrieve device")
	}
	if err := d.storage.Delete(device); err != nil {
		return err
	}
	if err := d.wgserver.RemovePeer(device.PublicKey); err != nil {
		return errors.Wrap(err, "device was removed from storage but failed to be removed from the wireguard interface")
	}
	return nil
}

var nextIPLock = sync.Mutex{}

func (d *DeviceManager) nextClientAddress() (string, error) {
	nextIPLock.Lock()
	defer nextIPLock.Unlock()

	devices, err := d.ListDevices()
	if err != nil {
		return "", errors.Wrap(err, "failed to list devices")
	}

	// TODO: read up on better ways to allocate client's IP
	// addresses from a configurable CIDR
	usedIPs := []net.IP{
		MustParseIP("10.0.0.0"),
		MustParseIP("10.0.0.1"),
		MustParseIP("10.0.0.255"),
	}
	for _, device := range devices {
		ip, _ := MustParseCIDR(device.Address)
		usedIPs = append(usedIPs, ip)
	}

	ip := vpnip
	for ip := ip.Mask(vpnsubnet.Mask); vpnsubnet.Contains(ip); ip = nextIP(ip) {
		if !contains(usedIPs, ip) {
			return fmt.Sprintf("%s/32", ip.String()), nil
		}
	}

	return "", fmt.Errorf("there are no free IP addresses in the vpn subnet: '%s'", vpnsubnet)
}

func contains(ips []net.IP, target net.IP) bool {
	for _, ip := range ips {
		if ip.Equal(target) {
			return true
		}
	}
	return false
}

func MustParseCIDR(cidr string) (net.IP, *net.IPNet) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return ip, ipnet
}

func MustParseIP(ip string) net.IP {
	netip, _ := MustParseCIDR(fmt.Sprintf("%s/32", ip))
	return netip
}

func nextIP(ip net.IP) net.IP {
	next := make([]byte, len(ip))
	copy(next, ip)
	for j := len(next) - 1; j >= 0; j-- {
		next[j]++
		if next[j] > 0 {
			break
		}
	}
	return next
}