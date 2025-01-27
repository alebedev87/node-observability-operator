package nodeobservabilityruncontroller

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	operatorv1alpha2 "github.com/openshift/node-observability-operator/api/v1alpha2"
	"github.com/openshift/node-observability-operator/pkg/operator/controller/utils/test"
	"github.com/openshift/node-observability-operator/pkg/utils"
)

const (
	name        = "node-observability-agent"
	nodeObsName = "cluster"
	namespace   = "test"
	caPath      = "../../../../hack/certs/serving-ca.crt"
	certPath    = "../../../../hack/certs/serving-localhost.crt"
	keyPath     = "../../../../hack/certs/serving-localhost.key"
)

func TestGetAgentEndpoints(t *testing.T) {
	caCert, err := utils.ReadCACert(caPath)
	if err != nil {
		t.Fatal("Failed to read the test CA")
	}
	cases := []struct {
		name            string
		errExpected     bool
		existingObjects []runtime.Object
	}{
		{
			name:            "not found",
			errExpected:     true,
			existingObjects: nil,
		},
		{
			name:        "found",
			errExpected: false,
			existingObjects: []runtime.Object{
				&corev1.Endpoints{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Subsets: []corev1.EndpointSubset{
						{Ports: []corev1.EndpointPort{{Name: "test-port"}}},
					},
				},
			},
		},
		{
			name:        "no subsets",
			errExpected: true,
			existingObjects: []runtime.Object{
				&corev1.Endpoints{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Subsets:    []corev1.EndpointSubset{},
				},
			},
		},
		{
			name:        "too many ports",
			errExpected: true,
			existingObjects: []runtime.Object{
				&corev1.Endpoints{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Subsets: []corev1.EndpointSubset{
						{Ports: []corev1.EndpointPort{{Name: "test-port"}, {Name: "test-port-2"}}},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		cl := fake.NewClientBuilder().WithScheme(test.Scheme).WithRuntimeObjects(tc.existingObjects...).Build()
		r := NodeObservabilityRunReconciler{
			Client:    cl,
			Namespace: namespace,
			CACert:    caCert,
		}
		_, err := r.getAgentEndpoints(context.TODO())
		if err != nil {
			if tc.errExpected {
				return
			}
			t.Fatalf("%s: error getting endpoints: %v", tc.name, err)
		}
	}
}

func TestHandleFailingAgent(t *testing.T) {
	testInstance := testNodeObservabilityRun()
	cases := []struct {
		inputAgents    []operatorv1alpha2.AgentNode
		inputFailed    []operatorv1alpha2.AgentNode
		failingNode    operatorv1alpha2.AgentNode
		expectedAgents []operatorv1alpha2.AgentNode
		expectedFailed []operatorv1alpha2.AgentNode
	}{
		{
			inputAgents:    []operatorv1alpha2.AgentNode{{Name: "good"}, {Name: "failing"}},
			inputFailed:    nil,
			failingNode:    operatorv1alpha2.AgentNode{Name: "failing"},
			expectedAgents: []operatorv1alpha2.AgentNode{{Name: "good"}},
			expectedFailed: []operatorv1alpha2.AgentNode{{Name: "failing"}},
		},
		{
			// not in list
			inputAgents:    []operatorv1alpha2.AgentNode{{Name: "one"}, {Name: "two"}, {Name: "three"}},
			inputFailed:    nil,
			failingNode:    operatorv1alpha2.AgentNode{Name: "failing"},
			expectedAgents: []operatorv1alpha2.AgentNode{{Name: "one"}, {Name: "two"}, {Name: "three"}},
			expectedFailed: nil,
		},
		{
			// last non-failing agent
			inputAgents:    []operatorv1alpha2.AgentNode{{Name: "failing"}},
			inputFailed:    []operatorv1alpha2.AgentNode{{Name: "one"}, {Name: "two"}, {Name: "three"}},
			failingNode:    operatorv1alpha2.AgentNode{Name: "failing"},
			expectedAgents: nil,
			expectedFailed: []operatorv1alpha2.AgentNode{{Name: "one"}, {Name: "two"}, {Name: "three"}, {Name: "failing"}},
		},
	}
	for _, tc := range cases {
		testInstance.Status.Agents = tc.inputAgents
		testInstance.Status.FailedAgents = tc.inputFailed

		handleFailingAgent(testInstance, tc.failingNode)

		if !reflect.DeepEqual(tc.expectedAgents, testInstance.Status.Agents) {
			t.Fatalf("Agents: expected result %v, got %v", tc.expectedAgents, testInstance.Status.Agents)
		}
		if !reflect.DeepEqual(tc.expectedFailed, testInstance.Status.FailedAgents) {
			t.Fatalf("FailingAgents: expected result %v, got %v", tc.expectedFailed, testInstance.Status.FailedAgents)
		}
	}
}

func TestIsFinished(t *testing.T) {
	testInstance := testNodeObservabilityRun()
	now := metav1.Now()
	zero := metav1.Now()
	zero.Reset()
	cases := []struct {
		finished bool
		time     *metav1.Time
	}{
		{
			finished: false,
			time:     nil,
		},
		{
			finished: false,
			time:     &zero,
		},
		{
			finished: true,
			time:     &now,
		},
	}
	for _, tc := range cases {
		testInstance.Status.FinishedTimestamp = tc.time
		gotResult := runFinished(testInstance)
		if gotResult != tc.finished {
			t.Fatalf("expected result %t, got %t", tc.finished, gotResult)
		}
	}
}

func TestIsInProgress(t *testing.T) {
	testInstance := testNodeObservabilityRun()
	now := metav1.Now()
	zero := metav1.Now()
	zero.Reset()
	cases := []struct {
		inProgress bool
		time       *metav1.Time
	}{
		{
			inProgress: false,
			time:       nil,
		},
		{
			inProgress: false,
			time:       &zero,
		},
		{
			inProgress: true,
			time:       &now,
		},
	}
	for _, tc := range cases {
		testInstance.Status.StartTimestamp = tc.time
		gotResult := runInProgress(testInstance)
		if gotResult != tc.inProgress {
			t.Fatalf("expected result %t, got %t", tc.inProgress, gotResult)
		}
	}
}

func TestReconcile(t *testing.T) {
	errCh := make(chan error)
	go func() {
		http.HandleFunc("/node-observability-pprof", pong)
		http.HandleFunc("/node-observability-status", pong)

		server := &http.Server{
			Addr:              "127.0.0.1:8443",
			ReadHeaderTimeout: 3 * time.Second,
		}
		if err := server.ListenAndServeTLS(certPath, keyPath); err != nil {
			errCh <- err
		}
	}()
	select {
	case <-time.After(time.Second * 2):
	case err := <-errCh:
		t.Fatal("Failed to run the fake http server:", err)
	}
	caCert, err := utils.ReadCACert(caPath)
	if err != nil {
		t.Fatal("Failed to read the test CA")
	}

	now := metav1.Now()
	ctx := logr.NewContext(context.Background(), zap.New(zap.UseDevMode(true)))
	cases := []struct {
		name            string
		existingObjects []runtime.Object
		req             reconcile.Request
		res             ctrl.Result
		errExpected     bool
		started         bool
		finished        bool
	}{
		{
			name:            "doesn't exist",
			existingObjects: nil,
			res:             ctrl.Result{},
			req:             reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}},
		},
		{
			name: "finished",
			existingObjects: []runtime.Object{
				testNodeObservabilityRunWithStatus(operatorv1alpha2.NodeObservabilityRunStatus{
					FinishedTimestamp: &now,
				}),
			},
			res:      ctrl.Result{},
			req:      reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}},
			finished: true,
		},
		{
			name: "start new run",
			existingObjects: []runtime.Object{
				testNodeObservability(),
				testNodeObservabilityRun(),
				&corev1.Endpoints{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Subsets: []corev1.EndpointSubset{
						{
							Addresses: []corev1.EndpointAddress{{IP: "127.0.0.1", TargetRef: &corev1.ObjectReference{Name: name}}},
							Ports:     []corev1.EndpointPort{{Name: "test-port", Port: 8443}},
						},
					},
				},
			},
			res:     ctrl.Result{RequeueAfter: time.Second * 30},
			req:     reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}},
			started: true,
		},
		{
			name: "run in progress",
			existingObjects: []runtime.Object{
				testNodeObservability(),
				testNodeObservabilityRunWithStatus(operatorv1alpha2.NodeObservabilityRunStatus{
					StartTimestamp: &now,
				}),
				&corev1.Endpoints{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Subsets: []corev1.EndpointSubset{
						{
							Addresses: []corev1.EndpointAddress{{IP: "127.0.0.1", TargetRef: &corev1.ObjectReference{Name: name}}},
							Ports:     []corev1.EndpointPort{{Name: "test-port", Port: 8443}},
						},
					},
				},
			},
			res:      ctrl.Result{},
			req:      reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}},
			started:  true,
			finished: true,
		},
	}
	for _, tc := range cases {
		cl := fake.NewClientBuilder().WithScheme(test.Scheme).WithRuntimeObjects(tc.existingObjects...).Build()
		r := NodeObservabilityRunReconciler{
			Client:    cl,
			Namespace: namespace,
			CACert:    caCert,
		}
		res, err := r.Reconcile(ctx, tc.req)
		if err != nil {
			if !tc.errExpected {
				t.Fatalf("%s: reconciler error: %v", tc.name, err)
			}
		}
		if !reflect.DeepEqual(res, tc.res) {
			t.Fatalf("%s: expected result %v, got %v", tc.name, tc.res, res)
		}

		got := &operatorv1alpha2.NodeObservabilityRun{}
		if err := cl.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, got); err != nil && !errors.IsNotFound(err) {
			t.Fatalf("%s: error while trying to get NodeObservabilityRun  %v", tc.name, res)
		}

		if tc.started && !runInProgress(got) {
			t.Fatalf("%s: error NodeObservabilityRun should be running", tc.name)
		}
		if tc.finished && !runFinished(got) {
			t.Fatalf("%s: error NodeObservabilityRun should be finished", tc.name)
		}
	}

}

