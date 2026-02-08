package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	scalesetv1alpha1 "github.com/coolguy1771/rssc/api/v1alpha1"
	"github.com/coolguy1771/rssc/internal/client"
	"github.com/coolguy1771/rssc/internal/controller"
	"github.com/coolguy1771/rssc/internal/listener"
	_ "github.com/coolguy1771/rssc/internal/metrics"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(scalesetv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var leaderElectionNamespace string
	var probeAddr string
	var enableWebhooks bool
	flag.StringVar(
		&metricsAddr,
		"metrics-bind-address",
		":8080",
		"The address the metric endpoint binds to.",
	)
	flag.StringVar(
		&probeAddr,
		"health-probe-bind-address",
		":8081",
		"The address the probe endpoint binds to.",
	)
	flag.BoolVar(
		&enableLeaderElection,
		"leader-elect",
		true,
		"Enable leader election for controller manager. Ensures only one active controller instance.",
	)
	flag.StringVar(
		&leaderElectionNamespace,
		"leader-election-namespace",
		"",
		"Namespace where the leader election resource will be created. Defaults to the pod namespace if running in cluster, or 'default' if not.",
	)
	flag.BoolVar(
		&enableWebhooks,
		"enable-webhooks",
		false,
		"Enable webhooks. Requires TLS certificates when running in-cluster.",
	)
	zapOpts := zap.Options{
		Development: true,
	}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	if leaderElectionNamespace == "" {
		leaderElectionNamespace = "default"
	}

	mgrOpts := ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionNamespace: leaderElectionNamespace,
		LeaderElectionID:        "scaleset-controller-leader-election",
	}

	if enableWebhooks {
		mgrOpts.WebhookServer = webhook.NewServer(webhook.Options{
			Port: 9443,
		})
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	clientFactory := client.NewClientFactory(mgr.GetClient())
	listenerManager := listener.NewManager(ctrl.Log.WithName("listener-manager"))

	shutdownRunnable := listener.NewShutdownRunnable(
		listenerManager,
		ctrl.Log.WithName("shutdown"),
	)
	if err := mgr.Add(shutdownRunnable); err != nil {
		setupLog.Error(err, "unable to add shutdown runnable")
		os.Exit(1)
	}

	cacheCleanupRunnable := client.NewCacheCleanupRunnable(
		clientFactory.GetCache(),
		ctrl.Log.WithName("cache-cleanup"),
	)
	if err := mgr.Add(cacheCleanupRunnable); err != nil {
		setupLog.Error(err, "unable to add cache cleanup runnable")
		os.Exit(1)
	}

	if err = (&controller.AutoscaleSetReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		ClientFactory: clientFactory,
		Recorder:      mgr.GetEventRecorderFor("autoscaleset-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AutoscaleSet")
		os.Exit(1)
	}

	if err = (&controller.RunnerSetReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		ClientFactory:   clientFactory,
		ListenerManager: listenerManager,
		Recorder:        mgr.GetEventRecorderFor("runnerset-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RunnerSet")
		os.Exit(1)
	}

	if enableWebhooks {
		if err = (&scalesetv1alpha1.AutoscaleSet{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "AutoscaleSet")
			os.Exit(1)
		}

		if err = (&scalesetv1alpha1.RunnerSet{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "RunnerSet")
			os.Exit(1)
		}
		setupLog.Info("Webhooks enabled")
	} else {
		setupLog.Info("Webhooks disabled (use --enable-webhooks to enable)")
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager", "metrics-addr", metricsAddr)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
