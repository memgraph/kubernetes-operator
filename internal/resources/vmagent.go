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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
)

// vmagentComponent labels the vmagent Deployment and its ConfigMap, and is the
// suffix both are named by.
const vmagentComponent = "vmagent"

// The vmagent pod's shape: where the scrape config, the pending-data buffer and
// the basic-auth credentials are mounted, and the identity it runs under. The
// config and the credentials are sibling directories, not one under the other:
// the runtime cannot create a mountpoint inside a mount that is already
// read-only. The image declares no user (it is built from scratch), so under
// the restricted Pod Security Standard the operator must pin a numeric one;
// 65534 is what VictoriaMetrics' own charts run it as.
const (
	// VMAgentConfigKey is the file name the scrape config is mounted as, which
	// is the key it sits under in the ConfigMap.
	VMAgentConfigKey = "scrape.yml"

	vmagentConfigVolume = "config"
	vmagentConfigPath   = "/etc/vmagent/config"
	vmagentDataVolume   = "data"
	vmagentDataPath     = "/tmpData"
	vmagentAuthVolume   = "basic-auth"
	vmagentAuthPath     = "/etc/vmagent/basic-auth"
	vmagentPortName     = "http"

	// vmagentUsernameEnv carries the basic-auth username from the Secret into
	// the pod, where Kubernetes expands it into the username flag: the pinned
	// vmagent reads the password from a file but has no usernameFile flag, and
	// the username is not the secret part.
	vmagentUsernameEnv = "VMAGENT_BASIC_AUTH_USERNAME"

	// vmagentConfigCheckInterval is how often vmagent re-reads its scrape
	// config, so a changed ConfigMap reaches it once the kubelet has synced
	// the mount, with no pod restart and no loss of the pending-data buffer.
	vmagentConfigCheckInterval = "1m"

	vmagentUserID int64 = 65534

	// vmagentJobName is the Prometheus job every Memgraph target is scraped
	// under, the HA chart's name for it.
	vmagentJobName = "memgraph"
)

// VMAgentName is the name of the vmagent Deployment the operator runs for a
// cluster that asks for one.
func VMAgentName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return cluster.Name + "-" + vmagentComponent
}

// VMAgentConfigName is the name of the ConfigMap holding that vmagent's scrape
// config.
func VMAgentConfigName(cluster *memgraphcomv1alpha1.MemgraphCluster) string {
	return VMAgentName(cluster) + "-config"
}

// UsesVMAgent reports whether the cluster asked for a vmagent.
func UsesVMAgent(cluster *memgraphcomv1alpha1.MemgraphCluster) bool {
	return cluster.Spec.Monitoring != nil && cluster.Spec.Monitoring.VMAgentRemote != nil
}

// scrapeConfig is the subset of a Prometheus scrape configuration vmagent is
// given, typed so the builder marshals it rather than templating text.
type scrapeConfig struct {
	Global        scrapeGlobal `json:"global"`
	ScrapeConfigs []scrapeJob  `json:"scrape_configs"`
}

type scrapeGlobal struct {
	ScrapeInterval string            `json:"scrape_interval"`
	ExternalLabels map[string]string `json:"external_labels,omitempty"`
}

type scrapeJob struct {
	JobName       string         `json:"job_name"`
	MetricsPath   string         `json:"metrics_path"`
	Scheme        string         `json:"scheme,omitempty"`
	TLSConfig     *scrapeTLS     `json:"tls_config,omitempty"`
	StaticConfigs []staticConfig `json:"static_configs"`
}

type scrapeTLS struct {
	InsecureSkipVerify bool `json:"insecure_skip_verify"`
}

type staticConfig struct {
	Targets []string `json:"targets"`
}

// VMAgentConfigMap builds the ConfigMap holding vmagent's scrape config: one
// job over every instance the operator runs, addressed by pod DNS on the
// metrics port, at the counts the controller applied. Static targets rather
// than discovery, as in the HA chart: the operator knows the topology, and
// vmagent then needs no access to the API server. The scheme follows
// spec.tls.bolt the way the ServiceMonitor's does, for the same reason.
//
// The builder is only called for a cluster whose spec carries the block.
func VMAgentConfigMap(
	cluster *memgraphcomv1alpha1.MemgraphCluster,
	coordinatorReplicas, dataReplicas int32,
) *corev1.ConfigMap {
	spec := normalize(cluster.Spec)
	block := spec.monitoring.vmagent

	targets := make([]string, 0, coordinatorReplicas+dataReplicas)
	for ordinal := range coordinatorReplicas {
		targets = append(targets,
			hostPort(podFQDN(cluster, CoordinatorName(cluster), spec, ordinal), memgraphcomv1alpha1.MetricsPort))
	}
	for ordinal := range dataReplicas {
		targets = append(targets,
			hostPort(podFQDN(cluster, DataName(cluster), spec, ordinal), memgraphcomv1alpha1.MetricsPort))
	}
	job := scrapeJob{
		JobName:       vmagentJobName,
		MetricsPath:   metricsPath,
		StaticConfigs: []staticConfig{{Targets: targets}},
	}
	if spec.boltTLSSecret != "" {
		job.Scheme = "https"
		job.TLSConfig = &scrapeTLS{InsecureSkipVerify: true}
	}
	config, err := yaml.Marshal(scrapeConfig{
		Global:        scrapeGlobal{ScrapeInterval: block.scrapeInterval, ExternalLabels: block.externalLabels},
		ScrapeConfigs: []scrapeJob{job},
	})
	if err != nil {
		// The struct holds strings and bools only; marshalling cannot fail.
		panic(fmt.Sprintf("marshal vmagent scrape config: %v", err))
	}

	labels := labels(cluster, vmagentComponent, nil)
	labels[MonitoringLabel] = MonitoringValue

	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      VMAgentConfigName(cluster),
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{VMAgentConfigKey: string(config)},
	}
}

