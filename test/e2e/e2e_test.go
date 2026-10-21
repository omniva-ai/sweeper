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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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

// failingPodName is the name of the pod created by runPodSweepPolicyScenario to exercise
// PodSweepPolicy's report/enforce behavior.
const failingPodName = "failing-pod"

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
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

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
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
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
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
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
			runPodSweepPolicyScenario(scenarioParams{
				testNamespace: "e2e-report-test",
				policyName:    "e2e-report",
				mode:          "report",
			}, func(testNamespace, policyName string) {
				By("verifying the current-candidates gauge reflects the pod once its TTL elapses")
				Eventually(func(g Gomega) {
					output := scrapeMetrics(metricsProbePodName, metricsToken)
					value, found := metricValue(output, "sweeper_evicted_failed_pods_current",
						map[string]string{"namespace": testNamespace, "policy": policyName})
					g.Expect(found).To(BeTrue(), "gauge series not yet present")
					g.Expect(value).To(BeNumerically(">=", 1))
				}, 2*time.Minute).Should(Succeed())

				By("verifying the pod is never deleted in report mode")
				Consistently(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "pod", failingPodName, "-n", testNamespace)
					_, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred(), "pod should not be deleted in report mode")
				}, 15*time.Second).Should(Succeed())
			})
		})

		It("deletes TTL-expired pods and increments the deletion counter in enforce mode", func() {
			runPodSweepPolicyScenario(scenarioParams{
				testNamespace: "e2e-enforce-test",
				policyName:    "e2e-enforce",
				mode:          "enforce",
			}, func(testNamespace, policyName string) {
				By("waiting for the pod to be deleted once its TTL elapses")
				Eventually(func(g Gomega) {
					cmd := exec.Command("kubectl", "get", "pod", failingPodName, "-n", testNamespace,
						"--ignore-not-found", "-o", "name")
					output, err := utils.Run(cmd)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(output).To(BeEmpty(), "pod should have been deleted")
				}, 2*time.Minute).Should(Succeed())

				By("verifying the deleted-pods counter incremented")
				Eventually(func(g Gomega) {
					output := scrapeMetrics(metricsProbePodName, metricsToken)
					value, found := metricValue(output, "sweeper_pods_deleted_total",
						map[string]string{"namespace": testNamespace, "policy": policyName})
					g.Expect(found).To(BeTrue(), "counter series not yet present")
					g.Expect(value).To(Equal(float64(1)))
				}, 2*time.Minute).Should(Succeed())
			})
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}

// scenarioParams describes one PodSweepPolicy end-to-end scenario: a namespace-scoped policy,
// applied at the given mode, evaluating a single pod that fails immediately.
type scenarioParams struct {
	testNamespace string
	policyName    string
	mode          string
}

// runPodSweepPolicyScenario creates an isolated namespace labeled for params.policyName's
// selector, applies a PodSweepPolicy at params.mode with a short TTL, creates a pod that fails
// immediately, waits for it to reach the Failed phase, then hands off to assert for
// mode-specific behavior. The namespace and policy are torn down via DeferCleanup regardless of
// outcome.
func runPodSweepPolicyScenario(params scenarioParams, assert func(testNamespace, policyName string)) {
	By("creating the test namespace")
	nsManifest := fmt.Sprintf(`
apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    sweeper-e2e-scenario: %s
`, params.testNamespace, params.policyName)
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(nsManifest)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")
	DeferCleanup(func() {
		cmd := exec.Command("kubectl", "delete", "ns", params.testNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	By(fmt.Sprintf("applying a %s-mode PodSweepPolicy scoped to the test namespace", params.mode))
	policyManifest := fmt.Sprintf(`
apiVersion: sweeper.omniva.com/v1alpha1
kind: PodSweepPolicy
metadata:
  name: %s
spec:
  namespaceSelector:
    matchLabels:
      sweeper-e2e-scenario: %s
  podSelector: {}
  ttl: 5s
  mode: %s
`, params.policyName, params.policyName, params.mode)
	cmd = exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(policyManifest)
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to apply PodSweepPolicy")
	DeferCleanup(func() {
		cmd := exec.Command("kubectl", "delete", "podsweeppolicy", params.policyName, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	By("creating a pod that fails immediately")
	podManifest := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  restartPolicy: Never
  containers:
  - name: fail
    image: busybox:1.36
    command: ["sh", "-c", "exit 1"]
`, failingPodName, params.testNamespace)
	cmd = exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(podManifest)
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to create failing pod")

	By("waiting for the pod to reach Failed phase")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "pod", failingPodName, "-n", params.testNamespace,
			"-o", "jsonpath={.status.phase}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("Failed"))
	}).Should(Succeed())

	assert(params.testNamespace, params.policyName)
}

// ensureMetricsRoleBinding creates the ClusterRoleBinding that lets the controller's service
// account read the metrics endpoint, if it doesn't already exist (the "Manager" context's
// metrics-endpoint spec creates the same binding, but PodSweepPolicy scenarios must not depend
// on that spec having run first).
func ensureMetricsRoleBinding() {
	cmd := exec.Command("kubectl", "get", "clusterrolebinding", metricsRoleBindingName)
	if _, err := utils.Run(cmd); err == nil {
		return
	}

	By("creating a ClusterRoleBinding for the service account to allow access to metrics")
	cmd = exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
		"--clusterrole=sweeper-metrics-reader",
		fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
	)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")
}

// startMetricsProbe creates a long-lived pod that can be curled repeatedly via kubectl exec, so
// scenarios can poll a metric value without paying pod-creation cost on every Eventually
// iteration.
func startMetricsProbe(podName string) {
	By(fmt.Sprintf("creating the %s pod to poll the metrics endpoint", podName))
	cmd := exec.Command("kubectl", "run", podName, "--restart=Never",
		"--namespace", namespace,
		"--image=curlimages/curl:latest",
		"--overrides",
		`{
			"spec": {
				"containers": [{
					"name": "curl",
					"image": "curlimages/curl:latest",
					"command": ["sleep", "3600"],
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
				}]
			}
		}`,
	)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to create %s pod", podName))

	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "pods", podName, "-o", "jsonpath={.status.phase}", "-n", namespace)
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("Running"))
	}).Should(Succeed())
}

// stopMetricsProbe deletes the pod started by startMetricsProbe.
func stopMetricsProbe(podName string) {
	cmd := exec.Command("kubectl", "delete", "pod", podName, "-n", namespace, "--ignore-not-found")
	_, _ = utils.Run(cmd)
}

// scrapeMetrics curls the metrics endpoint from inside the given probe pod and returns the raw
// response body.
func scrapeMetrics(probePodName, token string) string {
	cmd := exec.Command("kubectl", "exec", probePodName, "-n", namespace, "--",
		"curl", "-s", "-k",
		"-H", fmt.Sprintf("Authorization: Bearer %s", token),
		fmt.Sprintf("https://%s.%s.svc.cluster.local:8443/metrics", metricsServiceName, namespace),
	)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to curl metrics endpoint")
	return output
}

// metricValue scans Prometheus text-exposition output for a series matching metricName and
// every given label, returning its value. It reports found=false if no matching series exists
// yet (e.g. before the controller has evaluated the policy for the first time).
func metricValue(output, metricName string, labels map[string]string) (value float64, found bool) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, metricName+"{") {
			continue
		}

		matchesAllLabels := true
		for k, v := range labels {
			if !strings.Contains(line, fmt.Sprintf(`%s="%s"`, k, v)) {
				matchesAllLabels = false
				break
			}
		}
		if !matchesAllLabels {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		return val, true
	}
	return 0, false
}
