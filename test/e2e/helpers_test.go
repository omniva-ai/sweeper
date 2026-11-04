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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
	"github.com/omniva-ai/sweeper/test/utils"
)

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

// Condition reasons that the controller sets on a PodSweepPolicy.
const (
	reasonValidated             = "Validated"
	reasonEvaluated             = "Evaluated"
	reasonNoProtectedNamespaces = "NoProtectedNamespaces"
	reasonDisabled              = "Disabled"
	reasonProtectedExcluded     = "ProtectedNamespacesExcluded"
)

const (
	managerDeploymentName = "sweeper-controller-manager"
	managerArgsPath       = "/spec/template/spec/containers/0/args"
)

// waitForControllerPod waits until exactly one controller-manager pod that is not terminating
// is Running, and returns its name.
func waitForControllerPod() string {
	var podName string
	Eventually(func(g Gomega) {
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
		g.Expect(podNames[0]).To(ContainSubstring("controller-manager"))

		cmd = exec.Command("kubectl", "get",
			"pods", podNames[0], "-o", "jsonpath={.status.phase}",
			"-n", namespace,
		)
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
		podName = podNames[0]
	}).Should(Succeed())
	return podName
}

// patchManagerArgs appends args to the manager container, waits for the new controller pod to
// hold the leader lease and stores its name in controllerPod. The returned func restores the
// original args the same way.
func patchManagerArgs(args []string, controllerPod *string) (restore func()) {
	cmd := exec.Command("kubectl", "get", "deployment", managerDeploymentName, "-n", namespace,
		"-o", "jsonpath={.spec.template.spec.containers[0].args}")
	originalArgs, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to read the manager args")

	ops := make([]map[string]any, 0, len(args))
	for _, arg := range args {
		ops = append(ops, map[string]any{"op": "add", "path": managerArgsPath + "/-", "value": arg})
	}
	By(fmt.Sprintf("restarting the manager with the extra args %v", args))
	rolloutManager(ops, controllerPod)

	return func() {
		By("restoring the original manager args")
		rolloutManager([]map[string]any{
			{"op": "replace", "path": managerArgsPath, "value": json.RawMessage(originalArgs)},
		}, controllerPod)
	}
}

// rolloutManager applies the JSON patch ops to the manager Deployment, waits for the rollout and
// for the new controller pod to hold the leader lease, and stores the pod name in controllerPod.
func rolloutManager(ops []map[string]any, controllerPod *string) {
	patch, err := json.Marshal(ops)
	Expect(err).NotTo(HaveOccurred())
	cmd := exec.Command("kubectl", "patch", "deployment", managerDeploymentName, "-n", namespace,
		"--type=json", "-p", string(patch))
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to patch the manager Deployment")

	cmd = exec.Command("kubectl", "rollout", "status", "deployment/"+managerDeploymentName,
		"-n", namespace, "--timeout=3m")
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "manager rollout did not finish")

	*controllerPod = waitForControllerPod()

	// The old pod does not release the lease on exit, so the new pod waits for it to expire.
	By("waiting for the new controller pod to acquire the leader lease")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "logs", *controllerPod, "-n", namespace)
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(MatchRegexp("(?i)successfully acquired lease"))
	}).Should(Succeed())
}

// applyObject applies obj, encoded as JSON, with kubectl apply.
func applyObject(obj any) {
	manifest, err := json.Marshal(obj)
	Expect(err).NotTo(HaveOccurred())
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(string(manifest))
	_, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to apply %s", manifest)
}

// createTestNamespace creates a namespace with the given labels and deletes it when the spec ends.
func createTestNamespace(name string, labels map[string]string) {
	By(fmt.Sprintf("creating the test namespace %s", name))
	applyObject(&corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	})
	DeferCleanup(func() {
		cmd := exec.Command("kubectl", "delete", "ns", name, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})
}

// policySpec describes a PodSweepPolicy that selects namespaces and pods by matchLabels.
type policySpec struct {
	name              string
	namespaceSelector map[string]string
	podSelector       map[string]string
	ttl               time.Duration
	mode              string
}

