package client

import (
	"context"
	"fmt"

	"github.com/actions/scaleset"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	FinalizerName = "scaleset.actions.github.com/finalizer"
)

type ClientFactory struct {
	k8sClient client.Client
	cache     *Cache
}

func NewClientFactory(k8sClient client.Client) *ClientFactory {
	return &ClientFactory{
		k8sClient: k8sClient,
		cache:     NewCache(k8sClient),
	}
}

type ClientConfig struct {
	GitHubConfigURL string
	GitHubApp       *GitHubAppConfig
	PAT             *PATConfig
	SystemInfo      scaleset.SystemInfo
}

type GitHubAppConfig struct {
	ClientID       string
	InstallationID int64
	PrivateKey     string
}

type PATConfig struct {
	Token string
}

func (f *ClientFactory) CreateClient(ctx context.Context, config ClientConfig) (*scaleset.Client, error) {
	if config.GitHubApp != nil {
		return scaleset.NewClientWithGitHubApp(
			scaleset.ClientWithGitHubAppConfig{
				GitHubConfigURL: config.GitHubConfigURL,
				GitHubAppAuth: scaleset.GitHubAppAuth{
					ClientID:       config.GitHubApp.ClientID,
					InstallationID: config.GitHubApp.InstallationID,
					PrivateKey:     config.GitHubApp.PrivateKey,
				},
				SystemInfo: config.SystemInfo,
			},
		)
	}

	if config.PAT != nil {
		return scaleset.NewClientWithPersonalAccessToken(
			scaleset.NewClientWithPersonalAccessTokenConfig{
				GitHubConfigURL:     config.GitHubConfigURL,
				PersonalAccessToken: config.PAT.Token,
				SystemInfo:          config.SystemInfo,
			},
		)
	}

	return nil, fmt.Errorf("either GitHubApp or PAT must be provided")
}

func (f *ClientFactory) LoadGitHubAppFromSecret(
	ctx context.Context,
	namespace string,
	secretRef types.NamespacedName,
	clientID string,
	installationID int64,
) (*GitHubAppConfig, error) {
	return f.cache.GetGitHubAppConfig(
		ctx,
		namespace,
		secretRef,
		clientID,
		installationID,
	)
}

func (f *ClientFactory) LoadPATFromSecret(
	ctx context.Context,
	namespace string,
	secretRef types.NamespacedName,
) (*PATConfig, error) {
	return f.cache.GetPATConfig(ctx, namespace, secretRef)
}

func (f *ClientFactory) GetCachedScaleSet(
	scaleSetID int,
	getter func() (*scaleset.RunnerScaleSet, error),
) (*scaleset.RunnerScaleSet, error) {
	return f.cache.GetScaleSet(scaleSetID, getter)
}

func (f *ClientFactory) InvalidateSecret(secretRef types.NamespacedName) {
	f.cache.InvalidateSecret(secretRef)
}

func (f *ClientFactory) InvalidateScaleSet(scaleSetID int) {
	f.cache.InvalidateScaleSet(scaleSetID)
}

func (f *ClientFactory) GetCache() *Cache {
	return f.cache
}
