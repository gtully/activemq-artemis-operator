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

/*
As usual, we start with the necessary imports. We also define some utility variables.
*/
package controllers

import (
	"context"
	"fmt"
	"os"
	"time"

	cmv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmetav1 "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	brokerv1beta1 "github.com/artemiscloud/activemq-artemis-operator/api/v1beta1"
	"github.com/artemiscloud/activemq-artemis-operator/pkg/utils/common"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("minimal", func() {

	var installedCertManager bool = false

	BeforeEach(func() {
		BeforeEachSpec()

		if verbose {
			fmt.Println("Time with MicroSeconds: ", time.Now().Format("2006-01-02 15:04:05.000000"), " test:", CurrentSpecReport())
		}

		if os.Getenv("USE_EXISTING_CLUSTER") == "true" {
			//if cert manager/trust manager is not installed, install it
			if !CertManagerInstalled() {
				Expect(InstallCertManager()).To(Succeed())
				installedCertManager = true
			}

			rootIssuer = InstallClusteredIssuer(rootIssuerName, nil)

			rootCert = InstallCert(rootCertName, rootCertNamespce, func(candidate *cmv1.Certificate) {
				candidate.Spec.IsCA = true
				candidate.Spec.CommonName = "artemis.root.ca"
				candidate.Spec.SecretName = rootCertSecretName
				candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
					Name: rootIssuer.Name,
					Kind: "ClusterIssuer",
				}
			})

			caIssuer = InstallClusteredIssuer(caIssuerName, func(candidate *cmv1.ClusterIssuer) {
				candidate.Spec.SelfSigned = nil
				candidate.Spec.CA = &cmv1.CAIssuer{
					SecretName: rootCertSecretName,
				}
			})
			InstallCaBundle(caBundleName, rootCertSecretName, caPemTrustStoreName)

		}

	})

	AfterEach(func() {

		if false && os.Getenv("USE_EXISTING_CLUSTER") == "true" {
			UnInstallCaBundle(caBundleName)
			UninstallClusteredIssuer(caIssuerName)
			UninstallCert(rootCert.Name, rootCert.Namespace)
			UninstallClusteredIssuer(rootIssuerName)

			if installedCertManager {
				Expect(UninstallCertManager()).To(Succeed())
				installedCertManager = false
			}
		}
		AfterEachSpec()
	})

	Context("brokerProperties rbac restricted", func() {

		It("security with management role access", func() {

			if os.Getenv("USE_EXISTING_CLUSTER") != "true" {
				return
			}

			By("installing operator cert")
			InstallCert("operator-cert", defaultNamespace, func(candidate *cmv1.Certificate) {
				candidate.Spec.SecretName = "operator-cert"
				candidate.Spec.CommonName = "activemq-artemis-operator"
				candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
					Name: caIssuer.Name,
					Kind: "ClusterIssuer",
				}
			})

			ctx := context.Background()

			// empty CRD, name is used for cert subject
			crd := brokerv1beta1.ActiveMQArtemis{
				TypeMeta: metav1.TypeMeta{
					Kind:       "ActiveMQArtemis",
					APIVersion: brokerv1beta1.GroupVersion.Identifier(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      NextSpecResourceName(),
					Namespace: defaultNamespace,
				},
			}

			sharedOperandCertName := common.DefaultOperandCertSecretName
			By("installing shared broker/agent cert")
			By("Shared or not, depends on the host names, if shared it can only be the namespace")
			InstallCert(sharedOperandCertName, defaultNamespace, func(candidate *cmv1.Certificate) {
				candidate.Spec.SecretName = sharedOperandCertName
				candidate.Spec.CommonName = "activemq-artemis-operand"
				candidate.Spec.DNSNames = []string{common.OrdinalFQDNS(crd.Name, defaultNamespace, 0)}
				candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
					Name: caIssuer.Name,
					Kind: "ClusterIssuer",
				}
			})

			By("deploying logging")
			loggingSecret := &corev1.Secret{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Secret",
					APIVersion: "k8s.io.api.core.v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "broker-logging-config",
					Namespace: crd.ObjectMeta.Namespace,
				},
			}

			loggingSecret.StringData = map[string]string{LoggingConfigKey: `appender.stdout.name = STDOUT
appender.stdout.type = Console
rootLogger = INFO, STDOUT
			
# properties config
logger.pc.name=org.apache.activemq.artemis.core.config.impl.ConfigurationImpl
logger.pc.level=TRACE

# jaas
logger.jaas.name=org.apache.activemq.artemis.spi.core.security.jaas
logger.jaas.level=TRACE

# audit logging
logger.audit_base.name = org.apache.activemq.audit.base
logger.audit_base.level = INFO`}

			By("deploying jaas")
			jaasSecret := &corev1.Secret{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Secret",
					APIVersion: "k8s.io.api.core.v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "broker-jaas-config",
					Namespace: crd.ObjectMeta.Namespace,
				},
			}

			jaasSecret.StringData = map[string]string{JaasConfigKey: `
		    activemq {

				// only app specific users and roles
				org.apache.activemq.artemis.spi.core.security.jaas.PropertiesLoginModule sufficient
					reload=true
					org.apache.activemq.jaas.properties.user="users.properties"
					org.apache.activemq.jaas.properties.role="roles.properties";

			};`,
				"users.properties": `
				tom=tom
				joe=joe`,
				"roles.properties": `
				toms=tom
				joes=joe`,
			}

			crd.Spec.BrokerProperties = []string{
				"# create tom's work queue",
				"addressConfigurations.TOMS_WORK_QUEUE.routingTypes=ANYCAST",
				"addressConfigurations.TOMS_WORK_QUEUE.queueConfigs.TOMS_WORK_QUEUE.routingType=ANYCAST",
				"addressConfigurations.TOMS_WORK_QUEUE.queueConfigs.TOMS_WORK_QUEUE.durable=true",

				"# rbac, give tom's role send access",
				"securityRoles.TOMS_WORK_QUEUE.toms.send=true",

				"# rbac, give tom's role view/edit access for JMX",
				"securityRoles.\"mops.address.TOMS_WORK_QUEUE.*\".toms.view=true",
				"securityRoles.\"mops.address.TOMS_WORK_QUEUE.*\".toms.edit=true",

				"securityRoles.\"mops.#\".admin.view=true",
				"securityRoles.\"mops.#\".admin.edit=true",
			}

			crd.Spec.DeploymentPlan.Size = common.Int32ToPtr(1)
			crd.Spec.DeploymentPlan.ExtraMounts.Secrets = []string{loggingSecret.Name, jaasSecret.Name}

			crd.Spec.Restricted = common.NewTrue()

			By("Deploying the logging secret " + loggingSecret.ObjectMeta.Name)
			Expect(k8sClient.Create(ctx, loggingSecret)).Should(Succeed())

			By("Deploying the jaas secret " + jaasSecret.ObjectMeta.Name)
			Expect(k8sClient.Create(ctx, jaasSecret)).Should(Succeed())

			By("Deploying the CRD " + crd.ObjectMeta.Name)
			Expect(k8sClient.Create(ctx, &crd)).Should(Succeed())

			brokerKey := types.NamespacedName{Name: crd.Name, Namespace: crd.Namespace}
			createdCrd := &brokerv1beta1.ActiveMQArtemis{}

			By("Checking ready, operator can access broker status via jmx")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, brokerKey, createdCrd)).Should(Succeed())

				if verbose {
					fmt.Printf("STATUS: %v\n\n", createdCrd.Status.Conditions)
				}
				g.Expect(meta.IsStatusConditionTrue(createdCrd.Status.Conditions, brokerv1beta1.ReadyConditionType)).Should(BeTrue())
				g.Expect(meta.IsStatusConditionTrue(createdCrd.Status.Conditions, brokerv1beta1.ConfigAppliedConditionType)).Should(BeTrue())

			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			Expect(k8sClient.Delete(ctx, createdCrd)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, jaasSecret)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, loggingSecret)).Should(Succeed())

			UninstallCert("operator-cert", defaultNamespace)
			UninstallCert(sharedOperandCertName, defaultNamespace)
		})
	})

})
