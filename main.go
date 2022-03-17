package main

import (
	//"fmt"
	"log"
	"os"
	"strings"

	"context"

	"github.com/go-logr/logr"
	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	//Try to do some MCO stuff

	"github.com/fatih/color"

	//_ "github.com/prometheus/client_golang/bestutil"

	// build an image with libcontainer

	// add build configs
	buildv1 "github.com/openshift/api/build/v1"
	imagev1 "github.com/openshift/api/image/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

var yellow = color.New(color.FgYellow).SprintFunc()
var red = color.New(color.FgRed).SprintFunc()
var green = color.New(color.FgGreen, color.Bold).SprintFunc()

var kclient client.Client
var options = &client.ListOptions{
	//LabelSelector: nil,
	//FieldSelector: nil,
	Namespace: "",
	//Limit:         0,
	//Continue:      "",
	//Raw:           &v1.ListOptions{},
}

func init() {

}

func GetClient() (client.Client, *kubernetes.Clientset) {

	// We need a scheme apparently
	scheme := runtime.NewScheme()

	// And since we're going to deal with these types of things, we add them to the scheme
	mcfgv1.AddToScheme(scheme)
	corev1.AddToScheme(scheme)
	buildv1.AddToScheme(scheme)
	imagev1.AddToScheme(scheme)
	kubeconfig := ctrl.GetConfigOrDie()

	//TODO: need to make this an option
	kubeconfig.Insecure = true
	// You have to remove CAData if you want to use insecure
	kubeconfig.CAData = []byte{}
	controllerClient, err := client.New(kubeconfig, client.Options{Scheme: scheme})
	if err != nil {
		log.Fatal(err)
		return nil, nil
	}
	clientset, err := kubernetes.NewForConfig(kubeconfig)
	if err != nil {
		log.Fatal(err)
		return nil, nil
	}

	return controllerClient, clientset
}

func main() {

	kclient, clientset := GetClient()

	err := managerStuffNodeOnly(kclient, *clientset)
	if err != nil {
		panic(err)
	}

	return

}

/* =========================== BUILDCONFIG  =========================*/

type BuildConfigReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	recorder record.EventRecorder
}

func (b *BuildConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	buildLog := ctrl.Log.WithValues("buildconfig", req.NamespacedName)
	buildLog.Info("I'm in the build reconciler!")
	var bc buildv1.BuildConfig

	if err := b.Get(ctx, req.NamespacedName, &bc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		buildLog.Error(err, "unable to fetch BuildConfig")
		return ctrl.Result{}, err
	}

	buildLog.Info("has builds", bc.Name, bc.Spec.Output.To)

	return ctrl.Result{}, nil
}

func (b *BuildConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Register an event recorder so we can emit events
	b.recorder = mgr.GetEventRecorderFor("Node")

	// Register this controller with the manager
	return ctrl.NewControllerManagedBy(mgr).
		For(&buildv1.BuildConfig{}).
		Complete(b)

}

/* =========================== IMAGE STREAM =========================*/

type ImageStreamReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	recorder record.EventRecorder
}

func (is *ImageStreamReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	imageLog := ctrl.Log.WithValues("imagestream", req.NamespacedName)
	imageLog.Info("I'm in the imagestream reconciler!")
	var ic imagev1.ImageStream

	if err := is.Get(ctx, req.NamespacedName, &ic); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		imageLog.Error(err, "unable to fetch ImageStream")
		return ctrl.Result{}, err
	}

	var latestImage imagev1.TagEvent
	for _, tag := range ic.Status.Tags {
		for _, image := range tag.Items {
			imageLog.Info("has rendered", ic.Name, image.DockerImageReference)
			latestImage = image
			// right now I only want the first one
			break
		}
	}

	for _, owner := range ic.OwnerReferences {
		if owner.Kind == "MachineConfigPool" {
			if strings.HasPrefix(ic.Name, "mco-content") {
				// Get machine config pool
				// annotate machine config pool
				var mcp mcfgv1.MachineConfigPool

				err := is.Get(ctx, types.NamespacedName{Namespace: "openshift-machine-config-operator", Name: owner.Name}, &mcp)
				if apierrors.IsNotFound(err) {
					return ctrl.Result{}, nil
				}

				if imageReference, ok := mcp.Annotations["machineconfiguration.openshift.io/newest-layered-image"]; !ok || imageReference != latestImage.DockerImageReference {
					mcp.Annotations["machineconfiguration.openshift.io/newest-layered-image"] = latestImage.DockerImageReference
					mcp.Spec.Configuration.Name = latestImage.DockerImageReference
					err = is.Update(ctx, &mcp)
					if err != nil {
						return ctrl.Result{}, nil
					}
					mcp.Namespace = "openshift-machine-config-operator"
					is.recorder.Event(&mcp, corev1.EventTypeNormal, "Updated", "Moved pool "+mcp.Name+" to image "+latestImage.DockerImageReference)
				}
			}
		}
	}

	// look up owner pool
	// add annotation that the latest-image from the stream is that image
	// nodes in "layered" won't reconcile until that changes?
	// or do we just have them wait until the image gets updated?

	// when the imagestream reconciles with a new build, the pool needs to know so it can set the proper annotation
	// but honestly if we just annotate it here --> as in, look up the pool owner(s),

	// a new build won't roll a new pool event, which is why the pool can't update
	// unless we do something to the pool like annotate it, which will be good, the node-controller can handle that ?

	return ctrl.Result{}, nil
}

