/*
Copyright 2021.

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

package controllers

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"

	routev1 "github.com/openshift/api/route/v1"
	"go.uber.org/zap/zapcore"

	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	brokerv1alpha1 "github.com/artemiscloud/activemq-artemis-operator/api/v1alpha1"
	brokerv1beta1 "github.com/artemiscloud/activemq-artemis-operator/api/v1beta1"
	brokerv2alpha1 "github.com/artemiscloud/activemq-artemis-operator/api/v2alpha1"
	brokerv2alpha3 "github.com/artemiscloud/activemq-artemis-operator/api/v2alpha3"
	brokerv2alpha4 "github.com/artemiscloud/activemq-artemis-operator/api/v2alpha4"
	brokerv2alpha5 "github.com/artemiscloud/activemq-artemis-operator/api/v2alpha5"

	//+kubebuilder:scaffold:imports

	"github.com/artemiscloud/activemq-artemis-operator/pkg/utils/common"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

// Define utility constants for object names and testing timeouts/durations and intervals.
const (
	defaultNamespace        = "default"
	otherNamespace          = "other"
	timeout                 = time.Second * 30
	duration                = time.Second * 10
	interval                = time.Millisecond * 500
	existingClusterTimeout  = time.Second * 180
	existingClusterInterval = time.Second * 2
	verbose                 = false
	namespace1              = "namespace1"
	namespace2              = "namespace2"
	namespace3              = "namespace3"
)

var (
	testCount  int64
	currentDir string
	k8sClient  client.Client
	restConfig *rest.Config
	testEnv    *envtest.Environment
	ctx        context.Context
	cancel     context.CancelFunc

	// the manager may be stopped/restarted via tests
	managerCtx    context.Context
	managerCancel context.CancelFunc

	stateManager *common.StateManager

	brokerReconciler   *ActiveMQArtemisReconciler
	securityReconciler *ActiveMQArtemisSecurityReconciler

	crdRes = []string{
		"../deploy/install/010_crd_artemis.yaml",
		"../deploy/install/020_crd_artemis_security.yaml",
		"../deploy/install/030_crd_artemis_address.yaml",
		"../deploy/install/040_crd_artemis_scaledown.yaml",
	}

	oprRes = []string{
		"../deploy/install/050_service_account.yaml",
		"../deploy/install/060_namespace_role.yaml",
		"../deploy/install/070_namespace_role_binding.yaml",
		"../deploy/install/080_election_role.yaml",
		"../deploy/install/090_election_role_binding.yaml",
		"../deploy/install/100_operator_config.yaml",
		"../deploy/install/110_operator.yaml",
	}
	managerChannel chan struct{}

	logBuffer *bytes.Buffer

	artemisGvk = schema.GroupVersionKind{Group: "broker", Version: "v1beta1", Kind: "ActiveMQArtemis"}
)

func TestAPIs(t *testing.T) {

	RegisterFailHandler(Fail)

	suiteConfig, _ := GinkgoConfiguration()
	suiteConfig.Timeout = time.Duration(duration.Minutes()) * 20
	RunSpecs(t, "Controller Suite", suiteConfig)
}

func setUpEnvTest() {
	// for run in ide
	// os.Setenv("KUBEBUILDER_ASSETS", " .. <path from makefile> /kubebuilder-envtest/k8s/1.22.1-linux-amd64")
	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	testEnv.CRDInstallOptions.CleanUpAfterUse = true

	var err error
	restConfig, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(restConfig).NotTo(BeNil())

	setUpK8sClient()

	setUpTestProxy()

	stateManager = common.GetStateManager()

	createControllerManagerForSuite()
}

// Set up test-proxy for external http requests
func setUpTestProxy() {

	var err error
	var testProxyPort int32 = 3128
	var testProxyNodePort int32 = 30128
	var testProxyDeploymentReplicas int32 = 1
	testProxyLabels := map[string]string{"app": "test-proxy"}

	customConfig := corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "k8s.io.api.core.v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "squid-conf",
			Namespace: defaultNamespace,
		},
		Data: map[string]string{"squid.conf": `http_port 3128
		 acl all src all
		 http_access allow all
		 pid_filename   none
		 logfile_rotate 0
		 access_log     stdio:/dev/stdout
		 cache_log      stdio:/dev/stderr`,
		},
	}

	err = k8sClient.Create(ctx, &customConfig)
	Expect(err != nil || errors.IsConflict(err))

	testProxyDeployment := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: defaultNamespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: testProxyLabels,
			},
			Replicas: &testProxyDeploymentReplicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: testProxyLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "ningx",
							Image: "ubuntu/squid:edge",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: testProxyPort,
									Protocol:      "TCP",
								},
							},
							/* not necessary at the moment, image defaults are fine
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "squid-conf",
									MountPath: "/etc/squid/squid.conf",
									SubPath:   "squid.conf",
									ReadOnly:  true,
								},
							},
							*/
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "squid-conf",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{

									LocalObjectReference: corev1.LocalObjectReference{
										Name: "squid-conf",
									},
									Items: []corev1.KeyToPath{
										{
											Key:  "squid.conf",
											Path: "squid.conf",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err = k8sClient.Create(ctx, &testProxyDeployment)
	Expect(err != nil || errors.IsConflict(err))

	testProxyService := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: defaultNamespace,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: testProxyLabels,
			Ports: []corev1.ServicePort{
				{
					Port:       testProxyPort,
					TargetPort: intstr.IntOrString{IntVal: testProxyPort},
					NodePort:   testProxyNodePort,
				},
			},
		},
	}

	err = k8sClient.Create(ctx, &testProxyService)
	Expect(err != nil || errors.IsConflict(err))

	clusterUrl, err := url.Parse(testEnv.Config.Host)
	Expect(err).NotTo(HaveOccurred())

	proxyUrl, err := url.Parse(fmt.Sprintf("http://%s:%d", clusterUrl.Hostname(), testProxyNodePort))
	Expect(err).NotTo(HaveOccurred())

	http.DefaultTransport = &http.Transport{Proxy: http.ProxyURL(proxyUrl)}
}

