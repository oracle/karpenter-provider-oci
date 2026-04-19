/*
** Karpenter Provider OCI
**
** Copyright (c) 2026 Oracle and/or its affiliates.
** Licensed under the Universal Permissive License v 1.0 as shown at https://oss.oracle.com/licenses/upl/
 */

package kms

import (
	"context"
	"fmt"
	"strings"
	"sync"

	ociv1beta1 "github.com/oracle/karpenter-provider-oci/pkg/apis/v1beta1"
	"github.com/oracle/karpenter-provider-oci/pkg/cache"
	"github.com/oracle/karpenter-provider-oci/pkg/oci"
	"github.com/oracle/oci-go-sdk/v65/common"
	ocikms "github.com/oracle/oci-go-sdk/v65/keymanagement"
)

// TODO: derive second level domain from IMDS
var kmsManagementUrlFormat = "https://%s-management.kms.%s.oraclecloud.com"

type Provider interface {
	ResolveKmsKeyConfig(ctx context.Context,
		kmsKeyConfig *ociv1beta1.KmsKeyConfig) (*KmsKeyResolveResult, error)
}

type DefaultProvider struct {
	clusterCompartmentOcid string
	configProvider         common.ConfigurationProvider
	rateLimiter            *oci.RateLimiter
	kmsOcidCache           *cache.GetOrLoadCache[*ocikms.Key]
	kmsFilterCache         *cache.GetOrLoadCache[[]*ocikms.KeySummary]
	kmsClientCache         map[string]oci.KmsClient
	mutex                  sync.Mutex
}

func NewProvider(ctx context.Context,
	clusterCompartmentOcid string, configProvider common.ConfigurationProvider,
	rateLimiter ...*oci.RateLimiter) (*DefaultProvider, error) {
	var limiter *oci.RateLimiter
	if len(rateLimiter) > 0 {
		limiter = rateLimiter[0]
	}

	p := &DefaultProvider{
		configProvider:         configProvider,
		clusterCompartmentOcid: clusterCompartmentOcid,
		rateLimiter:            limiter,
		kmsOcidCache:           cache.NewDefaultGetOrLoadCache[*ocikms.Key](),
		kmsFilterCache:         cache.NewDefaultGetOrLoadCache[[]*ocikms.KeySummary](),
		kmsClientCache:         make(map[string]oci.KmsClient),
	}

	return p, nil
}

// ResolveKmsKeyConfig return the ocid of the resolved kms (intentionally as kms list API returns summary other keys)
func (p *DefaultProvider) ResolveKmsKeyConfig(ctx context.Context,
	kmsKeyConfig *ociv1beta1.KmsKeyConfig) (*KmsKeyResolveResult, error) {
	if kmsKeyConfig != nil && kmsKeyConfig.KmsKeyId != nil {
		return p.getKmsKey(ctx, *kmsKeyConfig.KmsKeyId)
	}
	return nil, nil
}

func (p *DefaultProvider) getKmsClient(endpoint string) (oci.KmsClient, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	c, ok := p.kmsClientCache[endpoint]
	if ok {
		return c, nil
	} else {
		nc, err := oci.NewKmsClient(p.configProvider, endpoint, p.rateLimiter)
		if err != nil {
			return nil, err
		}
		p.kmsClientCache[endpoint] = nc
		return nc, nil
	}
}

func (p *DefaultProvider) getKmsKey(ctx context.Context, kmsKeyOcid string) (*KmsKeyResolveResult, error) {
	key, err := p.kmsOcidCache.GetOrLoad(ctx, kmsKeyOcid, func(c context.Context, k string) (*ocikms.Key, error) {
		endpoint, err := getKmsEndpointFromKeyId(kmsKeyOcid)
		if err != nil {
			return nil, err
		}
		kmsClient, err := p.getKmsClient(endpoint)
		if err != nil {
			return nil, err
		}
		resp, err := kmsClient.GetKey(ctx, ocikms.GetKeyRequest{KeyId: &kmsKeyOcid})
		if err != nil {
			return nil, err
		}
		return &resp.Key, nil
	})
	if err != nil {
		return nil, err
	}
	return &KmsKeyResolveResult{
		Ocid:        *key.Id,
		DisplayName: *key.DisplayName,
	}, nil
}

func getKmsEndpointFromKeyId(kmsKeyId string) (string, error) {
	// Extract embedded vault prefix from key OCID.
	// KMS key OCIDs have a vault prefix embedded in the extensions part of an ocid.
	// example: ocid1.key.oc1.iad.annnb3f4aacww.abuwcljsyikzy2kj43aneuqdo22xmpum2i2g4bhjy6w5erzn64yvulcdgvgq

	extensions, err := getExtensions(kmsKeyId)
	if err != nil {
		return "", err
	}

	vaultPrefix := extensions[4]
	regionCode := extensions[3]
	region := common.StringToRegion(regionCode)

	// Fill in endpoint template with vault prefix
	endpoint := fmt.Sprintf(kmsManagementUrlFormat, vaultPrefix, string(region))

	return endpoint, nil
}

// getExtensions extracts the extensions from an OCID v2
// OCID format: ocid1.<resource_type>.oc1.<region>.<extension1>.<extension2>...
func getExtensions(ocid string) ([]string, error) {
	parts := strings.Split(ocid, ".")

	// OCID v2 format should have at least 5 parts: ocid1, type, oc1, region, extension(s)
	if len(parts) < 6 {
		return nil, fmt.Errorf("invalid OCID format: %s", ocid)
	}

	return parts, nil
}

func (p *DefaultProvider) SetKmsClient(endpoint string, client oci.KmsClient) {
	p.kmsClientCache[endpoint] = client
}

func (p *DefaultProvider) GetConfigProvider() common.ConfigurationProvider {
	return p.configProvider
}
