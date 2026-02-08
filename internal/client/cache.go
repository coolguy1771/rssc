package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/actions/scaleset"
	"github.com/coolguy1771/rssc/internal/metrics"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultSecretCacheTTL   = 5 * time.Minute
	defaultScaleSetCacheTTL = 1 * time.Minute
)

type cachedSecret struct {
	config    interface{}
	expiresAt time.Time
}

type cachedScaleSet struct {
	scaleSet  *scaleset.RunnerScaleSet
	expiresAt time.Time
}

type Cache struct {
	mu            sync.RWMutex
	secretCache   map[types.NamespacedName]*cachedSecret
	scaleSetCache map[int]*cachedScaleSet
	secretTTL     time.Duration
	scaleSetTTL   time.Duration
	k8sClient     client.Client
}

func NewCache(k8sClient client.Client) *Cache {
	return &Cache{
		secretCache:   make(map[types.NamespacedName]*cachedSecret),
		scaleSetCache: make(map[int]*cachedScaleSet),
		secretTTL:     defaultSecretCacheTTL,
		scaleSetTTL:   defaultScaleSetCacheTTL,
		k8sClient:     k8sClient,
	}
}

func (c *Cache) GetGitHubAppConfig(
	ctx context.Context,
	namespace string,
	secretRef types.NamespacedName,
	clientID string,
	installationID int64,
) (*GitHubAppConfig, error) {
	secretKey := types.NamespacedName{
		Namespace: secretRef.Namespace,
		Name:      secretRef.Name,
	}
	if secretRef.Namespace == "" {
		secretKey.Namespace = namespace
	}

	c.mu.RLock()
	cached, exists := c.secretCache[secretKey]
	c.mu.RUnlock()

	if exists && time.Now().Before(cached.expiresAt) {
		if config, ok := cached.config.(*GitHubAppConfig); ok {
			metrics.CacheHits.WithLabelValues("secret").Inc()
			return config, nil
		}
	}
	metrics.CacheMisses.WithLabelValues("secret").Inc()

	secret := &corev1.Secret{}
	if err := c.k8sClient.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("failed to get secret %s: %w", secretKey, err)
	}

	keyName := "privateKey"
	if len(secret.Data) > 0 {
		for k := range secret.Data {
			if k == "privateKey" || k == "private_key" || k == "key" {
				keyName = k
				break
			}
		}
	}

	privateKey, ok := secret.Data[keyName]
	if !ok {
		return nil, fmt.Errorf("secret %s does not contain private key", secretKey)
	}

	config := &GitHubAppConfig{
		ClientID:       clientID,
		InstallationID: installationID,
		PrivateKey:     string(privateKey),
	}

	c.mu.Lock()
	c.secretCache[secretKey] = &cachedSecret{
		config:    config,
		expiresAt: time.Now().Add(c.secretTTL),
	}
	c.mu.Unlock()

	return config, nil
}

func (c *Cache) GetPATConfig(
	ctx context.Context,
	namespace string,
	secretRef types.NamespacedName,
) (*PATConfig, error) {
	secretKey := types.NamespacedName{
		Namespace: secretRef.Namespace,
		Name:      secretRef.Name,
	}
	if secretRef.Namespace == "" {
		secretKey.Namespace = namespace
	}

	c.mu.RLock()
	cached, exists := c.secretCache[secretKey]
	c.mu.RUnlock()

	if exists && time.Now().Before(cached.expiresAt) {
		if config, ok := cached.config.(*PATConfig); ok {
			metrics.CacheHits.WithLabelValues("secret").Inc()
			return config, nil
		}
	}
	metrics.CacheMisses.WithLabelValues("secret").Inc()

	secret := &corev1.Secret{}
	if err := c.k8sClient.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("failed to get secret %s: %w", secretKey, err)
	}

	keyName := "token"
	if len(secret.Data) > 0 {
		for k := range secret.Data {
			if k == "token" || k == "pat" || k == "accessToken" {
				keyName = k
				break
			}
		}
	}

	token, ok := secret.Data[keyName]
	if !ok {
		return nil, fmt.Errorf("secret %s does not contain token", secretKey)
	}

	config := &PATConfig{
		Token: string(token),
	}

	c.mu.Lock()
	c.secretCache[secretKey] = &cachedSecret{
		config:    config,
		expiresAt: time.Now().Add(c.secretTTL),
	}
	c.mu.Unlock()

	return config, nil
}

func (c *Cache) InvalidateSecret(secretRef types.NamespacedName) {
	c.mu.Lock()
	delete(c.secretCache, secretRef)
	c.mu.Unlock()
}

func (c *Cache) GetScaleSet(
	scaleSetID int,
	getter func() (*scaleset.RunnerScaleSet, error),
) (*scaleset.RunnerScaleSet, error) {
	c.mu.RLock()
	cached, exists := c.scaleSetCache[scaleSetID]
	c.mu.RUnlock()

	if exists && time.Now().Before(cached.expiresAt) {
		metrics.CacheHits.WithLabelValues("scaleset").Inc()
		return cached.scaleSet, nil
	}
	metrics.CacheMisses.WithLabelValues("scaleset").Inc()

	scaleSet, err := getter()
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.scaleSetCache[scaleSetID] = &cachedScaleSet{
		scaleSet:  scaleSet,
		expiresAt: time.Now().Add(c.scaleSetTTL),
	}
	c.mu.Unlock()

	return scaleSet, nil
}

func (c *Cache) InvalidateScaleSet(scaleSetID int) {
	c.mu.Lock()
	delete(c.scaleSetCache, scaleSetID)
	c.mu.Unlock()
}

func (c *Cache) Cleanup() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, cached := range c.secretCache {
		if now.After(cached.expiresAt) {
			delete(c.secretCache, key)
		}
	}

	for key, cached := range c.scaleSetCache {
		if now.After(cached.expiresAt) {
			delete(c.scaleSetCache, key)
		}
	}
}