func cleanUpTestProxy() {
	var err error

	testProxyDeployment := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: defaultNamespace,
		},
	}

	err = k8sClient.Delete(ctx, &testProxyDeployment)
	Expect(err != nil || errors.IsNotFound(err))

	testProxyService := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: defaultNamespace,
		},
	}

	err = k8sClient.Delete(ctx, &testProxyService)
	Expect(err != nil || errors.IsNotFound(err))

	customConfig := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "squid-conf",
			Namespace: defaultNamespace,
		},
	}
	err = k8sClient.Delete(ctx, &customConfig)
	Expect(err != nil || errors.IsNotFound(err))
}

func createControllerManagerForSuite() {
	createControllerManager(false, "")
}

func createControllerManager(disableMetrics bool, watchNamespace string) {

	managerCtx, managerCancel = context.WithCancel(ctx)

	mgrOptions := ctrl.Options{
		Scheme: scheme.Scheme,
	}

	isLocal, watchList := common.ResolveWatchNamespaceForManager(defaultNamespace, watchNamespace)
	if isLocal {
		logf.Log.Info("setting up operator to watch local namespace")
		mgrOptions.Namespace = defaultNamespace
	} else {
		mgrOptions.Namespace = ""
		if watchList != nil {
			logf.Log.Info("setting up operator to watch multiple namespaces", "namespace(s)", watchList)
			mgrOptions.NewCache = cache.MultiNamespacedCacheBuilder(watchList)
		} else {
			logf.Log.Info("setting up operator to watch all namespaces")
		}
	}

	if disableMetrics {
		// if we can shutdown metrics port, we don't need disable it.
		mgrOptions.MetricsBindAddress = "0"
	}

	waitforever := time.Duration(-1)
	mgrOptions.GracefulShutdownTimeout = &waitforever
	mgrOptions.LeaderElectionReleaseOnCancel = true

	// start our controler
	k8Manager, err := ctrl.NewManager(restConfig, mgrOptions)
	Expect(err).ToNot(HaveOccurred())

	// Create and start a new auto detect process for this operator
	autodetect, err := common.NewAutoDetect(k8Manager)
	if err != nil {
		logf.Log.Error(err, "failed to start the background process to auto-detect the operator capabilities")
	} else {
		autodetect.DetectOpenshift()
	}

	brokerReconciler = &ActiveMQArtemisReconciler{
		Client: k8Manager.GetClient(),
		Scheme: k8Manager.GetScheme(),
	}

	if err = brokerReconciler.SetupWithManager(k8Manager); err != nil {
		logf.Log.Error(err, "unable to create controller", "controller", "ActiveMQArtemisReconciler")
	}

	securityReconciler = &ActiveMQArtemisSecurityReconciler{
		Client:           k8Manager.GetClient(),
		Scheme:           k8Manager.GetScheme(),
		BrokerReconciler: brokerReconciler,
	}

	err = securityReconciler.SetupWithManager(k8Manager)
	Expect(err).ToNot(HaveOccurred(), "failed to create security controller")

	addressReconciler := &ActiveMQArtemisAddressReconciler{
		Client: k8Manager.GetClient(),
		Scheme: k8Manager.GetScheme(),
	}

	err = addressReconciler.SetupWithManager(k8Manager, managerCtx)
	Expect(err).ToNot(HaveOccurred(), "failed to create address reconciler")

	scaleDownRconciler := &ActiveMQArtemisScaledownReconciler{
		Client: k8Manager.GetClient(),
		Scheme: k8Manager.GetScheme(),
		Config: k8Manager.GetConfig(),
	}

	err = scaleDownRconciler.SetupWithManager(k8Manager)
	Expect(err).ShouldNot(HaveOccurred(), "failed to create scale down reconciler")

	managerChannel = make(chan struct{}, 1)
	go func() {
		defer GinkgoRecover()
		err = k8Manager.Start(managerCtx)
		managerChannel <- struct{}{}
		Expect(err).ToNot(HaveOccurred(), "failed to run manager")
	}()
}

