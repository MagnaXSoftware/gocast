package controller

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"

	api "github.com/osrg/gobgp/v3/api"
	gobgp "github.com/osrg/gobgp/v3/pkg/server"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
	any "google.golang.org/protobuf/types/known/anypb"

	"github.com/mayuresh82/gocast/config"
)

type BgpListener struct {
	s          *gobgp.BgpServer
	recvdPaths chan string
}

// NewBgpListener starts a local BGP server for testing purposes
func NewBgpListener(localAS int) (*BgpListener, error) {
	s := gobgp.NewBgpServer()
	go s.Serve()
	if err := s.StartBgp(context.Background(), &api.StartBgpRequest{
		Global: &api.Global{
			Asn:      uint32(localAS),
			RouterId: "100.100.100.100",
		},
	}); err != nil {
		return nil, fmt.Errorf("Unable to start bgp: %v", err)
	}
	n := &BgpListener{s: s, recvdPaths: make(chan string)}
	err := s.WatchEvent(context.Background(), &api.WatchEventRequest{Table: &api.WatchEventRequest_Table{Filters: []*api.WatchEventRequest_Table_Filter{
		{
			Type: api.WatchEventRequest_Table_Filter_ADJIN,
		},
	}}}, func(p *api.WatchEventResponse) {
		for _, path := range p.GetTable().GetPaths() {
			// assumes v4 only paths !
			value, err := any.UnmarshalNew(path.Nlri, proto.UnmarshalOptions{})
			if err != nil {
				return
			}
			nlri := value.(*api.IPAddressPrefix)
			n.recvdPaths <- fmt.Sprintf("%s/%d", nlri.Prefix, nlri.PrefixLen)
		}
	})
	if err != nil {
		return nil, err
	}
	if err := s.AddPeer(context.Background(), &api.AddPeerRequest{
		Peer: &api.Peer{
			Conf: &api.PeerConf{
				NeighborAddress: "127.0.0.1",
				PeerAsn:         11111,
			},
			Transport: &api.Transport{PassiveMode: true},
		},
	}); err != nil {
		return nil, err
	}
	return n, nil
}

func (l *BgpListener) Shutdown() error {
	if err := l.s.StopBgp(context.Background(), &api.StopBgpRequest{}); err != nil {
		return err
	}
	return nil
}

// This test tests the BGP controller talking to a local BGP
// listener. It needs a few seconds to pass and *may* time out
// if the test timeouts are very small. It also needs to be run as
// root (sudo)
// Disabling this test in CI currently due to https://github.com/osrg/gobgp/issues/2366
func TestBgpNew(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping testing in CI environment")
	}
	listener, err := NewBgpListener(22222)
	if err != nil {
		panic(err)
	}
	defer func(listener *BgpListener) {
		_ = listener.Shutdown()
	}(listener)
	a := assert.New(t)
	c := config.BgpConfig{
		LocalAS:     11111,
		PeerAS:      22222,
		PeerIP:      "127.0.0.1",
		LocalIP:     "192.168.1.100",
		Communities: []string{"100:100"},
		Origin:      "igp",
	}
	ctrl, err := NewController(c)
	if err != nil {
		a.FailNow(err.Error())
		return
	}
	_, ipnet, _ := net.ParseCIDR("20.30.40.0/24")
	r := &Route{Net: ipnet}
	if err := ctrl.Announce(r); err != nil {
		a.FailNow(err.Error())
	}

	path := <-listener.recvdPaths
	a.Equal("20.30.40.0/24", path)
	_ = ctrl.Shutdown()
}
