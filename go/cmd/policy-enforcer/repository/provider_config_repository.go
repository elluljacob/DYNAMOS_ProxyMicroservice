package repository

import (
	"encoding/json"
	"fmt"

	"github.com/Jorrit05/DYNAMOS/pkg/api"
	"github.com/Jorrit05/DYNAMOS/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const providerConfigKeyPrefix = "/policyEnforcer/configs/"

// ProviderConfigRepository defines the interface for retrieving provider validation configurations.
// This abstraction allows determining which validation strategy to use for each data provider.
type ProviderConfigRepository interface {
	// GetProviderConfig retrieves the validation configuration for a specific data provider.
	// Returns the config and a boolean indicating if the config was found.
	GetProviderConfig(provider string) (*api.ProviderValidationConfig, bool, error)
}

// EtcdProviderConfigRepository implements ProviderConfigRepository using etcd as the backend.
type EtcdProviderConfigRepository struct {
	client *clientv3.Client
}

// NewEtcdProviderConfigRepository creates a new EtcdProviderConfigRepository.
func NewEtcdProviderConfigRepository(client *clientv3.Client) *EtcdProviderConfigRepository {
	return &EtcdProviderConfigRepository{
		client: client,
	}
}

// GetProviderConfig retrieves the validation configuration for a specific provider from etcd.
func (r *EtcdProviderConfigRepository) GetProviderConfig(provider string) (*api.ProviderValidationConfig, bool, error) {
	key := providerConfigKeyPrefix + provider
	output, err := etcd.GetValueFromEtcd(r.client, key)
	if err != nil {
		return nil, false, fmt.Errorf("error retrieving provider config from etcd for provider %s: %w", provider, err)
	}

	if output == "" {
		return nil, false, nil
	}

	var config api.ProviderValidationConfig
	if err := json.Unmarshal([]byte(output), &config); err != nil {
		return nil, false, fmt.Errorf("error unmarshalling provider config for provider %s: %w", provider, err)
	}

	return &config, true, nil
}
