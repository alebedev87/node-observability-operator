package nodeobservabilitycontroller

import (
	"context"
	"fmt"
	"net"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1alpha2 "github.com/openshift/node-observability-operator/api/v1alpha2"
)

const (
	podName           = "node-observability-agent"
	kbltCAMountPath   = "/var/run/secrets/kubelet-serving-ca/"
	kbltCAMountedFile = "ca-bundle.crt"
	kbltCAName        = "kubelet-ca"
	defaultScheduler  = "default-scheduler"
	daemonSetName     = "node-observability-ds"
	certsName         = "certs"
	certsMountPath    = "/var/run/secrets/openshift.io/certs"
	// kubeRBACProxyAddr binds on the any address to accept the connections from everywhere.
	kubeRBACProxyHost = "0.0.0.0"
	// kubeBRACProxyPort uses a port from 9000-9999 range.
	// More info on the port registry: https://github.com/openshift/enhancements/blob/master/dev-guide/host-port-registry.md
	kubeRBACProxyPort = 9743
	// agentHost binds on the localhost to be available only for the connections from the host.
	agentHost = "127.0.0.1"
	// localhost ports are not necessary to be from 9000-9999 range.
	// TODO: make agent unix socket targetable to using the hostport,
	// depends on kube-rbac-proxy upstream forwarding
	// (currently not possible with Unix domain socket)
	agentPort = 29740
)

// ensureDaemonSet ensures that the daemonset exists
// Returns a Boolean value indicating whether it exists, a pointer to the
// daemonset and an error when relevant
func (r *NodeObservabilityReconciler) ensureDaemonSet(ctx context.Context, nodeObs *v1alpha2.NodeObservability, sa *corev1.ServiceAccount, ns string) (*appsv1.DaemonSet, error) {
	nameSpace := types.NamespacedName{Namespace: ns, Name: daemonSetName}
	desired := r.desiredDaemonSet(nodeObs, sa, ns)
	if err := controllerutil.SetControllerReference(nodeObs, desired, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed to set the controller reference for daemonset: %w", err)
	}

	current, err := r.currentDaemonSet(ctx, nameSpace)
	if err != nil && !errors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get daemonset %q due to: %w", nameSpace, err)
	} else if err != nil && errors.IsNotFound(err) {

		// create daemon since it doesn't exist
		err := r.createConfigMap(ctx, nodeObs, ns)
		if err != nil {
			return nil, fmt.Errorf("failed to create the configmap for kubelet-serving-ca: %w", err)
		}

		if err := r.createDaemonSet(ctx, desired); err != nil {
			return nil, fmt.Errorf("failed to create daemonset %q: %w", nameSpace, err)
		}
		r.Log.V(1).Info("created daemonset", "ds.namespace", nameSpace.Namespace, "ds.name", nameSpace.Name)

		return r.currentDaemonSet(ctx, nameSpace)
	}

	updated, err := r.updateDaemonset(ctx, current, desired)
	if err != nil {
		return nil, fmt.Errorf("failed to update the daemonset %q: %w", nameSpace, err)
	}

	if updated {
		current, err = r.currentDaemonSet(ctx, nameSpace)
		if err != nil {
			return nil, fmt.Errorf("failed to get existing daemonset %q: %w", nameSpace, err)
		}
		r.Log.V(1).Info("successfully updated daemonset", "ds.name", nameSpace.Name, "ds.namespace", nameSpace.Namespace)
	}
	return current, nil
}

// currentDaemonSet check if the daemonset exists
func (r *NodeObservabilityReconciler) currentDaemonSet(ctx context.Context, nameSpace types.NamespacedName) (*appsv1.DaemonSet, error) {
	ds := &appsv1.DaemonSet{}
	if err := r.Get(ctx, nameSpace, ds); err != nil {
		return nil, err
	}
	return ds, nil
}

// createDaemonSet creates the serviceaccount
func (r *NodeObservabilityReconciler) createDaemonSet(ctx context.Context, ds *appsv1.DaemonSet) error {
	return r.Create(ctx, ds)
}

