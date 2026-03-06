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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	brokerv1beta1 "github.com/arkmq-org/activemq-artemis-operator/api/v1beta1"
	"github.com/arkmq-org/activemq-artemis-operator/pkg/utils/common"
)

var _ = Describe("broker-service multi-app scenarios", func() {

	var installedCertManager bool = false

	BeforeEach(func() {
		BeforeEachSpec()

		if verbose {
			fmt.Println("Time with MicroSeconds: ", time.Now().Format("2006-01-02 15:04:05.000000"), " test:", CurrentSpecReport())
		}

		if os.Getenv("USE_EXISTING_CLUSTER") == "true" {
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
			InstallCaBundle(common.DefaultOperatorCASecretName, rootCertSecretName, caPemTrustStoreName)

			By("installing operator cert")
			InstallCert(common.DefaultOperatorCertSecretName, defaultNamespace, func(candidate *cmv1.Certificate) {
				candidate.Spec.SecretName = common.DefaultOperatorCertSecretName
				candidate.Spec.CommonName = "activemq-artemis-operator"
				candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
					Name: caIssuer.Name,
					Kind: "ClusterIssuer",
				}
			})
		}
	})

	AfterEach(func() {
		if false && os.Getenv("USE_EXISTING_CLUSTER") == "true" {
			UnInstallCaBundle(common.DefaultOperatorCASecretName)
			UninstallClusteredIssuer(caIssuerName)
			UninstallCert(rootCert.Name, rootCert.Namespace)
			UninstallCert(common.DefaultOperatorCertSecretName, defaultNamespace)
			UninstallClusteredIssuer(rootIssuerName)

			if installedCertManager {
				Expect(UninstallCertManager()).To(Succeed())
				installedCertManager = false
			}
		}
		AfterEachSpec()
	})

	Context("multiple apps on single service", func() {

		It("should handle multiple apps with different capabilities", func() {

			if os.Getenv("USE_EXISTING_CLUSTER") != "true" {
				return
			}

			ctx := context.Background()
			serviceName := NextSpecResourceName()

			sharedOperandCertName := serviceName + "-" + common.DefaultOperandCertSecretName
			By("installing broker cert")
			InstallCert(sharedOperandCertName, defaultNamespace, func(candidate *cmv1.Certificate) {
				candidate.Spec.SecretName = sharedOperandCertName
				candidate.Spec.CommonName = serviceName
				candidate.Spec.DNSNames = []string{serviceName, fmt.Sprintf("%s.%s", serviceName, defaultNamespace), fmt.Sprintf("%s.%s.svc.%s", serviceName, defaultNamespace, common.GetClusterDomain()), common.ClusterDNSWildCard(serviceName, defaultNamespace)}
				candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
					Name: caIssuer.Name,
					Kind: "ClusterIssuer",
				}
			})

			prometheusCertName := common.DefaultPrometheusCertSecretName
			By("installing prometheus cert")
			InstallCert(prometheusCertName, defaultNamespace, func(candidate *cmv1.Certificate) {
				candidate.Spec.SecretName = prometheusCertName
				candidate.Spec.CommonName = "prometheus"
				candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
					Name: caIssuer.Name,
					Kind: "ClusterIssuer",
				}
			})

			By("creating BrokerService with label selector")
			crd := brokerv1beta1.BrokerService{
				TypeMeta: metav1.TypeMeta{
					Kind:       "BrokerService",
					APIVersion: brokerv1beta1.GroupVersion.Identifier(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      serviceName,
					Namespace: defaultNamespace,
					Labels:    map[string]string{"tier": "backend", "env": "test"},
				},
				Spec: brokerv1beta1.BrokerServiceSpec{
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			}

			Expect(k8sClient.Create(ctx, &crd)).Should(Succeed())

			serviceKey := types.NamespacedName{Name: crd.Name, Namespace: crd.Namespace}
			createdCrd := &brokerv1beta1.BrokerService{}

			By("waiting for service to be ready")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, serviceKey, createdCrd)).Should(Succeed())
				g.Expect(meta.IsStatusConditionTrue(createdCrd.Status.Conditions, brokerv1beta1.ReadyConditionType)).Should(BeTrue())
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("creating first app with queue capabilities")
			app1Name := "queue-app"
			app1Port := int32(61616)
			app1 := brokerv1beta1.BrokerApp{
				TypeMeta: metav1.TypeMeta{
					Kind:       "BrokerApp",
					APIVersion: brokerv1beta1.GroupVersion.Identifier(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      app1Name,
					Namespace: defaultNamespace,
				},
				Spec: brokerv1beta1.BrokerAppSpec{
					ServiceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"tier": "backend"},
					},
					Acceptor: brokerv1beta1.AppAcceptorType{Port: app1Port},
					Capabilities: []brokerv1beta1.AppCapabilityType{
						{
							Role:       "workQueue",
							ProducerOf: []brokerv1beta1.AppAddressType{{Address: "APP1.QUEUE"}},
							ConsumerOf: []brokerv1beta1.AppAddressType{{Address: "APP1.QUEUE"}},
						},
					},
				},
			}

			app1CertName := app1.Name + common.AppCertSecretSuffix
			InstallCert(app1CertName, defaultNamespace, func(candidate *cmv1.Certificate) {
				candidate.Spec.SecretName = app1CertName
				candidate.Spec.CommonName = app1.Name
				candidate.Spec.Subject.Organizations = nil
				candidate.Spec.Subject.OrganizationalUnits = []string{defaultNamespace}
				candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
					Name: caIssuer.Name,
					Kind: "ClusterIssuer",
				}
			})

			Expect(k8sClient.Create(ctx, &app1)).Should(Succeed())

			By("creating second app with topic capabilities")
			app2Name := "topic-app"
			app2Port := int32(61617)
			app2 := brokerv1beta1.BrokerApp{
				TypeMeta: metav1.TypeMeta{
					Kind:       "BrokerApp",
					APIVersion: brokerv1beta1.GroupVersion.Identifier(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      app2Name,
					Namespace: defaultNamespace,
				},
				Spec: brokerv1beta1.BrokerAppSpec{
					ServiceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"env": "test"},
					},
					Acceptor: brokerv1beta1.AppAcceptorType{Port: app2Port},
					Capabilities: []brokerv1beta1.AppCapabilityType{
						{
							Role:       "pubSub",
							ProducerOf: []brokerv1beta1.AppAddressType{{Address: "APP2.TOPIC"}},
							SubscriberOf: []brokerv1beta1.AppAddressType{
								{Address: "APP2.TOPIC::client-a.sub-a"},
							},
						},
					},
				},
			}

			app2CertName := app2.Name + common.AppCertSecretSuffix
			InstallCert(app2CertName, defaultNamespace, func(candidate *cmv1.Certificate) {
				candidate.Spec.SecretName = app2CertName
				candidate.Spec.CommonName = app2.Name
				candidate.Spec.Subject.Organizations = nil
				candidate.Spec.Subject.OrganizationalUnits = []string{defaultNamespace}
				candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
					Name: caIssuer.Name,
					Kind: "ClusterIssuer",
				}
			})

			Expect(k8sClient.Create(ctx, &app2)).Should(Succeed())

			By("waiting for both apps to be ready")
			app1Key := types.NamespacedName{Name: app1Name, Namespace: defaultNamespace}
			createdApp1 := &brokerv1beta1.BrokerApp{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, app1Key, createdApp1)).Should(Succeed())
				g.Expect(meta.IsStatusConditionTrue(createdApp1.Status.Conditions, brokerv1beta1.ReadyConditionType)).Should(BeTrue())
				g.Expect(createdApp1.Status.Binding).ShouldNot(BeNil())
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			app2Key := types.NamespacedName{Name: app2Name, Namespace: defaultNamespace}
			createdApp2 := &brokerv1beta1.BrokerApp{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, app2Key, createdApp2)).Should(Succeed())
				g.Expect(meta.IsStatusConditionTrue(createdApp2.Status.Conditions, brokerv1beta1.ReadyConditionType)).Should(BeTrue())
				g.Expect(createdApp2.Status.Binding).ShouldNot(BeNil())
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("verifying both apps are in service's ProvisionedApps status")
			brokerKey := types.NamespacedName{Name: serviceName, Namespace: defaultNamespace}
			brokerCrd := &brokerv1beta1.ActiveMQArtemis{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, brokerKey, brokerCrd)).Should(Succeed())
				g.Expect(meta.IsStatusConditionTrue(brokerCrd.Status.Conditions, brokerv1beta1.ConfigAppliedConditionType)).Should(BeTrue())
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, serviceKey, createdCrd)).Should(Succeed())
				if verbose {
					fmt.Printf("Service ProvisionedApps: %v\n", createdCrd.Status.ProvisionedApps)
				}
				g.Expect(createdCrd.Status.ProvisionedApps).Should(HaveLen(2))
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("verifying app properties secret contains both apps")

			app1ConfigKey := AppIdentityPrefixed(&app1, "capabilities.properties")
			app2ConfigKey := AppIdentityPrefixed(&app2, "capabilities.properties")
			secretName := AppPropertiesSecretName(serviceName)
			secret := &corev1.Secret{}
			secretKey := types.NamespacedName{Name: secretName, Namespace: defaultNamespace}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, secretKey, secret)).Should(Succeed())
				// Check for app-specific keys in the secret
				hasApp1Config := false
				hasApp2Config := false
				for key := range secret.Data {
					if verbose {
						fmt.Printf("Secret key: %s\n", key)
					}
					if key == app1ConfigKey {
						hasApp1Config = true
					}
					if key == app2ConfigKey {
						hasApp2Config = true
					}
				}
				g.Expect(hasApp1Config).Should(BeTrue(), "app1 config should be in secret")
				g.Expect(hasApp2Config).Should(BeTrue(), "app2 config should be in secret")
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("removing first app")
			Expect(k8sClient.Delete(ctx, createdApp1)).Should(Succeed())

			By("verifying only second app remains in ProvisionedApps status")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, serviceKey, createdCrd)).Should(Succeed())
				if verbose {
					fmt.Printf("Service ProvisionedApps after app1 delete: %v\n", createdCrd.Status.ProvisionedApps)
				}
				g.Expect(createdCrd.Status.ProvisionedApps).Should(HaveLen(1))
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("cleanup")
			Expect(k8sClient.Delete(ctx, createdApp2)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, createdCrd)).Should(Succeed())
			UninstallCert(app1CertName, defaultNamespace)
			UninstallCert(app2CertName, defaultNamespace)
			UninstallCert(sharedOperandCertName, defaultNamespace)
			UninstallCert(prometheusCertName, defaultNamespace)
		})
	})

	Context("app moving between services", func() {

		It("should properly update both services when app moves", func() {

			if os.Getenv("USE_EXISTING_CLUSTER") != "true" {
				return
			}

			ctx := context.Background()
			service1Name := NextSpecResourceName()
			service2Name := NextSpecResourceName()

			By("setting up certificates for both services")
			for _, svcName := range []string{service1Name, service2Name} {
				certName := svcName + "-" + common.DefaultOperandCertSecretName
				InstallCert(certName, defaultNamespace, func(candidate *cmv1.Certificate) {
					candidate.Spec.SecretName = certName
					candidate.Spec.CommonName = svcName
					candidate.Spec.DNSNames = []string{svcName, fmt.Sprintf("%s.%s", svcName, defaultNamespace), fmt.Sprintf("%s.%s.svc.%s", svcName, defaultNamespace, common.GetClusterDomain()), common.ClusterDNSWildCard(svcName, defaultNamespace)}
					candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
						Name: caIssuer.Name,
						Kind: "ClusterIssuer",
					}
				})
			}

			prometheusCertName := common.DefaultPrometheusCertSecretName
			InstallCert(prometheusCertName, defaultNamespace, func(candidate *cmv1.Certificate) {
				candidate.Spec.SecretName = prometheusCertName
				candidate.Spec.CommonName = "prometheus"
				candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
					Name: caIssuer.Name,
					Kind: "ClusterIssuer",
				}
			})

			By("creating first service with label env=dev")
			service1 := brokerv1beta1.BrokerService{
				TypeMeta: metav1.TypeMeta{
					Kind:       "BrokerService",
					APIVersion: brokerv1beta1.GroupVersion.Identifier(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      service1Name,
					Namespace: defaultNamespace,
					Labels:    map[string]string{"env": "dev"},
				},
				Spec: brokerv1beta1.BrokerServiceSpec{
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, &service1)).Should(Succeed())

			By("creating second service with label env=prod")
			service2 := brokerv1beta1.BrokerService{
				TypeMeta: metav1.TypeMeta{
					Kind:       "BrokerService",
					APIVersion: brokerv1beta1.GroupVersion.Identifier(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      service2Name,
					Namespace: defaultNamespace,
					Labels:    map[string]string{"env": "prod"},
				},
				Spec: brokerv1beta1.BrokerServiceSpec{
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, &service2)).Should(Succeed())

			By("waiting for both services to be ready")
			service1Key := types.NamespacedName{Name: service1Name, Namespace: defaultNamespace}
			createdService1 := &brokerv1beta1.BrokerService{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, service1Key, createdService1)).Should(Succeed())
				g.Expect(meta.IsStatusConditionTrue(createdService1.Status.Conditions, brokerv1beta1.ReadyConditionType)).Should(BeTrue())
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			service2Key := types.NamespacedName{Name: service2Name, Namespace: defaultNamespace}
			createdService2 := &brokerv1beta1.BrokerService{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, service2Key, createdService2)).Should(Succeed())
				g.Expect(meta.IsStatusConditionTrue(createdService2.Status.Conditions, brokerv1beta1.ReadyConditionType)).Should(BeTrue())
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("creating app that matches service1 (env=dev)")
			appName := "mobile-app"
			appPort := int32(61618)
			app := brokerv1beta1.BrokerApp{
				TypeMeta: metav1.TypeMeta{
					Kind:       "BrokerApp",
					APIVersion: brokerv1beta1.GroupVersion.Identifier(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: defaultNamespace,
				},
				Spec: brokerv1beta1.BrokerAppSpec{
					ServiceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"env": "dev"},
					},
					Acceptor: brokerv1beta1.AppAcceptorType{Port: appPort},
					Capabilities: []brokerv1beta1.AppCapabilityType{
						{
							Role:       "workQueue",
							ProducerOf: []brokerv1beta1.AppAddressType{{Address: "MOBILE.TASKS"}},
							ConsumerOf: []brokerv1beta1.AppAddressType{{Address: "MOBILE.TASKS"}},
						},
					},
				},
			}

			appCertName := app.Name + common.AppCertSecretSuffix
			InstallCert(appCertName, defaultNamespace, func(candidate *cmv1.Certificate) {
				candidate.Spec.SecretName = appCertName
				candidate.Spec.CommonName = app.Name
				candidate.Spec.Subject.Organizations = nil
				candidate.Spec.Subject.OrganizationalUnits = []string{defaultNamespace}
				candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
					Name: caIssuer.Name,
					Kind: "ClusterIssuer",
				}
			})

			Expect(k8sClient.Create(ctx, &app)).Should(Succeed())

			By("verifying app is ready and bound to service1")
			appKey := types.NamespacedName{Name: appName, Namespace: defaultNamespace}
			createdApp := &brokerv1beta1.BrokerApp{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, appKey, createdApp)).Should(Succeed())
				g.Expect(meta.IsStatusConditionTrue(createdApp.Status.Conditions, brokerv1beta1.ReadyConditionType)).Should(BeTrue())
				g.Expect(createdApp.Status.Binding).ShouldNot(BeNil())
				// Binding secret name should contain service name
				if verbose {
					fmt.Printf("App binding secret: %s\n", createdApp.Status.Binding.Name)
				}
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("verifying service1 has the app in ProvisionedApps")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, service1Key, createdService1)).Should(Succeed())
				if verbose {
					fmt.Printf("Service1 ProvisionedApps: %v\n", createdService1.Status.ProvisionedApps)
				}
				g.Expect(createdService1.Status.ProvisionedApps).Should(HaveLen(1))
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("verifying service2 has no apps")
			Expect(k8sClient.Get(ctx, service2Key, createdService2)).Should(Succeed())
			Expect(createdService2.Status.ProvisionedApps).Should(BeEmpty())

			By("moving app to service2 by changing selector to env=prod")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, appKey, createdApp)).Should(Succeed())
				createdApp.Spec.ServiceSelector = &metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "prod"},
				}
				g.Expect(k8sClient.Update(ctx, createdApp)).Should(Succeed())
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("verifying service1 no longer has the app")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, service1Key, createdService1)).Should(Succeed())
				if verbose {
					fmt.Printf("Service1 ProvisionedApps after move: %v\n", createdService1.Status.ProvisionedApps)
				}
				g.Expect(createdService1.Status.ProvisionedApps).Should(BeEmpty())
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("verifying service2 now has the app")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, service2Key, createdService2)).Should(Succeed())
				if verbose {
					fmt.Printf("Service2 ProvisionedApps after move: %v\n", createdService2.Status.ProvisionedApps)
				}
				g.Expect(createdService2.Status.ProvisionedApps).Should(HaveLen(1))
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("verifying app binding is updated")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, appKey, createdApp)).Should(Succeed())
				g.Expect(meta.IsStatusConditionTrue(createdApp.Status.Conditions, brokerv1beta1.ReadyConditionType)).Should(BeTrue())
				// Binding should still exist
				g.Expect(createdApp.Status.Binding).ShouldNot(BeNil())
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("cleanup")
			Expect(k8sClient.Delete(ctx, createdApp)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, createdService1)).Should(Succeed())
			Expect(k8sClient.Delete(ctx, createdService2)).Should(Succeed())
			UninstallCert(appCertName, defaultNamespace)
			UninstallCert(service1Name+"-"+common.DefaultOperandCertSecretName, defaultNamespace)
			UninstallCert(service2Name+"-"+common.DefaultOperandCertSecretName, defaultNamespace)
			UninstallCert(prometheusCertName, defaultNamespace)
		})
	})

	Context("validation and error handling", func() {

		It("should reject invalid resource names", func() {

			if os.Getenv("USE_EXISTING_CLUSTER") != "true" {
				return
			}

			ctx := context.Background()

			By("attempting to create service with path traversal in name")
			invalidService := brokerv1beta1.BrokerService{
				TypeMeta: metav1.TypeMeta{
					Kind:       "BrokerService",
					APIVersion: brokerv1beta1.GroupVersion.Identifier(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "../evil-service",
					Namespace: defaultNamespace,
				},
				Spec: brokerv1beta1.BrokerServiceSpec{},
			}

			// Kubernetes API should reject this before it reaches our controller
			err := k8sClient.Create(ctx, &invalidService)
			Expect(err).Should(HaveOccurred())
			if verbose {
				fmt.Printf("Expected error for invalid name: %v\n", err)
			}
		})

		It("should handle app without matching service gracefully", func() {

			if os.Getenv("USE_EXISTING_CLUSTER") != "true" {
				return
			}

			ctx := context.Background()
			appName := NextSpecResourceName()

			By("creating app with selector that matches no service")
			app := brokerv1beta1.BrokerApp{
				TypeMeta: metav1.TypeMeta{
					Kind:       "BrokerApp",
					APIVersion: brokerv1beta1.GroupVersion.Identifier(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      appName,
					Namespace: defaultNamespace,
				},
				Spec: brokerv1beta1.BrokerAppSpec{
					ServiceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"nonexistent": "label"},
					},
					Acceptor: brokerv1beta1.AppAcceptorType{Port: 61619},
					Capabilities: []brokerv1beta1.AppCapabilityType{
						{
							Role:       "workQueue",
							ProducerOf: []brokerv1beta1.AppAddressType{{Address: "TEST.QUEUE"}},
						},
					},
				},
			}

			appCertName := app.Name + common.AppCertSecretSuffix
			InstallCert(appCertName, defaultNamespace, func(candidate *cmv1.Certificate) {
				candidate.Spec.SecretName = appCertName
				candidate.Spec.CommonName = app.Name
				candidate.Spec.Subject.Organizations = nil
				candidate.Spec.Subject.OrganizationalUnits = []string{defaultNamespace}
				candidate.Spec.IssuerRef = cmmetav1.ObjectReference{
					Name: caIssuer.Name,
					Kind: "ClusterIssuer",
				}
			})

			Expect(k8sClient.Create(ctx, &app)).Should(Succeed())

			By("verifying app condition reflects no matching service")
			appKey := types.NamespacedName{Name: appName, Namespace: defaultNamespace}
			createdApp := &brokerv1beta1.BrokerApp{}

			// The app should exist but not be Ready
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, appKey, createdApp)).Should(Succeed())
				// Should have a condition indicating the problem
				readyCond := meta.FindStatusCondition(createdApp.Status.Conditions, brokerv1beta1.ReadyConditionType)
				if readyCond != nil {
					if verbose {
						fmt.Printf("App Ready condition: Status=%s, Reason=%s, Message=%s\n",
							readyCond.Status, readyCond.Reason, readyCond.Message)
					}
					g.Expect(readyCond.Status).Should(Equal(metav1.ConditionFalse))
				}
			}, existingClusterTimeout, existingClusterInterval).Should(Succeed())

			By("cleanup")
			Expect(k8sClient.Delete(ctx, createdApp)).Should(Succeed())
			UninstallCert(appCertName, defaultNamespace)
		})
	})
})