// applyPolicy applies p and deletes the policy when the spec ends.
func applyPolicy(p policySpec) {
	By(fmt.Sprintf("applying the %s-mode PodSweepPolicy %s", p.mode, p.name))
	applyObject(&sweeperv1alpha1.PodSweepPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: sweeperv1alpha1.GroupVersion.String(),
			Kind:       "PodSweepPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{Name: p.name},
		Spec: sweeperv1alpha1.PodSweepPolicySpec{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: p.namespaceSelector},
			PodSelector:       &metav1.LabelSelector{MatchLabels: p.podSelector},
			TTL:               metav1.Duration{Duration: p.ttl},
			Mode:              p.mode,
		},
	})
	DeferCleanup(func() {
		cmd := exec.Command("kubectl", "delete", "podsweeppolicy", p.name, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})
}

// createFailingPod creates a pod whose only container exits 1, waits until the pod is Failed,
// and deletes the pod when the spec ends.
func createFailingPod(ns, name string, labels map[string]string) {
	By(fmt.Sprintf("creating the pod %s/%s that fails immediately", ns, name))
	applyObject(&corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "fail",
				Image:   "busybox:1.36",
				Command: []string{"sh", "-c", "exit 1"},
			}},
		},
	})
	DeferCleanup(func() {
		cmd := exec.Command("kubectl", "delete", "pod", name, "-n", ns, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})
	waitForPodPhase(ns, name, corev1.PodFailed)
}

// createEvictedPod creates a pod that no node can schedule, then sets its status to the Failed
// phase with the reason Evicted, as the kubelet does when it evicts a pod. No kubelet owns the
// pod, so nothing overwrites the faked status. The pod is deleted when the spec ends.
func createEvictedPod(ns, name string) {
	By(fmt.Sprintf("creating the pod %s/%s that no node can schedule", ns, name))
	applyObject(&corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeSelector:  map[string]string{"sweeper-e2e/unschedulable": "true"},
			Containers: []corev1.Container{{
				Name:    "never-runs",
				Image:   "busybox:1.36",
				Command: []string{"sh", "-c", "exit 0"},
			}},
		},
	})
	DeferCleanup(func() {
		cmd := exec.Command("kubectl", "delete", "pod", name, "-n", ns, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})
	waitForPodPhase(ns, name, corev1.PodPending)

	By(fmt.Sprintf("marking the pod %s/%s as evicted", ns, name))
	cmd := exec.Command("kubectl", "patch", "pod", name, "-n", ns, "--subresource=status", "--type=merge",
		"-p", `{"status":{"phase":"Failed","reason":"Evicted","message":"The node was low on resource: memory."}}`)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to patch the pod status")

	cmd = exec.Command("kubectl", "get", "pod", name, "-n", ns, "-o", "jsonpath={.status.phase}/{.status.reason}")
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal("Failed/Evicted"))
}

// waitForPodPhase waits until the pod reaches phase.
func waitForPodPhase(ns, name string, phase corev1.PodPhase) {
	By(fmt.Sprintf("waiting for the pod %s/%s to reach the %s phase", ns, name, phase))
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "get", "pod", name, "-n", ns, "-o", "jsonpath={.status.phase}")
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal(string(phase)))
	}).Should(Succeed())
}

// podExists reports whether the pod exists. It fails the assertion on any kubectl error.
func podExists(g Gomega, ns, name string) bool {
	cmd := exec.Command("kubectl", "get", "pod", name, "-n", ns, "--ignore-not-found", "-o", "name")
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(output) != ""
}

// expectPodDeleted waits until the pod is gone.
func expectPodDeleted(ns, name string) {
	By(fmt.Sprintf("waiting for the pod %s/%s to be deleted", ns, name))
	Eventually(func(g Gomega) {
		g.Expect(podExists(g, ns, name)).To(BeFalse(), "pod should have been deleted")
	}).Should(Succeed())
}

// expectPodKept asserts that the pod exists for the whole duration.
func expectPodKept(ns, name string, duration time.Duration) {
	By(fmt.Sprintf("verifying the pod %s/%s is not deleted", ns, name))
	Consistently(func(g Gomega) {
		g.Expect(podExists(g, ns, name)).To(BeTrue(), "pod should not be deleted")
	}, duration).Should(Succeed())
}

