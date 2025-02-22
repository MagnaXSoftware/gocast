package controller

import (
	"encoding/json"
	"fmt"
	"github.com/mayuresh82/gocast/config"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang/glog"
)

const (
	nomadServiceListUrl = "/v1/services"
	nomadServiceUrl     = "/v1/service/%s"

	nomadMonitorIdentifier = "nomad"
)

type NomadMonitor struct {
	addr      string
	namespace string
	node      string
	token     string
	client    *http.Client
}

func NewNomadMonitor(addr, namespace, node, token string) (*NomadMonitor, error) {
	return &NomadMonitor{addr: addr, namespace: namespace, node: node, token: token, client: &http.Client{Timeout: monitorTimeout}}, nil
}

func (n *NomadMonitor) Monitor(m *MonitorMgr) {
	for {
		apps, err := n.queryServices()
		if err != nil {
			glog.Errorf("Failed to query nomad: %v", err)
		} else {
			for _, app := range apps {
				m.Add(app)
			}
			// remove currently running apps that are not discovered in this pass
			var toRemove []string
			m.monMu.Lock()
			for name, mon := range m.monitors {
				if mon.app.Source != nomadMonitorIdentifier {
					continue
				}
				var found bool
				for _, app := range apps {
					if name == app.Name {
						found = true
						break
					}
				}
				if !found {
					glog.V(2).Infof("Removing app: %s as it was not found in nomad", name)
					toRemove = append(toRemove, name)
				}
			}
			m.monMu.Unlock()
			for _, tr := range toRemove {
				m.Remove(tr)
			}
		}
		<-time.After(m.config.Agent.NomadQueryInterval)
	}
}

func (n *NomadMonitor) getHttpReq(method string, url string) (*http.Request, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	if n.token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", n.token))
	}

	return req, nil
}

type nomadMiniServiceInfo struct {
	Name string `json:"ServiceName"`
	Tags []string
}

func (n *NomadMonitor) queryServices() ([]*App, error) {
	var apps []*App

	addr := fmt.Sprintf("%s/%s?namespace=%s", n.addr, nomadServiceListUrl, url.QueryEscape(n.namespace))
	glog.V(4).Infof("Querying nomad service at %s", addr)
	req, err := n.getHttpReq("GET", addr)
	if err != nil {
		return nil, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	//goland:noinspection GoUnhandledErrorResult
	defer resp.Body.Close()
	var nomadData []struct {
		Namespace string
		Services  []nomadMiniServiceInfo
	}
	if err := json.NewDecoder(resp.Body).Decode(&nomadData); err != nil {
		return apps, fmt.Errorf("failed to decode nomad data: %v", err)
	}
	glog.V(5).Infof("Got nomad data: %+v", nomadData)
	for _, nsBlock := range nomadData {
		for _, service := range nsBlock.Services {
			if !contains(service.Tags, matchTag) {
				continue
			}

			app, err := n.serviceToApp(service, nsBlock.Namespace)
			if err != nil {
				glog.Errorf("unable to add nomad app: %v", err)
				continue
			}
			if app == nil {
				continue
			}

			apps = append(apps, app)
		}
	}

	return apps, nil
}

func (n *NomadMonitor) serviceToApp(s nomadMiniServiceInfo, ns string) (*App, error) {
	var (
		vip      string
		monitors []string
		nats     []string
	)
	addr := fmt.Sprintf(
		"%s/%s?namespace=%s&filter=%s",
		n.addr,
		fmt.Sprintf(nomadServiceUrl, url.PathEscape(s.Name)),
		url.QueryEscape(ns),
		url.QueryEscape(fmt.Sprintf("NodeID == \"%s\"", n.node)),
	)
	glog.V(4).Infof("Querying nomad service %s at %s", s.Name, addr)
	req, err := n.getHttpReq("GET", addr)
	if err != nil {
		return nil, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	//goland:noinspection GoUnhandledErrorResult
	defer resp.Body.Close()

	var services []struct {
		Name    string   `json:"ServiceName"`
		Tags    []string `json:"Tags"`
		Address string   `json:"Address"`
		Port    int      `json:"Port"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&services); err != nil {
		return nil, err
	}
	if len(services) < 1 {
		return nil, nil
	}
	service := services[0]

	glog.V(3).Infof("found service %s (ns: %s) for this node", service.Name, ns)

	var vipConf config.VipConfig
	for _, tag := range service.Tags {
		// try to find the required tags. Only vip is mandatory
		parts := strings.Split(tag, "=")
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "gocast_vip":
			vip = parts[1]
		case "gocast_vip_communities":
			vipConf.BgpCommunities = strings.Split(parts[1], ",")
		case "gocast_monitor":
			monitors = append(monitors, parts[1])
		case "gocast_nat":
			nats = append(nats, parts[1])
		}
	}
	if vip == "" {
		return nil, fmt.Errorf("no \"vip\" tag found in matched service :%s", s.Name)
	}
	app, err := NewApp(fmt.Sprintf("%s@%s", service.Name, ns), vip, vipConf, monitors, nats, nomadMonitorIdentifier)
	if err != nil {
		return nil, err
	}

	app.Addr = net.ParseIP(service.Address)
	app.Port = service.Port

	return app, nil
}