func (r *NodeObservabilityReconciler) updateDaemonset(ctx context.Context, current, desired *appsv1.DaemonSet) (bool, error) {
	updatedDS := current.DeepCopy()
	updated := false

	if !cmp.Equal(current.ObjectMeta.OwnerReferences, desired.ObjectMeta.OwnerReferences) {
		updatedDS.ObjectMeta.OwnerReferences = desired.ObjectMeta.OwnerReferences
		updated = true
	}

	if !cmp.Equal(current.Spec.Template.Labels, desired.Spec.Template.Labels) {
		updatedDS.Spec.Template.Labels = desired.Spec.Template.Labels
		updated = true
	}

	if changed, updatedContainers := containersChanged(current.Spec.Template.Spec.Containers, desired.Spec.Template.Spec.Containers); changed {
		updatedDS.Spec.Template.Spec.Containers = updatedContainers
		updated = true
	}

	if changed, updatedVolumes := volumesChanged(updatedDS.Spec.Template.Spec.Volumes, desired.Spec.Template.Spec.Volumes); changed {
		updatedDS.Spec.Template.Spec.Volumes = updatedVolumes
		updated = true
	}

	if !cmp.Equal(current.Spec.Template.Spec.DNSPolicy, desired.Spec.Template.Spec.DNSPolicy) {
		updatedDS.Spec.Template.Spec.DNSPolicy = desired.Spec.Template.Spec.DNSPolicy
		updated = true
	}

	if updated {
		if err := r.Update(ctx, updatedDS); err != nil {
			return false, err
		}
		return true, nil
	}

	return false, nil
}

// desiredDaemonSet returns a DaemonSet object
func (r *NodeObservabilityReconciler) desiredDaemonSet(nodeObs *v1alpha2.NodeObservability, sa *corev1.ServiceAccount, ns string) *appsv1.DaemonSet {
	ls := labelsForNodeObservability(nodeObs.Name)
	tgp := int64(30)

	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      daemonSetName,
			Namespace: ns,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Image:           r.AgentImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            podName,
							Command:         []string{"node-observability-agent"},
							Args: []string{
								"--tokenFile=/var/run/secrets/kubernetes.io/serviceaccount/token",
								"--storage=/run/node-observability",
								fmt.Sprintf("--caCertFile=%s%s", kbltCAMountPath, kbltCAMountedFile),
								fmt.Sprintf("--port=%d", agentPort),
								"--crioPreferUnixSocket=false",
							},
							Resources:                corev1.ResourceRequirements{},
							TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
							Env: []corev1.EnvVar{{
								Name: "NODE_IP",
								ValueFrom: &corev1.EnvVarSource{
									FieldRef: &corev1.ObjectFieldSelector{
										FieldPath: "status.hostIP",
									},
								},
							}},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: agentPort,
									HostPort:      agentPort,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: kbltCAMountPath,
									Name:      kbltCAName,
									ReadOnly:  true,
								},
							},
						},
						{
							Name:            "kube-rbac-proxy",
							Image:           "gcr.io/kubebuilder/kube-rbac-proxy:v0.11.0",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Args: []string{
								fmt.Sprintf("--secure-listen-address=%s", net.JoinHostPort(kubeRBACProxyHost, strconv.Itoa(kubeRBACProxyPort))),
								fmt.Sprintf("--upstream=http://%s/", net.JoinHostPort(agentHost, strconv.Itoa(agentPort))),
								fmt.Sprintf("--tls-cert-file=%s/tls.crt", certsMountPath),
								fmt.Sprintf("--tls-private-key-file=%s/tls.key", certsMountPath),
								"--logtostderr=true",
								"--v=2",
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: kubeRBACProxyPort,
									HostPort:      kubeRBACProxyPort,
									Protocol:      corev1.ProtocolTCP,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      certsName,
									MountPath: certsMountPath,
									ReadOnly:  true,
								},
							},
						},
					},
					DNSPolicy:                     corev1.DNSClusterFirst,
					RestartPolicy:                 corev1.RestartPolicyAlways,
					SchedulerName:                 defaultScheduler,
					ServiceAccountName:            sa.Name,
					TerminationGracePeriodSeconds: &tgp,
					Volumes: []corev1.Volume{
						{
							Name: kbltCAName,
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: nodeObs.Name,
									},
								},
							},
						},
						{
							Name: certsName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: secretName,
								},
							},
						},
					},
					NodeSelector: nodeObs.Spec.NodeSelector,
					HostNetwork:  true,
				},
			},
		},
	}
	return ds
}
