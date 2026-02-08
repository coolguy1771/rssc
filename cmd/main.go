/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"
	"path/filepath"

	scalesetv1alpha1 "github.com/coolguy1771/rssc/api/v1alpha1"
	"github.com/coolguy1771/rssc/internal/client"
	"github.com/coolguy1771/rssc/internal/constants"
	"github.com/coolguy1771/rssc/internal/controller"
	"github.com/coolguy1771/rssc/internal/listener"
	_ "github.com/coolguy1771/rssc/internal/metrics"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(scalesetv1alpha1.AddToScheme(scheme))
}

type managerFlags struct {
	metricsAddr                                      string
	metricsCertPath, metricsCertName, metricsCertKey string
	webhookCertPath, webhookCertName, webhookCertKey string
	probeAddr                                        string
	enableLeaderElection, secureMetrics, enableHTTP2 bool
}

func parseManagerFlags(zapOpts *zap.Options) managerFlags {
	var f managerFlags
	flag.StringVar(&f.metricsAddr, "metrics-bind-address", "0",
		"The address the metrics endpoint binds to. Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable.")
	flag.StringVar(&f.probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&f.enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager.")
	flag.BoolVar(&f.secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served via HTTPS. Use --metrics-secure=false for HTTP.")
	flag.StringVar(&f.webhookCertPath, "webhook-cert-path", "", "Directory containing the webhook certificate.")
	flag.StringVar(&f.webhookCertName, "webhook-cert-name", "tls.crt", "Webhook certificate file name.")
	flag.StringVar(&f.webhookCertKey, "webhook-cert-key", "tls.key", "Webhook key file name.")
	flag.StringVar(&f.metricsCertPath, "metrics-cert-path", "", "Directory containing the metrics server certificate.")
	flag.StringVar(&f.metricsCertName, "metrics-cert-name", "tls.crt", "Metrics certificate file name.")
	flag.StringVar(&f.metricsCertKey, "metrics-cert-key", "tls.key", "Metrics key file name.")
	flag.BoolVar(&f.enableHTTP2, "enable-http2", false, "Enable HTTP/2 for metrics and webhook servers.")
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	return f
}

func buildTLSOpts(enableHTTP2 bool) []func(*tls.Config) {
	var tlsOpts []func(*tls.Config)
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			setupLog.Info("disabling http/2")
			c.NextProtos = []string{"http/1.1"}
		})
	}
	return tlsOpts
}

func buildWebhookServer(f managerFlags) (webhook.Server, *certwatcher.CertWatcher, error) {
	tlsOpts := buildTLSOpts(f.enableHTTP2)
	webhookTLSOpts := tlsOpts
	var webhookCertWatcher *certwatcher.CertWatcher
	if len(f.webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher",
			"webhook-cert-path", f.webhookCertPath)
		var err error
		webhookCertWatcher, err = certwatcher.New(
			filepath.Join(f.webhookCertPath, f.webhookCertName),
			filepath.Join(f.webhookCertPath, f.webhookCertKey),
		)
		if err != nil {
			return nil, nil, err
		}
		webhookTLSOpts = append(webhookTLSOpts, func(c *tls.Config) {
			c.GetCertificate = webhookCertWatcher.GetCertificate
		})
	}
	return webhook.NewServer(webhook.Options{TLSOpts: webhookTLSOpts}), webhookCertWatcher, nil
}

func buildMetricsOptions(f managerFlags) (metricsserver.Options, *certwatcher.CertWatcher, error) {
	tlsOpts := buildTLSOpts(f.enableHTTP2)
	opts := metricsserver.Options{
		BindAddress:   f.metricsAddr,
		SecureServing: f.secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if f.secureMetrics {
		opts.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	var metricsCertWatcher *certwatcher.CertWatcher
	if len(f.metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher",
			"metrics-cert-path", f.metricsCertPath)
		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(f.metricsCertPath, f.metricsCertName),
			filepath.Join(f.metricsCertPath, f.metricsCertKey),
		)
		if err != nil {
			return metricsserver.Options{}, nil, err
		}
		opts.TLSOpts = append(opts.TLSOpts, func(c *tls.Config) {
			c.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}
	return opts, metricsCertWatcher, nil
}

func buildCacheOptions() (cache.Options, error) {
	req, err := labels.NewRequirement(
		constants.LabelAutoscaleSet,
		selection.Exists,
		nil,
	)
	if err != nil {
		return cache.Options{}, err
	}
	podSelector := labels.NewSelector().Add(*req)
	return cache.Options{
		ByObject: map[k8sclient.Object]cache.ByObject{
			&corev1.Pod{}: {Label: podSelector},
		},
	}, nil
}

func setupControllersAndRunnables(mgr ctrl.Manager) error {
	clientFactory := client.NewClientFactory(mgr.GetClient())
	listenerManager := listener.NewManager(ctrl.Log.WithName("listener-manager"))

	if err := (&controller.AutoscaleSetReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		ClientFactory: clientFactory,
		Recorder:      mgr.GetEventRecorder("autoscaleset-controller"),
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&controller.RunnerSetReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		ClientFactory:   clientFactory,
		ListenerManager: listenerManager,
		Recorder:        mgr.GetEventRecorder("runnerset-controller"),
	}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.Add(listener.NewShutdownRunnable(listenerManager, ctrl.Log.WithName("listener-shutdown"))); err != nil {
		return err
	}
	return mgr.Add(client.NewCacheCleanupRunnable(clientFactory.GetCache(), ctrl.Log.WithName("cache-cleanup")))
}

func addCertWatchersAndHealth(mgr ctrl.Manager, metricsCert, webhookCert *certwatcher.CertWatcher) error {
	if metricsCert != nil {
		if err := mgr.Add(metricsCert); err != nil {
			return err
		}
	}
	if webhookCert != nil {
		if err := mgr.Add(webhookCert); err != nil {
			return err
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	return mgr.AddReadyzCheck("readyz", healthz.Ping)
}

func main() {
	zapOpts := zap.Options{Development: true}
	f := parseManagerFlags(&zapOpts)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	webhookServer, webhookCertWatcher, err := buildWebhookServer(f)
	if err != nil {
		setupLog.Error(err, "Failed to initialize webhook certificate watcher")
		os.Exit(1)
	}
	metricsOpts, metricsCertWatcher, err := buildMetricsOptions(f)
	if err != nil {
		setupLog.Error(err, "failed to initialize metrics certificate watcher")
		os.Exit(1)
	}

	cacheOpts, err := buildCacheOptions()
	if err != nil {
		setupLog.Error(err, "unable to build cache options")
		os.Exit(1)
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Cache:                  cacheOpts,
		Metrics:                metricsOpts,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: f.probeAddr,
		LeaderElection:         f.enableLeaderElection,
		LeaderElectionID:       "scaleset-controller-leader-election",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}
	if err := controller.SetupPodCacheIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
		setupLog.Error(err, "unable to set up pod cache indexes")
		os.Exit(1)
	}
	if err := setupControllersAndRunnables(mgr); err != nil {
		setupLog.Error(err, "unable to set up controllers or runnables")
		os.Exit(1)
	}
	if err := addCertWatchersAndHealth(mgr, metricsCertWatcher, webhookCertWatcher); err != nil {
		setupLog.Error(err, "unable to add cert watchers or health checks")
		os.Exit(1)
	}
	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
