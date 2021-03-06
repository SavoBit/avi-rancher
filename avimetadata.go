package main

import (
	"fmt"
	"time"
	"strings"
	"strconv"
	"encoding/json"

	"github.com/Sirupsen/logrus"
	"github.com/rancher/go-rancher-metadata/metadata"
)

func getEnvironmentUUID(m metadata.Client) (string, error) {
	timeout := 30 * time.Second
	var err error
	var stack metadata.Stack
	for i := 1 * time.Second; i < timeout; i *= time.Duration(2) {
		stack, err = m.GetSelfStack()
		if err != nil {
			logrus.Errorf("Error reading stack info: %v...will retry", err)
			time.Sleep(i)
		} else {
			return stack.EnvironmentUUID, nil
		}
	}

	return "", fmt.Errorf("Error reading stack info: %v", err)
}

func getEnvironmentName(m metadata.Client) (string, error) {
	timeout := 30 * time.Second
	var err error
	var stack metadata.Stack
	for i := 1 * time.Second; i < timeout; i *= time.Duration(2) {
		stack, err = m.GetSelfStack()
		if err != nil {
			logrus.Errorf("Error reading stack info: %v...will retry", err)
			time.Sleep(i)
		} else {
			return stack.EnvironmentName, nil
		}
	}

	return "", fmt.Errorf("Error reading stack info: %v", err)
}

func GetMetadataServiceConfigs(m metadata.Client, cfg *AviConfig) (map[string]*Vservice, error) {
        Vservices := make(map[string]*Vservice)
        services, err := m.GetServices()
        if err != nil {
                log.Infof("Error reading services: %v", err)
		return Vservices, err
        }
	for _, service := range services {
		pools := []pool{}
		var serviceName string
		label_sname := ""
		labels := make(map[string]string)
		_, ok := service.Labels[AVI_INTEGRATION_LABEL]
		if ok {
			continue
		}
		if val, ok := service.Labels[AVI_PROXY_LABEL]; ok {
			var result map[string]interface{}
			arr := []byte(val)
			err := json.Unmarshal(arr, &result)
			if err == nil {
				_, ok := result["virtualservice"]
				if ok {
					val, ok := result["virtualservice"].(map[string]interface{})["name"]
					if ok {
						label_sname = val.(string)
					}
				}
			}
		}
		for label, val := range service.Labels {
			labels[label] = val
		}
		for _, container := range service.Containers {
			if len(container.ServiceName) == 0 {
				continue
			}
			if len(container.Ports) == 0 {
				continue
			}
			for _, port := range container.Ports {
				portspec := strings.Split(port, ":")
				if len(portspec) != 3 {
					log.Warnf("Unexpected format of port spec for container %s: %s", container.Name, port)
					continue
				}
				hostip := portspec[0]
				hostport, err := strconv.Atoi(portspec[1])
				if err != nil {
					log.Warnf("Unexpected format of Host port for container %s: %s", container.Name, port)
					continue
				}
				if hostip == "0.0.0.0" {
					log.Warnf("Unexpected format of Host IP for container %s: %s", container.Name, hostip)
					continue
				}
				protospec := strings.Split(portspec[2], "/")
				if len(protospec) != 2 {
					log.Warnf("Unexpected format of proto spec for container %s: %s", container.Name, port)
					continue
				}
				contport, err := strconv.Atoi(protospec[0])
				if err != nil {
					log.Warnf("Unexpected format of Container port for container %s: %s", container.Name, port)
					continue
				}
				proto := protospec[1]
				envname, _ := getEnvironmentName(m)
				if label_sname == "" {
					serviceName = fmt.Sprintf("%s-%s-%s", envname, service.StackName, service.Name)
				} else {
					serviceName = label_sname
				}
				s, ok := Vservices[serviceName]
				found := false
				if ok {
					for _, val := range s.pools {
						if val.hostip == hostip {
							if _, ok := val.ports[hostport]; ok {
								found = true
							}
						}
					}
				}
				if found {
					continue
				}
				poolmem := pool{}
				poolmem.hostip = hostip

				ports := make(map[int]int)
				ports[hostport] = contport

				poolmem.ports = ports
				poolmem.protocol = proto
				poolmem.poolName = fmt.Sprintf("%s-pool-%d-%s", serviceName, hostport, proto)
				pools = append(pools, poolmem)
			}
		}
		if len(pools) > 0 {
			dt := Vservice{}
			dt.serviceName = serviceName
			dt.labels = labels
			dt.pools = pools
			Vservices[dt.serviceName] = &dt
			log.Info(Vservices[dt.serviceName])
		}
	}
	return Vservices, err
}

func containerStateOK(container metadata.Container) bool {
	switch container.State {
	case "running":
	default:
		return false
	}

	switch container.HealthState {
	case "healthy":
	case "updating-healthy":
	case "":
	default:
		return false
	}

	return true
}

func parse_docker_tasks(p *Avi, tasks map[string]*Vservice) {
	for _, dt := range tasks {
		vs, err := p.GetVS(dt.serviceName)
		if err != nil {
			p.CreateUpdateVS(dt, true, nil)
		} else {
			check_sum := fmt.Sprintf("%x", CalculateChecksum(dt))
			if check_sum != vs["cloud_config_cksum"] {
				log.Infof("Checksum changed", check_sum, vs["cloud_config_cksum"])
				p.CreateUpdateVS(dt, false, vs)
			}
		}
	}
	vses, err := p.GetAllVses()
	if err != nil {
		log.Info("Failed in fetching all VSes: err", err)
		return
	}
	for _, vs := range vses {
		vs_name := vs["name"].(string)
		if _, ok := tasks[vs_name]; !ok {
			p.DeleteVS(vs)
		}
	}
}
