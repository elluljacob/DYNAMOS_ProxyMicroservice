package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
)

func registerAgent() {
	// Prepare agent configuration data
	// var service lib.MicroServiceData = lib.UnmarshalStackFile("/var/log/stack-files/agents.yaml")
	now := time.Now()
	agentConfig = lib.AgentDetails{
		Name:          serviceName,
		ActiveSince:   &now,
		ConfigUpdated: &now,
		RoutingKey:    serviceName + "-in",
		Dns:           fmt.Sprintf("%s.%s.svc.cluster.local", strings.ToLower(serviceName), strings.ToLower(serviceName)),
	}

	// Serialise agent configuration data as JSON
	configData, err := json.Marshal(agentConfig)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		for {
			if err := etcd.PutEtcdWithLease(etcdClient, fmt.Sprintf("/agents/online/%s", agentConfig.Name), string(configData)); err != nil {
				logger.Sugar().Errorf("Failed to register agent: %v, retrying in 5s", err)
				time.Sleep(5 * time.Second)
				continue
			}
			logger.Sugar().Infof("Successfully registered %s in etcd", agentConfig.Name)
			break
		}
	}()
}

func updateAgent() {
	// Update the ActiveSince field
	now := time.Now()
	agentConfig.ConfigUpdated = &now

	// Serialise agent configuration data as JSON
	configData, err := json.Marshal(agentConfig)
	if err != nil {
		log.Fatal(err)
	}

	go etcd.PutEtcdWithLease(etcdClient, fmt.Sprintf("/agents/online/%s", agentConfig.Name), string(configData))

}