func shutdownControllerManager() {
	managerCancel()
	// wait for start routine to exit
	<-managerChannel
}

func setUpRealOperator() {
	var err error
	restConfig, err = config.GetConfig()
	Expect(err).ShouldNot(HaveOccurred(), "failed to get the config")

	setUpK8sClient()

	logf.Log.Info("Installing CRDs")
	options := envtest.CRDInstallOptions{
		Paths:              crdRes,
		ErrorIfPathMissing: false,
	}
	_, err = envtest.InstallCRDs(restConfig, options)
	Expect(err).To(Succeed())

	err = installOperator()
	Expect(err).To(Succeed(), "failed to install operator")
}

func installOperator() error {
	logf.Log.Info("#### Installing Operator ####")
	for _, res := range oprRes {
		if err := installYamlResource(res); err != nil {
			return err
		}
	}

	return waitForOperator()
}

func uninstallOperator() error {
	logf.Log.Info("#### Uninstalling Operator ####")
	for _, res := range oprRes {
		if err := uninstallYamlResource(res); err != nil {
			return err
		}
	}

	//uninstall CRDs
	logf.Log.Info("Uninstalling CRDs")
	options := envtest.CRDInstallOptions{
		Paths:              crdRes,
		ErrorIfPathMissing: false,
	}
	return envtest.UninstallCRDs(restConfig, options)
}

func waitForOperator() error {
	podList := &corev1.PodList{}
	opts := &client.ListOptions{
		Namespace: defaultNamespace,
	}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.List(ctx, podList, opts)).Should(Succeed())

		g.Expect(len(podList.Items)).Should(BeEquivalentTo(1))
		oprPod := podList.Items[0]
		g.Expect(len(oprPod.Status.ContainerStatuses)).Should(BeEquivalentTo(1))
		g.Expect(oprPod.Status.ContainerStatuses[0].Ready).Should(BeTrue())
	}, existingClusterTimeout, existingClusterInterval).Should(Succeed())
	return nil
}

func loadYamlResource(yamlFile string) (runtime.Object, *schema.GroupVersionKind, error) {
	var info os.FileInfo
	var err error
	var filePath string

	// Return the error if ErrorIfPathMissing exists
	if info, err = os.Stat(yamlFile); os.IsNotExist(err) {
		return nil, nil, err
	}
	filePath = filepath.Dir(yamlFile)

	var b []byte
	b, err = ioutil.ReadFile(filepath.Join(filePath, info.Name()))
	if err != nil {
		return nil, nil, err
	}

	decode := scheme.Codecs.UniversalDeserializer().Decode
	robj, gKV, _ := decode(b, nil, nil)
	logf.Log.Info("Loaded resource", "kind", gKV.Kind)
	return robj, gKV, nil
}

func uninstallYamlResource(resPath string) error {
	logf.Log.Info("Uninstalling yaml resource", "yaml", resPath)
	robj, _, err := loadYamlResource(resPath)
	if err != nil {
		return err
	}
	cobj := robj.(client.Object)
	cobj.SetNamespace(defaultNamespace)

	err = k8sClient.Delete(ctx, cobj)
	if err != nil {
		return err
	}

	logf.Log.Info("Successfully uninstalled", "yaml", resPath)
	return nil
}

