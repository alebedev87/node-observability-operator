/*
Copyright 2022.

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

package nodeobservabilitycontroller

import "k8s.io/apimachinery/pkg/types"

const (
	// SourceKubeletCAConfigMapNamespace namespace where the Kubelet CA is stored
	SourceKubeletCAConfigMapNamespace = "openshift-config-managed"
	// KubeletCAConfigMapName name of the Kubelet CA configmap
	KubeletCAConfigMapName = "kubelet-serving-ca"
	// AgentServiceName name of the service backed up by the agent's daemonset.
	AgentServiceName = "node-observability-agent"
	// nolint - ignore G101: "secret" substring in the name
	// ServingCertSecretName secret which contains certificates
	// generated by OpenShift and used by the agent's daemonset.
	ServingCertSecretName = "node-observability-agent"
)

// NamespacedKubeletCAConfigMapName returns the namespaced name of the kubelet CA configmap with the provided namespace.
func NamespacedKubeletCAConfigMapName(namespace string) types.NamespacedName {
	return types.NamespacedName{
		Namespace: namespace,
		Name:      KubeletCAConfigMapName,
	}
}
