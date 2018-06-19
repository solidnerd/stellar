package server

import (
	"bytes"
	"fmt"
	"net"

	"github.com/containerd/containerd/errdefs"
	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

func (s *Server) initNetworking() error {
	c, err := s.client()
	if err != nil {
		return err
	}
	defer c.Close()

	// TODO: check for existing assigned subnet; if not, allocate
	localSubnetKey := fmt.Sprintf(dsSubnetsKey, s.NodeName())

	subnets, err := c.Network().Subnets()
	if err != nil {
		return err
	}

	if len(subnets) == 0 {
		return fmt.Errorf("no available subnets in network configuration")
	}

	bSubnetCIDR, err := c.Datastore().Get(dsNetworkBucketName, localSubnetKey)
	if err != nil {
		err = errdefs.FromGRPC(err)
		if !errdefs.IsNotFound(err) {
			return err
		}
	}

	if bytes.Equal(bSubnetCIDR, []byte("")) {
		logrus.Debug("local subnet key not found; assigning new subnet")
		// TODO: assign subnet

		searchKey := fmt.Sprintf(dsSubnetsKey, "")
		existingSubnets, err := c.Datastore().Search(dsNetworkBucketName, searchKey)
		if err != nil {
			err = errdefs.FromGRPC(err)
			if !errdefs.IsNotFound(err) {
				return err
			}
		}

		// TODO: check existing subnets
		assigned := len(existingSubnets)
		if len(subnets) < assigned {
			return fmt.Errorf("no available subnet for current node; need %d subnets", assigned)
		}

		bSubnetCIDR = []byte(subnets[assigned].CIDR)
		if err := c.Datastore().Set(dsNetworkBucketName, localSubnetKey, bSubnetCIDR, true); err != nil {
			return err
		}
	}

	subnetCIDR := string(bSubnetCIDR)
	logrus.Infof("setting up subnet %s", subnetCIDR)
	ip, ipnet, err := net.ParseCIDR(subnetCIDR)
	if err != nil {
		return err
	}

	gw := ip.Mask(ipnet.Mask)
	gw[3]++

	mask, _ := ipnet.Mask.Size()

	logrus.Debugf("setting up local gateway %s", gw.String())
	if err := s.setupGateway(gw, mask); err != nil {
		return err
	}

	// TODO: call configure service to configure local and peer routes

	return nil
}

func (s *Server) setupGateway(ip net.IP, mask int) error {
	bindAddr := s.config.AgentConfig.BindAddr
	bindIP := net.ParseIP(bindAddr)
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}

	bindInterface := ""

	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			logrus.Warnf("error getting addresses for interface %s", iface.Name)
			continue
		}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err != nil {
				logrus.Warnf("error parsing address %s", addr)
				continue
			}
			if bindIP.Equal(ip) {
				bindInterface = iface.Name
				break
			}
		}
	}

	// TODO: add ip alias to bind interface
	logrus.Debugf("bind interface: %s", bindInterface)
	dev, err := netlink.LinkByName(bindInterface)
	if err != nil {
		return err
	}

	aliasAddr, err := netlink.ParseAddr(fmt.Sprintf("%s/%d", ip.String(), mask))
	if err != nil {
		return err
	}
	aliasAddr.Label = dev.Attrs().Name + ".stellar-gw"

	logrus.Debugf("adding address %s to device %s", aliasAddr, dev.Attrs().Name)

	if err := netlink.AddrReplace(dev, aliasAddr); err != nil {
		return err
	}

	return nil
}