// policyCondition returns the status, reason and message of the policy's condType condition.
// It fails the assertion when the condition is absent.
func policyCondition(g Gomega, policy, condType string) (status, reason, message string) {
	cmd := exec.Command("kubectl", "get", "podsweeppolicy", policy, "-o", "json")
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	var obj sweeperv1alpha1.PodSweepPolicy
	g.Expect(json.Unmarshal([]byte(output), &obj)).To(Succeed())
	for _, c := range obj.Status.Conditions {
		if c.Type == condType {
			return string(c.Status), c.Reason, c.Message
		}
	}
	g.Expect(obj.Status.Conditions).To(ContainElement(HaveField("Type", condType)))
	return "", "", ""
}

// expectCondition asserts the status and reason of the policy's condType condition.
func expectCondition(g Gomega, policy, condType string, status metav1.ConditionStatus, reason string) {
	gotStatus, gotReason, message := policyCondition(g, policy, condType)
	g.Expect(gotStatus).To(Equal(string(status)), "%s condition: %s", condType, message)
	g.Expect(gotReason).To(Equal(reason), "%s condition: %s", condType, message)
}

// expectProtectedSkip waits until the policy reports ns as an excluded protected namespace.
func expectProtectedSkip(policy, ns string) {
	By(fmt.Sprintf("verifying the PodSweepPolicy %s skips the protected namespace %s", policy, ns))
	Eventually(func(g Gomega) {
		status, reason, message := policyCondition(g, policy,
			sweeperv1alpha1.ConditionTypeNamespacesSkippedProtected)
		g.Expect(status).To(Equal(string(metav1.ConditionTrue)), message)
		g.Expect(reason).To(Equal(reasonProtectedExcluded))
		g.Expect(message).To(ContainSubstring(ns))
	}).Should(Succeed())
}

// warningEventReasons returns the reasons of the Warning events about the policy. The policy is
// cluster-scoped, so its events can be in any namespace.
func warningEventReasons(g Gomega, policy string) []string {
	cmd := exec.Command("kubectl", "get", "events", "-A",
		"--field-selector", "involvedObject.kind=PodSweepPolicy,type=Warning,involvedObject.name="+policy,
		"-o", `jsonpath={range .items[*]}{.reason}{"\n"}{end}`)
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred())
	return utils.GetNonEmptyLines(output)
}

// expectEvaluatedCleanly waits until the policy is Ready, evaluated and has no protected
// namespaces excluded, and asserts that the policy has no Warning events.
func expectEvaluatedCleanly(policy string) {
	By(fmt.Sprintf("verifying the conditions of the PodSweepPolicy %s", policy))
	Eventually(func(g Gomega) {
		expectCondition(g, policy, sweeperv1alpha1.ConditionTypeReady, metav1.ConditionTrue, reasonValidated)
		expectCondition(g, policy, sweeperv1alpha1.ConditionTypeEvaluated, metav1.ConditionTrue, reasonEvaluated)
		expectCondition(g, policy, sweeperv1alpha1.ConditionTypeNamespacesSkippedProtected,
			metav1.ConditionFalse, reasonNoProtectedNamespaces)
	}).Should(Succeed())
	Expect(warningEventReasons(Default, policy)).To(BeEmpty())
}

// policySeriesNames returns the names of the sweeper metrics that have a series for the policy.
func policySeriesNames(output, policy string) []string {
	var names []string
	for _, name := range []string{currentGaugeName, deletedTotalName} {
		if _, found := metricValue(output, name, map[string]string{policyLabel: policy}); found {
			names = append(names, name)
		}
	}
	return names
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
		"--image=curlimages/curl:8.22.0",
		"--overrides",
		`{
			"spec": {
				"containers": [{
					"name": "curl",
					"image": "curlimages/curl:8.22.0",
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
// response body. It fails g on an HTTP error or a body without the controller-runtime metrics, so
// that a missing series means the series is absent and not that the scrape failed.
func scrapeMetrics(g Gomega, probePodName, token string) string {
	cmd := exec.Command("kubectl", "exec", probePodName, "-n", namespace, "--",
		"curl", "-sS", "-f", "-k",
		"-H", fmt.Sprintf("Authorization: Bearer %s", token),
		fmt.Sprintf("https://%s.%s.svc.cluster.local:8443/metrics", metricsServiceName, namespace),
	)
	output, err := utils.Run(cmd)
	g.Expect(err).NotTo(HaveOccurred(), "Failed to curl metrics endpoint")
	g.Expect(output).To(ContainSubstring("controller_runtime_reconcile_total"),
		"metrics response has no controller-runtime metrics")
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
