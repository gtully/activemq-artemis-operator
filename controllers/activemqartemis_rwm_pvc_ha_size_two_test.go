/*
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
// +kubebuilder:docs-gen:collapse=Apache License

package controllers

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	brokerv1beta1 "github.com/arkmq-org/activemq-artemis-operator/api/v1beta1"
	"github.com/arkmq-org/activemq-artemis-operator/pkg/utils/common"
)

var _ = Describe("shared size two fast failover", func() {

	BeforeEach(func() {
		BeforeEachSpec()
	})

	AfterEach(func() {
		AfterEachSpec()
	})

	Context("two peer ss", Label("slow"), func() {
		It("with shared store", func() {
			if os.Getenv("USE_EXISTING_CLUSTER") == "true" {

				By("deploying shared volume")

				sharedPvcName := "shared-data-dir"
				pvc := corev1.PersistentVolumeClaim{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1",
						Kind:       "PersistentVolumeClaim",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      sharedPvcName,
						Namespace: defaultNamespace,
					},
				}
				pvc.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteMany,
				}
				pvc.Spec.Resources = corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Mi"),
					},
				}
				// Note: specifying a StoregeClassName is important in production.
				// For shared store, the ReadWriteMany PV needs to support advisory locks,
				// that needs to be conveyed via the StoregeClassName
				//pvc.Spec.StorageClassName = ....

				Expect(k8sClient.Create(ctx, &pvc)).Should(Succeed())

				bound := false
				iterations := 0
				createdPvc := &corev1.PersistentVolumeClaim{}
				// our cluster needs to support rwm, lets check
				Eventually(func(g Gomega) {

					g.Expect(k8sClient.Get(ctx, types.NamespacedName{
						Name:      pvc.Name,
						Namespace: defaultNamespace}, createdPvc)).Should(Succeed())

					if verbose {
						fmt.Printf("\nRWM PVC Status:%v\n", createdPvc.Status)
					}

					bound = createdPvc.Status.Phase == corev1.ClaimBound
					if bound {
						return
					}
					iterations += 1
					if iterations == 10 {
						// give up
						return
					}
					g.Expect(bound).To(Equal(true))
				}, timeout, existingClusterInterval).Should(Succeed())

				if !bound {
					By("skip test when ReadWriteMany PVC is not bound, cluster limitation")
				} else {

					By("deploying artemis")

					peerLabel := "fast-ha-peer"
					peerPrefix := NextSpecResourceName()
					ctx := context.Background()
					cr := generateArtemisSpec(defaultNamespace)
					cr.Name = peerPrefix + "-peer-ss"

					// same namespace, the ss can accomodate, but with independence curtailed
					cr.Spec.DeploymentPlan.Size = common.Int32ToPtr(2)
					cr.Spec.Acceptors = []brokerv1beta1.AcceptorType{{Name: "tcp", Port: 61616, Expose: true}}

					if !isOpenshift {
						cr.Spec.IngressDomain = defaultTestIngressDomain
					}

					// standalone broker with persistence configured via brokerProperties
					cr.Spec.DeploymentPlan.PersistenceEnabled = boolFalse
					cr.Spec.DeploymentPlan.Clustered = &boolFalse

					cr.Spec.DeploymentPlan.Labels = map[string]string{peerLabel: peerPrefix}

					By("configuring the broker")

					cr.Spec.DeploymentPlan.ExtraVolumes = []corev1.Volume{
						{
							Name: sharedPvcName,
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: sharedPvcName,
								},
							},
						},
					}
					cr.Spec.DeploymentPlan.ExtraVolumeMounts = []corev1.VolumeMount{
						{
							Name:      sharedPvcName,
							MountPath: "/opt/amq-broker/data",
						},
					}

					cr.Spec.BrokerProperties = []string{

						"HAPolicyConfiguration=SHARED_STORE_PRIMARY",

						"# reference the shared pvc mount point",
						"journalDirectory=/opt/amq-broker/data/journal",
						"pagingDirectory=/opt/amq-broker/data/paging",
						"bindingsDirectory=/opt/amq-broker/data/bindings",
						"largeMessagesDirectory=/opt/amq-broker/data/largemessages",

						"criticalAnalyzer=false",

						"# app config",
						"addressConfigurations.JOBS.routingTypes=ANYCAST",
						"addressConfigurations.JOBS.queueConfigs.JOBS.routingType=ANYCAST",
					}

					By("Configuring probe to keep Pod alive (in starting state) while waiting to obtain shared file lock")

					cr.Spec.DeploymentPlan.LivenessProbe = &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							Exec: &corev1.ExecAction{
								Command: []string{
									"test", "-f",
									// the lock file is created by the run command, indicating that the instance has started
									"/home/jboss/amq-broker/lock/cli.lock",
								},
							},
						},
						InitialDelaySeconds: 5,
						TimeoutSeconds:      5,
						PeriodSeconds:       5,
						SuccessThreshold:    1,
						FailureThreshold:    2,
					}

					By("provisioning the broker")
					Expect(k8sClient.Create(ctx, &cr)).Should(Succeed())

					By("verifying one broker ready, other starting; it is a race to lock the data dir")
					deployedCrd := &brokerv1beta1.ActiveMQArtemis{}
					Eventually(func(g Gomega) {

						g.Expect(k8sClient.Get(ctx, types.NamespacedName{
							Name:      cr.Name,
							Namespace: defaultNamespace}, deployedCrd)).Should(Succeed())

						if verbose {
							fmt.Printf("\npeer-ss CR Status:%v", deployedCrd.Status)
						}

						// don't evRWMer get to CR ready state

						// probes keep locking peer in non-ready/starting state
						g.Expect(len(deployedCrd.Status.PodStatus.Starting) == 1 &&
							len(deployedCrd.Status.PodStatus.Ready) == 1).Should(BeTrue())

					}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

					brokerService := "broker-ha"
					By("provisioning service for these two CRs, for use within the cluster via dns")
					svc := &corev1.Service{
						TypeMeta: metav1.TypeMeta{
							APIVersion: "v1",
							Kind:       "Service",
						},
						ObjectMeta: metav1.ObjectMeta{
							Name:      brokerService,
							Namespace: defaultNamespace,
						},
						Spec: corev1.ServiceSpec{
							Selector: map[string]string{
								peerLabel: peerPrefix, // shared by both CRs
							},
							Ports: []corev1.ServicePort{
								{
									Port:       62616,
									TargetPort: intstr.IntOrString{IntVal: 61616},
								},
							},
						},
					}

					Expect(k8sClient.Create(ctx, svc)).Should(Succeed())

					By("verifying service is ok - status does not reflect endpoints which is not ideal")
					createdService := &corev1.Service{}
					Eventually(func(g Gomega) {

						g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: svc.Name,
							Namespace: defaultNamespace}, createdService)).Should(Succeed())

						if verbose {
							fmt.Printf("\nsvc Status:%v", createdService.Status)
						}
						g.Expect(len(createdService.Status.LoadBalancer.Ingress)).Should(BeNumerically("==", 0))
						g.Expect(len(createdService.Status.Conditions)).Should(BeNumerically("==", 0))

					}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

					By("validating access to service via exec on either pod, will use peer-b")
					url := "tcp://" + brokerService + ":62616"
					podName := cr.Name + "-ss-0"
					containerName := cr.Name + "-container"
					Eventually(func(g Gomega) {
						sendCmd := []string{"amq-broker/bin/artemis", "producer", "--user", "Jay", "--password", "activemq", "--url", url, "--message-count", "1", "--destination", "queue://JOBS"}
						content, err := RunCommandInPod(podName, containerName, sendCmd)
						g.Expect(err).To(BeNil())
						g.Expect(*content).Should(ContainSubstring("Produced: 1 messages"))

					}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

					By("killing active pod")
					activePod := &corev1.Pod{}
					activePod.Namespace = defaultNamespace
					if len(deployedCrd.Status.PodStatus.Ready) == 1 {
						activePod.Name = cr.Name + "-ss-0"
					} else {
						activePod.Name = cr.Name + "-ss-0"
					}
					Expect(k8sClient.Delete(ctx, activePod)).To(Succeed())

					By("consuming our message,c.Nam if peer-b is active, it may take a little while to restart")
					Eventually(func(g Gomega) {

						recvCmd := []string{"amq-broker/bin/artemis", "consumer", "--user", "Jay", "--password", "activemq", "--url", url, "--message-count", "1", "--receive-timeout", "5000", "--break-on-null", "--verbose", "--destination", "queue://JOBS"}
						content, err := RunCommandInPod(podName, containerName, recvCmd)

						g.Expect(err).To(BeNil())
						g.Expect(*content).Should(ContainSubstring("JMS Message ID:"))

					}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

					CleanResource(svc, svc.Name, defaultNamespace)
					CleanResource(&cr, cr.Name, defaultNamespace)
				}
				CleanResource(&pvc, pvc.Name, defaultNamespace)
			}
		})
	})
})
