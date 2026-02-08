package client

import (
	"context"
	"fmt"

	"github.com/actions/scaleset"
	scalesetv1alpha1 "github.com/coolguy1771/rssc/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClientFactory struct {
	k8sClient   client.Client
	cache       *Cache
	rateLimiter *RateLimiter
}

type ClientFactoryOption func(*ClientFactory)

func WithRateLimiter(rps float64, burst int) ClientFactoryOption {
	return func(f *ClientFactory) {
		f.rateLimiter = NewRateLimiter(rps, burst)
	}
}

func NewClientFactory(k8sClient client.Client, opts ...ClientFactoryOption) *ClientFactory {
	f := &ClientFactory{
		k8sClient: k8sClient,
		cache:     NewCache(k8sClient),
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

func (f *ClientFactory) GetRateLimiter() *RateLimiter {
	return f.rateLimiter
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

// Subsystem identifies the caller for GitHub API telemetry.
type Subsystem string

const (
	SubsystemController Subsystem = "controller"
	SubsystemListener   Subsystem = "listener"
)

// CreateClientForAutoscaleSet builds a scaleset client from an AutoscaleSet spec and credentials.
// subsystem is used for SystemInfo (e.g. SubsystemController, SubsystemListener).
func (f *ClientFactory) CreateClientForAutoscaleSet(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	subsystem Subsystem,
) (*scaleset.Client, error) {
	config := ClientConfig{
		GitHubConfigURL: autoscaleSet.Spec.GitHubConfigURL,
		SystemInfo: scaleset.SystemInfo{
			System:    "actions-runner-controller",
			Version:   "v1alpha1",
			CommitSHA: "unknown",
			Subsystem: string(subsystem),
		},
	}

	if autoscaleSet.Spec.GitHubApp != nil {
		appConfig, err := f.LoadGitHubAppFromSecret(
			ctx,
			autoscaleSet.Namespace,
			types.NamespacedName{
				Namespace: autoscaleSet.Spec.GitHubApp.PrivateKeySecretRef.Namespace,
				Name:      autoscaleSet.Spec.GitHubApp.PrivateKeySecretRef.Name,
			},
			autoscaleSet.Spec.GitHubApp.ClientID,
			autoscaleSet.Spec.GitHubApp.InstallationID,
		)
		if err != nil {
			return nil, err
		}
		config.GitHubApp = appConfig
	} else if autoscaleSet.Spec.PersonalAccessToken != nil {
		patConfig, err := f.LoadPATFromSecret(
			ctx,
			autoscaleSet.Namespace,
			types.NamespacedName{
				Namespace: autoscaleSet.Spec.PersonalAccessToken.TokenSecretRef.Namespace,
				Name:      autoscaleSet.Spec.PersonalAccessToken.TokenSecretRef.Name,
			},
		)
		if err != nil {
			return nil, err
		}
		config.PAT = patConfig
	} else {
		return nil, fmt.Errorf("either GitHubApp or PersonalAccessToken must be specified")
	}

	c, err := f.CreateClient(ctx, config)
	if err != nil {
		return nil, err
	}

	if autoscaleSet.Status.ScaleSetID != nil {
		c.SetSystemInfo(scaleset.SystemInfo{
			System:     "actions-runner-controller",
			Version:    "v1alpha1",
			CommitSHA:  "unknown",
			ScaleSetID: *autoscaleSet.Status.ScaleSetID,
			Subsystem:  string(subsystem),
		})
	}

	return c, nil
}
