package main

import (
	"fmt"
	"io/ioutil"
	"path/filepath"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	"github.com/Jorrit05/DYNAMOS/pkg/lib"
	pb "github.com/Jorrit05/DYNAMOS/pkg/proto"
	"go.uber.org/zap"
)

func registerPolicyEnforcerConfiguration() {
	logger.Debug("Start registerPolicyEnforcerConfiguration")
	// Load request types
	var requestsTypes []api.RequestType
	lib.UnmarshalJsonFile(requestTypeConfigLocation, &requestsTypes)

	for _, requestType := range requestsTypes {
		etcd.SaveStructToEtcd[api.RequestType](etcdClient, fmt.Sprintf("/requestTypes/%s", requestType.Name), requestType)
	}

	// Load archetypes
	var archeTypes []api.Archetype
	lib.UnmarshalJsonFile(archetypeConfigLocation, &archeTypes)

	for _, archeType := range archeTypes {
		etcd.SaveStructToEtcd[api.Archetype](etcdClient, fmt.Sprintf("/archetypes/%s", archeType.Name), archeType)
	}

	// Load labels and allowedOutputs (microservice.json)
	var microservices []api.MicroserviceMetadata

	lib.UnmarshalJsonFile(microserviceMetadataConfigLocation, &microservices)

	for _, microservice := range microservices {
		etcd.SaveStructToEtcd[api.MicroserviceMetadata](etcdClient, fmt.Sprintf("/microservices/%s/chainMetadata", microservice.Name), microservice)
	}

	// Load agreemnents  (agreemnents.json)
	var agreements []api.Agreement

	lib.UnmarshalJsonFile(agreementsConfigLocation, &agreements)

	for _, agreement := range agreements {
		etcd.SaveStructToEtcd[api.Agreement](etcdClient, fmt.Sprintf("/policyEnforcer/agreements/%s", agreement.Name), agreement)
	}

	// Load agreemnents  (agreemnents.json)
	var datasets []*pb.Dataset

	lib.UnmarshalJsonFile(dataSetConfigLocation, &datasets)

	for _, dataset := range datasets {
		etcd.SaveStructToEtcd[*pb.Dataset](etcdClient, fmt.Sprintf("/datasets/%s", dataset.Name), dataset)
	}

	// Load   optional_microservices.json
	var optionalServices []api.OptionalServices

	lib.UnmarshalJsonFile(optionalMSConfigLocation, &optionalServices)

	for _, services := range optionalServices {
		for k, msList := range services.Types {
			for _, ms := range msList {
				key := fmt.Sprintf("/agents/%s/requestType/%s/%s ", services.DataSteward, k, ms)
				etcd.PutValueToEtcd(etcdClient, key, ms)
			}
		}
	}

	// Load eFLINT models
	logger.Debug("Loading eFLINT models from directory", zap.String("directory", eflintModelsDirectory))
	eflintFiles, err := ioutil.ReadDir(eflintModelsDirectory)
	if err != nil {
		logger.Error("Failed to read eFLINT models directory", zap.Error(err))
	} else {
		for _, file := range eflintFiles {
			if !file.IsDir() && filepath.Ext(file.Name()) == ".eflint" {
				filePath := filepath.Join(eflintModelsDirectory, file.Name())
				content, readErr := ioutil.ReadFile(filePath)
				if readErr != nil {
					logger.Error("Failed to read eFLINT model file", zap.String("file", file.Name()), zap.Error(readErr))
					continue
				}
				// Extract model name from filename (without .eflint extension)
				modelName := file.Name()[:len(file.Name())-len(".eflint")]
				etcd.PutValueToEtcd(etcdClient, fmt.Sprintf("/policyEnforcer/eflintModels/%s", modelName), string(content))
				logger.Debug("Loaded eFLINT model", zap.String("model", modelName))
			}
		}
	}

	// Load provider configs (provider_configs.json)
	// Note: eFLINT states are bootstrapped on-demand by the policy-enforcer,
	// not by the orchestrator, since the eflint-server binary is only available
	// in the policy-enforcer container.
	var providerConfigs []api.ProviderValidationConfig

	lib.UnmarshalJsonFile(providerConfigsLocation, &providerConfigs)

	for _, config := range providerConfigs {
		etcd.SaveStructToEtcd[api.ProviderValidationConfig](etcdClient, fmt.Sprintf("/policyEnforcer/configs/%s", config.Name), config)
	}
}