// VMAgentDeployment builds the one-replica Deployment running vmagent. It
// mounts the scrape config read-only and re-reads it on an interval, buffers
// pending samples on an emptyDir because its root filesystem is read-only, and
// when the block names a basic-auth Secret takes the username from it through
// an environment variable and mounts the password read-only as a file vmagent
// re-reads every second, so the password is never on the command line and a
// rotated one needs no restart. The pod needs nothing from the API server, so
// it mounts no ServiceAccount token.
//
// The builder is only called for a cluster whose spec carries the block.
func VMAgentDeployment(cluster *memgraphcomv1alpha1.MemgraphCluster) *appsv1.Deployment {
	spec := normalize(cluster.Spec)
	block := spec.monitoring.vmagent

	labels := labels(cluster, vmagentComponent, nil)
	labels[MonitoringLabel] = MonitoringValue

	args := []string{
		"-promscrape.config=" + vmagentConfigPath + "/" + VMAgentConfigKey,
		"-promscrape.configCheckInterval=" + vmagentConfigCheckInterval,
		"-remoteWrite.url=" + block.remoteWriteURL,
		"-remoteWrite.tmpDataPath=" + vmagentDataPath,
		fmt.Sprintf("-httpListenAddr=:%d", memgraphcomv1alpha1.VMAgentPort),
	}
	mounts := []corev1.VolumeMount{
		{Name: vmagentConfigVolume, MountPath: vmagentConfigPath, ReadOnly: true},
		{Name: vmagentDataVolume, MountPath: vmagentDataPath},
	}
	volumes := []corev1.Volume{
		{
			Name: vmagentConfigVolume,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: VMAgentConfigName(cluster)},
				},
			},
		},
		{Name: vmagentDataVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	var env []corev1.EnvVar
	if block.basicAuthSecret != "" {
		args = append(args,
			"-remoteWrite.basicAuth.username=$("+vmagentUsernameEnv+")",
			"-remoteWrite.basicAuth.passwordFile="+vmagentAuthPath+"/"+memgraphcomv1alpha1.BasicAuthPasswordKey,
		)
		env = []corev1.EnvVar{{
			Name: vmagentUsernameEnv,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: block.basicAuthSecret},
					Key:                  memgraphcomv1alpha1.BasicAuthUsernameKey,
				},
			},
		}}
		mounts = append(mounts, corev1.VolumeMount{Name: vmagentAuthVolume, MountPath: vmagentAuthPath, ReadOnly: true})
		volumes = append(volumes, corev1.Volume{
			Name: vmagentAuthVolume,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: block.basicAuthSecret,
					Items: []corev1.KeyToPath{
						{Key: memgraphcomv1alpha1.BasicAuthPasswordKey, Path: memgraphcomv1alpha1.BasicAuthPasswordKey},
					},
				},
			},
		})
	}

	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      VMAgentName(cluster),
			Namespace: cluster.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: selectorLabels(cluster, vmagentComponent)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:      ptr.To(vmagentUserID),
						RunAsGroup:     ptr.To(vmagentUserID),
						FSGroup:        ptr.To(vmagentUserID),
						RunAsNonRoot:   ptr.To(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            vmagentComponent,
						Image:           block.image,
						ImagePullPolicy: block.pullPolicy,
						Args:            args,
						Env:             env,
						Ports: []corev1.ContainerPort{
							{Name: vmagentPortName, ContainerPort: memgraphcomv1alpha1.VMAgentPort},
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromString(vmagentPortName)},
							},
						},
						Resources:       block.resources,
						VolumeMounts:    mounts,
						SecurityContext: restrictedSecurityContext(),
					}},
					Volumes: volumes,
				},
			},
		},
	}
}
