//
// Copyright (c) 2018
// IOTech
//
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"fmt"
	"github.com/edgexfoundry/edgex-go/pkg/clients/coredata"
	logger "github.com/edgexfoundry/edgex-go/pkg/clients/logging"
	"github.com/edgexfoundry/edgex-go/pkg/clients/metadata"
	"github.com/edgexfoundry/edgex-go/pkg/clients/types"
	consulapi "github.com/hashicorp/consul/api"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// initDependencyClients
// Trigger Service Client Initializer to establish connection to Metadata and Core Data Services through Metadata Client and Core Data Client.
// Service Client Initializer also needs to check the service status of Metadata and Core Data Services, because they are important dependencies of Device Service.
// The initialization process should be pending until Metadata Service and Core Data Service are both available.
func initDependencyClients() {
	initializeLoggingClient()

	checkDependencyServices()

	initializeClients()
}

func initializeLoggingClient() {
	var remoteLog = false
	var logTarget string

	if svc.c.Logging.RemoteURL == "" {
		logTarget = svc.c.Logging.File

	} else if checkRemoteLoggingAvailable() {
		remoteLog = true
		logTarget = svc.c.Logging.RemoteURL
		fmt.Println("Ping remote logging service success, use remote logging.")
	} else {
		logTarget = svc.c.Logging.File
		fmt.Println("Ping remote logging service failed, use log file instead.")
	}

	svc.lc = logger.NewClient(svc.Name, remoteLog, logTarget)
}

func checkRemoteLoggingAvailable() bool {
	var available = true
	fmt.Println("Check Logging service's status ...")

	_, err := http.Get(svc.c.Logging.RemoteURL + apiV1 + "/ping")
	if err != nil {
		fmt.Println(fmt.Sprintf("Error getting ping: %v", err))
		available = false
	}

	return available
}

func checkDependencyServices() {
	var dependencyList = []string{ClientData, ClientMetadata}

	var waitGroup sync.WaitGroup
	waitGroup.Add(len(dependencyList))

	for i := 0; i < len(dependencyList); i++ {
		go func(wg *sync.WaitGroup, serviceName string) {
			checkServiceAvailable(serviceName)
			wg.Done()

		}(&waitGroup, dependencyList[i])

	}

	waitGroup.Wait()
}

func checkServiceAvailable(serviceId string) {
	if svc.useRegistry {
		if !checkServiceAvailableByConsul(svc.c.Clients[serviceId].Name) {
			time.Sleep(10 * time.Second)
			checkServiceAvailable(serviceId)
		}
	} else {
		var err = checkServiceAvailableByPing(serviceId)
		if err, ok := err.(net.Error); ok && err.Timeout() {
			checkServiceAvailable(serviceId)
		} else if err != nil {
			time.Sleep(10 * time.Second)
			checkServiceAvailable(serviceId)
		}
	}
}

func checkServiceAvailableByPing(serviceId string) error {
	svc.lc.Info(fmt.Sprintf("Check %v service's status ...", serviceId))
	host := svc.c.Clients[serviceId].Host
	port := strconv.Itoa(svc.c.Clients[serviceId].Port)
	addr := buildAddr(host, port)
	timeout := int64(svc.c.Clients[serviceId].Timeout) * int64(time.Millisecond)

	client := http.Client{
		Timeout: time.Duration(timeout),
	}

	_, err := client.Get(addr + apiV1 + "/ping")

	if err != nil {
		svc.lc.Error(fmt.Sprintf("Error getting ping: %v ", err))
	}
	return err
}

func checkServiceAvailableByConsul(serviceConsulId string) bool {
	svc.lc.Info(fmt.Sprintf("Check %v service's status by Consul...", serviceConsulId))

	result := false

	isConsulUp := checkConsulAvailable()
	if !isConsulUp {
		return false
	}

	// Get a new client
	var host = svc.c.Registry.Host
	var port = strconv.Itoa(svc.c.Registry.Port)
	var consulAddr = buildAddr(host, port)
	consulConfig := consulapi.DefaultConfig()
	consulConfig.Address = consulAddr
	client, err := consulapi.NewClient(consulConfig)
	if err != nil {
		svc.lc.Error(err.Error())
		return false
	}

	services, _, err := client.Catalog().Service(serviceConsulId, "", nil)
	if err != nil {
		svc.lc.Error(err.Error())
		return false
	}
	if len(services) <= 0 {
		svc.lc.Error(serviceConsulId + " service hasn't started...")
		return false
	}

	healthCheck, _, err := client.Health().Checks(serviceConsulId, nil)
	if err != nil {
		svc.lc.Error(err.Error())
		return false
	}
	status := healthCheck.AggregatedStatus()
	if status == "passing" {
		result = true
	} else {
		svc.lc.Error(serviceConsulId + " service hasn't been available...")
		result = false
	}

	return result
}

func checkConsulAvailable() bool {
	addr := fmt.Sprintf("%v:%v", svc.c.Registry.Host, svc.c.Registry.Port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		svc.lc.Error(fmt.Sprintf("Consul cannot be reached, address: %v and error is \"%v\" ", addr, err.Error()))
		return false
	}
	conn.Close()
	return true
}

func initializeClients() {
	// initialize Core Metadata clients
	metaPort := strconv.Itoa(svc.c.Clients[ClientMetadata].Port)
	metaHost := svc.c.Clients[ClientMetadata].Host
	metaAddr := buildAddr(metaHost, metaPort)
	metaPath := v1Addressable
	metaURL := metaAddr + metaPath

	params := types.EndpointParams{
		// TODO: Can't use edgex-go internal constants!
		//ServiceKey:internal.CoreMetaDataServiceKey,
		ServiceKey:  svc.c.Clients[ClientMetadata].Name,
		Path:        metaPath,
		UseRegistry: svc.useRegistry,
		Url:         metaURL}

	svc.ac = metadata.NewAddressableClient(params, types.Endpoint{})

	params.Path = v1Device
	params.Url = metaAddr + params.Path
	svc.dc = metadata.NewDeviceClient(params, types.Endpoint{})

	params.Path = v1DevService
	params.Url = metaAddr + params.Path
	svc.sc = metadata.NewDeviceServiceClient(params, types.Endpoint{})

	params.Path = v1Deviceprofile
	params.Url = metaAddr + params.Path
	svc.dpc = metadata.NewDeviceProfileClient(params, types.Endpoint{})

	// initialize Core Data clients
	dataPort := strconv.Itoa(svc.c.Clients[ClientData].Port)
	dataHost := svc.c.Clients[ClientData].Host
	dataAddr := buildAddr(dataHost, dataPort)
	dataPath := v1Event
	dataURL := dataAddr + dataPath

	params.ServiceKey = svc.c.Clients[ClientData].Name
	params.Path = dataPath
	params.UseRegistry = svc.useRegistry
	params.Url = dataURL

	svc.ec = coredata.NewEventClient(params, types.Endpoint{})

	params.Path = v1Valuedescriptor
	params.Url = dataAddr + params.Path
	svc.vdc = coredata.NewValueDescriptorClient(params, types.Endpoint{})
}
