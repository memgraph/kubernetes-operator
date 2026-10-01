/*
Copyright 2026.

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

package resources

import (
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

// vectorComponent labels the Vector configuration ConfigMap and names the
// sidecar container.
const vectorComponent = "vector"

// The Vector sidecar's shape: where its configuration and its state directory
// are mounted, and the environment it reads the pod's identity and the
// endpoint's credentials from. The configuration is one ConfigMap for both
// roles, because everything that differs per pod reaches Vector through the
// environment and Vector interpolates ${VAR} when it loads the file.
const (
	// VectorConfigKey is the file name the configuration is mounted as, which
	// is the key it sits under in the ConfigMap.
	VectorConfigKey = "vector.yaml"

	vectorConfigVolume = "vector-config"
	vectorConfigPath   = "/etc/vector/config"
	vectorDataVolume   = "vector-data"
	vectorDataPath     = "/vector-data"

	vectorRoleEnv      = "ROLE"
	vectorPodEnv       = "POD_NAME"
	vectorNamespaceEnv = "POD_NAMESPACE"
	vectorUsernameEnv  = "LOGS_USERNAME"
	vectorPasswordEnv  = "LOGS_PASSWORD"

	// vectorJob is the job and app label every line is pushed under, the HA
	// chart's value, which the dashboards Memgraph keeps select on; it is
	// also the id of the websocket source in the configuration.
	vectorJob = "memgraph"
)

// vectorRemap is the VRL program that turns Memgraph's websocket frames into
// the event the sink labels. Memgraph pushes each line as {"event": "log",
// "level": ..., "message": ...} but escapes only quotes and newlines, so a
// message carrying a backslash is not valid JSON: such a frame is kept whole
// as the message with its level unknown rather than dropped. Levels are the
// HA chart's: Memgraph's warning and critical become warn and fatal.
const vectorRemap = `level = "unknown"
parsed, err = parse_json(.message)
if err == null && is_object(parsed) {
  level = downcase(to_string(parsed.level) ?? "unknown")
  .message = to_string(parsed.message) ?? .message
}
if level == "warning" {
  level = "warn"
} else if level == "critical" {
  level = "fatal"
}
.level = level
`

// VectorConfigName is the name of the ConfigMap holding the sidecar's
// configuration for a cluster that asks for one.
func VectorConfigName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return cluster.Name + "-" + vectorComponent + "-config"
}

// UsesVector reports whether the cluster asked for the Vector sidecar.
func UsesVector(cluster *memgraphcomv1alpha1.MemgraphCluster) bool {
	return cluster.Spec.Monitoring != nil && cluster.Spec.Monitoring.VectorRemote != nil
}

// The subset of Vector's configuration the sidecar is given, typed so the
// builder marshals it rather than templating text.
type vectorConfig struct {
	DataDir    string                     `json:"data_dir"`
	Sources    map[string]vectorWebsocket `json:"sources"`
	Transforms map[string]vectorTransform `json:"transforms"`
	Sinks      map[string]vectorLoki      `json:"sinks"`
}

type vectorWebsocket struct {
	Type string     `json:"type"`
	URI  string     `json:"uri"`
	TLS  *vectorTLS `json:"tls,omitempty"`
}

type vectorTLS struct {
	Enabled           bool `json:"enabled"`
	VerifyCertificate bool `json:"verify_certificate"`
	VerifyHostname    bool `json:"verify_hostname"`
}

type vectorTransform struct {
	Type   string   `json:"type"`
	Inputs []string `json:"inputs"`
	Source string   `json:"source"`
}

type vectorLoki struct {
	Type              string            `json:"type"`
	Inputs            []string          `json:"inputs"`
	Endpoint          string            `json:"endpoint"`
	Healthcheck       vectorHealthcheck `json:"healthcheck"`
	Auth              *vectorBasicAuth  `json:"auth,omitempty"`
	Encoding          vectorEncoding    `json:"encoding"`
	Labels            map[string]string `json:"labels"`
	RemoveLabelFields bool              `json:"remove_label_fields"`
}

type vectorHealthcheck struct {
	Enabled bool `json:"enabled"`
}

type vectorBasicAuth struct {
	Strategy string `json:"strategy"`
	User     string `json:"user"`
	Password string `json:"password"`
}

type vectorEncoding struct {
	Codec string `json:"codec"`
}

// VectorConfigMap builds the ConfigMap holding the sidecar's configuration:
// Memgraph's websocket as the source, dialled inside the pod and over TLS
// without verification on a cluster serving Bolt TLS, since the websocket
// shares that context; the remap above; and a Loki sink at the endpoint with
// the chart's labels, the pod's own from the environment, the extra ones from
// the spec, and the credentials interpolated from the environment when the
// block names a Secret. The sink's health check is off because a gateway in
// front of VictoriaLogs need not answer Loki's ready endpoint.
//
// The builder is only called for a cluster whose spec carries the block.
func VectorConfigMap(cluster *memgraphcomv1alpha1.MemgraphCluster) *corev1.ConfigMap {
	spec := normalize(cluster.Spec)
	block := spec.monitoring.vector

	source := vectorWebsocket{
		Type: "websocket",
		URI:  fmt.Sprintf("ws://127.0.0.1:%d", memgraphcomv1alpha1.MonitoringPort),
	}
	if spec.boltTLSSecret != "" {
		source.URI = fmt.Sprintf("wss://127.0.0.1:%d", memgraphcomv1alpha1.MonitoringPort)
		source.TLS = &vectorTLS{Enabled: true}
	}

	lokiLabels := make(map[string]string, len(block.extraLabels)+6)
	maps.Copy(lokiLabels, block.extraLabels)
	lokiLabels["app"] = vectorJob
	lokiLabels["job"] = vectorJob
	lokiLabels["role"] = "${" + vectorRoleEnv + "}"
	lokiLabels["namespace"] = "${" + vectorNamespaceEnv + "}"
	lokiLabels["pod"] = "${" + vectorPodEnv + "}"
	lokiLabels["level"] = "{{ level }}"

	sink := vectorLoki{
		Type:              "loki",
		Inputs:            []string{"logs"},
		Endpoint:          block.logsEndpoint,
		Healthcheck:       vectorHealthcheck{Enabled: false},
		Encoding:          vectorEncoding{Codec: "text"},
		Labels:            lokiLabels,
		RemoveLabelFields: true,
	}
	if block.authSecret != "" {
		sink.Auth = &vectorBasicAuth{
			Strategy: "basic",
			User:     "${" + vectorUsernameEnv + "}",
			Password: "${" + vectorPasswordEnv + "}",
		}
	}

	config, err := yaml.Marshal(vectorConfig{
		DataDir:    vectorDataPath,
		Sources:    map[string]vectorWebsocket{vectorJob: source},
		Transforms: map[string]vectorTransform{"logs": {Type: "remap", Inputs: []string{vectorJob}, Source: vectorRemap}},
		Sinks:      map[string]vectorLoki{"loki": sink},
	})
	if err != nil {
		// The struct holds strings and bools only; marshalling cannot fail.
		panic(fmt.Sprintf("marshal vector config: %v", err))
	}

	labels := labels(cluster, vectorComponent, nil)
	labels[MonitoringLabel] = MonitoringValue

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: configMapKind},
		ObjectMeta: metav1.ObjectMeta{
			Name:      VectorConfigName(cluster),
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{VectorConfigKey: string(config)},
	}
}

// vectorSidecar is the container that ships one pod's logs: Vector on the
// configuration above, told its role, pod and namespace through the
// environment, and the endpoint's credentials the same way when the block
// names a Secret. It runs under the pod's identity and the restricted security
// context every operator container runs under, with its state on an emptyDir
// because its root filesystem is read-only. It carries no probe: a sidecar's
// readiness must never gate the pod's, which is what registration waits on.
func vectorSidecar(component string, spec normalizedSpec) corev1.Container {
	block := spec.monitoring.vector

	env := []corev1.EnvVar{
		{Name: vectorRoleEnv, Value: component},
		{Name: vectorPodEnv, ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
		}},
		{Name: vectorNamespaceEnv, ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
		}},
	}
	if block.authSecret != "" {
		for _, credential := range []struct{ env, key string }{
			{vectorUsernameEnv, memgraphcomv1alpha1.BasicAuthUsernameKey},
			{vectorPasswordEnv, memgraphcomv1alpha1.BasicAuthPasswordKey},
		} {
			env = append(env, corev1.EnvVar{Name: credential.env, ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: block.authSecret},
					Key:                  credential.key,
				},
			}})
		}
	}

	return corev1.Container{
		Name:            vectorComponent,
		Image:           block.image,
		ImagePullPolicy: block.pullPolicy,
		Args:            []string{"--config", vectorConfigPath + "/" + VectorConfigKey},
		Env:             env,
		Resources:       block.resources,
		VolumeMounts: []corev1.VolumeMount{
			{Name: vectorConfigVolume, MountPath: vectorConfigPath, ReadOnly: true},
			{Name: vectorDataVolume, MountPath: vectorDataPath},
		},
		SecurityContext: restrictedSecurityContext(),
	}
}

// vectorVolumes are the sidecar's two volumes: its configuration and its
// state directory.
func vectorVolumes(cluster *memgraphcomv1alpha1.MemgraphCluster) []corev1.Volume {
	return []corev1.Volume{
		{
			Name: vectorConfigVolume,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: VectorConfigName(cluster)},
				},
			},
		},
		{Name: vectorDataVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
}
