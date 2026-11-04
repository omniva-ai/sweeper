//go:build e2e
// +build e2e

/*
Copyright 2026 Omniva.

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

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
	"github.com/omniva-ai/sweeper/test/utils"
)

// namespace where the project is deployed in
const namespace = "sweeper-system"

// serviceAccountName created for the project
const serviceAccountName = "sweeper-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "sweeper-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "sweeper-metrics-binding"

// failingPodName is the name of the pod that PodSweepPolicy scenarios create to fail immediately.
const failingPodName = "failing-pod"

// scenarioLabel labels the namespaces and pods of one PodSweepPolicy scenario, so that each
// scenario's policy selects only its own objects.
const scenarioLabel = "sweeper-e2e-scenario"

const (
	currentGaugeName = "sweeper_evicted_failed_pods_current"
	deletedTotalName = "sweeper_pods_deleted_total"
	namespaceLabel   = "namespace"
	policyLabel      = "policy"
)

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		// A spec's DeferCleanup runs after this AfterAll when that spec is the last one in the
		// container, so policies can still exist here. Their finalizer needs the controller, so
		// they must be gone before undeploy or CRD deletion blocks forever.
		By("deleting all PodSweepPolicies while the controller can still finalize them")
		cmd = exec.Command("kubectl", "delete", "podsweeppolicies", "--all", "--timeout=2m")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd = exec.CommandContext(ctx, "make", "undeploy")
		// Without WaitDelay, CombinedOutput keeps waiting after the deadline kills make while
		// a kubectl child still holds the output pipe.
		cmd.WaitDelay = 10 * time.Second
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to undeploy the controller-manager")

		By("verifying the PodSweepPolicy CRD is gone")
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "crd", "podsweeppolicies.sweeper.omniva.com")
			_, err := utils.Run(cmd)
			g.Expect(err).To(MatchError(ContainSubstring("NotFound")))
		}).Should(Succeed())

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching PodSweepPolicies")
			cmd = exec.Command("kubectl", "get", "podsweeppolicies", "-o", "yaml")
			policiesOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "PodSweepPolicies:\n%s", policiesOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get PodSweepPolicies: %s", err)
			}

			By("Fetching PodSweepPolicy Warning events")
			cmd = exec.Command("kubectl", "get", "events", "-A",
				"--field-selector", "involvedObject.kind=PodSweepPolicy,type=Warning")
			policyEventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "PodSweepPolicy Warning events:\n%s", policyEventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get PodSweepPolicy Warning events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			controllerPodName = waitForControllerPod()
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=sweeper-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:8.22.0",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:8.22.0",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	Context("PodSweepPolicy", Ordered, func() {
		const metricsProbePodName = "metrics-probe"

		var metricsToken string

		scrape := func(g Gomega) string {
			return scrapeMetrics(g, metricsProbePodName, metricsToken)
		}

		expectDeletedTotal := func(testNamespace, policyName string, want float64) {
			By(fmt.Sprintf("verifying the deleted-pods counter of %s reaches %v", policyName, want))
			Eventually(func(g Gomega) {
				value, found := metricValue(scrape(g), deletedTotalName,
					map[string]string{namespaceLabel: testNamespace, policyLabel: policyName})
				g.Expect(found).To(BeTrue(), "counter series not yet present")
				g.Expect(value).To(Equal(want))
			}).Should(Succeed())
		}

		expectNoPolicySeries := func(policyName string) {
			By(fmt.Sprintf("verifying no metric series remain for %s", policyName))
			Eventually(func(g Gomega) {
				g.Expect(policySeriesNames(scrape(g), policyName)).To(BeEmpty())
			}).Should(Succeed())
		}

		BeforeAll(func() {
			ensureMetricsRoleBinding()

			By("getting the service account token for scraping metrics")
			var err error
			metricsToken, err = serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(metricsToken).NotTo(BeEmpty())

			startMetricsProbe(metricsProbePodName)
		})

		AfterAll(func() {
			stopMetricsProbe(metricsProbePodName)
		})

		It("evaluates candidates and emits metrics without deleting pods in report mode", func() {
			const testNamespace, policyName = "e2e-report-test", "e2e-report"
			createTestNamespace(testNamespace, map[string]string{scenarioLabel: policyName})
			applyPolicy(policySpec{
				name:              policyName,
				namespaceSelector: map[string]string{scenarioLabel: policyName},
				ttl:               5 * time.Second,
				mode:              sweeperv1alpha1.ModeReport,
			})
			createFailingPod(testNamespace, failingPodName, nil)
			expectEvaluatedCleanly(policyName)

			By("verifying the current-candidates gauge reflects the pod once its TTL elapses")
			Eventually(func(g Gomega) {
				value, found := metricValue(scrape(g), currentGaugeName,
					map[string]string{namespaceLabel: testNamespace, policyLabel: policyName})
				g.Expect(found).To(BeTrue(), "gauge series not yet present")
				g.Expect(value).To(BeNumerically(">=", 1))
			}).Should(Succeed())

			expectPodKept(testNamespace, failingPodName, 15*time.Second)
		})

		It("deletes TTL-expired pods and increments the deletion counter in enforce mode", func() {
			const testNamespace, policyName = "e2e-enforce-test", "e2e-enforce"
			createTestNamespace(testNamespace, map[string]string{scenarioLabel: policyName})
			applyPolicy(policySpec{
				name:              policyName,
				namespaceSelector: map[string]string{scenarioLabel: policyName},
				ttl:               5 * time.Second,
				mode:              sweeperv1alpha1.ModeEnforce,
			})
			createFailingPod(testNamespace, failingPodName, nil)
			expectEvaluatedCleanly(policyName)

			expectPodDeleted(testNamespace, failingPodName)
			expectDeletedTotal(testNamespace, policyName, 1)
		})

		It("stops deleting after switching from enforce to disable", func() {
			const testNamespace, policyName = "e2e-disable-test", "e2e-disable"
			scenario := map[string]string{scenarioLabel: policyName}
			createTestNamespace(testNamespace, scenario)
			applyPolicy(policySpec{
				name:              policyName,
				namespaceSelector: scenario,
				podSelector:       scenario,
				ttl:               5 * time.Second,
				mode:              sweeperv1alpha1.ModeEnforce,
			})

			createFailingPod(testNamespace, "pod-a", scenario)
			expectPodDeleted(testNamespace, "pod-a")
			expectDeletedTotal(testNamespace, policyName, 1)

			By("switching the policy to disable mode")
			cmd := exec.Command("kubectl", "patch", "podsweeppolicy", policyName,
				"--type=merge", "-p", `{"spec":{"mode":"disable"}}`)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to patch the PodSweepPolicy mode")

			By("verifying the conditions report the disabled policy")
			Eventually(func(g Gomega) {
				expectCondition(g, policyName, sweeperv1alpha1.ConditionTypeReady,
					metav1.ConditionTrue, reasonDisabled)
				expectCondition(g, policyName, sweeperv1alpha1.ConditionTypeEvaluated,
					metav1.ConditionFalse, reasonDisabled)
				expectCondition(g, policyName, sweeperv1alpha1.ConditionTypeNamespacesSkippedProtected,
					metav1.ConditionFalse, reasonDisabled)
			}).Should(Succeed())

			createFailingPod(testNamespace, "pod-b", scenario)
			expectPodKept(testNamespace, "pod-b", 15*time.Second)
			expectNoPolicySeries(policyName)
		})

		It("evaluates overlapping policies independently", func() {
			const (
				testNamespace = "e2e-overlap-test"
				reportPolicy  = "e2e-overlap-report"
				enforcePolicy = "e2e-overlap-enforce"
			)
			scenario := map[string]string{scenarioLabel: "e2e-overlap"}
			createTestNamespace(testNamespace, scenario)
			applyPolicy(policySpec{
				name:              reportPolicy,
				namespaceSelector: scenario,
				podSelector:       scenario,
				ttl:               5 * time.Second,
				mode:              sweeperv1alpha1.ModeReport,
			})
			// The longer TTL gives the report policy time to count the pod before the delete.
			applyPolicy(policySpec{
				name:              enforcePolicy,
				namespaceSelector: scenario,
				podSelector:       scenario,
				ttl:               30 * time.Second,
				mode:              sweeperv1alpha1.ModeEnforce,
			})
			createFailingPod(testNamespace, failingPodName, scenario)

			By("verifying the report policy counts the pod before the enforce policy deletes it")
			Eventually(func(g Gomega) {
				value, found := metricValue(scrape(g), currentGaugeName,
					map[string]string{namespaceLabel: testNamespace, policyLabel: reportPolicy})
				g.Expect(found).To(BeTrue(), "gauge series not yet present")
				g.Expect(value).To(BeNumerically(">=", 1))
				g.Expect(podExists(g, testNamespace, failingPodName)).To(BeTrue(),
					"pod was deleted before the report policy counted it")
			}).Should(Succeed())

			expectPodDeleted(testNamespace, failingPodName)
			expectDeletedTotal(testNamespace, enforcePolicy, 1)

			By("verifying the report policy never counts a delete")
			Eventually(func(g Gomega) {
				_, found := metricValue(scrape(g), deletedTotalName, map[string]string{policyLabel: reportPolicy})
				g.Expect(found).To(BeFalse(), "report policy must not have a deleted-pods series")
			}).Should(Succeed())
		})

		It("removes metric series and keeps pods when a policy is deleted", func() {
			const (
				testNamespace = "e2e-delete-test"
				enforcePolicy = "e2e-delete-enforce"
				reportPolicy  = "e2e-delete-report"
				roleLabel     = "sweeper-e2e-role"
			)
			scenario := map[string]string{scenarioLabel: "e2e-delete"}
			sweep := map[string]string{roleLabel: "sweep"}
			keep := map[string]string{roleLabel: "keep"}
			createTestNamespace(testNamespace, scenario)
			applyPolicy(policySpec{
				name:              enforcePolicy,
				namespaceSelector: scenario,
				podSelector:       sweep,
				ttl:               5 * time.Second,
				mode:              sweeperv1alpha1.ModeEnforce,
			})
			applyPolicy(policySpec{
				name:              reportPolicy,
				namespaceSelector: scenario,
				podSelector:       keep,
				ttl:               5 * time.Second,
				mode:              sweeperv1alpha1.ModeReport,
			})
			createFailingPod(testNamespace, "sweep-pod", sweep)
			createFailingPod(testNamespace, "keep-pod", keep)

			expectPodDeleted(testNamespace, "sweep-pod")
			expectDeletedTotal(testNamespace, enforcePolicy, 1)

			By("verifying the report policy gauge counts the kept pod")
			Eventually(func(g Gomega) {
				value, found := metricValue(scrape(g), currentGaugeName,
					map[string]string{namespaceLabel: testNamespace, policyLabel: reportPolicy})
				g.Expect(found).To(BeTrue(), "gauge series not yet present")
				g.Expect(value).To(BeNumerically(">=", 1))
			}).Should(Succeed())

			By("deleting both policies")
			cmd := exec.Command("kubectl", "delete", "podsweeppolicy", enforcePolicy, reportPolicy,
				"--wait=true", "--timeout=60s")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete the PodSweepPolicies")

			By("verifying the finalizer let both policies go")
			for _, policyName := range []string{enforcePolicy, reportPolicy} {
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "podsweeppolicy", policyName)
					_, err := utils.Run(cmd)
					g.Expect(err).To(MatchError(ContainSubstring("NotFound")))
				}).Should(Succeed())
			}

			expectNoPolicySeries(enforcePolicy)
			expectNoPolicySeries(reportPolicy)
			expectPodKept(testNamespace, "keep-pod", 10*time.Second)
		})

		It("reports an invalid pod selector through conditions and a Warning event", func() {
			const policyName = "e2e-invalid-selector"
			// The CRD schema accepts any matchLabels value, so only the controller rejects this one.
			applyPolicy(policySpec{
				name:              policyName,
				namespaceSelector: map[string]string{scenarioLabel: policyName},
				podSelector:       map[string]string{"app": "not a valid value!"},
				ttl:               5 * time.Second,
				mode:              sweeperv1alpha1.ModeReport,
			})

			By("verifying the conditions report the invalid pod selector")
			Eventually(func(g Gomega) {
				expectCondition(g, policyName, sweeperv1alpha1.ConditionTypeReady,
					metav1.ConditionFalse, "InvalidPodSelector")
				expectCondition(g, policyName, sweeperv1alpha1.ConditionTypeEvaluated,
					metav1.ConditionFalse, "NotReady")
			}).Should(Succeed())

			By("verifying a Warning event reports the invalid pod selector")
			Eventually(func(g Gomega) {
				g.Expect(warningEventReasons(g, policyName)).To(ContainElement("InvalidPodSelector"))
			}).Should(Succeed())
		})

		It("deletes an evicted pod after its TTL", func() {
			const testNamespace, policyName = "e2e-evicted-test", "e2e-evicted"
			createTestNamespace(testNamespace, map[string]string{scenarioLabel: policyName})
			applyPolicy(policySpec{
				name:              policyName,
				namespaceSelector: map[string]string{scenarioLabel: policyName},
				ttl:               20 * time.Second,
				mode:              sweeperv1alpha1.ModeEnforce,
			})
			createEvictedPod(testNamespace, "evicted-pod")

			// The pod has no container statuses, so its TTL counts from when the controller first
			// sees it as Failed, which is after the status patch.
			expectPodKept(testNamespace, "evicted-pod", 10*time.Second)
			expectPodDeleted(testNamespace, "evicted-pod")
			expectDeletedTotal(testNamespace, policyName, 1)
		})

		// The allow spec restarts the manager with other flags, so this context runs last.
		Context("protected namespaces", Ordered, func() {
			const (
				namespaceNameLabel   = "kubernetes.io/metadata.name"
				kubeSystem           = "kube-system"
				kubeSystemPolicy     = "e2e-protected-default"
				kubeSystemPod        = "sweeper-e2e-protected-pod"
				lockedNamespace      = "sweeper-e2e-locked"
				lockedPolicy         = "e2e-protected-locked"
				allowedNamespace     = "sweeper-e2e-protected"
				allowedPolicy        = "e2e-protected-allowed"
				protectedPodSelector = "protected-default"
			)

			var restoreManagerArgs func()

			// A DeferCleanup in the last spec runs after the outer AfterAll has undeployed the
			// manager, so the args are restored here instead.
			AfterAll(func() {
				if restoreManagerArgs != nil {
					restoreManagerArgs()
				}
			})

			applyNamespacePolicy := func(policyName, ns string, podSelector map[string]string) {
				applyPolicy(policySpec{
					name:              policyName,
					namespaceSelector: map[string]string{namespaceNameLabel: ns},
					podSelector:       podSelector,
					ttl:               5 * time.Second,
					mode:              sweeperv1alpha1.ModeEnforce,
				})
			}

			setUpKubeSystemScenario := func() {
				podLabels := map[string]string{scenarioLabel: protectedPodSelector}
				createFailingPod(kubeSystem, kubeSystemPod, podLabels)
				applyNamespacePolicy(kubeSystemPolicy, kubeSystem, podLabels)
				expectProtectedSkip(kubeSystemPolicy, kubeSystem)
			}

			It("skips kube-system by default", func() {
				setUpKubeSystemScenario()

				By("verifying a Warning event reports the excluded namespace")
				Eventually(func(g Gomega) {
					g.Expect(warningEventReasons(g, kubeSystemPolicy)).To(ContainElement(reasonProtectedExcluded))
				}).Should(Succeed())

				expectPodKept(kubeSystem, kubeSystemPod, 15*time.Second)

				By("verifying the policy has no gauge series for kube-system")
				Eventually(func(g Gomega) {
					_, found := metricValue(scrape(g), currentGaugeName,
						map[string]string{namespaceLabel: kubeSystem, policyLabel: kubeSystemPolicy})
					g.Expect(found).To(BeFalse())
				}).Should(Succeed())
			})

			It("unlocks an allowed protected namespace", func() {
				restoreManagerArgs = patchManagerArgs([]string{
					"--protected-namespace=" + strings.Join([]string{kubeSystem, allowedNamespace, lockedNamespace}, ","),
					"--allow-protected-namespace=" + allowedNamespace,
				}, &controllerPodName)

				createTestNamespace(lockedNamespace, nil)
				createTestNamespace(allowedNamespace, nil)
				createFailingPod(lockedNamespace, failingPodName, nil)
				createFailingPod(allowedNamespace, failingPodName, nil)
				applyNamespacePolicy(lockedPolicy, lockedNamespace, nil)
				applyNamespacePolicy(allowedPolicy, allowedNamespace, nil)
				setUpKubeSystemScenario()

				expectProtectedSkip(lockedPolicy, lockedNamespace)

				expectPodDeleted(allowedNamespace, failingPodName)
				Eventually(func(g Gomega) {
					expectCondition(g, allowedPolicy, sweeperv1alpha1.ConditionTypeNamespacesSkippedProtected,
						metav1.ConditionFalse, reasonNoProtectedNamespaces)
				}).Should(Succeed())

				By("verifying the pods in the protected namespaces that are not allowed stay")
				Consistently(func(g Gomega) {
					g.Expect(podExists(g, lockedNamespace, failingPodName)).To(BeTrue())
					g.Expect(podExists(g, kubeSystem, kubeSystemPod)).To(BeTrue())
				}, 15*time.Second).Should(Succeed())
			})
		})
	})
})
