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
	"io"
	"net/http"
	"os"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	brokerv1beta1 "github.com/artemiscloud/activemq-artemis-operator/api/v1beta1"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/resources/configmaps"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/utils/common"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/utils/namer"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/utils/selectors"
	"github.com/artemiscloud/activemq-artemis-operator/version"
)

var _ = Describe("work queue", func() {

	BeforeEach(func() {
		BeforeEachSpec()
	})

	AfterEach(func() {
		AfterEachSpec()
	})

	Context("ha pub and ha competing sub, compromised total message order", Label("slow"), func() {
		It("validation", func() {
			if os.Getenv("USE_EXISTING_CLUSTER") == "true" {

				ctx := context.Background()
				brokerCrd := generateArtemisSpec(defaultNamespace)

				brokerCrd.Spec.Console.Expose = true

				brokerCrd.Spec.DeploymentPlan.PersistenceEnabled = boolFalse
				brokerCrd.Spec.DeploymentPlan.Clustered = &boolFalse
				brokerCrd.Spec.DeploymentPlan.Size = common.Int32ToPtr(2)

				By("deplying secret with jaas config for auth")
				jaasSecret := &corev1.Secret{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Secret",
						APIVersion: "k8s.io.api.core.v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pub-sub-jaas-config",
						Namespace: brokerCrd.ObjectMeta.Namespace,
					},
				}

				jaasSecret.StringData = map[string]string{JaasConfigKey: `
				activemq {
	
					// ensure the operator can connect to the mgmt console by referencing the existing properties config
					org.apache.activemq.artemis.spi.core.security.jaas.PropertiesLoginModule sufficient
						org.apache.activemq.jaas.properties.user="artemis-users.properties"
						org.apache.activemq.jaas.properties.role="artemis-roles.properties"
						baseDir="/home/jboss/amq-broker/etc";
	
					// app specific users and roles	
					org.apache.activemq.artemis.spi.core.security.jaas.PropertiesLoginModule sufficient
						reload=true
						debug=true
						org.apache.activemq.jaas.properties.user="users.properties"
						org.apache.activemq.jaas.properties.role="roles.properties";
	
				};`,
					"users.properties": `
					 control-plane=passwd

					 p=passwd
					 c=passwd`,

					"roles.properties": `
					
					# rbac
					control-plane=control-plane
					consumers=c
					producers=p`,
				}

				By("Deploying the jaas secret " + jaasSecret.Name)
				Expect(k8sClient.Create(ctx, jaasSecret)).Should(Succeed())
				brokerCrd.Spec.DeploymentPlan.ExtraMounts.Secrets = []string{jaasSecret.Name}

				By("deploying custom logging")
				loggingConfigMapName := brokerCrd.Name + "-logging-config"
				loggingData := make(map[string]string)
				loggingData[LoggingConfigKey] = `appender.stdout.name = STDOUT
			appender.stdout.type = Console
			rootLogger = info, STDOUT
			logger.activemq.name=org.apache.activemq.artemis.protocol.amqp.connect.federation.AMQPFederationQueueConsumer
			logger.activemq.level=TRACE
			logger.rest.name=org.apache.activemq.artemis.core
			logger.rest.level=ERROR`

				loggingConfigMap := configmaps.MakeConfigMap(defaultNamespace, loggingConfigMapName, loggingData)
				Expect(k8sClient.Create(ctx, loggingConfigMap)).Should(Succeed())
				brokerCrd.Spec.DeploymentPlan.ExtraMounts.ConfigMaps = []string{loggingConfigMapName}

				// this env var can be used in properties, as it will be part of the broker POD env
				brokerCrd.Spec.Env = []corev1.EnvVar{
					{
						Name: "CR_NAME",
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{
								FieldPath: "metadata.labels['" + selectors.LabelResourceKey + "']"},
						},
					},
					{
						Name: "IGNORE_JAVA_ARGS_APPEND",

						// brokerCrd.Spec.DeploymentPlan.EnableMetricsPlugin = part one
						Value: "-Dwebconfig.bindings.artemis.apps.metrics.war=metrics.war -Dwebconfig.bindings.artemis.apps.metrics.url=metrics",
					},
				}

				By("configuring the broker")
				brokerCrd.Spec.BrokerProperties = []string{
					"addressConfigurations.JOBS.routingTypes=ANYCAST",
					"addressConfigurations.JOBS.queueConfigs.JOBS.routingType=ANYCAST",

					"# rbac",
					"securityRoles.JOBS.producers.send=true",
					"securityRoles.JOBS.consumers.consume=true",
					"securityRoles.JOBS.consumers.createNonDurableQueue=true",
					"securityRoles.JOBS.consumers.deleteNonDurableQueue=true",

					"# control-plane rbac",
					"securityRoles.JOBS.control-plane.createDurableQueue=true",
					"securityRoles.JOBS.control-plane.consume=true",
					"securityRoles.JOBS.control-plane.send=true",

					"securityRoles.$ACTIVEMQ_ARTEMIS_FEDERATION.control-plane.createNonDurableQueue=true",
					"securityRoles.$ACTIVEMQ_ARTEMIS_FEDERATION.control-plane.consume=true",
					"securityRoles.$ACTIVEMQ_ARTEMIS_FEDERATION.control-plane.send=true",

					// federation uses a dynamic event address that needs a prefix as it has a uuid at the end
					// to cover that permission, we need the wildcard permisison set for control-plane
					"securityRoles.#.control-plane.createNonDurableQueue=true",
					"securityRoles.#.control-plane.createAddress=true",
					"securityRoles.#.control-plane.consume=true",
					"securityRoles.#.control-plane.send=true",

					"# federate the queue in both directions",
					"broker-0.AMQPConnections.target.uri=tcp://${CR_NAME}-ss-1.${CR_NAME}-hdls-svc:61616",
					"broker-1.AMQPConnections.target.uri=tcp://${CR_NAME}-ss-0.${CR_NAME}-hdls-svc:61616",

					"# speed up mesh formation",
					"AMQPConnections.target.retryInterval=500",

					"AMQPConnections.target.user=control-plane",
					"AMQPConnections.target.password=passwd",
					"AMQPConnections.target.autostart=true",

					"# in pull mode",
					"AMQPConnections.target.federations.peerN.properties.amqpCredit=0",
					"AMQPConnections.target.federations.peerN.localQueuePolicies.forJobs.includes.justJobs.queueMatch=JOBS",

					// brokerCrd.Spec.DeploymentPlan.EnableMetricsPlugin = part two
					// but needs the metrics plugin to become a broker plugin
					// or we add the ability to set a property with any class instance
					//"metricsPlugin.\"com.redhat.amq.broker.core.server.metrics.plugins.ArtemisPrometheusMetricsPlugin.class\".init=\"\"",

					"metricsConfiguration.logging=true",
					"metricsConfiguration.processor=true",
					"metricsConfiguration.uptime=true",
					"metricsConfiguration.fileDescriptors=true",
					"metricsConfiguration.jvmMemory=false",
				}

				brokerCrd.Spec.Acceptors = []brokerv1beta1.AcceptorType{{Name: "tcp", Port: 61616, Expose: true}}

				brokerCrd.Spec.DeploymentPlan.EnableMetricsPlugin = &boolTrue

				if !isOpenshift {
					brokerCrd.Spec.IngressDomain = defaultTestIngressDomain
				}

				By("provisioning the broker")
				Expect(k8sClient.Create(ctx, &brokerCrd)).Should(Succeed())

				By("provisioning loadbalanced service for this CR, for use within the cluster via dns")
				svc := &corev1.Service{
					TypeMeta: metav1.TypeMeta{
						APIVersion: "v1",
						Kind:       "Service",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      brokerCrd.Name,
						Namespace: defaultNamespace,
					},
					Spec: corev1.ServiceSpec{
						Selector: map[string]string{
							selectors.LabelResourceKey: brokerCrd.Name,
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

				createdBrokerCrd := &brokerv1beta1.ActiveMQArtemis{}
				createdBrokerCrdKey := types.NamespacedName{
					Name:      brokerCrd.Name,
					Namespace: defaultNamespace,
				}

				By("verifying broker started")
				Eventually(func(g Gomega) {

					g.Expect(k8sClient.Get(ctx, createdBrokerCrdKey, createdBrokerCrd)).Should(Succeed())
					if verbose {
						fmt.Printf("\nStatus:%v", createdBrokerCrd.Status)
					}
					g.Expect(meta.IsStatusConditionTrue(createdBrokerCrd.Status.Conditions, brokerv1beta1.ReadyConditionType)).Should(BeTrue())

				}, existingClusterTimeout*5, existingClusterInterval).Should(Succeed())

				By("verifying out service has two endpoints so our consumers will get distributed")
				Eventually(func(g Gomega) {

					endpoints := &corev1.Endpoints{}
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace}, endpoints)).Should(Succeed())
					if verbose {
						fmt.Printf("\nEndpoints:%v", endpoints.Subsets)
					}
					g.Expect(len(endpoints.Subsets)).Should(BeNumerically("==", 1))
					g.Expect(len(endpoints.Subsets[0].Addresses)).Should(BeNumerically("==", 2))

				}, existingClusterTimeout*5, existingClusterInterval).Should(Succeed())

				By("provisioning an app, publisher and consumers, using the broker image to access the artemis client from within the cluster")
				jobTemplate := func(name string, replicas int32, command []string) batchv1.Job {
					appLables := map[string]string{"app": name}
					return batchv1.Job{

						TypeMeta:   metav1.TypeMeta{Kind: "Job", APIVersion: "batch/v1"},
						ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: defaultNamespace, Labels: appLables},
						Spec: batchv1.JobSpec{
							Parallelism: common.Int32ToPtr(replicas),
							Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: appLables},
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{
											Name:    name,
											Image:   version.LatestKubeImage,
											Command: command,
										},
									},
									RestartPolicy: corev1.RestartPolicyOnFailure,
								}},
						},
					}

				}

				// small prefetch to ensure messages are federated when there is demand even if the
				// majority of consumers go to a single broker
				serviceUrl := "tcp://" + brokerCrd.Name + ":62616?jms.prefetchPolicy.all=1"

				numConsumers := 10
				numMessagesToConsume := "100"
				numMessagesToProduce := "1000"

				By("deploying single producer to send " + numMessagesToProduce + " to one broker and sleep!")
				producer := jobTemplate(
					"producer",
					1,
					[]string{"/bin/sh", "-c", "/opt/amq/bin/artemis producer --protocol=AMQP --user p --password passwd --url " + serviceUrl + " --message-count " + numMessagesToProduce + " --destination queue://JOBS; sleep 300"},
				)
				Expect(k8sClient.Create(ctx, &producer)).Should(Succeed())

				By("deploying  " + fmt.Sprintf("%d", numConsumers) + " to consume in batches of " + numMessagesToConsume)
				consumers := jobTemplate(
					"consumer",
					int32(numConsumers),
					[]string{"/bin/sh", "-c", "/opt/amq/bin/artemis consumer --protocol=AMQP --user c --password passwd --url " + serviceUrl + " --message-count " + numMessagesToConsume + " --destination queue://JOBS"},
				)
				Expect(k8sClient.Create(ctx, &consumers)).Should(Succeed())

				By("verifying - messages flowing.. via routed_message_count")

				metricsMessageCountCheck := func(g Gomega, ordinal string, routedNonZeroCheck bool) bool {
					pod := &corev1.Pod{}
					podName := namer.CrToSS(createdBrokerCrd.Name) + "-" + ordinal
					podNamespacedName := types.NamespacedName{Name: podName, Namespace: defaultNamespace}
					g.Expect(k8sClient.Get(ctx, podNamespacedName, pod)).Should(Succeed())

					g.Expect(pod.Status).ShouldNot(BeNil())
					g.Expect(pod.Status.PodIP).ShouldNot(BeEmpty())

					resp, err := http.Get("http://" + pod.Status.PodIP + ":8161/metrics")
					g.Expect(err).Should(Succeed())

					defer resp.Body.Close()
					body, err := io.ReadAll(resp.Body)
					g.Expect(err).Should(Succeed())

					lines := strings.Split(string(body), "\n")

					var done = false
					if verbose {
						fmt.Printf("\nStart Metrics for JOBS on %v with Headers %v \n", ordinal, resp.Header)
					}
					for _, line := range lines {

						if verbose && strings.Contains(line, "\"JOBS\"") {
							fmt.Printf("%s\n", line)
						}

						if routedNonZeroCheck {
							if strings.Contains(line, "artemis_routed_message_count{address=\"JOBS\",broker=\"amq-broker\",}") {
								if !strings.Contains(line, "} 0.0") {
									done = true
								}
							}
						} else if strings.Contains(line, "artemis_message_count{address=\"JOBS\",broker=\"amq-broker\",queue=\"JOBS\",} 0.0") {
							done = true
						}
					}
					return done
				}

				By("verifying artemis_routed_message_count metric on JOBS")
				doRoutedNonZeroCheck := true
				Eventually(func(g Gomega) {

					foundMetric0 := metricsMessageCountCheck(g, "0", doRoutedNonZeroCheck)
					foundMetric1 := metricsMessageCountCheck(g, "1", doRoutedNonZeroCheck)

					g.Expect(foundMetric0).Should(Equal(true))
					g.Expect(foundMetric1).Should(Equal(true))

				}, existingClusterTimeout, existingClusterInterval*5).Should(Succeed())

				By("verifying artemis_message_count metric 0, all messaged consumed")
				doRoutedNonZeroCheck = false
				Eventually(func(g Gomega) {

					foundMetric0 := metricsMessageCountCheck(g, "0", doRoutedNonZeroCheck)
					foundMetric1 := metricsMessageCountCheck(g, "1", doRoutedNonZeroCheck)

					g.Expect(foundMetric0).Should(Equal(true))
					g.Expect(foundMetric1).Should(Equal(true))

				}, existingClusterTimeout, existingClusterInterval*5).Should(Succeed())

				CleanResource(&producer, producer.Name, defaultNamespace)
				CleanResource(&consumers, consumers.Name, defaultNamespace)
				CleanResource(createdBrokerCrd, brokerCrd.Name, defaultNamespace)
				CleanResource(jaasSecret, jaasSecret.Name, defaultNamespace)
				CleanResource(loggingConfigMap, loggingConfigMap.Name, defaultNamespace)
				CleanResource(svc, svc.Name, defaultNamespace)
			}
		})
	})
})
