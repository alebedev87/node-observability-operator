package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/google/pprof/profile"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/kubectl/pkg/cmd/cp"
	cmdtesting "k8s.io/kubectl/pkg/cmd/testing"

	operatorv1alpha2 "github.com/openshift/node-observability-operator/api/v1alpha2"
	//+kubebuilder:scaffold:imports
)

const (
	defaultTestName        = "test-instance"
	operatorNamespace      = "node-observability-operator"
	operatorDeploymentName = "node-observability-operator-controller-manager"
	agentContainerName     = "node-observability-agent"
	profilingDataDir       = "/run/node-observability"
)

// testNodeObservability - minimal CR for the test
func testNodeObservability() *operatorv1alpha2.NodeObservability {
	return &operatorv1alpha2.NodeObservability{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: operatorv1alpha2.NodeObservabilitySpec{
			NodeSelector: map[string]string{
				"node-role.kubernetes.io/worker": "",
			},
			Type: operatorv1alpha2.CrioKubeletNodeObservabilityType,
		},
	}
}

// testNodeObservabilityRun - minimal CR for the test
func testNodeObservabilityRun(testName string) *operatorv1alpha2.NodeObservabilityRun {
	return &operatorv1alpha2.NodeObservabilityRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testName,
			Namespace: operatorNamespace,
		},
		Spec: operatorv1alpha2.NodeObservabilityRunSpec{
			NodeObservabilityRef: &operatorv1alpha2.NodeObservabilityRef{
				Name: "cluster",
			},
		},
	}
}

func testNodeObservabilityMachineConfig(testName string, nodeSelector map[string]string, enable bool) *operatorv1alpha2.NodeObservabilityMachineConfig {
	return &operatorv1alpha2.NodeObservabilityMachineConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: testName,
		},
		Spec: operatorv1alpha2.NodeObservabilityMachineConfigSpec{
			NodeSelector: nodeSelector,
			Debug: operatorv1alpha2.NodeObservabilityDebug{
				EnableCrioProfiling: enable,
			},
		},
	}
}

// waitForOperatorDeploymentStatusCondition waits for the given condition(s) on the operator deployment.
func waitForOperatorDeploymentStatusCondition(cl client.Client, conditions ...appsv1.DeploymentCondition) error {
	return wait.Poll(2*time.Second, 1*time.Minute, func() (bool, error) {
		dep := &appsv1.Deployment{}
		name := types.NamespacedName{
			Name:      operatorDeploymentName,
			Namespace: operatorNamespace,
		}
		if err := cl.Get(context.TODO(), name, dep); err != nil {
			return false, nil
		}

		expected := deploymentConditionMap(conditions...)
		current := deploymentConditionMap(dep.Status.Conditions...)
		return conditionsMatchExpected(expected, current), nil
	})
}

// operatorScheduledNodeName returns the node name assigned to the operator's POD.
func operatorScheduledNodeName(cl client.Client) (string, error) {
	pods := &corev1.PodList{}
	if err := cl.List(context.TODO(), pods, client.InNamespace(operatorNamespace)); err != nil {
		return "", err
	}

	for _, p := range pods.Items {
		if strings.HasPrefix(p.Name, operatorDeploymentName) {
			return p.Spec.NodeName, nil
		}
	}
	return "", nil
}

func deploymentConditionMap(conditions ...appsv1.DeploymentCondition) map[string]string {
	conds := map[string]string{}
	for _, cond := range conditions {
		conds[string(cond.Type)] = string(cond.Status)
	}
	return conds
}

func conditionsMatchExpected(expected, actual map[string]string) bool {
	filtered := map[string]string{}
	for k := range actual {
		if _, comparable := expected[k]; comparable {
			filtered[k] = actual[k]
		}
	}
	return reflect.DeepEqual(expected, filtered)
}

func verifyPodPprofData(clientConfig *restclient.Config, clientSet *kubernetes.Clientset, pod string) error {
	tempDir, err := os.MkdirTemp("", "nobpprof")
	if err != nil {
		return fmt.Errorf("failed to create temp dir for profiling data: %w", err)
	}

	src := fmt.Sprintf("%s:%s", pod, profilingDataDir)
	dst := tempDir
	if err := podCopy(clientConfig, clientSet, operatorNamespace, agentContainerName, src, dst); err != nil {
		return fmt.Errorf("failed to copy profiling data from pod %q: %w", pod, err)
	}

	files, err := os.ReadDir(dst)
	if err != nil {
		return fmt.Errorf("failed to read profiling data dir %q: %w", dst, err)
	}

	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".pprof") {
			fpath := filepath.Join(dst, f.Name())
			if err := verifyPprofFile(fpath); err != nil {
				return fmt.Errorf("failed to verify pprof file %q: %w", fpath, err)
			}
		}
	}

	return nil
}

type testCmdFactory struct {
	*cmdtesting.TestFactory
	clientSet *kubernetes.Clientset
	config    *restclient.Config
}

func (f *testCmdFactory) KubernetesClientSet() (*kubernetes.Clientset, error) {
	return f.clientSet, nil
}

func (f *testCmdFactory) ToRESTConfig() (*restclient.Config, error) {
	return f.config, nil
}

func podCopy(clientCfg *restclient.Config, clientSet *kubernetes.Clientset, ns, container, src, dst string) error {
	tf := &testCmdFactory{
		TestFactory: cmdtesting.NewTestFactory().WithNamespace(ns),
		clientSet:   clientSet,
		config:      clientCfg,
	}
	ioStreams, _, _, _ := genericclioptions.NewTestIOStreams()
	cmd := cp.NewCmdCp(tf, ioStreams)
	opts := cp.NewCopyOptions(ioStreams)
	if err := opts.Complete(tf, cmd, []string{src, dst}); err != nil {
		return fmt.Errorf("failed to complete copy command options: %w", err)
	}
	opts.Container = container
	if err := opts.Run(); err != nil {
		return fmt.Errorf("failed to run copy command: %w", err)
	}
	return nil
}

func verifyPprofFile(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("failed to open pprof file: %w", err)
	}

	profile, err := profile.Parse(f)
	if err != nil {
		return fmt.Errorf("failed to parse pprof file: %w", err)
	}

	if err := profile.CheckValid(); err != nil {
		return fmt.Errorf("invalid pprof file: %w", err)
	}

	return nil
}