func installYamlResource(resPath string) error {
	logf.Log.Info("Installing yaml resource", "yaml", resPath)
	robj, gkv, err := loadYamlResource(resPath)
	if err != nil {
		return err
	}
	cobj := robj.(client.Object)
	cobj.SetNamespace(defaultNamespace)

	if oprImg := os.Getenv("OPERATOR_IMAGE"); oprImg != "" {
		if gkv.Kind == "Deployment" {
			logf.Log.Info("Using custom operator image", "url", oprImg)
			oprObj := cobj.(*appsv1.Deployment)
			oprObj.Spec.Template.Spec.Containers[0].Image = oprImg
		}
	}

	err = k8sClient.Create(ctx, cobj)
	if err != nil {
		return err
	}

	//make sure the create ok
	Eventually(func() bool {
		key := types.NamespacedName{Name: cobj.GetName(), Namespace: defaultNamespace}
		err := k8sClient.Get(ctx, key, cobj)
		return err == nil
	}, timeout, interval).Should(Equal(true))

	logf.Log.Info("Successfully installed", "yaml", resPath)
	return nil
}

func setUpK8sClient() {

	logf.Log.Info("Setting up k8s client")

	err := routev1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = brokerv2alpha5.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = brokerv2alpha4.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = brokerv2alpha3.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = brokerv2alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = brokerv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = brokerv1beta1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(restConfig, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
}

var _ = BeforeSuite(func() {
	opts := zap.Options{
		Development: true,
		DestWriter:  GinkgoWriter,
		TimeEncoder: zapcore.ISO8601TimeEncoder,
	}

	logf.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if verbose {
		GinkgoWriter.TeeTo(os.Stderr)
	}

	testWriter := TestLogWriter{}

	GinkgoWriter.TeeTo(testWriter)

	ctx, cancel = context.WithCancel(context.TODO())

	// force isLocalOnly=false check from artemis reconciler such that scale down controller will create
	// role binding to service account for the drainer pod
	os.Setenv("OPERATOR_WATCH_NAMESPACE", "SomeValueToCauesEqualitytoFailInIsLocalSoDrainControllerSortsCreds")

	var err error
	currentDir, err = os.Getwd()
	Expect(err).To(Succeed())
	logf.Log.Info("#### Starting test ####", "working dir", currentDir)
	if os.Getenv("DEPLOY_OPERATOR") == "true" {
		logf.Log.Info("#### Setting up a real operator ####")
		setUpRealOperator()
	} else {
		logf.Log.Info("#### Setting up EnvTest ####")
		setUpEnvTest()
	}
})

func cleanUpPVC() {
	pvcs := &corev1.PersistentVolumeClaimList{}
	opts := []client.ListOption{}
	k8sClient.List(context.TODO(), pvcs, opts...)
	for _, pvc := range pvcs.Items {
		logf.Log.Info("Deleting/GC PVC: " + pvc.Name)
		k8sClient.Delete(context.TODO(), &pvc, &client.DeleteOptions{})
	}
}

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	if os.Getenv("USE_EXISTING_CLUSTER") == "true" {
		cleanUpPVC()
		cleanUpTestProxy()
	}

	os.Unsetenv("OPERATOR_WATCH_NAMESPACE")

	if os.Getenv("DEPLOY_OPERATOR") == "true" {
		err := uninstallOperator()
		Expect(err).NotTo(HaveOccurred())
	} else {

		shutdownControllerManager()

		if stateManager != nil {
			stateManager.Clear()
		}

		// scaledown controller lifecycle seems a little loose, it does not complete on signal hander like the others
		for _, drainController := range controllers {
			close(*drainController.GetStopCh())
		}

		err := testEnv.Stop()
		Expect(err).NotTo(HaveOccurred())
	}

})

type TestLogWriter struct {
}

func (e TestLogWriter) Write(p []byte) (int, error) {
	if logBuffer != nil {
		return logBuffer.Write(p)
	} else {
		return len(p), nil
	}
}

func StartCapturingLog() {
	logBuffer = bytes.NewBuffer(nil)
}

func MatchCapturedLog(pattern string) (matched bool, err error) {
	return regexp.Match(pattern, logBuffer.Bytes())
}

func FindAllFromCapturedLog(pattern string) []string {
	re, err := regexp.Compile(pattern)
	if err == nil {
		return re.FindAllString(logBuffer.String(), -1)
	}
	return nil
}

func StopCapturingLog() {
	logBuffer = nil
}