func (is *ImageStreamReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Register an event recorder so we can emit events
	is.recorder = mgr.GetEventRecorderFor("Node")

	// Register this controller with the manager
	return ctrl.NewControllerManagedBy(mgr).
		For(&imagev1.ImageStream{}).
		Complete(is)

}

/* =========================== IMAGE =========================*/

type ImageReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	recorder record.EventRecorder
}

func (is *ImageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	imageLog := ctrl.Log.WithValues("image", req.NamespacedName)
	imageLog.Info("I'm in the image reconciler!")
	var ic imagev1.Image

	if err := is.Get(ctx, req.NamespacedName, &ic); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		imageLog.Error(err, "unable to fetch ImageStream")
		return ctrl.Result{}, err
	}

	imageLog.Info("image rendered", ic.Namespace, ic.Name)

	// look up owner pool
	// add annotation that the latest-image from the stream is that image
	// nodes in "layered" won't reconcile until that changes?
	// or do we just have them wait until the image gets updated?

	// when the imagestream reconciles with a new build, the pool needs to know so it can set the proper annotation
	// but honestly if we just annotate it here --> as in, look up the pool owner(s),

	// a new build won't roll a new pool event, which is why the pool can't update
	// unless we do something to the pool like annotate it, which will be good, the node-controller can handle that ?

	return ctrl.Result{}, nil
}

func (is *ImageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Register an event recorder so we can emit events
	is.recorder = mgr.GetEventRecorderFor("Image")

	// Register this controller with the manager
	return ctrl.NewControllerManagedBy(mgr).
		For(&imagev1.Image{}).
		Complete(is)

}

/* =========================== NODE =========================*/

type NodeReconciler struct {
	client.Client
	kubernetes.Clientset
	Log      logr.Logger
	Scheme   *runtime.Scheme
	recorder record.EventRecorder
}

func (nr *NodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	nodeLog := ctrl.Log.WithValues("node", req.NamespacedName)
	nodeLog.Info("I'm in the node reconciler!")
	var n corev1.Node

	if err := nr.Get(ctx, req.NamespacedName, &n); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		nodeLog.Error(err, "unable to fetch Node")
		return ctrl.Result{}, err
	}

	nodeLog.Info("node updated", n.Namespace, n.Name)
	for key, value := range n.Annotations {
		if strings.HasPrefix(key, "machineconfiguration.openshift.io") {
			nodeLog.Info("anno", key, value)
		}
	}
	/*
	serviceServingSignerCA, err := nr.Clientset.CoreV1().ConfigMaps("openshift-config").Get(context.TODO(), "openshift-service-ca.crt", metav1.GetOptions{})
	if err != nil {
		fmt.Errorf("Failed to retrieve the openshift-service-ca.crt configmap: %w", err)
	}
	fmt.Printf("CERT: %s", serviceServingSignerCA)
	*/
	return ctrl.Result{}, nil
}

func (nr *NodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Register an event recorder so we can emit events
	nr.recorder = mgr.GetEventRecorderFor("Node")

	// Register this controller with the manager
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Complete(nr)

}

/* =========================== POOL =========================*/

type PoolReconciler struct {
	client.Client
	kubernetes.Clientset
	Log      logr.Logger
	Scheme   *runtime.Scheme
	recorder record.EventRecorder
}