// testNodeObservabilityRun - minimal CR for the test
func testNodeObservabilityRun() *operatorv1alpha2.NodeObservabilityRun {
	return &operatorv1alpha2.NodeObservabilityRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind: "NodeObservability",
					Name: nodeObsName,
				},
			},
		},
		Spec: operatorv1alpha2.NodeObservabilityRunSpec{
			NodeObservabilityRef: &operatorv1alpha2.NodeObservabilityRef{
				Name: nodeObsName,
			},
		},
		Status: operatorv1alpha2.NodeObservabilityRunStatus{},
	}
}

// testNodeObservability - minimal CR for the test
func testNodeObservability() *operatorv1alpha2.NodeObservability {
	return &operatorv1alpha2.NodeObservability{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeObsName,
		},
		Spec: operatorv1alpha2.NodeObservabilitySpec{},
		Status: operatorv1alpha2.NodeObservabilityStatus{
			ConditionalStatus: operatorv1alpha2.ConditionalStatus{
				Conditions: []metav1.Condition{
					{
						Type:   operatorv1alpha2.DebugReady,
						Status: metav1.ConditionTrue,
					},
				},
			},
		},
	}
}

func testNodeObservabilityRunWithStatus(s operatorv1alpha2.NodeObservabilityRunStatus) *operatorv1alpha2.NodeObservabilityRun {
	run := testNodeObservabilityRun()
	run.Status = s
	return run
}

func pong(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("pong\n"))
}