func (pr *PoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	poolLog := ctrl.Log.WithValues("pool", req.NamespacedName)
	poolLog.Info("I'm in the pool reconciler!")
	var p mcfgv1.MachineConfigPool

	if err := pr.Get(ctx, req.NamespacedName, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		poolLog.Error(err, "unable to fetch Node")
		return ctrl.Result{}, err
	}

	if p.Name == "layered" {

		// The the trust for the repo serving cert is not in our system trust by default, but we know what signed it, so we
		// get the pubkey for that and add it to our tls config so we trust it

		poolLog.Info("pool updated", p.Namespace, p.Name)

		// So we grab the selector
		selector, err := metav1.LabelSelectorAsSelector(p.Spec.NodeSelector)
		if err != nil {
			return ctrl.Result{}, err
		}

		// Then we get the list of nodes
		nodes, err := pr.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return ctrl.Result{}, err
		}

		// Then we go through the nodes and see which ones match
		for _, node := range nodes.Items {
			poolLog.Info("Found node", "node", node.Name)
			// If a pool with a nil or empty selector creeps in, it should match nothing, not everything.
			if selector.Empty() || !selector.Matches(labels.Set(node.Labels)) {
				continue
			}
			// If we made it here, the node belongs to this pool, so report it
			poolLog.Info("Layering owns this node", "node", node.Name)

			desiredImage := node.Annotations["machineconfiguration.openshift.io/desired-layered-image"]
			currentImage := node.Annotations["machineconfiguration.openshift.io/current-layered-image"]

			// Annotate the pool if it's not already annotated
			if desiredImage != p.Annotations["machineconfiguration.openshift.io/newest-layered-image"] {
				poolLog.Info("Need to update node", "current", currentImage, "desired", desiredImage)
				node.Annotations["machineconfiguration.openshift.io/desired-layered-image"] = p.Annotations["machineconfiguration.openshift.io/newest-layered-image"]
				err := pr.Update(ctx, &node)
				if err != nil {
					return ctrl.Result{}, err
				}
			}

			if desiredImage != "" && currentImage != desiredImage {
				poolLog.Info("Node needs to update, but hasn't yet :(", "node", node.Name)
			}

		}
	}
	// Well of course this emits every time you start it, it has to reconcile everything that's already there :)
	//p.Namespace = "openshift-machine-config-operator"
	//pr.recorder.Event(&p, corev1.EventTypeNormal, "Updated", "Pool was updated!")

	return ctrl.Result{}, nil
}

func (pr *PoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Register an event recorder so we can emit events
	pr.recorder = mgr.GetEventRecorderFor("MachineConfigPool")

	// Register this controller with the manager
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcfgv1.MachineConfigPool{}).
		Complete(pr)

}

func managerStuff(theClient client.Client, theClientset kubernetes.Clientset) error {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true})))

	setupLog := ctrl.Log.WithName("setup")
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:    theClient.Scheme(),
		Namespace: "openshift-machine-config-operator",
		//	MetricsBindAddress:     metricsAddr,
		//	Port:                   9443,
		//HealthProbeBindAddress: probeAddr,
		//	LeaderElection:         enableLeaderElection,
		//	LeaderElectionID:       "d84d636a.padok.fr",
	})

	// BuildConfigs
	bcr := &BuildConfigReconciler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("controllers").WithName("BuildConfig"),
		Scheme: mgr.GetScheme(),
	}

	err = bcr.SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Image Streams
	isr := &ImageStreamReconciler{
		Client: mgr.GetClient(),
		Log:    ctrl.Log.WithName("controllers").WithName("ImageStream"),
		Scheme: mgr.GetScheme(),
	}

	// Set up the controller
	err = isr.SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Nodes
	nr := &NodeReconciler{
		Client:    mgr.GetClient(),
		Clientset: theClientset,
		Log:       ctrl.Log.WithName("controllers").WithName("ImageStream"),
		Scheme:    mgr.GetScheme(),
	}

	// Set up the controller
	err = nr.SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Nodes
	pr := &PoolReconciler{
		Client:    mgr.GetClient(),
		Clientset: theClientset,
		Log:       ctrl.Log.WithName("controllers").WithName("ImageStream"),
		Scheme:    mgr.GetScheme(),
	}

	// Set up the controller
	err = pr.SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}

	return nil
}

func managerStuffNodeOnly(theClient client.Client, theClientset kubernetes.Clientset) error {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true})))

	setupLog := ctrl.Log.WithName("setup")
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:    theClient.Scheme(),
		Namespace: "openshift-machine-config-operator",
		//	MetricsBindAddress:     metricsAddr,
		//	Port:                   9443,
		//HealthProbeBindAddress: probeAddr,
		//	LeaderElection:         enableLeaderElection,
		//	LeaderElectionID:       "d84d636a.padok.fr",
	})

	// Nodes
	nr := &NodeReconciler{
		Client:    mgr.GetClient(),
		Clientset: theClientset,
		Log:       ctrl.Log.WithName("controllers").WithName("ImageStream"),
		Scheme:    mgr.GetScheme(),
	}

	// Set up the controller
	err = nr.SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}

	return nil
}
